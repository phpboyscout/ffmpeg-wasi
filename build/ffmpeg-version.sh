#!/bin/sh
# build/ffmpeg-version.sh — resolve the FFmpeg version this build targets (spec 0035 D3).
#
# build/ffmpeg-version.txt is the single source of truth. This prints the resolved
# version on stdout and nothing else, so a caller can do:
#
#   FFMPEG_VERSION="$(sh build/ffmpeg-version.sh)"
#   export FFMPEG_VERSION
#
# (two statements, not `export FFMPEG_VERSION="$(...)"` — the latter reports
# export's exit status, which is always 0, so a failure here would be swallowed.)
#
# On an n* release tag the tag's version prefix (n8.1.2-12 -> n8.1.2) must equal
# the file, and a disagreement is a hard failure. A tag is how a release is cut,
# and cutting one against a version no merge request ever built is exactly the
# mistake this guard exists to catch.
set -eu

HERE="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
FILE="${FFMPEG_VERSION_FILE:-$HERE/ffmpeg-version.txt}"

[ -f "$FILE" ] || { echo "ffmpeg-version: no version file at $FILE" >&2; exit 2; }

# First meaningful line: strip comments and all whitespace, drop what is left empty.
version="$(sed -e 's/#.*//' -e 's/[[:space:]]//g' "$FILE" | grep -v '^$' | head -n 1)"
[ -n "$version" ] || { echo "ffmpeg-version: $FILE names no version" >&2; exit 2; }

# Only n* tags name an FFmpeg version; repo-meta tags (e.g. 0.1.0) do not and are
# not checked. Absent a tag — a merge request, a branch, a local build — the file
# is simply the answer.
case "${CI_COMMIT_TAG:-}" in
n[0-9]*)
  tagged="${CI_COMMIT_TAG%-*}"
  if [ "$tagged" != "$version" ]; then
    echo "ffmpeg-version: tag ${CI_COMMIT_TAG} names FFmpeg ${tagged}, but ${FILE} says ${version}." >&2
    echo "ffmpeg-version: the file is authoritative (spec 0035 D3). Bump it in a merge" >&2
    echo "ffmpeg-version: request -- which builds it -- before tagging, or retag to match." >&2
    exit 1
  fi
  ;;
esac

printf '%s\n' "$version"
