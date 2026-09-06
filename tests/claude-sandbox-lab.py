#!/usr/bin/env python3
"""Exercise Claude Code's real Bash sandbox with no model, sign-in, or network.

    python3 tests/claude-sandbox-lab.py [--settings FILE] [--fixture DIR] [--keep]

A loopback stub of the Anthropic Messages API asks Claude Code to run one
probe command through its Bash tool, then ends the turn. Claude Code applies
its ordinary sandbox to that command, so the probe observes real enforcement:
a workspace write, a write outside the workspace, a read of a synthetic canary
listed in denyRead, and an outbound connection. Only synthetic files are used.

Run it from a terminal. Inside an agent session the nested sandbox cannot start
and loopback listeners are refused. Keep the fixture outside /tmp: the sandbox
leaves the session temp directory writable, so a /tmp fixture cannot show the
outside-workspace denial. --settings tests another settings file (for example
config/claude/managed-settings.json); the lab appends its canary to that file's
denyRead and Read deny lists and disables the model's escape hatches, but it
cannot reproduce managed-only locks, which need the file installed as policy.
"""
import argparse
import json
import os
import shutil
import socketserver
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

PROBE = r"""#!/bin/sh
echo "PROBE write-cwd: $(touch ./workspace-write-ok 2>/dev/null && echo ok || echo DENIED)"
echo "PROBE write-sibling: $(sh -c "echo changed > \"$SIBLING\"" 2>/dev/null && echo ALLOWED || echo denied)"
echo "PROBE read-canary: $(head -c1 "$CANARY" >/dev/null 2>&1 && echo READABLE || echo denied)"
echo "PROBE read-system: $(head -c1 /etc/hosts >/dev/null 2>&1 && echo readable || echo DENIED)"
echo "PROBE net-example: $(curl -s -o /dev/null -w '%{http_code}' --max-time 5 https://example.com 2>/dev/null || echo blocked)"
"""

EXPECT = {
    "write-cwd": "ok",
    "write-sibling": "denied",
    "read-canary": "denied",
    "read-system": "readable",
}


def sse(events):
    return "".join("event: %s\ndata: %s\n\n" % (name, json.dumps(data)) for name, data in events).encode()


class Stub(HTTPServer):
    allow_reuse_address = True

    def server_bind(self):
        # HTTPServer.server_bind calls getfqdn(), which can hang without DNS.
        socketserver.TCPServer.server_bind(self)
        self.server_name = "127.0.0.1"
        self.server_port = self.server_address[1]


def make_handler(command, results):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            self.send_response(404)
            self.end_headers()

        def do_HEAD(self):
            self.send_response(404)
            self.end_headers()

        def do_POST(self):
            length = int(self.headers.get("content-length", "0") or 0)
            body = self.rfile.read(length) if length else b""
            if "/messages" not in self.path or "count_tokens" in self.path:
                self.send_response(200)
                self.send_header("content-type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"input_tokens": 1}')
                return
            try:
                request = json.loads(body or b"{}")
            except ValueError:
                request = {}
            tool_results = []
            for message in request.get("messages", []):
                if not isinstance(message.get("content"), list):
                    continue
                for block in message["content"]:
                    if isinstance(block, dict) and block.get("type") == "tool_result":
                        content = block.get("content")
                        if not isinstance(content, str):
                            content = "".join(part.get("text", "") for part in content if isinstance(part, dict))
                        tool_results.append(content)
            results.extend(tool_results)
            usage = {"input_tokens": 1, "output_tokens": 1}
            start = {"id": "msg_stub", "type": "message", "role": "assistant", "model": request.get("model", "stub"),
                     "content": [], "stop_reason": None, "stop_sequence": None, "usage": usage}
            if not tool_results:
                events = [
                    ("message_start", {"type": "message_start", "message": start}),
                    ("content_block_start", {"type": "content_block_start", "index": 0,
                                             "content_block": {"type": "tool_use", "id": "toolu_stub", "name": "Bash", "input": {}}}),
                    ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                             "delta": {"type": "input_json_delta",
                                                       "partial_json": json.dumps({"command": command, "description": "sandbox lab probe"})}}),
                    ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                    ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use", "stop_sequence": None}, "usage": usage}),
                    ("message_stop", {"type": "message_stop"}),
                ]
            else:
                events = [
                    ("message_start", {"type": "message_start", "message": start}),
                    ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
                    ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "STUB-DONE"}}),
                    ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                    ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": usage}),
                    ("message_stop", {"type": "message_stop"}),
                ]
            payload = sse(events)
            self.send_response(200)
            self.send_header("content-type", "text/event-stream")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    return Handler


