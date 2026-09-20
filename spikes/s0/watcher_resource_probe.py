#!/usr/bin/env python3
"""Measure the unchanged Python S0 watcher using isolated synthetic samples.

This measures neither a future Go implementation nor long-term resource use.
It makes no network requests, sends no notifications and changes no limits.
"""

from __future__ import annotations

import argparse
import datetime as dt
import errno
import json
import os
from pathlib import Path
import platform
import resource
import subprocess
import sys
import tempfile
import time

from watcher_probe import SyntheticWatcher, synthetic_manifest


def open_fd_count() -> int:
    # /dev/fd enumeration briefly creates its own descriptor. Validate names
    # after enumeration closes it so that descriptor is excluded.
    count = 0
    for name in os.listdir('/dev/fd'):
        try:
            os.fstat(int(name))
        except (ValueError, OSError):
            continue
        count += 1
    return count


def resident_kib() -> int:
    return int(subprocess.check_output(
        ['/bin/ps', '-o', 'rss=', '-p', str(os.getpid())], text=True).strip())


def cpu_seconds() -> float:
    usage = resource.getrusage(resource.RUSAGE_SELF)
    return usage.ru_utime + usage.ru_stime


def child_sample(directory: Path, idle_seconds: float) -> dict:
    baseline = {'rss_kib': resident_kib(), 'open_fds': open_fd_count()}
    result = {
        'manifest_count': len(list(directory.glob('*.json'))),
        'requested_idle_seconds': idle_seconds,
        'baseline_before_watcher': baseline,
        'inherited_nofile_limit': list(resource.getrlimit(resource.RLIMIT_NOFILE)),
    }
    started = time.perf_counter()
    cpu_started = cpu_seconds()
    try:
        watcher = SyntheticWatcher(directory)
    except OSError as exc:
        result.update({
            'status': 'startup_failed', 'error_type': type(exc).__name__,
            'errno': exc.errno,
            'error_name': errno.errorcode.get(exc.errno, 'UNKNOWN'),
            'startup_wall_seconds': time.perf_counter() - started,
            'startup_cpu_seconds': cpu_seconds() - cpu_started,
            'peak_rss_bytes': resource.getrusage(resource.RUSAGE_SELF).ru_maxrss,
            'note': 'Constructor failure may leave descriptors open; this isolated child exits immediately without changing its limit.',
        })
        return result
    try:
        result.update({
            'status': 'completed',
            'startup_wall_seconds': time.perf_counter() - started,
            'startup_cpu_seconds': cpu_seconds() - cpu_started,
            'startup_rss_kib': resident_kib(),
            'startup_open_fds': open_fd_count(),
            'manifest_file_descriptors': len(watcher.files),
        })
        idle_start = time.perf_counter()
        idle_cpu_start = cpu_seconds()
        watcher.drain_for(idle_seconds)
        idle_cpu = cpu_seconds() - idle_cpu_start
        elapsed = time.perf_counter() - idle_start
        result.update({
            'idle_wall_seconds': elapsed,
            'idle_cpu_seconds': idle_cpu,
            'idle_cpu_percent_of_one_core': idle_cpu / elapsed * 100 if idle_seconds else None,
            'idle_end_rss_kib': resident_kib(),
            'idle_end_open_fds': open_fd_count(),
            'peak_rss_bytes': resource.getrusage(resource.RUSAGE_SELF).ru_maxrss,
            'idle_kernel_events': watcher.kernel_event_count,
            'idle_local_events': len(watcher.events),
        })
    finally:
        watcher.close()
    result['after_close_open_fds'] = open_fd_count()
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--idle-seconds', type=float, default=20.0)
    parser.add_argument('--child-directory', type=Path, help=argparse.SUPPRESS)
    parser.add_argument('--output', type=Path, default=Path(__file__).resolve().parents[2] / '.omx/experiments/s0-watcher-resource-results.json')
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        parser.error('This probe measures macOS kqueue and Darwin ru_maxrss units')
    if args.idle_seconds < 0:
        parser.error('idle-seconds must be nonnegative')
    if args.child_directory:
        print(json.dumps(child_sample(args.child_directory, args.idle_seconds)))
        return 0
    report = {
        'schema_version': 1,
        'created_at': dt.datetime.now(dt.timezone.utc).isoformat(),
        'scope': 'Unchanged Python S0 SyntheticWatcher; synthetic manifests, no ongoing file changes, separate child for each sample.',
        'host': {'system': platform.system(), 'macos': platform.mac_ver()[0],
                 'machine': platform.machine(), 'python': platform.python_version()},
        'measurement': {
            'rss': '/bin/ps RSS in KiB, includes Python interpreter and imports',
            'peak_rss': 'resource.RUSAGE_SELF ru_maxrss in bytes on macOS',
            'cpu': 'Self user+system CPU; idle window excludes ps and descriptor sampling',
            'fds': 'Validated /dev/fd entries; excludes transient enumeration descriptor',
            '1000_manifests': 'Startup-only descriptor-budget experiment; zero idle duration',
        },
        'safety': {'synthetic_temporary_files_only': True, 'real_zcode_paths_read': False,
                   'network_operations_requested': False, 'notifications_displayed': False,
                   'dependencies_installed': False, 'rlimits_changed': False,
                   'system_settings_changed': False},
        'limitations': [
            'This is a Python feasibility prototype, not the future Go Guardian memory footprint.',
            'One sample per file count; short idle samples do not establish a 24-hour memory, CPU or latency SLA.',
            'No real ZCode manifest parser, notifications, continuous writes, suspend/resume or incident persistence is measured.',
            'The original watcher retains one descriptor per manifest and has no production descriptor-budget handling.',
            'RSS can fluctuate with the OS and Python allocator; baseline differences are not precise component attribution.',
        ],
        'samples': [],
    }
    for count in (10, 100, 1000):
        with tempfile.TemporaryDirectory(prefix=f'laodi-watch-resource-{count}-') as temporary:
            directory = Path(temporary)
            for sequence in range(count):
                (directory / f'manifest-{sequence:04d}.json').write_bytes(synthetic_manifest(sequence))
            idle = args.idle_seconds if count < 1000 else 0
            process = subprocess.run(
                [sys.executable, '-B', str(Path(__file__).resolve()),
                 '--child-directory', str(directory), '--idle-seconds', str(idle)],
                capture_output=True, text=True, timeout=idle + 30,
            )
            if process.returncode:
                sample = {'manifest_count': count, 'status': 'child_failed',
                          'returncode': process.returncode, 'stderr': process.stderr[-2000:]}
            else:
                sample = json.loads(process.stdout)
            report['samples'].append(sample)
            print(json.dumps(sample), flush=True)
    report['required_idle_samples_completed'] = all(
        sample['status'] == 'completed' for sample in report['samples'][:2])
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({'output': str(args.output),
                      'required_idle_samples_completed': report['required_idle_samples_completed']}))
    return 0 if report['required_idle_samples_completed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
