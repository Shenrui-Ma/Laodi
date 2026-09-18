#!/usr/bin/env python3
"""Compare synthetic development tasks with and without a denied .git directory.

Uses existing macOS tools only. No actual AI client, network request, credential,
user repository, persistent service, or system setting is accessed or changed.
The deliberately narrow sandbox profile is a file-compatibility probe, not a
production isolation policy or proof about an entire real AI client.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import platform
import shutil
import statistics
import subprocess
import sys
import tempfile
import time


GIT_OPTIONS = [
    "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
    "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
    "-c", "core.attributesFile=/dev/null", "-c", "core.autocrlf=false",
    "-c", "core.pager=cat",
]
HISTORY_MARKER = "LAODI_SYNTHETIC_HISTORY_ONLY_COMPATIBILITY_42"


def isolated_environment(root: Path, tool_paths: dict[str, str | None]) -> dict[str, str]:
    for directory in ("home", "tmp", "xdg-config", "xdg-cache", "empty-template"):
        (root / directory).mkdir()
    search = ["/usr/bin", "/bin", "/usr/sbin", "/sbin"]
    search.extend(str(Path(p).parent) for p in tool_paths.values() if p)
    return {
        "PATH": ":".join(dict.fromkeys(search)),
        "HOME": str(root / "home"), "TMPDIR": str(root / "tmp"),
        "XDG_CONFIG_HOME": str(root / "xdg-config"),
        "XDG_CACHE_HOME": str(root / "xdg-cache"),
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_SYSTEM": "/dev/null",
        "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0",
        "GIT_AUTHOR_NAME": "Laodi Synthetic Probe",
        "GIT_AUTHOR_EMAIL": "laodi-probe@example.invalid",
        "GIT_COMMITTER_NAME": "Laodi Synthetic Probe",
        "GIT_COMMITTER_EMAIL": "laodi-probe@example.invalid",
        "GIT_AUTHOR_DATE": "2000-01-01T00:00:00+0000",
        "GIT_COMMITTER_DATE": "2000-01-01T00:00:00+0000",
        "LANG": "C", "LC_ALL": "C", "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONNOUSERSITE": "1", "PAGER": "cat",
    }


def execute(command: list[str], cwd: Path, env: dict[str, str], timeout: int = 30) -> dict:
    start = time.perf_counter_ns()
    try:
        result = subprocess.run(command, cwd=cwd, env=env, text=True,
                                capture_output=True, timeout=timeout, check=False)
        return {
            "exit_code": result.returncode,
            "elapsed_ms": round((time.perf_counter_ns() - start) / 1e6, 3),
            "stdout": result.stdout[:3000], "stderr": result.stderr[:3000],
        }
    except subprocess.TimeoutExpired:
        return {"exit_code": None, "elapsed_ms": round(
            (time.perf_counter_ns() - start) / 1e6, 3),
            "stdout": "", "stderr": "Probe timeout; result is inconclusive."}


def git_command(tool_paths: dict[str, str | None], *arguments: str) -> list[str]:
    return [tool_paths["git"], *GIT_OPTIONS, *arguments]


def make_fixture(root: Path, name: str, env: dict[str, str], tools: dict) -> Path:
    repo = root / name
    repo.mkdir()
    files = {
        "calc.py": "def add(a, b):\n    return a + b\n",
        "test_calc.py": (
            "import unittest\nfrom calc import add\n"
            "class TestCalc(unittest.TestCase):\n"
            "    def test_add(self):\n        self.assertEqual(add(40, 2), 42)\n"
        ),
        "calc.cjs": "exports.add = (a, b) => a + b;\n",
        "test_calc.cjs": (
            "const test = require('node:test');\n"
            "const assert = require('node:assert/strict');\n"
            "const { add } = require('./calc.cjs');\n"
            "test('add', () => assert.equal(add(40, 2), 42));\n"
        ),
        "calc.c": '#include <stdio.h>\nint main(void) { puts("42"); return 0; }\n',
        "README.md": "Synthetic compatibility fixture.\n",
        "historical-secret.txt": HISTORY_MARKER + "\n",
    }
    for path, value in files.items():
        (repo / path).write_text(value, encoding="utf-8")
    commands = [
        ("init", "--quiet", "--initial-branch=main", f"--template={root / 'empty-template'}"),
        ("add", "--", "."), ("commit", "--quiet", "-m", "Synthetic historical fixture"),
    ]
    for arguments in commands:
        result = execute(git_command(tools, *arguments), repo, env)
        if result["exit_code"] != 0:
            raise RuntimeError(f"Fixture prerequisite failed: {arguments}: {result}")
    (repo / "historical-secret.txt").unlink()
    for arguments in [("add", "--all"), ("commit", "--quiet", "-m", "Remove synthetic marker")]:
        result = execute(git_command(tools, *arguments), repo, env)
        if result["exit_code"] != 0:
            raise RuntimeError(f"Fixture prerequisite failed: {arguments}: {result}")
    (repo / "README.md").write_text("Synthetic compatibility fixture, current edit.\n")
    (repo / "new.txt").write_text("Synthetic new file.\n")
    return repo


def cases(tools: dict) -> list[dict]:
    python = tools["python"]
    result = [
        {"name": "git_status", "command": git_command(tools, "status", "--porcelain=v1"), "git": True},
        {"name": "git_diff", "command": git_command(tools, "diff", "--no-ext-diff", "--no-textconv"), "git": True},
        {"name": "git_log", "command": git_command(tools, "log", "--oneline", "-2"), "git": True},
        {"name": "git_show_history", "command": git_command(tools, "show", "HEAD~1:historical-secret.txt"), "git": True},
        {"name": "git_add", "command": git_command(tools, "add", "--", "new.txt"), "git": True},
        {"name": "git_commit", "command": git_command(tools, "commit", "--quiet", "-am", "Synthetic current edit"), "git": True},
        {"name": "current_source_read", "command": [python, "-c", "from pathlib import Path; assert 'return a + b' in Path('calc.py').read_text(); print('current source readable')"], "git": False},
        {"name": "current_source_edit", "command": [python, "-c", "from pathlib import Path; p=Path('calc.py'); p.write_text('def add(a, b):\\n    return a+b\\n'); assert p.read_text() == 'def add(a, b):\\n    return a+b\\n'; print('current source edited')"], "git": False},
        {"name": "python_unit_tests", "command": [python, "-m", "unittest", "-v", "test_calc"], "git": False},
    ]
    if tools["node"]:
        result.append({"name": "node_unit_tests", "command": [tools["node"], "--test", "test_calc.cjs"], "git": False})
    if tools["cc"]:
        code = (
            "import subprocess; "
            f"subprocess.run({[tools['cc'], 'calc.c', '-o', 'calc-bin']!r}, check=True); "
            "r=subprocess.run(['./calc-bin'], capture_output=True, text=True, check=True); "
            "assert r.stdout.strip() == '42'; print('compiled and executed: 42')"
        )
        result.append({"name": "c_compile_and_execute", "command": [python, "-c", code], "git": False})
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repetitions", type=int, default=5)
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[2] / "docs/spikes/s0-compatibility-results.json")
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be positive")
    if platform.system() != "Darwin":
        parser.error("This probe requires macOS and /usr/bin/sandbox-exec")
    tools = {"python": str(Path(sys.executable).resolve()), "git": shutil.which("git"),
             "node": shutil.which("node"), "cc": shutil.which("cc"),
             "sandbox_exec": "/usr/bin/sandbox-exec"}
    for required in ("python", "git", "sandbox_exec"):
        if not tools[required] or not os.access(tools[required], os.X_OK):
            parser.error(f"Required executable unavailable: {required}: {tools[required]}")
    report = {
        "schema_version": 1, "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "scope": "Synthetic single-repository development compatibility only; not real-client isolation certification.",
        "host": {"system": platform.system(), "macos": platform.mac_ver()[0], "machine": platform.machine()},
        "repetitions_per_case_and_mode": args.repetitions, "tools": tools,
        "policy_template": '(version 1)\n(allow default)\n(deny file-read* file-write* (subpath "<resolved synthetic repository>/.git"))\n',
        "safety": {"synthetic_only": True, "network_calls": False, "dependencies_installed": False,
                   "git_config_home_hooks_signing_isolated": True, "system_settings_changed": False},
        "method": "Each sample has a fresh two-commit fixture with a history-only synthetic marker, tracked current edit, and untracked new file. Setup is outside timing. Mode order alternates per repetition. Times include subprocess and sandbox-exec launch. Same-mode commands never share a mutable fixture.",
        "limitations": [
            "Allows all operations except reads/writes under this one .git path; no network policy is tested.",
            "No real AI application, GUI helper, existing process, worktree, alternate object store, or IPC escape is tested here.",
            "Tiny built-in tests require no package installation; framework integration, large builds, source maps and IDE indexing are untested.",
            "Five samples are a smoke comparison, not a statistically robust performance benchmark; filesystem/compiler caches may influence timings.",
            "A failing Git command proves a compatibility loss only when its unprotected control succeeds; generic 'not a git repository' is not an independent audit event.",
        ], "results": [],
    }
    with tempfile.TemporaryDirectory(prefix="laodi-compatibility-") as temporary:
        root = Path(temporary).resolve()
        env = isolated_environment(root, tools)
        report["versions"] = {name: execute([path, "--version"], root, env)["stdout"].splitlines()[:2]
                              for name, path in tools.items() if path and name != "sandbox_exec"}
        for case_index, case in enumerate(cases(tools)):
            samples = {"unprotected": [], "protected": []}
            for repetition in range(args.repetitions):
                modes = ["unprotected", "protected"] if repetition % 2 == 0 else ["protected", "unprotected"]
                for mode in modes:
                    repo = make_fixture(root, f"case-{case_index}-{repetition}-{mode}", env, tools)
                    command = list(case["command"])
                    if mode == "protected":
                        profile = "(version 1)\n(allow default)\n(deny file-read* file-write* (subpath " + json.dumps(str((repo / ".git").resolve())) + "))\n"
                        command = [tools["sandbox_exec"], "-p", profile, *command]
                    sample = execute(command, repo, env)
                    for field in ("stdout", "stderr"):
                        sample[field] = sample[field].replace(str(root), "<synthetic-temp>")
                    if case["name"] == "git_show_history":
                        sample["history_marker_observed"] = HISTORY_MARKER in sample["stdout"]
                    samples[mode].append(sample)
            controls_pass = all(s["exit_code"] == 0 for s in samples["unprotected"])
            protected_pass = all(s["exit_code"] == 0 for s in samples["protected"])
            protected_rejected = all(s["exit_code"] is not None and s["exit_code"] != 0 for s in samples["protected"])
            valid = controls_pass and (protected_rejected if case["git"] else protected_pass)
            if case["name"] == "git_show_history":
                valid = valid and all(s["history_marker_observed"] for s in samples["unprotected"]) and not any(s["history_marker_observed"] for s in samples["protected"])
            medians = {mode: round(statistics.median(s["elapsed_ms"] for s in rows), 3) for mode, rows in samples.items()}
            report["results"].append({"case": case["name"], "expected": "unprotected succeeds, protected fails" if case["git"] else "both modes succeed",
                "expectation_met": valid, "controls_all_succeeded": controls_pass,
                "protected_all_succeeded": protected_pass, "median_elapsed_ms": medians,
                "median_delta_ms": round(medians["protected"] - medians["unprotected"], 3), "samples": samples})
    report["verification_passed"] = all(r["expectation_met"] for r in report["results"])
    report["cases_executed"] = len(report["results"])
    report["optional_cases_skipped"] = [name for name in ("node", "cc") if not tools[name]]
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "verification_passed": report["verification_passed"],
                      "cases": [{k: r[k] for k in ("case", "expectation_met", "median_elapsed_ms")} for r in report["results"]]}, indent=2))
    return 0 if report["verification_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
