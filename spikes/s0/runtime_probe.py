#!/usr/bin/env python3
"""Probe a separately downloaded, pinned sandbox-runtime; synthetic/loopback only."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.parse
import uuid

from security_probe import Collector, clean_env, get_payload, git


def client(action, target, endpoint, tag, direct):
    result = {"read_ok": False, "send_ok": False}
    try:
        payload = get_payload(action, target)
        result["read_ok"] = True
        destination = urllib.parse.urlsplit(endpoint)
        headers = {}
        if direct == "true":
            peer = (destination.hostname, destination.port)
            request_target = destination.path
        else:
            proxy = urllib.parse.urlsplit(os.environ["HTTP_PROXY"])
            peer = (proxy.hostname, proxy.port)
            request_target = endpoint
            if proxy.username:
                auth = urllib.parse.unquote(proxy.username) + ":" + urllib.parse.unquote(proxy.password or "")
                headers["Proxy-Authorization"] = "Basic " + base64.b64encode(auth.encode()).decode()
        data = json.dumps({"tag": tag, "payload": base64.b64encode(payload).decode()}).encode()
        headers.update({"Host": destination.netloc, "Content-Type": "application/json",
                        "Content-Length": str(len(data)), "Connection": "close"})
        request = ("POST " + request_target + " HTTP/1.1\r\n" +
                   "\r\n".join(k + ": " + v for k, v in headers.items()) + "\r\n\r\n").encode() + data
        with socket.create_connection(peer, timeout=3) as connection:
            connection.sendall(request)
            response = connection.recv(4096)
        status = int(response.split(b" ", 2)[1])
        result["http_status"] = status
        result["send_ok"] = status == 200
    except Exception as exc:
        result["error_class"] = type(exc).__name__
        result["error_errno"] = getattr(exc, "errno", None)
        result["error_phase"] = "send" if result["read_ok"] else "read"
        if isinstance(exc, subprocess.CalledProcessError):
            result["child_stderr"] = (exc.stderr or b"").decode(errors="replace")[-300:]
    print(json.dumps(result))


def main(runtime_dir, output):
    runtime_dir = runtime_dir.resolve()
    pkg = runtime_dir / "node_modules/@anthropic-ai/sandbox-runtime"
    package = json.loads((pkg / "package.json").read_text())
    if package["version"] != "0.0.76":
        raise SystemExit("Only the inspected version 0.0.76 is accepted by this S0 experiment")
    cli = pkg / "dist/cli.js"
    node = shutil.which("node")
    rows = []
    # macOS sockaddr_un paths are bounded; the default /var/folders tree is too
    # long once SRT appends its mux socket filename beneath our isolated TMPDIR.
    with tempfile.TemporaryDirectory(prefix="laodi-srt-probe-", dir="/private/tmp") as temp:
        root = Path(temp).resolve()
        home = root / "home"
        home.mkdir()
        env = clean_env(home)
        env["SHELL"] = "/bin/bash"
        repo = root / "repo"
        repo.mkdir()
        git(repo, env, "init", "-q", "--template=" + str(home))
        secret = ("LAODI_SRT_SECRET_" + uuid.uuid4().hex).encode()
        (repo / "history-only.txt").write_bytes(secret)
        (repo / "current.txt").write_text("SYNTHETIC_PUBLIC_SOURCE")
        git(repo, env, "add", ".")
        git(repo, env, "commit", "-qm", "synthetic history")
        oid = git(repo, env, "rev-parse", "HEAD:history-only.txt")
        blob = repo / ".git/objects" / oid[:2] / oid[2:]
        git(repo, env, "rm", "-q", "history-only.txt")
        git(repo, env, "commit", "-qm", "current source only")
        first, second = Collector(secret), Collector(secret)
        try:
            tests = [
                ("control_git_history", "git", str(repo), False, first, False, True, True, None),
                ("control_native_blob", "loose", str(blob), False, first, False, True, True, None),
                ("control_child_history", "child", str(blob), False, first, False, True, True, None),
                ("history_git_denied", "git", str(repo), True, first, False, False, False, None),
                ("history_native_blob_denied", "loose", str(blob), True, first, False, False, False, 1),
                ("history_child_denied", "child", str(blob), True, first, False, False, False, None),
                ("current_source_allowed", "file", str(repo / "current.txt"), True, first, False, True, True, None),
                ("control_second_endpoint", "file", str(repo / "current.txt"), False, second, False, True, True, None),
                ("unapproved_endpoint_denied", "file", str(repo / "current.txt"), True, second, False, True, False, None),
                ("direct_connect_bypass_denied", "file", str(repo / "current.txt"), True, first, True, True, False, 1),
            ]
            for name, action, target, protected, receiver, direct, read_ok, send_ok, errno in tests:
                tag = uuid.uuid4().hex
                allowed = [f"127.0.0.1:{first.port}"]
                if not protected:
                    allowed.append(f"127.0.0.1:{second.port}")
                config = {
                    "network": {"allowedDomains": allowed, "deniedDomains": [], "strictAllowlist": True,
                                "allowLocalBinding": False, "allowAllUnixSockets": False},
                    "filesystem": {"denyRead": [str(Path.home().resolve())] + ([str(repo / ".git")] if protected else []),
                                   "allowRead": [str(Path(__file__).resolve().parent)],
                                   "allowWrite": [str(root)], "denyWrite": [str(repo / ".git")] if protected else []}}
                settings = root / (name + ".json")
                settings.write_text(json.dumps(config))
                argv = [node, str(cli), "--settings", str(settings), "--", sys.executable,
                        str(Path(__file__).resolve()), "--client", action, target, receiver.url, tag, str(direct).lower()]
                start = time.monotonic()
                proc = subprocess.run(argv, cwd=repo, env=env, capture_output=True, text=True, timeout=25)
                candidates = [line for line in proc.stdout.splitlines() if line.startswith('{"read_ok"')]
                result = json.loads(candidates[-1]) if candidates else {"runner_error": True}
                received = first.records.get(tag, b"") + second.records.get(tag, b"")
                expected_secret = not protected and action in ("git", "loose", "child")
                met = (proc.returncode == 0 and result.get("read_ok") == read_ok and
                       result.get("send_ok") == send_ok and (secret in received) == expected_secret)
                if not send_ok:
                    met = met and len(received) == 0
                if errno is not None:
                    met = met and result.get("error_errno") == errno
                if name == "unapproved_endpoint_denied":
                    met = met and result.get("http_status") == 403
                if name == "history_child_denied":
                    child_error = result.get("child_stderr", "")
                    met = met and "PermissionError" in child_error and "Operation not permitted" in child_error and ".git/objects/" in child_error
                row = {"case": name, "runtime_exit": proc.returncode, "client": result,
                       "received_bytes": len(received), "secret_received": secret in received,
                       "expectation_met": met, "elapsed_ms": round((time.monotonic() - start)*1000, 2)}
                if not met:
                    row["runtime_stderr"] = proc.stderr[-2000:]
                    row["runtime_stdout"] = proc.stdout[-2000:]
                rows.append(json.loads(json.dumps(row).replace(str(root), "<synthetic-root>")))
        finally:
            first.close()
            second.close()
    lock = (runtime_dir / "package-lock.json").read_bytes()
    lock_data = json.loads(lock)
    runtime_lock = next(value for key, value in lock_data["packages"].items()
                        if key.endswith("node_modules/@anthropic-ai/sandbox-runtime"))
    report = {"schema_version": 1, "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),
              "platform": platform.mac_ver()[0], "runtime_version": package["version"],
              "runtime_integrity": runtime_lock["integrity"],
              "package_lock_sha256": hashlib.sha256(lock).hexdigest(),
              "cases": rows, "all_expectations_met": all(r["expectation_met"] for r in rows),
              "installation": "temporary directory only, official npm registry, --ignore-scripts; no global/project dependency changes",
              "limitations": ["Synthetic Python client, not real AI client", "Loopback HTTP only; no TLS or public DNS/domain test",
                              "Read restrictions and proxy-mediated port policy tested; no ES/NE",
                              "Runtime startup time included; tiny samples not performance benchmark"]}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n")
    output.with_name("s0-runtime-package-lock.json").write_bytes(lock)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report["all_expectations_met"] else 1


if __name__ == "__main__":
    if len(sys.argv)>1 and sys.argv[1]=="--client":
        client(*sys.argv[2:])
    else:
        parser=argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--runtime-dir",type=Path,required=True)
        parser.add_argument("--output",type=Path,default=Path(__file__).resolve().parents[2]/"docs/spikes/s0-runtime-results.json")
        args=parser.parse_args()
        raise SystemExit(main(args.runtime_dir,args.output))
