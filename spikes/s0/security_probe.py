#!/usr/bin/env python3
"""macOS S0 experiment, NOT a production sandbox or Laodi implementation.

Only generated temporary fixtures and loopback collectors are used. No third-party
packages, real AI clients, user credentials, or system settings are required.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import uuid
import zlib

GIT = "/usr/bin/git"
SANDBOX = "/usr/bin/sandbox-exec"
SCRIPT = str(Path(__file__).resolve())


def clean_env(home: Path) -> dict[str, str]:
    return {
        "PATH": "/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        "HOME": str(home), "TMPDIR": str(home), "LANG": "C",
        "XDG_CONFIG_HOME": str(home), "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_TERMINAL_PROMPT": "0",
        "GIT_OPTIONAL_LOCKS": "0", "GIT_NO_LAZY_FETCH": "1",
        "GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": "user.name",
        "GIT_CONFIG_VALUE_0": "S0 Synthetic", "GIT_CONFIG_KEY_1": "user.email",
        "GIT_CONFIG_VALUE_1": "s0@example.invalid",
        "GIT_CONFIG_KEY_2": "core.hooksPath", "GIT_CONFIG_VALUE_2": str(home),
        "GIT_CONFIG_KEY_3": "commit.gpgsign", "GIT_CONFIG_VALUE_3": "false",
    }


def git(repo: Path, env: dict, *args: str) -> str:
    return subprocess.run([GIT, "-C", str(repo), *args], env=env,
                          capture_output=True, check=True, timeout=15).stdout.decode().strip()


def get_payload(action: str, target: str) -> bytes:
    if action == "git":
        return subprocess.check_output([GIT, "-C", target, "show", "HEAD~1:history-only.txt"],
                                       stderr=subprocess.PIPE, timeout=8)
    if action == "loose":
        return zlib.decompress(Path(target).read_bytes()).split(b"\0", 1)[1]
    if action == "file":
        return Path(target).read_bytes()
    if action == "env":
        return os.environ["LAODI_TEST_PRELOADED_SECRET"].encode()
    if action == "fd":
        return zlib.decompress(os.read(int(target), 100000)).split(b"\0", 1)[1]
    if action == "helper":
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(target, timeout=2) as r:
            return r.read()
    if action == "child":
        return subprocess.check_output([sys.executable, "-I", SCRIPT, "--read", "loose", target],
                                       stderr=subprocess.PIPE, timeout=8)
    if action == "detached-child":
        return subprocess.check_output([sys.executable, "-I", SCRIPT, "--read", "loose", target],
                                       stderr=subprocess.PIPE, timeout=8, start_new_session=True)
    if action == "node":
        code = "let b=require('zlib').inflateSync(require('fs').readFileSync(process.argv[1]));process.stdout.write(b.subarray(b.indexOf(0)+1))"
        return subprocess.check_output([shutil.which("node"), "-e", code, target],
                                       stderr=subprocess.PIPE, timeout=8)
    if action == "native":
        binary, blob = target.split("|", 1)
        return zlib.decompress(subprocess.check_output([binary, blob], stderr=subprocess.PIPE, timeout=8)).split(b"\0", 1)[1]
    raise ValueError(action)


def client(action: str, target: str, endpoint: str, tag: str) -> int:
    result = {"read_ok": False, "send_ok": False, "error_class": None}
    try:
        payload = get_payload(action, target)
        result["read_ok"] = True
        data = json.dumps({"tag": tag, "payload": base64.b64encode(payload).decode()}).encode()
        req = urllib.request.Request(endpoint, data=data, headers={"Content-Type": "application/json"})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(req, timeout=2) as response:
            response.read()
        result["send_ok"] = True
    except Exception as exc:
        result["error_class"] = type(exc).__name__
        result["error"] = str(exc)[:300]
        result["error_phase"] = "send" if result["read_ok"] else "read"
        result["error_errno"] = getattr(getattr(exc, "reason", exc), "errno", None)
        if isinstance(exc, subprocess.CalledProcessError):
            result["child_stderr"] = (exc.stderr or b"").decode(errors="replace")[-600:]
    print(json.dumps(result))
    return 0


class Collector:
    def __init__(self, secret: bytes, serve_secret: bool = False):
        self.records: dict[str, bytes] = {}
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                n = int(self.headers.get("Content-Length", "0"))
                if n > 100000:
                    self.send_error(413)
                    return
                item = json.loads(self.rfile.read(n))
                owner.records[item["tag"]] = base64.b64decode(item["payload"])
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"ok")

            def do_GET(self):
                # Deliberate pre-existing unsandboxed helper: synthetic data only.
                if not serve_secret:
                    self.send_error(404)
                    return
                self.send_response(200)
                self.end_headers()
                self.wfile.write(secret)

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.port = self.server.server_port
        self.url = f"http://127.0.0.1:{self.port}/receive"
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)


def profile(roots: list[Path], ports: list[int]) -> str:
    # SBPL host grammar is localhost or *, not arbitrary dotted IP literals.
    lines = ["(version 1)", "(allow default)", "(deny network*)"]
    for port in ports:
        lines.append(f'(allow network-outbound (remote ip "localhost:{port}"))')
    for root in sorted({str(p.resolve()) for p in roots}):
        lines.append(f"(deny file-read* file-write* (subpath {json.dumps(root)}))")
    return "\n".join(lines)


def main(output: Path) -> int:
    if sys.platform != "darwin":
        raise SystemExit("This probe is macOS-only")
    if not shutil.which("node"):
        raise SystemExit("Existing node runtime required; nothing is installed automatically")
    rows = []
    with tempfile.TemporaryDirectory(prefix="laodi-s0-") as temp:
        root = Path(temp).resolve()
        home = root / "home"
        home.mkdir()
        env = clean_env(home)
        source = root / "source"
        source.mkdir()
        git(source, env, "init", "-q", "--template=" + str(home))
        secret = ("LAODI_TEST_SECRET_" + uuid.uuid4().hex).encode()
        (source / "history-only.txt").write_bytes(secret)
        (source / "app.txt").write_text("PUBLIC_CURRENT_CONTENT\n")
        git(source, env, "add", ".")
        git(source, env, "commit", "-qm", "synthetic historical content")
        blob_id = git(source, env, "rev-parse", "HEAD:history-only.txt")
        blob = source / ".git/objects" / blob_id[:2] / blob_id[2:]
        git(source, env, "rm", "-q", "history-only.txt")
        git(source, env, "commit", "-qm", "remove historical content from working tree")
        assert not (source / "history-only.txt").exists()
        assert secret == git(source, env, "show", "HEAD~1:history-only.txt").encode()

        worktree = root / "worktree"
        git(source, env, "worktree", "add", "--detach", str(worktree), "HEAD")
        shared = root / "shared"
        git(source, env, "clone", "--shared", "--no-hardlinks", str(source), str(shared))
        separate = root / "separate"
        separate_git = root / "separate-git"
        git(source, env, "clone", "--no-hardlinks", "--separate-git-dir=" + str(separate_git), str(source), str(separate))
        separate_blob = separate_git / "objects" / blob_id[:2] / blob_id[2:]
        assert separate_blob.is_file(), "separate git direct-object fixture prerequisite"
        alias = root / "symlink-blob"
        alias.symlink_to(blob)
        hardlink = root / "hardlink-blob"
        os.link(blob, hardlink)

        c_source = root / "reader.c"
        c_source.write_text('#include <stdio.h>\nint main(int n,char**v){if(n!=2)return 2;FILE*f=fopen(v[1],"rb");if(!f){perror("fopen");return 3;}char b[4096];size_t k;while((k=fread(b,1,sizeof b,f)))fwrite(b,1,k,stdout);return fclose(f);}\n')
        native = root / "native-reader"
        subprocess.run(["/usr/bin/clang", str(c_source), "-o", str(native)], env=env,
                       capture_output=True, check=True, timeout=30)
        collectors = [Collector(secret), Collector(secret), Collector(secret, serve_secret=True)]
        receiver, denied, helper = collectors

        def run(case: str, action: str, target: str, roots: list[Path], ports: list[int],
                endpoint: str | None = None, expect_secret: bool | None = False,
                expected_send: bool | None = None, extra_env: dict | None = None,
                fds: tuple = (), category: str = "prevention", expected_read: bool | None = None,
                expected_errno: int | None = None):
            tag = case + "-" + uuid.uuid4().hex[:8]
            policy = profile(roots, ports)
            child_env = dict(env)
            child_env.update(extra_env or {})
            started = time.perf_counter()
            proc = subprocess.run([SANDBOX, "-p", policy, sys.executable, "-I", SCRIPT,
                                   "--client", action, target, endpoint or receiver.url, tag],
                                  env=child_env, cwd=source, capture_output=True, text=True,
                                  timeout=15, pass_fds=fds)
            try:
                result = json.loads(proc.stdout)
            except json.JSONDecodeError:
                result = {"runner_error": proc.stderr[:500]}
            received = b"".join(c.records.get(tag, b"") for c in collectors)
            got_secret = secret in received
            checked = proc.returncode == 0 and "runner_error" not in result
            if expect_secret is not None:
                checked = checked and got_secret == expect_secret
            if expected_send is not None:
                checked = checked and result.get("send_ok") == expected_send
                if expected_send is False:
                    checked = checked and len(received) == 0
            if expected_read is not None:
                checked = checked and result.get("read_ok") == expected_read
            if expected_errno is not None:
                checked = checked and result.get("error_errno") == expected_errno
            row = {"case": case, "category": category, "client_exit": proc.returncode,
                   "client": result, "secret_received": got_secret,
                   "received_bytes": len(received), "expected_secret": expect_secret,
                   "expected_send": expected_send, "expected_read": expected_read,
                   "expected_errno": expected_errno, "expectation_met": checked,
                   "elapsed_ms": round((time.perf_counter() - started) * 1000, 2),
                   "policy_sha256": hashlib.sha256(policy.encode()).hexdigest()}
            # Fixture paths are temporary; avoid persisting even their random full prefix.
            row = json.loads(json.dumps(row).replace(str(root), "<synthetic-root>").replace(SCRIPT, "<probe-script>"))
            rows.append(row)
            return row

        try:
            protected = [source / ".git"]
            both = [receiver.port, denied.port]
            run("control_git_history_upload", "git", str(source), [], both, expect_secret=True, expected_send=True, category="positive_control")
            run("control_loose_blob_upload", "loose", str(blob), [], both, expect_secret=True, expected_send=True, category="positive_control")
            for name, action, target in [
                ("git_history", "git", str(source)),
                ("python_loose_object", "loose", str(blob)),
                ("python_child", "child", str(blob)),
                ("detached_child", "detached-child", str(blob)),
                ("node_native_fs", "node", str(blob)),
                ("c_fopen", "native", str(native) + "|" + str(blob)),
                ("symlink", "loose", str(alias)),
            ]:
                run("control_" + name, action, target, [], [receiver.port], expect_secret=True,
                    expected_read=True, expected_send=True, category="positive_control")
                run(name, action, target, protected, [receiver.port], expected_send=False, expected_read=False)
            run("current_source_allowed", "file", str(source / "app.txt"), protected, [receiver.port], expected_send=True)
            run("control_second_endpoint", "file", str(source / "app.txt"), [], both, endpoint=denied.url, expected_send=True, category="positive_control")
            run("unapproved_endpoint_denied", "file", str(source / "app.txt"), protected, [receiver.port], endpoint=denied.url,
                expected_send=False, expected_read=True, expected_errno=1)
            run("offline_mode", "file", str(source / "app.txt"), protected, [], expected_send=False, expected_read=True, expected_errno=1)

            # Narrow path guards are insufficient for linked Git storage.
            for label, repo in [("worktree", worktree), ("alternate", shared), ("separate", separate)]:
                run("control_" + label, "git", str(repo), [], [receiver.port], expect_secret=True,
                    expected_read=True, expected_send=True, category="positive_control")
            run("worktree_naive_dotgit_guard", "git", str(worktree), [worktree / ".git"], [receiver.port], expect_secret=False, category="boundary")
            run("worktree_external_object_unresolved_leaks", "loose", str(blob), [worktree / ".git"],
                [receiver.port], expect_secret=True, expected_send=True, category="boundary")
            run("worktree_resolved_common_guard", "git", str(worktree), protected + [worktree / ".git"], [receiver.port], expected_send=False)
            run("worktree_external_object_resolved_denied", "loose", str(blob), protected + [worktree / ".git"],
                [receiver.port], expected_read=False, expected_send=False)
            run("alternate_objects_only_naive_guard", "git", str(shared), [shared / ".git/objects"], [receiver.port], expect_secret=False, category="boundary")
            run("alternate_external_object_unresolved_leaks", "loose", str(blob), [shared / ".git"],
                [receiver.port], expect_secret=True, expected_send=True, category="boundary")
            run("alternate_resolved_guard", "git", str(shared), [shared / ".git", source / ".git"], [receiver.port], expected_send=False)
            run("alternate_external_object_resolved_denied", "loose", str(blob), [shared / ".git", source / ".git"],
                [receiver.port], expected_read=False, expected_send=False)
            run("separate_git_resolved_guard", "git", str(separate), [separate_git, separate / ".git"], [receiver.port], expected_send=False)
            run("control_separate_direct_object", "loose", str(separate_blob), [], [receiver.port], expect_secret=True,
                expected_read=True, expected_send=True, category="positive_control")
            run("separate_external_object_unresolved_leaks", "loose", str(separate_blob), [separate / ".git"], [receiver.port],
                expect_secret=True, expected_read=True, expected_send=True, category="boundary")
            run("separate_external_object_resolved_denied", "loose", str(separate_blob), [separate_git, separate / ".git"], [receiver.port],
                expected_read=False, expected_send=False, expected_errno=1)

            # These cases measure acknowledged boundary gaps, never count as protection.
            run("hardlink_alias_probe", "loose", str(hardlink), protected, [receiver.port], expect_secret=True, category="boundary")
            run("preloaded_environment_leaks", "env", "unused", protected, [receiver.port], expect_secret=True, expected_send=True,
                extra_env={"LAODI_TEST_PRELOADED_SECRET": secret.decode()}, category="boundary")
            run("sanitized_environment", "env", "unused", protected, [receiver.port], expected_send=False)
            with open(blob, "rb") as held:
                run("inherited_fd_probe", "fd", str(held.fileno()), protected, [receiver.port], expect_secret=True, fds=(held.fileno(),), category="boundary")
                run("inherited_fd_closed", "fd", str(held.fileno()), protected, [receiver.port], expected_read=False, expected_send=False)
            run("existing_helper_allowed_leaks", "helper", helper.url, protected, [receiver.port, helper.port], expect_secret=True, expected_send=True, category="boundary")
            run("existing_helper_endpoint_denied", "helper", helper.url, protected, [receiver.port], expected_send=False,
                expected_read=False, expected_errno=1)
            report = {
                "schema_version": 1, "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "platform": {"macos": platform.mac_ver()[0], "arch": platform.machine(),
                             "python": platform.python_version(), "git": git(source, env, "--version")},
                "backend": "native sandbox-exec / Seatbelt, experimental hand-written probe only; NOT sandbox-runtime validation",
                "network": "127.0.0.1 loopback collectors only; no external upload",
                "fixtures": "fresh synthetic data, deleted automatically after test",
                "cases": rows, "all_expectations_met": all(r["expectation_met"] for r in rows),
                "limitations": ["No signed-in real AI client tested", "No production backend policy",
                                "No TLS or domain filtering tested", "No ES/NE installation or permission changes",
                                "Boundary rows may deliberately demonstrate leaks", "Existing helper is simulated, not Electron GUI"]}
        finally:
            for c in collectors:
                c.close()
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps({"output": str(output), "cases": len(rows),
                      "expectations_met": sum(r["expectation_met"] for r in rows),
                      "observations": [{"case": r["case"], "secret_received": r["secret_received"],
                                        "met": r["expectation_met"]} for r in rows]}, indent=2))
    return 0 if report["all_expectations_met"] else 1


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--read":
        sys.stdout.buffer.write(get_payload(sys.argv[2], sys.argv[3]))
    elif len(sys.argv) > 1 and sys.argv[1] == "--client":
        raise SystemExit(client(*sys.argv[2:]))
    else:
        parser = argparse.ArgumentParser()
        parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[2] / "docs/spikes/s0-security-results.json")
        raise SystemExit(main(parser.parse_args().output))
