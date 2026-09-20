#!/bin/sh
set -eu

if [ "$(uname -s)" != Darwin ]; then
  echo 'This package requires macOS 13 or later.' >&2
  exit 1
fi
release_os_major=$(/usr/bin/sw_vers -productVersion | /usr/bin/cut -d . -f 1)
if [ "$release_os_major" -lt 13 ]; then
  echo 'Laodi 的此预览包需要 macOS 13 或更新版本。' >&2
  exit 1
fi
release_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$release_directory"
exec ./laodi install