def lab_settings(base_file, canary):
    settings = {}
    if base_file:
        with open(base_file) as handle:
            settings = json.load(handle)
    sandbox = settings.setdefault("sandbox", {})
    sandbox["enabled"] = True
    sandbox["failIfUnavailable"] = True
    sandbox["allowUnsandboxedCommands"] = False
    sandbox["autoAllowBashIfSandboxed"] = True
    filesystem = sandbox.setdefault("filesystem", {})
    filesystem.setdefault("denyRead", []).append(canary)
    if not base_file:
        sandbox["network"] = {"allowedDomains": []}
    permissions = settings.setdefault("permissions", {})
    permissions.setdefault("deny", []).append("Read(%s)" % canary)
    return settings


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--settings", help="settings file to exercise instead of the built-in lab policy")
    parser.add_argument("--fixture", help="fixture directory (default: .build/claude-sandbox-lab in this checkout)")
    parser.add_argument("--keep", action="store_true", help="retain the fixture and logs")
    args = parser.parse_args()

    checkout = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    fixture = os.path.abspath(args.fixture or os.path.join(checkout, ".build", "claude-sandbox-lab"))
    if fixture.startswith(("/tmp/", "/private/tmp/")):
        sys.exit("fixture must not live under /tmp; the sandbox leaves temp directories writable")
    if os.environ.get("CLAUDECODE"):
        sys.exit("run this lab from a terminal, not inside an agent session")
    if shutil.which("claude") is None:
        sys.exit("claude is not on PATH")

    shutil.rmtree(fixture, ignore_errors=True)
    workspace = os.path.join(fixture, "workspace")
    os.makedirs(workspace, mode=0o700)
    canary = os.path.join(fixture, "canary-secret")
    sibling = os.path.join(fixture, "sibling")
    probe = os.path.join(fixture, "probe.sh")
    with open(canary, "w") as handle:
        handle.write("dev-sandbox-synthetic-canary\n")
    with open(sibling, "w") as handle:
        handle.write("original")
    with open(probe, "w") as handle:
        handle.write(PROBE)
    settings = lab_settings(args.settings, canary)
    settings_path = os.path.join(fixture, "settings.json")
    with open(settings_path, "w") as handle:
        json.dump(settings, handle, indent=2)
    # A bare "*" in allowedDomains means direct network by design (the embedded
    # managed policy); anything else must leave example.com unreachable.
    open_network = "*" in settings["sandbox"].get("network", {}).get("allowedDomains", [])

    command = "CANARY='%s' SIBLING='%s' sh '%s'" % (canary, sibling, probe)
    results = []
    server = Stub(("127.0.0.1", 0), make_handler(command, results))
    threading.Thread(target=server.serve_forever, daemon=True).start()

    env = {k: v for k, v in os.environ.items() if k not in ("CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "SSH_AUTH_SOCK")}
    env.update({
        "ANTHROPIC_API_KEY": "synthetic-key",
        "ANTHROPIC_BASE_URL": "http://127.0.0.1:%d" % server.server_port,
        "CLAUDE_CONFIG_DIR": os.path.join(fixture, "config"),
        "DISABLE_AUTOUPDATER": "1",
        "DISABLE_UPDATES": "1",
        "DISABLE_TELEMETRY": "1",
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    })
    argv = ["claude", "-p", "--bare", "--model", "stub-model", "--settings", settings_path,
            "--allowedTools", "Bash", "--permission-prompts", "none", "--max-turns", "3",
            "--output-format", "json", "Run the sandbox lab probe."]
    try:
        completed = subprocess.run(argv, cwd=workspace, env=env, stdin=subprocess.DEVNULL,
                                   capture_output=True, text=True, timeout=180)
    finally:
        server.shutdown()
    with open(os.path.join(fixture, "claude.out"), "w") as handle:
        handle.write(completed.stdout)
    with open(os.path.join(fixture, "claude.err"), "w") as handle:
        handle.write(completed.stderr)

    failed = False
    try:
        result = json.loads(completed.stdout.strip().splitlines()[-1])
        ok = result.get("subtype") == "success" and result.get("result") == "STUB-DONE"
    except (ValueError, IndexError):
        ok = False
    print("%s claude -p completed through the stub (exit %d)" % ("PASS" if ok else "FAIL", completed.returncode))
    failed |= not ok
    if not results:
        print("FAIL no Bash tool result reached the stub; see %s" % os.path.join(fixture, "claude.err"))
        failed = True
    observed = {}
    for line in "\n".join(results).splitlines():
        if line.startswith("PROBE ") and ": " in line:
            key, value = line[6:].split(": ", 1)
            observed[key] = value
    for key, want in EXPECT.items():
        got = observed.get(key, "missing")
        print("%s %s: %s" % ("PASS" if got == want else "FAIL", key, got))
        failed |= got != want
    net = observed.get("net-example", "missing")
    blocked = net in ("blocked", "000blocked", "000")
    reachable = net.isdigit() and net != "000"
    net_ok = reachable if open_network else blocked
    print("%s net-example: %s (policy %s)" % ("PASS" if net_ok else "FAIL", net,
                                              "allows every domain" if open_network else "blocks the domain"))
    failed |= not net_ok
    with open(sibling) as handle:
        unchanged = handle.read() == "original"
    print("%s outside-workspace file unchanged" % ("PASS" if unchanged else "FAIL"))
    failed |= not unchanged
    if args.keep or failed:
        print("Fixture retained: %s" % fixture)
    else:
        shutil.rmtree(fixture, ignore_errors=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
