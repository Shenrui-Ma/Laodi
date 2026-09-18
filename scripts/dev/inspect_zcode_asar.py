#!/usr/bin/env python3
"""Read bounded snippets from a locally installed Electron ASAR; never execute it.

Uses only Python's standard library. Does not read ZCode user data, extract files,
or modify the application. Intended for version-specific adapter review.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import struct


class Asar:
    def __init__(self, path):
        self.path = Path(path)
        with self.path.open("rb") as stream:
            prefix = stream.read(16)
            if len(prefix) != 16:
                raise ValueError("Truncated ASAR prefix")
            size_size, header_size, pickle_size, json_size = struct.unpack("<4I", prefix)
            if size_size != 4 or not 0 < json_size <= pickle_size <= header_size <= 32 * 1024 * 1024:
                raise ValueError("Invalid or oversized ASAR header")
            self.header = json.loads(stream.read(json_size))
            self.base = 8 + header_size

    def entries(self, node=None, prefix=""):
        node = self.header if node is None else node
        for name, entry in node.get("files", {}).items():
            path = prefix + name
            if "files" in entry:
                yield from self.entries(entry, path + "/")
            else:
                yield path, entry

    def read(self, entry):
        if entry.get("unpacked") or "offset" not in entry:
            raise ValueError("Only packed regular files are supported")
        offset, size = int(entry["offset"]), int(entry["size"])
        if offset < 0 or size < 0 or size > 32 * 1024 * 1024:
            raise ValueError("Invalid or oversized ASAR entry")
        if self.base + offset + size > self.path.stat().st_size:
            raise ValueError("ASAR entry exceeds archive")
        with self.path.open("rb") as stream:
            stream.seek(self.base + offset)
            content = stream.read(size)
        if len(content) != size:
            raise ValueError("Truncated ASAR entry")
        return content


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--asar", default="/Applications/ZCode.app/Contents/Resources/app.asar")
    parser.add_argument("--member", default=r"^out/(host|main)/.*\.js$")
    parser.add_argument("--pattern", required=True, help="Regular expression searched in UTF-8 JS")
    parser.add_argument("--context", type=int, default=200)
    parser.add_argument("--limit", type=int, default=20)
    args = parser.parse_args()
    if not 0 <= args.context <= 10000 or not 1 <= args.limit <= 200:
        parser.error("context must be 0..10000 and limit 1..200")
    archive = Asar(args.asar)
    member_pattern, pattern = re.compile(args.member), re.compile(args.pattern)
    matches = []
    for member, entry in archive.entries():
        if not member_pattern.search(member) or entry.get("unpacked"):
            continue
        content = archive.read(entry)
        source = content.decode("utf-8")
        digest = hashlib.sha256(content).hexdigest()
        for match in pattern.finditer(source):
            start, end = max(0, match.start() - args.context), min(len(source), match.end() + args.context)
            matches.append({"member": member, "sha256": digest,
                            "character_offset": match.start(),
                            "snippet": source[start:end]})
            if len(matches) == args.limit:
                break
        if len(matches) == args.limit:
            break
    print(json.dumps({"asar": str(archive.path), "matches": matches}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
