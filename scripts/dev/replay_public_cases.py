#!/usr/bin/env python3
"""Replay public-case metadata through the actual monitor; never run ZCode."""

import argparse
from collections import Counter
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def main(binary, output):
    before = hashlib.sha256(binary.read_bytes()).hexdigest()
    version = subprocess.check_output([str(binary), "version"], text=True).strip()
    measurements = []
    with tempfile.TemporaryDirectory(prefix="laodi-public-replay-") as temp:
        root = Path(temp)
        evidence, local = root / "checkpoints", root / "laodi-state"
        evidence.mkdir()
        workspace = evidence / "0123456789ab"
        key = "/SYNTHETIC/private-project"
        a, b, c = "a" * 64, "b" * 64, "c" * 64

        def write(path, data):
            path.parent.mkdir(parents=True, exist_ok=True)
            staging = path.with_suffix(".staging")
            staging.write_text(json.dumps(data))
            os.replace(staging, path)

        def state(**fields):
            write(workspace / "state.json", {"workspaceKey": key, "workspacePath": key, **fields})

        def await_events(expected):
            until = time.monotonic() + 8
            while time.monotonic() < until:
                if process.poll() is not None:
                    raise RuntimeError("monitor exited before evidence was recorded")
                try:
                    saved = json.loads((local / "state.json").read_text())
                except FileNotFoundError:
                    saved = {}
                if saved.get("initialized"):
                    counts = Counter(event["kind"] for event in saved.get("events") or [])
                    if all(counts[kind] == count for kind, count in expected.items()):
                        return saved
                time.sleep(0.02)
            raise RuntimeError("synthetic replay exceeded the bounded observation deadline")

        env = {"HOME": str(root), "PATH": "/usr/bin:/bin"}
        with (root / "stdout").open("w") as stdout, (root / "stderr").open("w") as stderr:
            process = subprocess.Popen([
                str(binary), "watch", "--root", str(evidence), "--state-dir", str(local),
                "--build", "3.12.3.7463", "--duration", "30s",
            ], env=env, stdout=stdout, stderr=stderr)
            try:
                await_events({})
                start = time.monotonic()
                write(workspace / "manifests" / (a + ".json"), {
                    "schema": "repo_snapshot_manifest/v2", "workspaceKey": key,
                    "files": [{"path": ".git/objects/pack/SYNTHETIC.pack", "sizeBytes": 257535221}],
                })
                write(workspace / "extra-manifests" / (b + ".json"), {
                    "schema": "repo_snapshot_extra_manifest/v1",
                    "groups": [{"groupId": "global-configs", "files": [
                        {"path": "settings.behavior.json", "sizeBytes": 10, "contentHash": c}]}],
                })
                state(failureCount=564, pendingUpload={"nextManifestHash": a, "nextExtraManifestHash": b, "attemptCount": 0})
                saved = await_events({"sensitive_manifest_match": 1, "global_config_manifest_match": 1})
                assert len(saved["events"]) == 2
                measurements.append({"phase": "manifest", "recorded_after_seconds": time.monotonic() - start})

                start = time.monotonic()
                state(activeUpload={"nextManifestHash": a, "nextExtraManifestHash": b, "attemptCount": 1})
                saved = await_events({"upload_attempt_recorded": 1, "global_config_upload_attempt_recorded": 1})
                assert len(saved["events"]) == 4
                measurements.append({"phase": "attempt", "recorded_after_seconds": time.monotonic() - start})

                start = time.monotonic()
                write(workspace / "manifests" / (c + ".json"), {
                    "schema": "repo_snapshot_manifest/v2", "workspaceKey": key,
                    "files": [{"path": "docs/SYNTHETIC.txt", "sizeBytes": 100}],
                })
                # Older ordinary snapshot accepted while the Git snapshot remains pending.
                state(lastAcceptedManifestHash=c, lastAcceptedExtraManifestHash=b,
                      activeUpload={"nextManifestHash": a, "attemptCount": 1})
                saved = await_events({"workspace_snapshot_upload_acceptance_recorded": 1,
                                      "global_config_upload_acceptance_recorded": 1})
                counts = Counter(event["kind"] for event in saved["events"])
                assert counts["upload_acceptance_recorded"] == 0
                assert len(saved["events"]) == 7
                assert all(not e["baseline_existing"] for e in saved["events"])
                measurements.append({"phase": "separate_acceptance", "recorded_after_seconds": time.monotonic() - start})
                summary = subprocess.check_output([str(binary), "incidents", "--state-dir", str(local),
                                                   "--format", "agent-summary"], env=env, text=True)
                for private in (key, "SYNTHETIC", a, b, c, "settings.behavior.json", str(temp)):
                    assert private not in summary
            finally:
                if process.poll() is None:
                    process.terminate()
                process.wait(timeout=5)
        assert process.returncode == 0
        saved = json.loads((local / "state.json").read_text())
        assert not saved.get("running", False)
    unchanged = hashlib.sha256(binary.read_bytes()).hexdigest() == before
    assert unchanged
    result = {"schema_version": 1, "version": version, "binary_sha256": before,
              "binary_unchanged": unchanged, "poll_interval_seconds": 2, "measurements": measurements,
              "events_recorded": 7, "false_git_acceptance_events": 0, "summary_redacted": True,
              "monitor_exit": 0, "verification_passed": True,
              "real_zcode_started": False, "real_repository_or_secrets_read": False,
              "network_operations_requested": False, "notifications_sent": False,
              "scope": "Synthetic metadata replay; latency is local persistence, not OS notification delivery or pre-upload interception."}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    main(args.binary.resolve(), args.output)
