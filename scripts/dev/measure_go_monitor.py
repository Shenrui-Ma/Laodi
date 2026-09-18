#!/usr/bin/env python3
"""Measure the development Go monitor with bounded synthetic evidence for 22 seconds.

No dependencies, notifications, real ZCode paths or system changes are used.
"""

from __future__ import annotations

import argparse
import ctypes
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import platform
import statistics
import subprocess
import tempfile
import time


def fd_count(pid: int) -> int:
    library = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
    function = library.proc_pidinfo
    function.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.c_uint64, ctypes.c_void_p, ctypes.c_int]
    function.restype = ctypes.c_int
    required = function(pid, 1, 0, None, 0)  # PROC_PIDLISTFDS
    if required <= 0:
        raise OSError(ctypes.get_errno(), 'proc_pidinfo size query failed')
    buffer = ctypes.create_string_buffer(required)
    received = function(pid, 1, 0, buffer, required)
    if received < 0:
        raise OSError(ctypes.get_errno(), 'proc_pidinfo descriptor query failed')
    # proc_fdinfo is a signed 32-bit descriptor plus unsigned 32-bit type.
    return received // 8


def sample(pid: int, start: float) -> dict:
    output = subprocess.check_output(
        ['/bin/ps', '-o', 'rss=,time=', '-p', str(pid)], text=True).strip()
    rss, cpu = output.split(maxsplit=1)
    return {'elapsed_seconds': time.monotonic() - start,
            'rss_kib': int(rss), 'cpu_time_ps': cpu, 'open_fds': fd_count(pid)}


