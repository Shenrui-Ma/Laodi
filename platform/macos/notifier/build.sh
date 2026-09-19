#!/bin/sh
set -eu

notifier_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
notifier_output=${1:-"$notifier_root/build"}
notifier_version=${2:-"0.3.0"}
notifier_bundle="$notifier_output/LaodiNotify.app"

if ! printf '%s\n' "$notifier_version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo 'Notifier version must have the form MAJOR.MINOR.PATCH.' >&2
  exit 1
fi

if ! command -v xcrun >/dev/null 2>&1; then
  echo 'Xcode Command Line Tools (xcrun/clang) are required.' >&2
  exit 1
fi

mkdir -p "$notifier_bundle/Contents/MacOS"
cp "$notifier_root/Info.plist" "$notifier_bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $notifier_version" "$notifier_bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $notifier_version" "$notifier_bundle/Contents/Info.plist"
xcrun clang -fobjc-arc -fblocks -Wall -Wextra -Werror \
  -arch arm64 -arch x86_64 \
  -mmacosx-version-min=12.0 \
  -framework Foundation -framework UserNotifications \
  "$notifier_root/main.m" -o "$notifier_bundle/Contents/MacOS/LaodiNotify"
plutil -lint "$notifier_bundle/Contents/Info.plist"
codesign --force --sign - --timestamp=none "$notifier_bundle"
codesign --verify --strict --verbose=2 "$notifier_bundle"
echo "Built $notifier_bundle"
echo 'Universal development bundle, ad-hoc signed. Not Developer ID signed or notarized.'
