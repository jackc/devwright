#!/usr/bin/env python3
"""Exercise preservation, overrides, dotfiles, credential transport, and restart.

Run only on a disposable VM created by environment/dev. This deliberately edits
that VM's personal settings and test credentials. It never reads real secrets.
"""
import argparse
from pathlib import Path
import tempfile
from unittest.mock import patch

import yaml
import launcher


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('name', help='Disposable VM; name must start with ansible-parity-')
    args = parser.parse_args()
    if not args.name.startswith('ansible-parity-'):
        parser.error('Use a disposable ansible-parity-* VM')
    state = launcher.info(args.name)
    with tempfile.TemporaryDirectory(prefix='ansible-parity-fixtures-') as tmp:
        directory = Path(tmp)
        dotfiles = directory / 'dotfiles'
        dotfiles.mkdir()
        installer = dotfiles / 'install'
        installer.write_text('#!/bin/bash\nset -eu\nprintf "%s\\n" "$USER" > "$HOME/.parity-dotfiles"\nprintf "return 0\\n" > "$HOME/.profile"\n')
        installer.chmod(0o755)
        launcher.run(['git', 'init', '-q', dotfiles])
        launcher.run(['git', '-C', dotfiles, 'add', 'install'])
        launcher.run(['git', '-C', dotfiles, '-c', 'user.name=Parity Test', '-c', 'user.email=parity@example.invalid',
                      'commit', '-qm', 'Test dotfiles installer'])
        requirements = directory / 'requirements.toml'
        requirements.write_text((launcher.ROOT / 'config/codex/requirements.toml').read_text() + '\n# custom parity policy\n')
        managed = directory / 'managed.json'
        managed.write_text((launcher.ROOT / 'config/claude/managed-settings.json').read_text())
        codex = directory / 'codex.toml'
        codex.write_text((launcher.ROOT / 'config/codex/config.toml').read_text() + '\n# explicit parity replacement\n')
        claude = directory / 'claude.json'
        claude.write_text((launcher.ROOT / 'config/claude/settings.json').read_text())
        recipe = {
            'dotfiles_repo': str(dotfiles), 'packages': ['tree'],
            'environment': {'PARITY_ENV': "literal $HOME and 'quotes'"},
            'codex_requirements': str(requirements), 'claude_managed_settings': str(managed),
            'codex_config': str(codex), 'claude_config': str(claude),
        }
        config_file = directory / 'recipe.yaml'
        launcher.ssh(state, 'dev', ['python3', '-c', '''from pathlib import Path
import json
p=Path.home()/'.codex/config.toml'; p.write_text(p.read_text()+'\\n# preserve-parity-personal\\n')
p=Path.home()/'.claude/settings.json'; d=json.loads(p.read_text()); d['env']={'PARITY_PRESERVE':'yes'}; p.write_text(json.dumps(d))
(Path.home()/'projects/parity-preserve').write_text('keep me')
'''])
        declarations = [{'name': 'PARITY_CREDENTIAL', 'description': 'Synthetic test value'}]
        for value in ["first '$HOME' value", "second '$HOME' value"]:
            with patch.object(launcher.sys.stdin, 'isatty', return_value=True), patch.object(launcher.getpass, 'getpass', return_value=value):
                launcher.credentials(state, declarations, replace=True)

        def apply():
            config_file.write_text(yaml.safe_dump(recipe))
            launcher.run([launcher.HERE / 'dev', 'configure', args.name, '--config', config_file])

        def assert_guest(preserved, custom):
            code = '''import json,os
from pathlib import Path
h=Path.home()
assert (h/'projects/parity-preserve').read_text()=='keep me'
assert os.environ['PARITY_ENV']=="literal $HOME and 'quotes'"
assert os.environ['PARITY_CREDENTIAL']=="second '$HOME' value"
assert "first " not in (h/'.config/devwright/credentials.sh').read_text()
assert (h/'.parity-dotfiles').read_text().strip()=='dev'
assert os.access('/usr/bin/tree',os.X_OK)
'''
            if preserved:
                code += "assert 'preserve-parity-personal' in (h/'.codex/config.toml').read_text()\n"
                code += "assert json.loads((h/'.claude/settings.json').read_text())['env']['PARITY_PRESERVE']=='yes'\n"
            else:
                code += "assert 'explicit parity replacement' in (h/'.codex/config.toml').read_text()\n"
                code += "assert 'preserve-parity-personal' not in (h/'.codex/config.toml').read_text()\n"
                code += "assert 'env' not in json.loads((h/'.claude/settings.json').read_text())\n"
            launcher.ssh(state, 'dev', ['python3', '-c', code])
            root_code = f'''from pathlib import Path
p=Path('/usr/local/share/devwright')
assert (p/'custom-requirements.toml').exists()=={custom!r}
assert (p/'custom-managed-settings.json').exists()=={custom!r}
assert Path('/root/.parity-dotfiles').read_text().strip()=='root'
assert ('custom parity policy' in Path('/etc/codex/requirements.toml').read_text())=={custom!r}
'''
            launcher.ssh(state, 'root', ['python3', '-c', root_code])

        apply()
        assert_guest(preserved=True, custom=True)
        print('PASS custom policy selection, default preservation, host-bundled dotfiles, environment, credential rotation', flush=True)
        for key in ('codex_requirements', 'claude_managed_settings', 'codex_config', 'claude_config'):
            recipe.pop(key)
        apply()
        assert_guest(preserved=True, custom=True)
        print('PASS repeated configuration preserves selected policy and user state', flush=True)
        recipe |= {'reset_codex_requirements': True, 'reset_claude_managed_settings': True,
                   'codex_config': str(codex), 'claude_config': str(claude),
                   'replace_codex_config': True, 'replace_claude_config': True}
        apply()
        assert_guest(preserved=False, custom=False)
        print('PASS explicit replacement and managed-policy reset', flush=True)
        launcher.run(['limactl', 'stop', args.name])
        launcher.run(['limactl', 'start', '--tty=false', args.name])
        state = launcher.info(args.name)
        launcher.verify(state)
        assert_guest(preserved=False, custom=False)
        print('PASS restart retains isolation and SSH connectivity', flush=True)


if __name__ == '__main__':
    main()
