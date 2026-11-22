#!/bin/sh
# build/memcheck.sh — run the conformance suite with every native driver under
# valgrind memcheck (ffmpeg-wasi#75).
#   sh build/memcheck.sh <artifacts-dir>
#
# An uninitialised read fails the job it happens in, on every run. The plain
# suite only catches one when the stack happens to hold the wrong bytes: #74
# failed 26 runs in 100 on one build and none on another, and ASan does not
# look for this class at all.
set -eu

[ "$#" -eq 1 ] || { echo "usage: memcheck.sh <artifacts-dir>" >&2; exit 2; }

if ! command -v valgrind >/dev/null; then
  echo "memcheck: valgrind is not on PATH" >&2
  exit 1
fi

dir="$(cd "$1" && pwd)"
# Without a native driver every test skips, and the run would pass having
# checked nothing.
found=0
for f in "$dir"/ffmpeg-wasi-driver-linux-amd64-*; do
  [ -x "$f" ] && found=1
done
if [ "$found" -eq 0 ]; then
  echo "memcheck: no executable native driver in $dir, so every test would skip" >&2
  exit 1
fi

FFMPEG_WASI_ARTIFACTS="$dir" \
FFMPEG_WASI_NATIVE_WRAPPER="valgrind -q --error-exitcode=99 --track-origins=yes" \
  go test ./internal/conformance/ -count=1 -v -timeout 90m
