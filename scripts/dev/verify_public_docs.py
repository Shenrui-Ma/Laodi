#!/usr/bin/env python3
"""Check publishable documentation without inspecting ignored private materials."""
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FORBIDDEN_NAMES = re.compile(r"zcode|glm|智谱", re.IGNORECASE)
DOCUMENT_SUFFIXES = {".md", ".txt", ".rst", ".html", ".htm", ".adoc"}


def main():
    names = subprocess.check_output(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        cwd=ROOT,
    ).decode().split("\0")
    failures = []
    checked = 0
    for name in sorted(set(names) - {""}):
        path = ROOT / name
        # Deleted files remain in the index until staging. They are not published.
        if not path.is_file():
            continue
        if name.startswith("docs/spikes/") or name == "docs/BENCHMARKS.md":
            failures.append(f"private experiment material: {name}")
        if path.suffix.lower() not in DOCUMENT_SUFFIXES:
            continue
        checked += 1
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if FORBIDDEN_NAMES.search(line):
                failures.append(f"restricted name in document: {name}:{number}")
    if failures:
        print("\n".join(failures))
        return 1
    print(f"Public documentation check passed: {checked} documents.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
