#!/usr/bin/env python3
"""Extract a fixed public producer into an isolated, offline component fixture.

Does not execute the desktop application, read account data, or contact a service.
The generated module is a component experiment, not a desktop-client replay.
"""
import argparse
import hashlib
import json
from pathlib import Path

from inspect_zcode_asar import Asar

ARCHIVE = "14aa5db53b67a9f1b3cf9ecbd7fb314588ff928e5ed753b1f5249f4826fbc731"
HOST = "30911a90dadc5c384959d00d95ccc70c8cf38c74a9cb99c3168b0897d046d215"

HEADER = r'''import * as labPath from 'node:path';
const s=(value,name)=>value;
const po=()=>{}; // application audit observer; filesystem calls remain original
const tX=()=>false; // synthetic filesystem errors use the ordinary retry branch
const zq='SYNTHETIC_LOCK_TIMEOUT';
const ht=()=>labPath.join(process.env.HOME,'.zcode','v2');
const Kr=e=>e.workspaceIdentity?.trim()||e.workspacePath;
const xV='repo_snapshot_manifest/v2',NV='repo_snapshot_manifest_hash/v1',
MV='repo_snapshot_delta/v2',kv='repo_snapshot_extra_manifest/v1',
OV='repo_snapshot_extra_delta/v1',$V='repo_snapshot_encrypted_artifact/v2',
DV='repo_snapshot_encryption_aad/v2';
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("asar", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    if args.asar.stat().st_size > 512 * 1024 * 1024:
        raise ValueError("archive exceeds limit")
    if hashlib.sha256(args.asar.read_bytes()).hexdigest() != ARCHIVE:
        raise ValueError("unreviewed program archive")
    archive = Asar(args.asar)
    entries = dict(archive.entries())
    source_bytes = archive.read(entries["out/host/index.js"])
    if hashlib.sha256(source_bytes).hexdigest() != HOST:
        raise ValueError("unreviewed producer")
    source = source_bytes.decode("utf-8")
    pieces, evidence = [], []
    for start, end in [
        ('import{mkdir as gPe,', 's(Jl,"atomicWriteJson");'),
        ('import{createHash as Qx}', 's(nY,"selectDeltaExtraFiles");'),
        ('import{constants as B5e,', 's(Eme,"getRepoSnapshotArtifactPaths");'),
        ('import{spawn as w9e}', 's(qme,"createRepoSnapshotAbortError");'),
    ]:
        first = source.index(start)
        last = source.index(end, first) + len(end)
        piece = source[first:last]
        pieces.append(piece)
        evidence.append(dict(start=first, end=last, sha256=hashlib.sha256(piece.encode()).hexdigest()))
    args.output.mkdir(parents=True, exist_ok=True)
    module = HEADER + "\n".join(pieces) + "\nexport {Sme,Gme,c4,Eme,Q3,eA,Pme,tY,nY,wa};\n"
    (args.output / "original-component.mjs").write_text(module, encoding="utf-8")
    (args.output / "extraction.json").write_text(json.dumps(dict(
        archive_sha256=ARCHIVE, host_sha256=HOST, unchanged_segments=evidence,
        adapters=["function naming", "synthetic home", "workspace key", "audit observer", "ordinary error retry", "schema constants"],
        scope="original scan, delta, tar, compression, encryption and filesystem code; synthetic inputs and receiver",
    ), indent=2), encoding="utf-8")
    print("Verified original archive component prepared; no application launched.")


if __name__ == "__main__":
    main()
