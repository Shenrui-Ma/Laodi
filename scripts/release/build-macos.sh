#!/bin/sh
# Build release assets without installing anything or changing user settings.
set -eu
umask 022

release_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
release_tag=${1:-}
release_allow_dirty=false
if [ "${2:-}" = '--allow-dirty' ] && [ "$#" -eq 2 ]; then
  release_allow_dirty=true
elif [ "$#" -ne 1 ]; then
  echo 'Usage: GO=/path/to/go sh scripts/release/build-macos.sh vMAJOR.MINOR.PATCH[-prerelease] [--allow-dirty]' >&2
  exit 2
fi
if ! printf '%s\n' "$release_tag" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; then
  echo 'Release tag must be vMAJOR.MINOR.PATCH with an optional prerelease suffix.' >&2
  exit 2
fi
if [ "$(uname -s)" != Darwin ]; then
  echo 'A macOS build host with Xcode Command Line Tools is required.' >&2
  exit 1
fi

release_go=${GO:-go}
for release_tool in "$release_go" git xcrun codesign plutil zip shasum; do
  if ! command -v "$release_tool" >/dev/null 2>&1; then
    echo "Required build tool not found: $release_tool" >&2
    exit 1
  fi
done
cd "$release_root"
release_commit=$(git rev-parse --verify HEAD)
release_dirty=false
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
  release_dirty=true
  if [ "$release_allow_dirty" != true ]; then
    echo 'Refusing a release build from a dirty tree. Commit reviewed changes first; --allow-dirty is for local verification only.' >&2
    exit 1
  fi
fi
release_version=${release_tag#v}
release_base_version=${release_version%%-*}
release_go_version=$("$release_go" env GOVERSION)
if ! printf '%s\n' "$release_go_version" | grep -Eq '^[A-Za-z0-9._+-]+$'; then
  echo 'Unsupported Go version string for build metadata.' >&2
  exit 1
fi
release_host_arch=$(uname -m)
case "$release_host_arch" in arm64|x86_64) ;; *) echo 'Unsupported macOS build host architecture.' >&2; exit 1 ;; esac
release_output="$release_root/dist/$release_tag"
if [ -e "$release_output" ]; then
  echo "Output already exists: $release_output (move it aside before rebuilding)." >&2
  exit 1
fi
release_work=$(mktemp -d "${TMPDIR:-/tmp}/laodi-release.XXXXXX")
trap 'rm -rf "$release_work"' EXIT HUP INT TERM
release_bundle="$release_work/package/Laodi-skills"
mkdir -p "$release_bundle/skills"

for release_arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH="$release_arch" GOTOOLCHAIN=local \
    "$release_go" build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$release_version" \
    -o "$release_work/laodi-$release_arch" ./cmd/laodi
done
xcrun lipo -create "$release_work/laodi-arm64" "$release_work/laodi-amd64" -output "$release_bundle/laodi"
codesign --force --sign - --identifier dev.laodi.cli --timestamp=none "$release_bundle/laodi"
sh platform/macos/notifier/build.sh "$release_work/notifier" "$release_base_version"
cp -R "$release_work/notifier/LaodiNotify.app" "$release_bundle/LaodiNotify.app"
# Only tracked Skill sources belong in a distribution, never ignored local files.
git ls-files skills/laodi | while IFS= read -r release_skill; do
  mkdir -p "$release_bundle/$(dirname "$release_skill")"
  cp "$release_skill" "$release_bundle/$release_skill"
done
cp LICENSE "$release_bundle/LICENSE"
cp assets/README.md "$release_bundle/ASSETS.md"
cp scripts/release/INSTALL.txt "$release_bundle/INSTALL.txt"
cp scripts/release/install.command "$release_bundle/install.command"
cp scripts/release/uninstall.command "$release_bundle/uninstall.command"
chmod 755 "$release_bundle/laodi" "$release_bundle/install.command" "$release_bundle/uninstall.command"

xcrun lipo "$release_bundle/laodi" -verify_arch arm64 x86_64
xcrun lipo "$release_bundle/LaodiNotify.app/Contents/MacOS/LaodiNotify" -verify_arch arm64 x86_64
codesign --verify --strict --all-architectures --verbose=2 "$release_bundle/laodi"
codesign --verify --strict --all-architectures --verbose=2 "$release_bundle/LaodiNotify.app"
if [ "$("$release_bundle/laodi" version)" != "$release_version" ]; then
  echo 'Built CLI does not report the requested release version.' >&2
  exit 1
fi
"$release_bundle/laodi" --help >/dev/null
release_minos=$(xcrun vtool -show-build "$release_work/laodi-arm64" | awk '$1 == "minos" { print $2; exit }')
if ! printf '%s\n' "$release_minos" | grep -Eq '^[0-9]+\.[0-9]+(\.[0-9]+)?$'; then
  echo 'Unable to verify the Go binary deployment target.' >&2
  exit 1
fi
release_archive="Laodi-skills-$release_tag-macos-universal.zip"
release_built_at=$(date -u +'%Y-%m-%dT%H:%M:%SZ')
cat > "$release_work/build-info.json" <<EOF
{
  "version": "$release_version",
  "tag": "$release_tag",
  "commit": "$release_commit",
  "dirty": $release_dirty,
  "built_at": "$release_built_at",
  "go_version": "$release_go_version",
  "platform": "darwin",
  "architectures": ["arm64", "amd64"],
  "build_host_architecture": "$release_host_arch",
  "cli_deployment_target": "$release_minos",
  "notifier_deployment_target": "12.0",
  "documented_minimum_macos": "13.0",
  "signature": "ad-hoc",
  "developer_id_signed": false,
  "notarized": false,
  "verification": ["both_architectures_present", "code_signature_valid", "native_cli_version_and_help"],
  "not_verified": ["other_architecture_runtime", "gatekeeper_download_flow", "notification_delivery", "live_agent_callbacks"],
  "archive": "$release_archive"
}
EOF
# COPYFILE_DISABLE avoids resource forks; -X excludes machine-specific extra fields.
(cd "$release_work/package" && COPYFILE_DISABLE=1 zip -q -X -r "$release_work/$release_archive" Laodi-skills)
mkdir -p "$release_output"
cp "$release_work/$release_archive" "$release_output/$release_archive"
cp "$release_work/build-info.json" "$release_output/build-info.json"
(cd "$release_output" && shasum -a 256 "$release_archive" > SHA256SUMS && shasum -a 256 -c SHA256SUMS)
echo "Release assets: $release_output"
echo 'Ad-hoc signed preview; no Developer ID signature or notarization.'
