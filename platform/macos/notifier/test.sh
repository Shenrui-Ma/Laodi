#!/bin/sh
set -eu

notifier_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
notifier_test_dir=$(mktemp -d "${TMPDIR:-/tmp}/laodi-notifier-test.XXXXXX")
trap 'rm -rf "$notifier_test_dir"' EXIT HUP INT TERM
sh "$notifier_root/build.sh" "$notifier_test_dir" 0.3.0
notifier_test_bundle="$notifier_test_dir/LaodiNotify.app"
notifier_test_exe="$notifier_test_bundle/Contents/MacOS/LaodiNotify"
iconutil -c iconset "$notifier_test_bundle/Contents/Resources/Laodi.icns" \
  -o "$notifier_test_dir/Laodi.iconset"
test -s "$notifier_test_dir/Laodi.iconset/icon_512x512.png"
"$notifier_test_exe" --help | grep -q 'archive-blocked-test'
if "$notifier_test_exe" --send --id opaque --kind untrusted-kind >"$notifier_test_dir/invalid.json"; then
  echo 'Unknown notification kind unexpectedly accepted.' >&2
  exit 1
fi
grep -q 'invalid_arguments' "$notifier_test_dir/invalid.json"
xcrun clang -fobjc-arc -fblocks -Wall -Wextra -Werror \
  -arch arm64 -arch x86_64 -mmacosx-version-min=12.0 \
  -framework Foundation -framework UserNotifications \
  "$notifier_root/test.m" -o "$notifier_test_exe"
codesign --force --sign - --timestamp=none "$notifier_test_bundle"
codesign --verify --strict "$notifier_test_bundle"
"$notifier_test_exe"
codesign --verify --strict "$notifier_test_bundle"
