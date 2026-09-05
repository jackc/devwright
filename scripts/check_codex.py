#!/usr/bin/env python3
"""Read effective Codex policy through its app-server API; no model or credentials needed."""
import json
import selectors
import subprocess
import time


def check():
    with subprocess.Popen(["codex", "app-server", "--stdio", "--strict-config",
                           "-c", "features.apps=true", "-c", "features.plugins=true"],
                          stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, text=True, bufsize=1) as process:
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)

        def send(message):
            process.stdin.write(json.dumps(message) + "\n")
            process.stdin.flush()

        def request(method, params, ident):
            send({"id": ident, "method": method, "params": params})
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                if not selector.select(timeout=1):
                    continue
                line = process.stdout.readline()
                if not line:
                    raise RuntimeError("Codex app-server exited before replying")
                message = json.loads(line)
                if message.get("id") == ident:
                    if "error" in message:
                        raise RuntimeError(str(message["error"]))
                    return message["result"]
            raise TimeoutError(method)

        try:
            request("initialize", {"clientInfo": {"name": "agent_vm_verify", "version": "1.0"},
                                   "capabilities": {"experimentalApi": True}}, 1)
            send({"method": "initialized"})
            requirements = request("configRequirements/read", {}, 2)
            config = request("config/read", {"includeLayers": False}, 3)
            return requirements, config
        finally:
            selector.close()
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == "__main__":
    requirements, config = check()
    policy = requirements["requirements"]
    assert policy["allowedPermissionProfiles"] == {"vm_dev": True}
    assert policy["defaultPermissions"] == "vm_dev"
    assert config["config"]["default_permissions"] == "vm_dev"
    keys = ("apps", "plugins", "browser_use", "in_app_browser", "computer_use")
    assert all(policy["featureRequirements"][key] is False for key in keys)
    # config/read returns raw requested feature settings, not resolved feature pins.
    result = subprocess.run(["codex", "-c", "features.apps=true", "-c", "features.plugins=true",
                             "features", "list"], text=True, capture_output=True, check=True, timeout=20)
    features = {line.split()[0]: line.split()[-1] for line in result.stdout.splitlines() if line.split()}
    assert all(features[key] == "false" for key in keys), "Managed feature pin was not enforced"
    print("PASS Codex app-server reads managed profile; resolved features reject apps/plugins overrides")
