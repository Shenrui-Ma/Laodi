#!/bin/sh
# Download a verified release and delegate setup to its installer. No build tools.
# Usage: curl -fsSL https://raw.githubusercontent.com/Shenrui-Ma/Laodi-skills/main/install.sh | sh
# Options: sh install.sh [--version vMAJOR.MINOR.PATCH[-PRERELEASE]] [install options]
set -eu
umask 077

fail() {
  printf 'Laodi: %s\n' "$1" >&2
  exit 1
}

main() {
  # User archive-tool defaults must not alter inspection or the extraction directory.
  unset UNZIP UNZIPOPT ZIPINFO ZIPINFOOPT
  laodi_version=${LAODI_VERSION:-v0.3.0-preview.2}
  case "${1:-}" in
    --version)
      [ "$#" -ge 2 ] || fail '--version requires a release tag.'
      laodi_version=$2
      shift 2
      ;;
    --version=*) laodi_version=${1#--version=}; shift ;;
  esac
  if [ "${1:-}" = -- ]; then shift; fi

  for laodi_tool in uname sw_vers curl shasum unzip zipinfo awk mktemp mkdir chmod rm find; do
    command -v "$laodi_tool" >/dev/null 2>&1 || fail "Required system command missing: $laodi_tool"
  done
  # A release tag is a path component, never a URL or executable shell fragment.
  if ! printf '%s\n' "$laodi_version" | awk '
    NR != 1 { bad = 1 }
    NR == 1 {
      if (length($0) > 128 || $0 !~ /^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$/) bad = 1
      core = $0; sub(/^v/, "", core); sub(/-.*/, "", core)
      n = split(core, parts, ".")
      for (i = 1; i <= n; i++) if (parts[i] ~ /^0[0-9]+$/) bad = 1
      suffix = $0
      if (sub(/^v[0-9]+\.[0-9]+\.[0-9]+-/, "", suffix)) {
        n = split(suffix, parts, ".")
        for (i = 1; i <= n; i++) if (parts[i] ~ /^0[0-9]+$/) bad = 1
      }
    }
    END { exit (NR != 1 || bad) }
  '; then
    fail 'Use a release tag such as v0.3.0-preview.2.'
  fi

  [ "$(uname -s)" = Darwin ] || fail 'This release supports macOS only; Windows support is planned.'
  case "$(uname -m)" in arm64|x86_64) ;; *) fail 'Unsupported Mac architecture.' ;; esac
  laodi_macos=$(sw_vers -productVersion) || fail 'Cannot determine the macOS version.'
  if ! printf '%s\n' "$laodi_macos" | awk -F. '
    NR == 1 && /^[0-9]+(\.[0-9]+)*$/ && $1 >= 13 { ok = 1 }
    END { exit (NR != 1 || !ok) }
  '; then
    fail 'macOS 13 or later is required.'
  fi

  laodi_tmp=$(mktemp -d "${TMPDIR:-/tmp}/laodi-install.XXXXXXXX") || fail 'Cannot create a private temporary directory.'
  trap 'rm -rf "$laodi_tmp"' 0
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  chmod 700 "$laodi_tmp"
  laodi_asset="Laodi-skills-$laodi_version-macos-universal.zip"
  laodi_base="https://github.com/Shenrui-Ma/Laodi-skills/releases/download/$laodi_version"
  printf 'Laodi: downloading %s for macOS.\n' "$laodi_version"
  # -q ignores curlrc; HTTPS-only redirects preserve TLS verification and proxy settings.
  if ! curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 300 --retry 2 --max-filesize 268435456 \
    --output "$laodi_tmp/package.zip" "$laodi_base/$laodi_asset"; then
    fail 'Release download failed; no installer was run.'
  fi
  if ! curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 60 --retry 2 --max-filesize 1048576 \
    --output "$laodi_tmp/SHA256SUMS" "$laodi_base/SHA256SUMS"; then
    fail 'Checksum download failed; no installer was run.'
  fi
  if ! laodi_expected=$(awk -v asset="$laodi_asset" '
    { name = $2; sub(/^\*/, "", name) }
    name == asset {
      count++
      if (NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-fA-F]+$/) bad = 1
      digest = tolower($1)
    }
    END { if (count != 1 || bad) exit 1; print digest }
  ' "$laodi_tmp/SHA256SUMS"); then
    fail 'The release must contain exactly one valid checksum for this ZIP.'
  fi
  laodi_actual=$(shasum -a 256 "$laodi_tmp/package.zip") || fail 'Cannot calculate the archive checksum.'
  laodi_actual=${laodi_actual%% *}
  [ "$laodi_actual" = "$laodi_expected" ] || fail 'Checksum mismatch; no installer was run.'

  zipinfo -1 "$laodi_tmp/package.zip" > "$laodi_tmp/paths" || fail 'Cannot inspect the release ZIP.'
  # Releases contain ordinary files only. Reject traversal, ambiguous/duplicate paths,
  # control characters, whitespace and names outside the single package directory.
  if ! laodi_entries=$(LC_ALL=C awk '
    {
      if ($0 !~ /^Laodi-skills\/[A-Za-z0-9._\/-]*$/ || $0 ~ /\/\// || $0 ~ /\/(\.|\.\.)(\/|$)/) bad = 1
      path = tolower($0); sub(/\/$/, "", path)
      if (seen[path]++) bad = 1
    }
    END { if (!NR || bad) exit 1; print NR }
  ' "$laodi_tmp/paths"); then
    fail 'Unsafe or unexpected paths in the release ZIP; no installer was run.'
  fi
  zipinfo -l "$laodi_tmp/package.zip" > "$laodi_tmp/attributes" || fail 'Cannot inspect ZIP file types.'
  if ! LC_ALL=C awk -v expected="$laodi_entries" '
    length($1) == 10 && $1 ~ /^[-d][rwx-]+$/ {
      safe++
      if ($4 !~ /^[0-9]+$/) bad = 1
      bytes += $4
    }
    END { exit (bad || safe != expected || expected > 256 || bytes > 134217728) }
  ' "$laodi_tmp/attributes"; then
    fail 'Unsupported file types or archive exceeds 256 entries / 128 MiB; no installer was run.'
  fi
  mkdir "$laodi_tmp/unpacked"
  unzip -q "$laodi_tmp/package.zip" -d "$laodi_tmp/unpacked" || fail 'Cannot extract the verified release.'
  laodi_links=$(find "$laodi_tmp/unpacked" -type l -print) || fail 'Cannot verify extracted file types.'
  [ -z "$laodi_links" ] || fail 'Unexpected links in the extracted release; no installer was run.'
  laodi_binary="$laodi_tmp/unpacked/Laodi-skills/laodi"
  [ -f "$laodi_binary" ] && [ -x "$laodi_binary" ] || fail 'The release does not contain an executable installer.'
  printf 'Laodi: checksum and archive verified; starting setup.\n'
  # Preserve argv and the installer status, including partially completed setup errors.
  "$laodi_binary" install "$@" < /dev/null
}

main "$@"