def main() -> int:
    root = Path(__file__).resolve().parents[2]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, default=root / 'bin/laodi')
    parser.add_argument('--output', type=Path, default=root / 'docs/spikes/v1-go-resource-results.json')
    parser.add_argument('--manifest-count', type=int, default=100)
    parser.add_argument('--files-per-manifest', type=int, default=1)
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        parser.error('This resource probe requires macOS')
    if not 1 <= args.manifest_count <= 100 or not 1 <= args.files_per_manifest <= 100000 or args.manifest_count * args.files_per_manifest > 100000:
        parser.error('Use 1–100 manifests and at most 100,000 synthetic file entries in total')
    binary = args.binary.resolve()
    version = subprocess.check_output([str(binary), '--version'], text=True).strip()
    binary_hash_before = hashlib.sha256(binary.read_bytes()).hexdigest()
    report = {
        'schema_version': 1, 'created_at': dt.datetime.now(dt.timezone.utc).isoformat(),
        'scope': 'Actual Go development monitor, synthetic manifests, 22-second bounded run, no changes after startup.',
        'fixture': {'manifest_count': args.manifest_count, 'files_per_manifest': args.files_per_manifest},
        'host': {'system': platform.system(), 'macos': platform.mac_ver()[0], 'machine': platform.machine()},
        'binary': {'version': version, 'bytes': binary.stat().st_size,
                   'sha256': binary_hash_before, 'mtime_ns': binary.stat().st_mtime_ns},
        'measurement': {
            'rss': '/bin/ps RSS in KiB; periodic samples are not the exact memory peak',
            'fds': 'macOS proc_pidinfo(PROC_PIDLISTFDS), only the spawned monitor PID',
            'cpu': 'wait4 rusage for monitor PID only, excludes Python sampler and ps',
            'peak_rss': 'wait4 ru_maxrss in bytes on macOS',
            'sample_interval_seconds': 2,
        },
        'safety': {'synthetic_temporary_files_only': True, 'real_zcode_paths_read': False,
                   'notifier_configured': False, 'notifications_requested': False,
                   'network_operations_requested': False, 'dependencies_installed': False,
                   'background_service_installed': False, 'system_settings_changed': False},
        'limitations': [
            'One short run on one development host is not a 24-hour SLA or production certification.',
            'No continuous file writes, system notifications, real application activity or suspend/resume is measured; fixture dimensions describe the measured manifest size.',
            'Periodic FD and RSS sampling can miss transient peaks; no general memory ceiling is established.',
            'These synthetic files validate resource behavior, not the correctness of all real ZCode versions or upload detection.',
        ],
        'samples': [],
    }
    with tempfile.TemporaryDirectory(prefix='laodi-go-resource-') as temporary:
        directory = Path(temporary)
        evidence = directory / 'checkpoints'
        manifests = evidence / '0123456789ab' / 'manifests'
        manifests.mkdir(parents=True)
        payload = {'schema': 'repo_snapshot_manifest/v2', 'workspaceKey': '/synthetic/repo',
                   'files': [{'path': f'.git/objects/SYNTHETIC-{i}', 'sizeBytes': 123}
                             for i in range(args.files_per_manifest)]}
        for sequence in range(args.manifest_count):
            (manifests / f'{sequence:064x}.json').write_text(json.dumps(payload), encoding='utf-8')
        state_dir = directory / 'new-private-dir'
        command = [str(binary), 'watch', '--root', str(evidence), '--state-dir', str(state_dir),
                   '--build', '3.12.3.7463', '--interval', '2s', '--duration', '22s']
        report['command_shape'] = 'bin/laodi watch --root <synthetic-checkpoints> --state-dir <fresh-private-state> --build 3.12.3.7463 --interval 2s --duration 22s'
        with (directory / 'stdout').open('w+') as stdout, (directory / 'stderr').open('w+') as stderr:
            start = time.monotonic()
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr)
            finished = None
            timeout = False
            try:
                while True:
                    waited, status, usage = os.wait4(process.pid, os.WNOHANG)
                    if waited:
                        process.returncode = os.waitstatus_to_exitcode(status)
                        finished = usage
                        break
                    if time.monotonic() - start > 35:
                        timeout = True
                        process.kill()
                        _, status, finished = os.wait4(process.pid, 0)
                        process.returncode = os.waitstatus_to_exitcode(status)
                        break
                    try:
                        report['samples'].append(sample(process.pid, start))
                    except (OSError, subprocess.CalledProcessError, ValueError) as exc:
                        report.setdefault('sampling_errors', []).append(str(exc))
                    time.sleep(2)
            finally:
                if process.returncode is None:
                    process.kill()
                    _, status, _ = os.wait4(process.pid, 0)
                    process.returncode = os.waitstatus_to_exitcode(status)
            elapsed = time.monotonic() - start
            stdout.seek(0)
            stderr.seek(0)
            report.update({'exit_code': process.returncode, 'timed_out': timeout,
                           'observed_wall_seconds': elapsed,
                           'stdout': stdout.read(), 'stderr': stderr.read()})
            if finished:
                cpu = finished.ru_utime + finished.ru_stime
                report['process_resources'] = {
                    'user_cpu_seconds': finished.ru_utime, 'system_cpu_seconds': finished.ru_stime,
                    'total_cpu_seconds': cpu,
                    'cpu_percent_of_one_core_over_observed_wall': cpu / elapsed * 100,
                    'peak_rss_bytes': finished.ru_maxrss,
                }
        state_file = state_dir / 'state.json'
        state = json.loads(state_file.read_text()) if state_file.exists() else {}
        report['saved_state'] = {
            'exists': state_file.exists(), 'initialized': state.get('initialized'),
            'coverage': state.get('coverage'), 'running': state.get('running', False),
            'seen_count': len(state.get('seen', {})), 'event_count': len(state.get('events', [])),
            'bytes': state_file.stat().st_size if state_file.exists() else None,
        }
    report['binary_unchanged_during_run'] = hashlib.sha256(binary.read_bytes()).hexdigest() == binary_hash_before
    rss = [point['rss_kib'] for point in report['samples']]
    fds = [point['open_fds'] for point in report['samples']]
    report['sample_summary'] = {
        'count': len(rss), 'rss_kib_min': min(rss) if rss else None,
        'rss_kib_median': statistics.median(rss) if rss else None,
        'rss_kib_max': max(rss) if rss else None,
        'fds_min': min(fds) if fds else None, 'fds_max': max(fds) if fds else None,
    }
    report['probe_execution_passed'] = (
        report['exit_code'] == 0 and not report['timed_out'] and len(rss) >= 10
        and report['saved_state']['initialized'] is True
        and report['saved_state']['coverage'] == 'observing'
        and report['saved_state']['seen_count'] == args.manifest_count
        and report['saved_state']['running'] is False
    )
    report['verification_passed'] = report['probe_execution_passed'] and report['binary_unchanged_during_run']
    if not report['binary_unchanged_during_run']:
        report['limitations'].append('The binary path changed during measurement. This run describes the development process launched after the recorded pre-spawn hash; it does not establish performance of the replacement/current artifact.')
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps({'output': str(args.output), 'verification_passed': report['verification_passed'],
                      'sample_summary': report['sample_summary'],
                      'process_resources': report.get('process_resources'),
                      'saved_state': report['saved_state']}, ensure_ascii=False, indent=2))
    return 0 if report['verification_passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
