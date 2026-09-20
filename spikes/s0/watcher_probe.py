#!/usr/bin/env python3
"""Exercise local kqueue snapshot detection with synthetic files only.

This is a bounded monitoring feasibility probe, not a ZCode parser, production
watcher, upload detector, or macOS notification implementation.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import select
import statistics
import tempfile
import time


SCHEMA = "laodi-s0-synthetic-snapshot-v1"
MAX_MANIFEST_BYTES = 64 * 1024


def synthetic_manifest(sequence: int) -> bytes:
    return json.dumps({"schema": SCHEMA, "synthetic": True, "sequence": sequence,
                       "files": [".git/objects/SYNTHETIC_OBJECT", "synthetic-src/main.py"]},
                      sort_keys=True).encode("utf-8")


class SyntheticWatcher:
    def __init__(self, directory: Path):
        self.directory = directory
        self.queue = select.kqueue()
        flags = os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW
        self.directory_fd = os.open(directory, flags | os.O_DIRECTORY)
        self.files: dict[str, tuple[int, tuple[int, int]]] = {}
        self.fingerprints: dict[str, str] = {}
        self.events: list[dict] = []
        self.invalid_json_reads = 0
        self.kernel_event_count = 0
        self.valid = True
        self._register(self.directory_fd)
        self._reconcile(emit=False)

    def _register(self, fd: int) -> None:
        changes = [select.kevent(fd, filter=select.KQ_FILTER_VNODE,
                                 flags=select.KQ_EV_ADD | select.KQ_EV_ENABLE | select.KQ_EV_CLEAR,
                                 fflags=(select.KQ_NOTE_WRITE | select.KQ_NOTE_EXTEND |
                                         select.KQ_NOTE_ATTRIB | select.KQ_NOTE_DELETE |
                                         select.KQ_NOTE_RENAME | select.KQ_NOTE_REVOKE))]
        self.queue.control(changes, 0, 0)

    def _read(self, name: str, fd: int, emit: bool) -> None:
        raw = os.pread(fd, MAX_MANIFEST_BYTES + 1, 0)
        if len(raw) > MAX_MANIFEST_BYTES:
            raise RuntimeError("Synthetic manifest exceeded the probe's bounded read size")
        try:
            payload = json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError):
            self.invalid_json_reads += 1
            return
        if not isinstance(payload, dict) or payload.get("schema") != SCHEMA or payload.get("synthetic") is not True:
            raise RuntimeError("Only the known synthetic schema is permitted in this probe")
        fingerprint = hashlib.sha256(raw).hexdigest()
        changed = self.fingerprints.get(name) != fingerprint
        self.fingerprints[name] = fingerprint
        if emit and changed:
            self.events.append({
                "kind": "synthetic_manifest_complete", "file": name,
                "sequence": payload["sequence"], "observed_at_ns": time.perf_counter_ns(),
                "evidence": "A complete synthetic local JSON manifest was parsed; no upload is asserted.",
            })

    def _reconcile(self, emit: bool) -> None:
        present: set[str] = set()
        with os.scandir(self.directory) as entries:
            for entry in entries:
                if not entry.name.endswith(".json") or not entry.is_file(follow_symlinks=False):
                    continue
                present.add(entry.name)
                info = entry.stat(follow_symlinks=False)
                identity = (info.st_dev, info.st_ino)
                attached = self.files.get(entry.name)
                if attached is not None and attached[1] != identity:
                    os.close(attached[0])
                    del self.files[entry.name]
                    attached = None
                if attached is None:
                    fd = os.open(entry.path, os.O_RDONLY | os.O_CLOEXEC | os.O_NOFOLLOW)
                    self._register(fd)
                    self.files[entry.name] = (fd, identity)
                self._read(entry.name, self.files[entry.name][0], emit)
        for name in set(self.files) - present:
            os.close(self.files.pop(name)[0])
            self.fingerprints.pop(name, None)

    def poll(self, timeout: float) -> list[dict]:
        begin = len(self.events)
        batch = self.queue.control([], 64, timeout)
        self.kernel_event_count += len(batch)
        # A renamed directory remains attached by inode. Treat the configured
        # pathname as invalid instead of pretending that coverage is unchanged.
        invalid_flags = select.KQ_NOTE_RENAME | select.KQ_NOTE_DELETE | select.KQ_NOTE_REVOKE
        for event in batch:
            if event.ident == self.directory_fd and event.fflags & invalid_flags:
                names = [label for label, flag in (
                    ("NOTE_RENAME", select.KQ_NOTE_RENAME), ("NOTE_DELETE", select.KQ_NOTE_DELETE),
                    ("NOTE_REVOKE", select.KQ_NOTE_REVOKE)) if event.fflags & flag]
                if self.valid:
                    self.valid = False
                    self.events.append({"kind": "watch_path_invalidated", "flags": names,
                                        "observed_at_ns": time.perf_counter_ns(),
                                        "configured_path_exists": self.directory.exists(),
                                        "watched_inode_fd_still_open": os.fstat(self.directory_fd).st_ino > 0})
        if not self.valid:
            return self.events[begin:]
        for event in batch:
            if event.ident == self.directory_fd:
                self._reconcile(emit=True)
            else:
                current = next(((name, fd) for name, (fd, _) in self.files.items()
                                if fd == event.ident), None)
                if current is not None:
                    self._read(current[0], current[1], emit=True)
        return self.events[begin:]

    def until(self, predicate, timeout: float = 2.0) -> dict | None:
        deadline = time.perf_counter() + timeout
        while time.perf_counter() < deadline:
            for event in self.poll(max(0.0, deadline - time.perf_counter())):
                if predicate(event):
                    return event
        return None

    def drain_for(self, seconds: float) -> list[dict]:
        before = len(self.events)
        deadline = time.perf_counter() + seconds
        while time.perf_counter() < deadline:
            self.poll(max(0.0, deadline - time.perf_counter()))
        return self.events[before:]

    def close(self) -> None:
        for fd, _ in self.files.values():
            os.close(fd)
        os.close(self.directory_fd)
        self.queue.close()


def publish_atomic(staging: Path, destination: Path, contents: bytes) -> int:
    with staging.open("wb") as handle:
        handle.write(contents)
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(staging, destination)
    return time.perf_counter_ns()


def latency_ms(event: dict | None, write_completed_ns: int) -> float | None:
    if event is None:
        return None
    return round((event["observed_at_ns"] - write_completed_ns) / 1e6, 3)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[2] / ".omx/experiments/s0-watcher-results.json")
    args = parser.parse_args()
    if args.samples < 20:
        parser.error("Use at least 20 complete-manifest samples")
    if platform.system() != "Darwin" or not hasattr(select, "kqueue"):
        parser.error("This probe requires macOS Python with select.kqueue")
    report = {
        "schema_version": 1, "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "scope": "Synthetic local file-event monitoring feasibility; neither upload evidence nor system notification delivery.",
        "host": {"system": platform.system(), "macos": platform.mac_ver()[0], "machine": platform.machine(),
                 "python": platform.python_version()},
        "implementation": "Python standard-library select.kqueue; directory vnode events plus dynamically registered manifest file vnode events; content hash deduplication and startup baseline.",
        "measurement": "Atomic publish/append returns, then an idle synchronous event loop consumes queued kernel events and parses complete JSON. Latency ends when the local event is appended in memory. Setup and staged-file writes are excluded for atomic publication. No background-load or OS notification latency is measured.",
        "safety": {"synthetic_temporary_files_only": True, "real_zcode_paths_read": False,
                   "network_operations_requested": False, "dependencies_installed": False,
                   "notification_permission_requested": False, "notifications_displayed": False,
                   "system_settings_changed": False},
        "notification_delivery": {"status": "not_tested", "reason": "No notification permission request or popup was authorized or needed for this local watcher feasibility probe."},
        "limitations": [
            "Recognizes a synthetic JSON schema only; does not validate any real ZCode version or checkpoint field meaning.",
            "A parsed local manifest is not evidence that data was uploaded or that the server accepted/deleted it.",
            "Directory events alone cannot reliably signal in-place appends; this probe explicitly attaches individual file vnode watches.",
            "One descriptor per observed manifest is used; large-file-count scalability and descriptor budgeting are untested.",
            "Rename invalidation is detected but automatic reattachment, root recreation, service restarts and missed-event recovery are not implemented.",
            "No durability/finality claim is made for a syntactically complete file, and adversarial concurrent modification is not tested.",
            "The small idle sample does not validate p95 latency under load, sleep/resume, storage errors, event backlog, or notification delivery.",
        ], "tests": {},
    }
    with tempfile.TemporaryDirectory(prefix="laodi-watcher-") as temporary:
        root = Path(temporary).resolve()
        snapshots = root / "synthetic-snapshots"
        snapshots.mkdir()
        (snapshots / "startup-existing.json").write_bytes(synthetic_manifest(-1))
        watcher = SyntheticWatcher(snapshots)
        try:
            startup_events = watcher.drain_for(0.05)
            report["tests"]["existing_startup_file"] = {
                "passed": not startup_events and "startup-existing.json" in watcher.fingerprints,
                "baseline_file_count": len(watcher.fingerprints), "new_events": startup_events,
                "meaning": "The pre-existing valid manifest seeds state and is not emitted as a new incident.",
            }
            samples = []
            for sequence in range(args.samples):
                name = f"manifest-{sequence:03}.json"
                raw = synthetic_manifest(sequence)
                completed = publish_atomic(root / "staging.tmp", snapshots / name, raw)
                event = watcher.until(lambda item: item["kind"] == "synthetic_manifest_complete" and item["file"] == name)
                samples.append({"sequence": sequence, "bytes": len(raw),
                                "detected": event is not None, "local_detection_delay_ms": latency_ms(event, completed)})
            times = sorted(sample["local_detection_delay_ms"] for sample in samples if sample["detected"])
            report["tests"]["new_complete_manifests"] = {
                "passed": len(times) == args.samples, "samples": samples,
                "detected_count": len(times), "expected_count": args.samples,
                "local_detection_ms": {"p50": round(statistics.median(times), 3) if times else None,
                                       "p95_nearest_rank": times[math.ceil(0.95 * len(times)) - 1] if times else None,
                                       "max": max(times) if times else None},
            }
            partial_name = "partial-then-complete.json"
            raw = synthetic_manifest(args.samples)
            cutoff = len(raw) // 2
            (snapshots / partial_name).write_bytes(raw[:cutoff])
            invalid_before = watcher.invalid_json_reads
            partial_events = watcher.drain_for(0.1)
            incomplete_reads = watcher.invalid_json_reads - invalid_before
            with (snapshots / partial_name).open("ab") as handle:
                handle.write(raw[cutoff:])
                handle.flush()
                os.fsync(handle.fileno())
            completed = time.perf_counter_ns()
            completion_event = watcher.until(lambda item: item["kind"] == "synthetic_manifest_complete" and item["file"] == partial_name)
            premature = [item for item in partial_events if item.get("file") == partial_name]
            report["tests"]["partial_json_then_in_place_completion"] = {
                "passed": incomplete_reads > 0 and not premature and completion_event is not None,
                "incomplete_json_reads": incomplete_reads, "premature_events": premature,
                "completion_detected": completion_event is not None,
                "completion_to_local_detection_ms": latency_ms(completion_event, completed),
                "meaning": "The partial document is not emitted; the file's subsequent vnode write event triggers successful rereading without a directory rescan timer.",
            }
            snapshots.rename(root / "renamed-snapshots")
            completed = time.perf_counter_ns()
            invalidated = watcher.until(lambda item: item["kind"] == "watch_path_invalidated")
            report["tests"]["directory_rename_invalidates_configured_path"] = {
                "passed": invalidated is not None and "NOTE_RENAME" in invalidated["flags"] and not watcher.valid,
                "event": invalidated, "rename_to_local_detection_ms": latency_ms(invalidated, completed),
                "meaning": "kqueue remains attached to the inode, but the configured pathname no longer exists; coverage is marked invalid and parsing stops until explicit recovery.",
            }
            report["observed_kernel_event_count"] = watcher.kernel_event_count
            report["local_event_count"] = len(watcher.events)
            report["peak_manifest_file_descriptors"] = len(watcher.files)
        finally:
            watcher.close()
    report["verification_passed"] = all(test["passed"] for test in report["tests"].values())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "verification_passed": report["verification_passed"],
                      "tests": {name: test["passed"] for name, test in report["tests"].items()},
                      "complete_manifest_local_detection_ms": report["tests"]["new_complete_manifests"]["local_detection_ms"],
                      "system_notifications": "not_tested"}, indent=2))
    return 0 if report["verification_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
