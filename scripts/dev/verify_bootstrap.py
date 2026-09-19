#!/usr/bin/env python3
"""Test the POSIX bootstrap with fake releases; never install a real service."""

import argparse
import hashlib
import json
from pathlib import Path
import stat
import subprocess
import tempfile
import zipfile


REPO = Path(__file__).resolve().parents[2]
TAG = "v0.3.0-preview.1"
SECRET = "BOOTSTRAP-PRIVATE-ENV-MUST-NOT-BE-PRINTED"


def archive(path, extra=None, executable=True):
    entries = {
        "Laodi-skills/laodi": (
            "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$LAODI_TEST_RESULT\"\n"
            "cat > \"$LAODI_TEST_STDIN\"\n"
            "exit \"${LAODI_TEST_INSTALL_EXIT:-0}\"\n",
            stat.S_IFREG | (0o755 if executable else 0o644),
        ),
        "Laodi-skills/skills/laodi/SKILL.md": ("synthetic fixture\n", stat.S_IFREG | 0o644),
        "Laodi-skills/LaodiNotify.app/Contents/Info.plist": ("fixture\n", stat.S_IFREG | 0o644),
    }
    if extra:
        entries.update(extra)
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as output:
        for name, (body, mode) in entries.items():
            info = zipfile.ZipInfo(name)
            info.create_system = 3
            info.external_attr = mode << 16
            output.writestr(info, body)


def script(path, text):
    path.write_text("#!/bin/sh\nset -eu\n" + text)
    path.chmod(0o700)


