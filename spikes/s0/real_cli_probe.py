#!/usr/bin/env python3
"""S0 only: real Codex CLI + deterministic localhost Responses stub, no model.

All CLI runs use a fresh HOME/CODEX_HOME, an outer Seatbelt profile denying the
real user's home, and network access only to the stub's ephemeral loopback port.
No installed user hooks/config/auth are used. Only temporary synthetic data is
read or changed. This is not a production sandbox or a ZCode test.
"""
from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid

CODEX = "/opt/homebrew/bin/codex"
GIT = "/usr/bin/git"
SANDBOX = "/usr/bin/sandbox-exec"


def redact_thread_ids(text: str) -> str:
    return re.sub(r'("thread_id"\s*:\s*")[^"]+("\s*[,}])', r'\1<SYNTHETIC_THREAD_ID>\2', text)


def clean_env(home: Path) -> dict[str, str]:
    return {
        "PATH": "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        "HOME": str(home), "CODEX_HOME": str(home / "codex"),
        "XDG_CONFIG_HOME": str(home / "config"), "TMPDIR": str(home),
        "SHELL": "/bin/bash", "LANG": "C", "TERM": "dumb",
        "RUST_LOG": "codex_api=debug,codex_client=debug,reqwest=debug",
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_TERMINAL_PROMPT": "0", "GIT_NO_LAZY_FETCH": "1",
        "GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": "user.name",
        "GIT_CONFIG_VALUE_0": "S0 Synthetic", "GIT_CONFIG_KEY_1": "user.email",
        "GIT_CONFIG_VALUE_1": "s0@example.invalid",
        "GIT_CONFIG_KEY_2": "core.hooksPath", "GIT_CONFIG_VALUE_2": str(home),
        "GIT_CONFIG_KEY_3": "commit.gpgsign", "GIT_CONFIG_VALUE_3": "false",
    }


def git(repo: Path, env: dict, *args: str) -> str:
    return subprocess.check_output([GIT, "-C", str(repo), *args], env=env,
                                   stderr=subprocess.PIPE, timeout=10).decode().strip()


class Stub:
    def __init__(self, command: str, repo: Path, secret: str):
        self.requests = []
        self.selected_tool = None
        self.error = None
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.send_error(404)

            def do_POST(self):
                # This accepts HTTP proxy absolute-form only for this stub's own
                # URL. It never forwards traffic or implements CONNECT.
                base = f"http://127.0.0.1:{owner.server.server_port}"
                if self.path not in ("/v1/responses", base + "/v1/responses"):
                    owner.error = "Rejected request target outside this stub: " + self.path
                    self.send_error(404)
                    return
                length = int(self.headers.get("Content-Length", "0"))
                if length > 4 * 1024 * 1024:
                    self.send_error(413)
                    return
                try:
                    request = json.loads(self.rfile.read(length))
                except (ValueError, UnicodeError) as exc:
                    owner.error = "Invalid request body: " + str(exc)
                    self.send_error(400)
                    return
                raw = json.dumps(request)
                outputs = [i for i in request.get("input", [])
                           if isinstance(i, dict) and i.get("type") == "function_call_output"]
                owner.requests.append({
                    "path": self.path, "model": request.get("model"),
                    "contains_synthetic_secret": secret in raw,
                    "function_outputs": [str(i.get("output", "")).replace(secret, "<SYNTHETIC_SECRET>")
                                         for i in outputs],
                    "tool_names": [i.get("name") for i in request.get("tools", [])],
                })
                if len(owner.requests) > 4:
                    owner.error = "Too many requests; stopped rather than retrying indefinitely"
                    self.send_error(400)
                    return
                response_id = "resp_s0_" + str(len(owner.requests))
                if len(owner.requests) == 1:
                    names = [i.get("name") for i in request.get("tools", [])]
                    if "exec_command" in names:
                        name = "exec_command"
                        args = {"cmd": command, "workdir": str(repo), "max_output_tokens": 2000,
                                "yield_time_ms": 1000, "login": False}
                    elif "shell_command" in names:
                        name = "shell_command"
                        args = {"command": command, "workdir": str(repo), "timeout_ms": 10000}
                    elif "shell" in names:
                        name = "shell"
                        args = {"command": ["/bin/bash", "--noprofile", "--norc", "-c", command],
                                "workdir": str(repo), "timeout_ms": 10000}
                    else:
                        owner.error = "No recognized deterministic shell tool in request: " + str(names)
                        self.send_error(400)
                        return
                    owner.selected_tool = name
                    item = {"type": "function_call", "id": "fc_s0", "call_id": "call_s0",
                            "name": name, "arguments": json.dumps(args), "status": "completed"}
                else:
                    item = {"type": "message", "id": "msg_s0", "role": "assistant",
                            "status": "completed", "content": [{"type": "output_text",
                            "text": "LOCAL_STUB_DONE", "annotations": []}]}
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
                response = {"id": response_id, "object": "response", "created_at": int(time.time()),
                            "model": request.get("model"), "status": "in_progress", "output": []}
                events = [
                    {"type": "response.created", "response": response},
                    {"type": "response.output_item.added", "output_index": 0, "item": item},
                    {"type": "response.output_item.done", "output_index": 0, "item": item},
                    {"type": "response.completed", "response": {**response, "status": "completed",
                     "output": [item], "usage": {"input_tokens": 1, "output_tokens": 1,
                                                  "total_tokens": 2}}},
                ]
                for seq, event in enumerate(events):
                    event["sequence_number"] = seq
                    self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event)
                                      + "\n\n").encode())
                self.wfile.flush()

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)


