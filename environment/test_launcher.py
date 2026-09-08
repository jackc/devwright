import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import yaml

spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class RecipeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name).resolve()

    def load(self, data):
        file = self.directory / 'config.yaml'
        file.write_text(yaml.safe_dump(data))
        return launcher.load_config(file)

    def test_relative_policy_selection_and_preservation_defaults(self):
        (self.directory / 'requirements.toml').write_text('[features]\napps = false\n')
        result = self.load({'codex_requirements': 'requirements.toml'})
        self.assertEqual(result['codex_policy_mode'], 'custom')
        self.assertEqual(result['claude_policy_mode'], 'preserve')
        self.assertEqual(result['codex_requirements'], str(self.directory / 'requirements.toml'))

    def test_reject_invalid_inputs_before_vm_access(self):
        for data in [
            {'ansible_connection': 'local'}, {'packages': 'curl'},
            {'environment': {'INVALID-NAME': 'value'}},
            {'dotfiles_install': '../escape'}, {'dotfiles_repo': '--upload-pack=bad'},
            {'replace_codex_config': True}, {'reset_codex_requirements': 'false'},
            {'credentials': [{'name': 'TOKEN', 'description': 'token'}], 'environment': {'TOKEN': 'bad'}},
        ]:
            with self.subTest(data=data), self.assertRaises(ValueError):
                self.load(data)

    def test_invalid_custom_settings(self):
        (self.directory / 'settings.json').write_text('{"sandbox":{"enabled":"yes"}}')
        with self.assertRaises(Exception):
            self.load({'claude_config': 'settings.json'})
        (self.directory / 'bad.toml').write_text('[features]\napps = "yes"\n')
        with self.assertRaises(ValueError):
            self.load({'codex_requirements': 'bad.toml'})
        (self.directory / 'managed.json').write_text('{"sandbox":{"enabled":"yes"}}')
        with self.assertRaises(launcher.jsonschema.ValidationError):
            self.load({'claude_managed_settings': 'managed.json'})

    def test_ssh_install_preserves_existing_config_and_is_repeatable(self):
        state = {'name': 'test', 'dir': '/tmp/lima directory/test'}
        (self.directory / '.ssh').mkdir()
        config = self.directory / '.ssh/config'
        original = 'Host personal\n  HostName example.com\n'
        config.write_text(original)
        with patch.object(launcher.Path, 'home', return_value=self.directory):
            launcher.install_ssh(state)
            launcher.install_ssh(state)
        self.assertEqual(config.read_text().count('Include'), 1)
        self.assertTrue(config.read_text().endswith(original))
        self.assertEqual(len(list(config.parent.glob('config.before-*'))), 1)
        generated = (config.parent / 'lima-environments/test.config').read_text()
        self.assertIn('ControlPath ~/.ssh/control-%C', generated)
        self.assertLess(generated.index('IdentityAgent none'), generated.index('Include'))


class BoundaryTests(unittest.TestCase):
    def state(self):
        return {'name': 'test', 'dir': '/tmp/lima/test', 'config': {
            'plain': True, 'mounts': [], 'provision': [],
            'ssh': {'forwardAgent': False, 'loadDotSSHPubKeys': False},
            'user': {'name': 'dev', 'home': '/home/dev', 'uid': 1000}}}

    def test_global_overrides_fail_closed(self):
        for key, value in [('mounts', [{'location': '~'}]), ('provision', [{'script': 'bad'}]),
                           ('plain', False), ('propagateProxyEnv', True),
                           ('portForwards', [{'static': True}])]:
            state = self.state()
            state['config'][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                launcher.check_instance(state, 'test')
        launcher.check_instance(self.state(), 'test')

    def test_ssh_separates_root_and_dev_and_disables_agent(self):
        with patch.object(launcher, 'run') as run:
            for user in ['dev', 'root']:
                launcher.ssh(self.state(), user, ['echo', 'literal $(not-a-command)'])
        for call, user in zip(run.call_args_list, ['dev', 'root']):
            args = call.args[0]
            self.assertEqual(args[args.index('-l') + 1], user)
            self.assertIn('ControlPath=~/.ssh/control-%C', args)
            self.assertIn('IdentityAgent=none', args)
            self.assertEqual(args[-1], "echo 'literal $(not-a-command)'")

    def test_missing_credentials_require_terminal_without_prompting_or_writing(self):
        with patch.object(launcher, 'ssh', return_value=subprocess.CompletedProcess([], 0, '[]')) as ssh:
            with patch.object(launcher.sys.stdin, 'isatty', return_value=False):
                with self.assertRaises(ValueError):
                    launcher.credentials(self.state(), [{'name': 'TOKEN', 'description': 'test'}])
        self.assertEqual(ssh.call_count, 1)

    def test_existing_credentials_are_not_requested(self):
        with patch.object(launcher, 'ssh', return_value=subprocess.CompletedProcess([], 0, '["TOKEN"]')) as ssh:
            with patch.object(launcher.getpass, 'getpass', side_effect=AssertionError('Unexpected prompt')):
                launcher.credentials(self.state(), [{'name': 'TOKEN', 'description': 'test'}])
        self.assertEqual(ssh.call_count, 1)


class EnvironmentWriterTests(unittest.TestCase):
    def test_literal_values_and_existing_credentials_survive_reapplication(self):
        spec = importlib.util.spec_from_file_location('environment_writer', Path(__file__).parent / 'files/environment.py')
        writer = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(writer)
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            directory = home / '.config/devwright'
            directory.mkdir(parents=True)
            credentials = directory / 'credentials.sh'
            credentials.write_text("export EXISTING='preserve'\n")
            writer.configure(home, {'LITERAL': "a 'quote', $HOME, and $(false)"})
            writer.configure(home, {'LITERAL': "a 'quote', $HOME, and $(false)"})
            self.assertEqual(credentials.read_text().count('environment.sh'), 1)
            self.assertIn("export EXISTING='preserve'", credentials.read_text())
            self.assertEqual((directory / 'environment.sh').stat().st_mode & 0o777, 0o600)
            result = subprocess.run(['bash', '-c', 'source "$1"; printf "%s" "$LITERAL"', 'bash', str(directory / 'environment.sh')],
                                    text=True, capture_output=True, check=True)
            self.assertEqual(result.stdout, "a 'quote', $HOME, and $(false)")


if __name__ == '__main__':
    unittest.main()
