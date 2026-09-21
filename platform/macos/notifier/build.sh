#!/bin/sh
set -eu

notifier_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
notifier_output=${1:-"$notifier_root/build"}
notifier_version=${2:-"0.3.0"}
notifier_bundle="$notifier_output/LaodiNotify.app"
notifier_logo="$notifier_root/../../../assets/laodi-logo.png"

if ! printf '%s\n' "$notifier_version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo 'Notifier version must have the form MAJOR.MINOR.PATCH.' >&2
  exit 1
fi

if ! command -v xcrun >/dev/null 2>&1; then
  echo 'Xcode Command Line Tools (xcrun/clang) are required.' >&2
  exit 1
fi

for notifier_tool in sips iconutil; do
  if ! command -v "$notifier_tool" >/dev/null 2>&1; then
    echo "Required macOS icon tool not found: $notifier_tool" >&2
    exit 1
  fi
done

# Keep the supplied artwork unchanged; generate the standard macOS icon sizes.
notifier_icon_work=$(mktemp -d "${TMPDIR:-/tmp}/laodi-icon.XXXXXX")
trap 'rm -rf "$notifier_icon_work"' EXIT HUP INT TERM
notifier_iconset="$notifier_icon_work/Laodi.iconset"
mkdir -p "$notifier_iconset" "$notifier_bundle/Contents/MacOS" "$notifier_bundle/Contents/Resources"
for notifier_size in 16 32 128 256 512; do
  sips -z "$notifier_size" "$notifier_size" "$notifier_logo" \
    --out "$notifier_iconset/icon_${notifier_size}x${notifier_size}.png" >/dev/null
  notifier_retina=$((notifier_size * 2))
  sips -z "$notifier_retina" "$notifier_retina" "$notifier_logo" \
    --out "$notifier_iconset/icon_${notifier_size}x${notifier_size}@2x.png" >/dev/null
done
iconutil -c icns "$notifier_iconset" -o "$notifier_bundle/Contents/Resources/Laodi.icns"
# Remove the obsolete attachment asset when rebuilding an existing output.
rm -f "$notifier_bundle/Contents/Resources/LaodiNotification.png"
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