def main(output):
    checks = []
    with tempfile.TemporaryDirectory(prefix="laodi-bootstrap-test-") as directory:
        root = Path(directory).resolve()
        mock = root / "commands"
        mock.mkdir(mode=0o700)
        script(mock / "uname", 'case "$1" in -s) printf "%s\\n" "${LAODI_TEST_OS:-Darwin}";; -m) printf "%s\\n" "${LAODI_TEST_ARCH:-arm64}";; *) exit 2;; esac\n')
        script(mock / "sw_vers", 'printf "%s\\n" "${LAODI_TEST_MACOS:-13.0}"\n')
        script(mock / "curl", '''
[ "$1" = -q ] || exit 91
printf '%s\\n' "$@" >> "$LAODI_TEST_DOWNLOADS"
destination=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) destination=$2; shift 2;;
    --proto|--proto-redir|--connect-timeout|--max-time|--retry|--max-filesize) shift 2;;
    -q|--fail|--silent|--show-error|--location) shift;;
    https://github.com/Shenrui-Ma/Laodi-skills/releases/download/*) url=$1; shift;;
    *) exit 92;;
  esac
done
[ "${LAODI_TEST_CURL_FAIL:-0}" = 0 ] || exit 22
case "$url" in
  */SHA256SUMS) cp "$LAODI_TEST_SUMS" "$destination";;
  */Laodi-skills-*-macos-universal.zip) cp "$LAODI_TEST_ZIP" "$destination";;
  *) exit 93;;
esac
''')
        script(mock / "zipinfo", '''
if [ "$1" = -l ] && [ -n "${LAODI_TEST_REPORTED_SIZE:-}" ]; then
  /usr/bin/zipinfo "$@" | awk -v size="$LAODI_TEST_REPORTED_SIZE" '
    length($1) == 10 && $1 ~ /^[-d][rwx-]+$/ { $4 = size }
    { print }
  '
else
  exec /usr/bin/zipinfo "$@"
fi
''')
        script(mock / "unzip", 'printf called > "$LAODI_TEST_EXTRACTED"\nexec /usr/bin/unzip "$@"\n')
        for forbidden in ("go", "node", "python", "python3", "sudo", "xattr", "spctl"):
            script(mock / forbidden, 'printf forbidden > "$LAODI_TEST_FORBIDDEN"\nexit 94\n')

        def run(name, args=(), override=None, extra=None, sums=None, code=0, executed=True, executable=True, expected_args=None, extraction=None, script_stdin=None):
            case = root / name
            case.mkdir(mode=0o700)
            home, temp = case / "home", case / "temporary"
            home.mkdir(mode=0o700)
            temp.mkdir(mode=0o700)
            package, checksum = case / "package.zip", case / "SHA256SUMS"
            archive(package, extra, executable)
            digest = hashlib.sha256(package.read_bytes()).hexdigest()
            tag = (override or {}).get("LAODI_VERSION", TAG)
            if args and args[0] == "--version":
                tag = args[1]
            elif args and args[0].startswith("--version="):
                tag = args[0].split("=", 1)[1]
            asset = f"Laodi-skills-{tag}-macos-universal.zip"
            checksum.write_text(sums(digest, asset) if sums else f"{digest}  {asset}\n")
            result_path, downloads = case / "called", case / "downloads"
            env = {
                "HOME": str(home), "TMPDIR": str(temp), "PATH": f"{mock}:/usr/bin:/bin", "LC_ALL": "C",
                "LAODI_TEST_ZIP": str(package), "LAODI_TEST_SUMS": str(checksum),
                "LAODI_TEST_RESULT": str(result_path), "LAODI_TEST_DOWNLOADS": str(downloads),
                "LAODI_TEST_STDIN": str(case / "installer-stdin"), "LAODI_TEST_EXTRACTED": str(case / "extracted"),
                "LAODI_TEST_FORBIDDEN": str(case / "forbidden"), "PRIVATE_TEST_VALUE": SECRET,
            }
            env.update(override or {})
            # sh -s is the same stdin execution mode as curl | sh, without a network call.
            command = ["/bin/sh", "-s", "--", *args] if script_stdin is None else ["/bin/sh", str(REPO / "install.sh"), *args]
            input_bytes = (REPO / "install.sh").read_bytes() if script_stdin is None else script_stdin
            result = subprocess.run(command, input=input_bytes,
                                    env=env, capture_output=True, timeout=15)
            assert result.returncode == code, (name, result.returncode, result.stderr.decode())
            assert result_path.exists() == executed, name
            if executed:
                received = [v.decode() for v in result_path.read_bytes().split(b"\0")[:-1]]
                assert received == ["install", *(expected_args if expected_args is not None else args)], (name, received)
                assert (case / "installer-stdin").read_bytes() == b"", (name, "installer inherited script input")
            if extraction is not None:
                assert (case / "extracted").exists() == extraction, (name, "unexpected extraction")
            assert not list(temp.iterdir()), (name, "temporary download was not removed")
            assert not list(home.iterdir()), (name, "synthetic bootstrap modified HOME")
            assert not (case / "forbidden").exists(), (name, "build tools or security bypass invoked")
            assert SECRET.encode() not in result.stdout + result.stderr, name
            assert not (case / "escaped").exists(), name
            if downloads.exists():
                calls = downloads.read_text()
                assert "--proto\n=https\n--proto-redir\n=https\n" in calls, name
                assert "http://" not in calls and "--insecure" not in calls, name
            checks.append(name)

        run("success", args=("--dry-run", "--no-notifications", "value with spaces", "literal $(not-a-command)"))
        run("intel", override={"LAODI_TEST_ARCH": "x86_64", "LAODI_TEST_MACOS": "26.3"})
        run("version-argument", args=("--version", "v1.2.3-rc.4", "--dry-run"), expected_args=("--dry-run",))
        run("version-equals", args=("--version=v1.2.3", "--", "--dry-run"), expected_args=("--dry-run",))
        run("version-environment", override={"LAODI_VERSION": "v2.0.0"})
        run("archive-defaults", override={"UNZIP": "-j", "UNZIPOPT": "-j", "ZIPINFO": "-v", "ZIPINFOOPT": "-v"})
        run("installer-stdin-isolated", script_stdin=b"must not reach the installed binary\n")
        run("installer-fails", override={"LAODI_TEST_INSTALL_EXIT": "42"}, code=42)
        run("download-fails", override={"LAODI_TEST_CURL_FAIL": "1"}, code=1, executed=False)
        run("wrong-checksum", sums=lambda d, a: f"{'0' * 64}  {a}\n", code=1, executed=False)
        run("duplicate-checksum", sums=lambda d, a: f"{d}  {a}\n{d}  {a}\n", code=1, executed=False)
        run("missing-checksum", sums=lambda d, a: f"{d}  another.zip\n", code=1, executed=False)
        run("invalid-checksum", sums=lambda d, a: f"{'g' * 64}  {a}\n", code=1, executed=False)
        run("entry-budget", extra={f"Laodi-skills/file-{i}": ("", stat.S_IFREG | 0o644) for i in range(254)}, code=1, executed=False, extraction=False)
        run("size-budget", override={"LAODI_TEST_REPORTED_SIZE": "134217729"}, code=1, executed=False, extraction=False)
        run("aggregate-size-budget", override={"LAODI_TEST_REPORTED_SIZE": "70000000"}, code=1, executed=False, extraction=False)
        run("traversal", extra={"Laodi-skills/../../escaped": ("bad", stat.S_IFREG | 0o644)}, code=1, executed=False)
        run("case-collision", extra={"Laodi-skills/LAODI": ("bad", stat.S_IFREG | 0o755)}, code=1, executed=False)
        run("duplicate-normalized-path", extra={"Laodi-skills/skills/": ("", stat.S_IFDIR | 0o755), "Laodi-skills/skills": ("bad", stat.S_IFREG | 0o644)}, code=1, executed=False)
        run("absolute-path", extra={"/tmp/laodi-bootstrap-should-not-exist": ("bad", stat.S_IFREG | 0o644)}, code=1, executed=False)
        run("symlink", extra={"Laodi-skills/link": ("/tmp", stat.S_IFLNK | 0o777)}, code=1, executed=False)
        run("special-file", extra={"Laodi-skills/pipe": ("", stat.S_IFIFO | 0o600)}, code=1, executed=False)
        run("newline-path", extra={"Laodi-skills/file\ninjected": ("bad", stat.S_IFREG | 0o644)}, code=1, executed=False)
        run("non-executable", executable=False, code=1, executed=False)
        run("unsupported-os", override={"LAODI_TEST_OS": "Linux"}, code=1, executed=False)
        run("unsupported-arch", override={"LAODI_TEST_ARCH": "i386"}, code=1, executed=False)
        run("unsupported-macos", override={"LAODI_TEST_MACOS": "12.7.4"}, code=1, executed=False)
        run("unsafe-version", override={"LAODI_VERSION": "../latest"}, code=1, executed=False)
        run("version-leading-zero", override={"LAODI_VERSION": "v01.2.3"}, code=1, executed=False)
        run("prerelease-leading-zero", override={"LAODI_VERSION": "v1.2.3-01"}, code=1, executed=False)
    report = {"verification_passed": True, "test_count": len(checks), "tests": checks,
              "real_installation": False, "network_requests": False, "secrets_saved": False}
    if output:
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path)
    main(parser.parse_args().output)
