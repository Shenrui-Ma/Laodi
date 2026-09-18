#!/bin/sh
set -eu

notifier_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
notifier_output=${1:-"$notifier_root/build"}
notifier_bundle="$notifier_output/LaodiNotify.app"

if ! command -v xcrun >/dev/null 2>&1; then
  echo 'Xcode Command Line Tools (xcrun/clang) are required.' >&2
  exit 1
fi

mkdir -p "$notifier_bundle/Contents/MacOS"
cp "$notifier_root/Info.plist" "$notifier_bundle/Contents/Info.plist"
xcrun clang -fobjc-arc -fblocks -Wall -Wextra -Werror \
  -mmacosx-version-min=12.0 \
  -framework Foundation -framework UserNotifications \
  "$notifier_root/main.m" -o "$notifier_bundle/Contents/MacOS/LaodiNotify"
plutil -lint "$notifier_bundle/Contents/Info.plist"
echo "Built $notifier_bundle"
echo 'Development bundle only: not installed, Developer ID signed, or notarized.'