def run_case(root: Path, real_home: Path, blocked: bool, runtime_cli: Path | None = None) -> dict:
    label = "history_blocked" if blocked else "history_open_control"
    case = root / label
    home = case / "home"
    home.mkdir(parents=True)
    (home / "codex").mkdir()
    repo = case / "repo"
    repo.mkdir()
    env = clean_env(home)
    git(repo, env, "init", "-q", "--template=" + str(home / "config"))
    secret = "LAODI_CLI_TEST_SECRET_" + uuid.uuid4().hex
    (repo / "old.txt").write_text(secret)
    (repo / "app.py").write_text("def add(a, b):\n    return a + b\n")
    git(repo, env, "add", ".")
    git(repo, env, "commit", "-qm", "synthetic history")
    blob_id = git(repo, env, "rev-parse", "HEAD:old.txt")
    blob = repo / ".git/objects" / blob_id[:2] / blob_id[2:]
    git(repo, env, "rm", "-q", "old.txt")
    git(repo, env, "commit", "-qm", "remove history-only secret")
    helper = case / "synthetic_task.py"
    helper.write_text('''import json, pathlib, subprocess, zlib
repo = pathlib.Path(__file__).parent / "repo"
data = (repo / "app.py").read_text()
print("CURRENT_READ_OK" if "return a + b" in data else "CURRENT_READ_FAILED")
(repo / "generated.py").write_text("VALUE = 42\\n")
print("EDIT_OK")
namespace = {}
exec(data, namespace)
print("TESTS_OK" if namespace["add"](2, 3) == 5 else "TESTS_FAILED")
result = subprocess.run(["/usr/bin/git", "-C", str(repo), "show", "HEAD~1:old.txt"], capture_output=True, text=True)
print("GIT_READ_RC=" + str(result.returncode))
print(result.stdout)
print(result.stderr)
try:
    payload = zlib.decompress(pathlib.Path(BLOB).read_bytes()).split(b"\\0", 1)[1]
    print("DIRECT_READ_OK=" + payload.decode())
except Exception as exc:
    print("DIRECT_READ_ERROR=" + type(exc).__name__)
'''.replace("BLOB", repr(str(blob))))
    command = "/usr/bin/python3 -I " + shlex.quote(str(helper))
    stub = Stub(command, repo, secret)
    port = stub.server.server_port
    controlled_proxy = f"http://127.0.0.1:{port}"
    for key in ("HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"):
        env[key] = controlled_proxy
    env["NO_PROXY"] = env["no_proxy"] = ""
    sb = ["(version 1)", "(allow default)", "(deny network*)",
          f'(allow network-outbound (remote ip "localhost:{port}"))',
          f"(deny file-read* file-write* (subpath {json.dumps(str(real_home))}))"]
    if blocked:
        sb.append(f"(deny file-read* file-write* (subpath {json.dumps(str(repo / '.git'))}))")
    sandbox_file = case / "profile.sb"
    sandbox_file.write_text("\n".join(sb))
    config = {
        "model": '"gpt-5.4"', "model_provider": '"laodi_stub"',
        "model_providers.laodi_stub.name": '"Laodi local synthetic stub"',
        "model_providers.laodi_stub.base_url": json.dumps(f"http://127.0.0.1:{port}/v1"),
        "model_providers.laodi_stub.wire_api": '"responses"',
        "model_providers.laodi_stub.requires_openai_auth": "false",
        "model_providers.laodi_stub.request_max_retries": "0",
        "model_providers.laodi_stub.stream_max_retries": "0",
        "model_providers.laodi_stub.stream_idle_timeout_ms": "5000",
        "web_search": '"disabled"', "project_root_markers": "[]",
        "check_for_update_on_startup": "false", "features.shell_snapshot": "false",
        "features.skills": "false", "features.hooks": "false",
        "features.multi_agent": "false", "features.plugins": "false",
        "features.use_linux_sandbox_bwrap": "false", "analytics.enabled": "false",
        "feedback.enabled": "false",
    }
    cmd = [CODEX, "exec", "--ephemeral",
           "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--json",
           "--dangerously-bypass-approvals-and-sandbox", "-C", str(repo)]
    for key, value in config.items():
        cmd += ["-c", key + "=" + value]
    cmd += ["Execute the local synthetic test task requested by the stub provider. No network tools."]
    if runtime_cli is None:
        cmd = [SANDBOX, "-f", str(sandbox_file)] + cmd
    else:
        runtime_settings = case / "runtime.json"
        runtime_settings.write_text(json.dumps({
            "network": {"allowedDomains": [f"127.0.0.1:{port}"], "deniedDomains": [],
                        "strictAllowlist": True, "allowLocalBinding": False, "allowAllUnixSockets": False},
            "filesystem": {"denyRead": [str(real_home)] + ([str(repo / ".git")] if blocked else []),
                           "allowRead": [], "allowWrite": [str(root)],
                           "denyWrite": [str(repo / ".git")] if blocked else []}}))
        # SRT 0.0.76 adds 127.0.0.1 to NO_PROXY automatically. Our local-only
        # fixture must still go through SRT's policy proxy, not direct-connect.
        cmd = [shutil.which("node"), str(runtime_cli), "--settings", str(runtime_settings), "--",
               "/usr/bin/env", "NO_PROXY=", "no_proxy="] + cmd
    start = time.monotonic()
    process = subprocess.Popen(cmd, env=env, cwd=repo, text=True,
                               stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               start_new_session=True)
    timed_out = False
    try:
        stdout, stderr = process.communicate(timeout=35)
    except subprocess.TimeoutExpired:
        timed_out = True
        os.killpg(process.pid, signal.SIGKILL)
        stdout, stderr = process.communicate()
    finally:
        stub.close()
    outputs = "\n".join(out for req in stub.requests for out in req["function_outputs"])
    proxy_seen = "proxy(" in stderr and "intercepts" in stderr
    # SRT intentionally injects its own proxy rather than controlled_proxy.
    # 7897 is the concrete system-proxy endpoint established by the earlier run.
    unexpected_proxy = "proxy(http://127.0.0.1:7897/)" in stderr and "intercepts" in stderr
    saw_secret = any(req["contains_synthetic_secret"] for req in stub.requests)
    complete = process.returncode == 0 and len(stub.requests) >= 2 and bool(outputs)
    capabilities = {name: name in outputs for name in ("CURRENT_READ_OK", "EDIT_OK", "TESTS_OK")}
    git_codes = [int(code) for code in re.findall(r"GIT_READ_RC=(\d+)", outputs)]
    git_read_rc = git_codes[-1] if git_codes else None
    direct_read_ok = "DIRECT_READ_OK=" in outputs
    direct_permission_denied = "DIRECT_READ_ERROR=PermissionError" in outputs
    expected = (complete and stub.error is None and all(capabilities.values())
                and saw_secret == (not blocked))
    if blocked:
        expected = expected and git_read_rc is not None and git_read_rc != 0 and direct_permission_denied
    else:
        expected = expected and git_read_rc == 0 and direct_read_ok
    return {"case": label, "history_denial_enabled": blocked, "returncode": process.returncode,
            "timed_out": timed_out, "elapsed_s": round(time.monotonic() - start, 3),
            "selected_tool": stub.selected_tool, "stub_error": stub.error,
            "proxy_interception_observed": proxy_seen,
            "system_proxy_interception_observed": unexpected_proxy,
            "status": "passed" if expected else ("blocked_by_system_proxy" if unexpected_proxy else "incomplete"),
            "client_tool_cycle_completed": complete, "synthetic_secret_reached_stub": saw_secret,
            "git_read_returncode": git_read_rc, "direct_read_succeeded": direct_read_ok,
            "direct_read_permission_denied": direct_permission_denied,
            "normal_task_capabilities": capabilities, "expectation_met": expected,
            "requests": stub.requests,
            "stdout": redact_thread_ids(stdout).replace(secret, "<SYNTHETIC_SECRET>").replace(str(root), "<TEMP>"),
            "stderr": redact_thread_ids(stderr).replace(secret, "<SYNTHETIC_SECRET>").replace(str(root), "<TEMP>")}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--runtime-dir", type=Path,
                        help="Existing temporary install of @anthropic-ai/sandbox-runtime 0.0.76")
    args = parser.parse_args()
    runtime_cli = None
    runtime_metadata = None
    if args.runtime_dir:
        runtime_dir = args.runtime_dir.resolve()
        package_dir = runtime_dir / "node_modules/@anthropic-ai/sandbox-runtime"
        package = json.loads((package_dir / "package.json").read_text())
        if package["version"] != "0.0.76" or not shutil.which("node"):
            parser.error("Runtime mode requires the inspected 0.0.76 package and existing Node")
        runtime_cli = package_dir / "dist/cli.js"
        if not runtime_cli.is_file():
            parser.error("Installed runtime CLI path is missing")
        lock = (runtime_dir / "package-lock.json").read_bytes()
        entry = next(value for key, value in json.loads(lock)["packages"].items()
                     if key.endswith("node_modules/@anthropic-ai/sandbox-runtime"))
        runtime_metadata = {"name": "@anthropic-ai/sandbox-runtime", "version": package["version"],
                            "integrity": entry["integrity"],
                            "package_lock_sha256": hashlib.sha256(lock).hexdigest()}
    previous_experiments = []
    if args.output.exists():
        previous = json.loads(args.output.read_text())
        previous_experiments = previous.get("previous_experiments", []) + [{
            "experiment": previous.get("experiment"), "proxy_strategy": previous.get("proxy_strategy", "NO_PROXY_only"),
            "all_expectations_met": previous.get("all_expectations_met"),
            "cases": [{key: row.get(key) for key in ("case", "status", "returncode", "client_tool_cycle_completed",
                       "system_proxy_interception_observed", "synthetic_secret_reached_stub")}
                      | {"received_request_count": len(row.get("requests", []))} for row in previous["cases"]]}]
    if sys.platform != "darwin" or not all(Path(p).is_file() for p in (CODEX, GIT, SANDBOX)):
        parser.error("Requires existing macOS sandbox-exec, Git, and /opt/homebrew/bin/codex")
    real_home = Path.home().resolve()
    # SRT's Unix socket names must fit macOS sockaddr_un; keep TMPDIR short.
    with tempfile.TemporaryDirectory(prefix="lc-", dir="/private/tmp") as temp:
        root = Path(temp).resolve()
        if root.is_relative_to(real_home):
            parser.error("Temporary root must be outside the actual user's home")
        rows = [run_case(root, real_home, blocked, runtime_cli) for blocked in (False, True)]
        # Version is a read-only CLI metadata query, not a logged-in session.
        version = subprocess.check_output([CODEX, "--version"], text=True, timeout=5).strip()
        report = {"schema_version": 1, "experiment": "real_cli_local_stub", "client": version,
                  "backend": "sandbox-runtime" if runtime_cli else "native-seatbelt",
                  "runtime": runtime_metadata,
                  "platform": platform.platform(), "date": time.strftime("%Y-%m-%d"),
                  "provider": "deterministic Responses stub, no real model or provider account",
                  "proxy_strategy": ("SRT injects its policy proxy; host proxy variables point only to nonforwarding stub"
                                     if runtime_cli else "all six explicit proxy variables point to the same stub; no forwarding"),
                  "safety": {"network": ("SRT strictAllowlist contains only stub literal 127.0.0.1:port"
                                         if runtime_cli else "outer deny network*, permit only one localhost port"),
                             "user_home": "outer deny file-read* and file-write*",
                             "config_auth": "fresh HOME/CODEX_HOME + --ignore-user-config + --ignore-rules",
                             "fixtures": "temporary synthetic repository and scripts; removed after run"},
                  "limitations": ["Not a ZCode GUI test or vendor model test",
                                  "Experimental backend integration only; no product launcher or system extension validated",
                                  "Only an isolated fresh CLI process tree is tested",
                                  "Allowed source code reaches the approved provider endpoint by design"],
                  "previous_experiments": previous_experiments,
                  "cases": rows, "all_expectations_met": all(row["expectation_met"] for row in rows)}
        data = json.dumps(report, ensure_ascii=False, indent=2).replace(str(root), "<TEMP>")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(data + "\n")
    print(json.dumps({"output": str(args.output), "all_expectations_met": report["all_expectations_met"],
                      "cases": [{k: row[k] for k in ("case", "returncode", "client_tool_cycle_completed",
                                 "synthetic_secret_reached_stub", "expectation_met", "stub_error")}
                                for row in rows]}, indent=2))
    return 0 if report["all_expectations_met"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
