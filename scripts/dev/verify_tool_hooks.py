#!/usr/bin/env python3
"""Exercise installed hook commands in a synthetic HOME; no Agent or model runs."""

import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import statistics
import subprocess
import tempfile
import time


def main(binary, output):
    binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    version = subprocess.check_output([str(binary), "version"], text=True).strip()
    timings = []
    with tempfile.TemporaryDirectory(prefix="laodi-hooks-") as temp:
        root = Path(temp).resolve()
        home, data = root / "home", root / "state"
        home.mkdir(mode=0o700)
        env = {"HOME": str(home), "PATH": "/usr/bin:/bin"}
        token = "ghp_Q7m2Lp9Rx4Vt8Na3Ks6Yw1Bd5Jc0HfUzEeAi"
        marker = "SESSION-CANARY-should-not-be-stored"
        paths = {"zcode": home / ".zcode/cli/config.json", "claude-code": home / ".claude/settings.json"}
        commands = {}
        for adapter, path in paths.items():
            path.parent.mkdir(parents=True, mode=0o700)
            path.write_text(json.dumps({"unrelated": "preserve-me"}))
            original = path.read_bytes()
            state_before = {str(p.relative_to(data)): p.read_bytes() for p in data.rglob("*") if p.is_file()} if data.exists() else {}
            command = [str(binary), "hooks", "install", "--adapter", adapter, "--state-dir", str(data)]
            preview = subprocess.run(command, env=env, capture_output=True, text=True, check=True)
            state_after = {str(p.relative_to(data)): p.read_bytes() for p in data.rglob("*") if p.is_file()} if data.exists() else {}
            assert path.read_bytes() == original and state_before == state_after
            assert "preserve-me" not in preview.stdout
            subprocess.run(command + ["--apply"], env=env, capture_output=True, check=True)
            installed = path.read_bytes()
            subprocess.run(command + ["--apply"], env=env, capture_output=True, check=True)
            assert path.read_bytes() == installed
            settings = json.loads(installed)
            assert settings["unrelated"] == "preserve-me"
            events = settings["hooks"]["events"] if adapter == "zcode" else settings["hooks"]
            for event in ("PreToolUse", "PostToolUse", "PostToolUseFailure"):
                entry = events[event][0]
                assert entry["matcher"] == "Bash|Read" and entry["hooks"][0]["async"] is True
                commands[(adapter, event)] = entry["hooks"][0]["command"]

        def payload(event, tool_id, normal=False):
            return {"hook_event_name": event, "session_id": marker, "tool_use_id": tool_id,
                    "cwd": "/PATH-CANARY/project", "transcript_path": "/PATH-CANARY/transcript",
                    "tool_name": "Bash", "tool_input": {"command": "ls ~/.ssh" if normal else "cat /PATH-CANARY/.env"},
                    "tool_response": {"stdout": "a" * 64 if normal else token, "stderr": ""}}

        def fire(adapter, event, body):
            start = time.monotonic()
            result = subprocess.run(["/bin/sh", "-c", commands[(adapter, event)]], input=body,
                                    env=env, capture_output=True, text=True, timeout=4)
            assert result.returncode == 0 and result.stdout == "" and result.stderr == ""
            return time.monotonic() - start

        def wait_for(expected):
            deadline = time.monotonic() + 8
            while time.monotonic() < deadline:
                assert process.poll() is None, "monitor exited"
                try:
                    saved = json.loads((data / "state.json").read_text())
                except FileNotFoundError:
                    time.sleep(0.02)
                    continue
                counts = Counter(e["kind"] for e in saved.get("events") or [])
                status = json.loads(subprocess.check_output([str(binary), "hooks", "status", "--state-dir", str(data)], env=env))
                if saved.get("initialized") and counts == Counter(expected) and status["pending"] == 0:
                    return saved
                time.sleep(0.04)
            raise AssertionError("event/queue expectations not reached")

        # All notifications are captured by a synthetic helper, never macOS.
        helper, notices = root / "notice.sh", root / "notice-args"
        helper.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + str(notices) + "'\nprintf '{\"delivery\":\"accepted_by_os\"}'\n")
        helper.chmod(0o700)
        command = [str(binary), "watch", "--hooks-only", "--state-dir", str(data),
                   "--interval", "1s", "--duration", "30s", "--notifier", str(helper)]
        with (root / "monitor-output").open("w") as stdout, (root / "monitor-error").open("w") as stderr:
            process = subprocess.Popen(command, env=env, stdout=stdout, stderr=stderr)
            try:
                wait_for({})
                for adapter in paths:
                    for event in ("PreToolUse", "PostToolUse"):
                        timings.append(fire(adapter, event, json.dumps(payload(event, "TOOL-CANARY", False))))
                        timings.append(fire(adapter, event, json.dumps(payload(event, "normal", True))))
                expected = {"sensitive_tool_access_requested": 2, "sensitive_tool_output_detected": 2}
                wait_for(expected)
                work = [(list(paths)[i % 2], "PostToolUse", json.dumps(payload("PostToolUse", "parallel-" + str(i)))) for i in range(6)]
                with ThreadPoolExecutor(max_workers=4) as pool:
                    timings.extend(pool.map(lambda item: fire(*item), work))
                expected["sensitive_tool_output_detected"] += 6
                wait_for(expected)
                partial = payload("PostToolUse", "partial")
                partial["tool_name"] = "Read"
                partial["tool_response"] = {"truncated": True, "content": [{"type": "text", "text": token}, {"type": "image", "data": "not-scanned"}]}
                timings.append(fire("zcode", "PostToolUse", json.dumps(partial)))
                timings.append(fire("claude-code", "PostToolUse", "{"))
                expected["sensitive_tool_output_detected"] += 1
                expected["hook_coverage_degraded"] = 2
                saved = wait_for(expected)
                assert not any(e["baseline_existing"] for e in saved["events"])
                assert all(e["notification"] == "recorded_only" for e in saved["events"] if e["kind"] == "sensitive_tool_access_requested")
            finally:
                if process.poll() is None:
                    process.terminate()
                process.wait(timeout=5)
        assert process.returncode == 0
        # Restart after a duplicate submission: the same observation must not reappear.
        timings.append(fire("zcode", "PostToolUse", json.dumps(payload("PostToolUse", "TOOL-CANARY"))))
        command[command.index("--duration") + 1] = "2s"
        restarted = subprocess.run(command, env=env, capture_output=True, timeout=6)
        assert restarted.returncode == 0
        saved = json.loads((data / "state.json").read_text())
        assert Counter(e["kind"] for e in saved["events"]) == Counter(expected)
        assert not saved.get("running", False)
        assert json.loads(subprocess.check_output([str(binary), "hooks", "status", "--state-dir", str(data)], env=env))["pending"] == 0
        summary = subprocess.check_output([str(binary), "incidents", "--state-dir", str(data), "--format", "agent-summary"], env=env)
        for private in (token, marker, "TOOL-CANARY", "PATH-CANARY"):
            assert private.encode() not in summary
            assert private.encode() not in restarted.stdout + restarted.stderr
            assert private not in (root / "monitor-output").read_text() + (root / "monitor-error").read_text()
            for path in data.rglob("*"):
                if path.is_file():
                    assert private.encode() not in path.read_bytes(), str(path)
        for adapter, path in paths.items():
            subprocess.run([str(binary), "hooks", "uninstall", "--adapter", adapter, "--state-dir", str(data), "--apply"], env=env, capture_output=True, check=True)
            assert json.loads(path.read_text()) == {"unrelated": "preserve-me"}
    assert hashlib.sha256(binary.read_bytes()).hexdigest() == binary_hash
    result = {"version": version, "binary_sha256": binary_hash, "verification_passed": True,
              "adapters": ["zcode", "claude-code"], "event_counts": expected,
              "hook_invocations": len(timings), "hook_seconds_median": statistics.median(timings),
              "hook_seconds_max": max(timings), "concurrent_producers": 4,
              "configuration_preserved_and_uninstalled": True, "hook_stdout_stderr_empty": True,
              "no_secret_or_raw_identity_in_persisted_state": True, "restart_duplicate_events": 0,
              "real_agent_started": False, "network_operations_requested": False, "system_notifications_sent": False,
              "scope": "Generated POSIX hook commands and actual Laodi binary under temporary HOME; does not verify real client callback dispatch."}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    main(args.binary.resolve(), args.output)
