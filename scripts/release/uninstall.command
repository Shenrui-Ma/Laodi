#!/bin/sh
set -eu

release_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$release_directory"
exec ./laodi remove
