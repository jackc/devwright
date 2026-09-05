import importlib.util
import json
import os
from pathlib import Path
import unittest
import tempfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("vm", ROOT / "scripts/vm.py")
vm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vm)


class VmTests(unittest.TestCase):
    def test_render_has_no_unresolved_payloads(self):
        config = vm.render()
        self.assertNotIn("_B64__", json.dumps(config))
        self.assertNotIn("__CODEX_VERSION__", json.dumps(config))
        self.assertEqual(config["user"]["name"], "jack")
        self.assertTrue(config["plain"])

    def test_refuse_foreign_instance_and_unsafe_overrides(self):
        config = vm.render()
        vm.check_instance({"config": config})
        for key, value in (("plain", False), ("mounts", [{"location": "~"}]),
                           ("ssh", {"forwardAgent": True}), ("param", {}),
                           ("user", {"name": "dev"})):
            with self.subTest(key=key), self.assertRaises(ValueError):
                vm.check_instance({"config": {**config, key: value}})

    def test_do_not_inherit_host_credentials(self):
        with patch.dict(os.environ, {"SSH_AUTH_SOCK": "/secret/socket", "GH_TOKEN": "test-token",
                                     "GITHUB_TOKEN": "test-token", "OPENAI_API_KEY": "test-key"}):
            env = vm.environment()
            for key in ("SSH_AUTH_SOCK", "GH_TOKEN", "GITHUB_TOKEN", "OPENAI_API_KEY"):
                self.assertNotIn(key, env)

    def test_ssh_never_reuses_admin_control_socket(self):
        args = vm.ssh_args({"dir": "/tmp/a path", "name": "test"})
        self.assertIn("ControlPath=none", args)
        self.assertIn("IdentityAgent=none", args)
        self.assertEqual(args[-3:], ["-l", "dev", "lima-test"])

    def test_token_is_only_sent_on_stdin(self):
        state = {"name": "test", "dir": "/tmp/test", "status": "Running", "config": vm.render()}
        with patch("sys.argv", ["vm.py", "set-token", "test"]), \
             patch.object(vm, "info", return_value=state), \
             patch.object(vm.getpass, "getpass", return_value="synthetic-token"), \
             patch.object(vm, "remote") as remote, patch("builtins.print"):
            vm.main()
        self.assertNotIn("synthetic-token", repr(remote.call_args.args))
        self.assertEqual(remote.call_args.kwargs["input"], "synthetic-token\n")

    def test_ssh_install_preserves_config_and_refreshes_alias(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            (home / ".ssh").mkdir()
            config = home / ".ssh/config"
            original = "Host personal\n  HostName example.invalid\n"
            config.write_text(original)
            with patch.object(Path, "home", return_value=home), \
                 patch.object(vm, "ssh_config", return_value="Host test\n  Port 1234\n"), \
                 patch("builtins.print"):
                vm.install_ssh({"name": "test"}, add_include=True)
                vm.install_ssh({"name": "test"}, add_include=True)
                self.assertEqual(config.read_text().count('Include "~/.ssh/agent-vms/*.config"'), 1)
                self.assertTrue(config.read_text().endswith(original))
                self.assertEqual(len(list((home / ".ssh").glob("config.before-agent-vms-*"))), 1)
            with patch.object(Path, "home", return_value=home), \
                 patch.object(vm, "ssh_config", return_value="Host test\n  Port 5678\n"):
                vm.install_ssh({"name": "test"})
            self.assertIn("5678", (home / ".ssh/agent-vms/test.config").read_text())


if __name__ == "__main__":
    unittest.main()
