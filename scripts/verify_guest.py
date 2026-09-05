#!/usr/bin/env python3
"""Credential-free Linux and Codex checks, run through a real dev SSH session."""
import errno
import json
import os
from pathlib import Path
import pwd
import subprocess
import tempfile
import tomllib


def run(args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, timeout=45, **kwargs)


def main():
    assert pwd.getpwuid(os.getuid()).pw_name == "dev", "Must run as dev"
    assert os.environ.get("SSH_AUTH_SOCK") is None, "Agent socket was forwarded"
    assert run(["sudo", "-n", "true"]).returncode != 0, "dev has sudo access"
    assert set(os.getgroups()) <= {pwd.getpwnam("dev").pw_gid}, "Unexpected supplementary groups"
    for path in ("/home/jack", "/root"):
        try:
            os.listdir(path)
        except PermissionError:
            pass
        else:
            raise AssertionError(f"Administrator home accessible: {path}")
    for path in ("/etc/codex", "/etc/codex/requirements.toml", "/usr/local/bin", "/usr/local/share/agent-vm"):
        assert os.stat(path).st_uid == 0 and not os.access(path, os.W_OK), f"Writable policy/tool path: {path}"
    mounts = Path("/proc/mounts").read_text()
    assert not any(t in mounts for t in ("virtiofs", "9p", "fuse.sshfs")), "Unexpected shared filesystem"
    assert not Path("/run/host-services/ssh-auth.sock").exists()
    assert not os.access("/var/run/docker.sock", os.R_OK | os.W_OK)
    req = tomllib.loads(Path("/etc/codex/requirements.toml").read_text())
    assert req["allowed_permission_profiles"] == {"vm_dev": True}
    assert req["mcp_servers"] == {}
    assert all(req["features"][k] is False for k in ("apps", "plugins", "browser_use", "in_app_browser", "computer_use"))
    version = Path("/usr/local/share/agent-vm/codex-version").read_text().strip()
    assert run(["codex", "--version"]).stdout.strip() == "codex-cli " + version
    print("PASS Linux account, policy ownership, mounts, SSH forwarding, and Codex installation")
    result = run(["python3", "/usr/local/share/agent-vm/check_codex.py"])
    assert result.returncode == 0, result.stderr
    print(result.stdout.strip())

    # Use a synthetic denied file only; never read or overwrite an existing secret.
    canary = Path.home() / ".pgpass"
    if canary.exists() or canary.is_symlink():
        raise RuntimeError("Cannot run canary test: ~/.pgpass already exists; no secrets were read")
    with tempfile.TemporaryDirectory(prefix="verify-", dir=Path.home() / "projects") as fixture:
        work = str(Path(fixture) / "workspace")
        Path(work).mkdir()
        sibling = Path(fixture) / "outside-workspace"
        sibling.write_text("original")
        created = False
        try:
            with canary.open("x") as f:
                created = True
                f.write("agent-vm-synthetic-canary\n")
            # Confirm it exists and is readable outside the sandbox.
            assert canary.read_text() == "agent-vm-synthetic-canary\n"
            code = '''import errno, pathlib, sys
pathlib.Path("workspace-write-ok").write_text("ok")
try:
    pathlib.Path.home().joinpath(".pgpass").read_bytes()
except OSError as exc:
    assert exc.errno in (errno.EACCES, errno.EPERM), repr(exc)
else:
    raise AssertionError("managed deny-read did not hold")
try:
    pathlib.Path(sys.argv[1]).write_text("changed")
except OSError as exc:
    assert exc.errno in (errno.EACCES, errno.EPERM, errno.EROFS), repr(exc)
else:
    raise AssertionError("outside-workspace write succeeded")
print("PASS Codex workspace write, outside-workspace write denial, and managed secret read denial")
'''
            for overrides in ([], ["-c", 'permissions.vm_dev.filesystem."~/.pgpass"="read"']):
                result = run(["codex", "sandbox", "--include-managed-config", "-P", "vm_dev", "-C", work,
                              *overrides, "python3", "-c", code, str(sibling)])
                if overrides:
                    assert result.returncode != 0 and "conflicts with a config-defined profile" in result.stderr, result.stderr
                    continue
                assert result.returncode == 0, result.stderr + result.stdout
                assert "PASS Codex" in result.stdout, result.stdout
                print(result.stdout.strip())
            assert sibling.read_text() == "original"
            print("PASS conflicting config override rejected before execution")
        finally:
            if created:
                canary.unlink()
    print("NOT TESTED: authenticated model run, private GitHub repository scope, desktop-provided tool inventory")


if __name__ == "__main__":
    main()
