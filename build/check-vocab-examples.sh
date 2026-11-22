#!/bin/sh
# build/check-vocab-examples.sh — every vocab_version a doc example shows is the
# one src/driver.c declares.
#   sh build/check-vocab-examples.sh
#
# The examples are hand-copied engine output, so nothing regenerates them when
# AFMPEG_VOCAB_VERSION moves. They still said 9 two bumps later (#76).
set -eu

want="$(sed -n 's/^#define AFMPEG_VOCAB_VERSION \([0-9][0-9]*\).*/\1/p' src/driver.c)"
[ -n "$want" ] || { echo "check-vocab-examples: no AFMPEG_VOCAB_VERSION in src/driver.c" >&2; exit 2; }

# Both shapes the engine prints: the JSON of `version` and `--capabilities`,
# and the plain-text report.
stale="$(grep -rnoE 'vocab_version"?[:] ?[0-9]+' README.md docs \
  | grep -vE "[:] ?${want}\$" || true)"

if [ -n "$stale" ]; then
  echo "check-vocab-examples: these examples do not show vocab_version ${want} (src/driver.c):" >&2
  echo "$stale" >&2
  exit 1
fi
echo "check-vocab-examples: every example shows vocab_version ${want}"
