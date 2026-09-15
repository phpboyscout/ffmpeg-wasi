#!/bin/sh
# build/check-release-set.sh — the release links exactly what the signature covers.
#
# Two files name the release set, and they must agree:
#
#   build/sign-release.sh   the arguments to sha256sum. This is the set
#                           checksums.txt covers and checksums.txt.sig
#                           certifies, so it is the authority.
#   .colophon.yaml          `assets`, which is what colophon links on the
#                           release object.
#
# The manifest is the signed set plus checksums.txt and checksums.txt.sig,
# which cannot appear in a checksum of themselves.
#
# Why this exists: n9.0.1-6 linked seventeen of twenty-three. Six gzipped
# native drivers were built, signed and uploaded, and then not offered on the
# release page, because the link list had been written once beside the signing
# script and never compared with it. Nothing failed; the release was simply
# missing a third of itself, and the count was the only clue.
#
# The upload job no longer needs checking: it reads checksums.txt directly.
set -eu

HERE="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
SIGN="$HERE/build/sign-release.sh"
MANIFEST="$HERE/.colophon.yaml"

[ -f "$SIGN" ] || { echo "check-release-set: no $SIGN" >&2; exit 2; }
[ -f "$MANIFEST" ] || { echo "check-release-set: no $MANIFEST" >&2; exit 2; }

# The sha256sum arguments, one per line: from the line starting the command to
# the redirect that ends it, with continuations and the command name removed.
signed="$(
  sed -n '/^sha256sum /,/> checksums.txt/p' "$SIGN" \
    | sed -e 's/\\$//' -e 's/^sha256sum //' -e 's/> checksums.txt//' \
    | tr ' ' '\n' \
    | sed '/^[[:space:]]*$/d'
)"

# `assets:` to the end of the block: "  - name" lines only.
listed="$(
  sed -n '/^assets:/,$p' "$MANIFEST" \
    | sed -n 's/^[[:space:]]*-[[:space:]]*//p' \
    | sed '/^[[:space:]]*$/d'
)"

expected="$(printf '%s\nchecksums.txt\nchecksums.txt.sig\n' "$signed" | sort -u)"
actual="$(printf '%s\n' "$listed" | sort -u)"

if [ "$expected" = "$actual" ]; then
  echo "check-release-set: .colophon.yaml links all $(printf '%s\n' "$actual" | wc -l | tr -d ' ') signed artefacts"
  exit 0
fi

echo "check-release-set: .colophon.yaml and the signed set disagree." >&2
echo >&2

# Reported by walking each list rather than with comm and process
# substitution: this runs under the busybox sh in the shellcheck image, where
# <(...) is a syntax error rather than a slower answer.
echo "$expected" | while IFS= read -r name; do
  [ -n "$name" ] || continue
  if ! echo "$actual" | grep -qxF "$name"; then
    echo "  signed, not linked (published and never offered): $name" >&2
  fi
done

echo "$actual" | while IFS= read -r name; do
  [ -n "$name" ] || continue
  if ! echo "$expected" | grep -qxF "$name"; then
    echo "  linked, not signed (colophon would refuse the release): $name" >&2
  fi
done

exit 1
