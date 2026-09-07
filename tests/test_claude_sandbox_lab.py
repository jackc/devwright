"""Regression checks for the lab probe, without Claude or network access."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "claude_sandbox_lab", Path(__file__).with_name("claude-sandbox-lab.py")
)
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class CanaryProbeTests(unittest.TestCase):
    def test_content_denial(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "workspace").mkdir()
            (root / "bin").mkdir()
            # Run the real probe script but prevent its network check from
            # leaving the fixture. All filesystem writes target this directory.
            curl = root / "bin" / "curl"
            curl.write_text("#!/bin/sh\nprintf '000'\nexit 1\n")
            curl.chmod(0o700)
            readable = root / "canary"
            readable.write_text("dev-sandbox-synthetic-canary\n")
            empty = root / "empty"
            empty.touch()
            unreadable = root / "unreadable"
            unreadable.write_text("dev-sandbox-synthetic-canary\n")
            unreadable.chmod(0)
            cases = [
                (readable, "READABLE"),
                (empty, "denied"),
                (Path(os.devnull), "denied"),
                (root / "missing", "denied"),
            ]
            if os.geteuid() != 0:
                cases.append((unreadable, "denied"))
            for canary, expected in cases:
                with self.subTest(canary=canary):
                    result = subprocess.run(
                        ["/bin/sh", "-c", lab.PROBE],
                        cwd=root / "workspace",
                        env={
                            "PATH": str(root / "bin") + ":/usr/bin:/bin",
                            "CANARY": str(canary),
                            "SIBLING": str(root / "sibling"),
                        },
                        capture_output=True,
                        text=True,
                        check=True,
                    )
                    self.assertIn(
                        "PROBE read-canary: " + expected, result.stdout.splitlines()
                    )


if __name__ == "__main__":
    unittest.main()
