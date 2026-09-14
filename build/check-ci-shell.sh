#!/bin/sh
# build/check-ci-shell.sh — reject AND-OR chains in CI job script lines.
#   sh build/check-ci-shell.sh .gitlab-ci.yml build/engine.gitlab-ci.yml
#
# POSIX 2.11: `set -e` is ignored for every command of an AND-OR list except the
# last. So `apt-get update && apt-get install ...` carries straight on when the
# update fails, and the job dies minutes later somewhere that names neither.
# That is ffmpeg-wasi#68 — the visible failure was `git: not found` in deps.sh,
# four minutes and one stage removed from the apt hash mismatch that caused it.
#
# GitLab is not the culprit and neither is the before_script boundary; both
# behave as documented. The `&&` is the whole of it, which is why this checks
# the shape rather than the stage.
#
# One command per line. A conditional wants `if ...; then ...; fi`, whose body
# `set -e` still governs; anything longer belongs in a script under build/.
set -eu

[ "$#" -gt 0 ] || { echo "usage: check-ci-shell.sh <ci-yaml>..." >&2; exit 2; }

status=0
for f in "$@"; do
  if [ ! -f "$f" ]; then
    echo "check-ci-shell: no such file: $f" >&2
    exit 2
  fi
  # Track the script blocks by indentation: a key at column N owns every line
  # indented deeper than N, which covers both `- cmd` items and block scalars.
  found=$(awk '
    function trim(s) { sub(/^[ \t]+/, "", s); return s }
    {
      if ($0 ~ /^[ \t]*$/ || $0 ~ /^[ \t]*#/) next
      match($0, /^[ \t]*/); ind = RLENGTH
      if (inblock && ind <= keyind) inblock = 0
      if ($0 ~ /^[ \t]*(before_script|script|after_script):/) {
        keyind = ind
        rest = $0; sub(/^[ \t]*(before_script|script|after_script):/, "", rest)
        if (index(rest, "&&")) printf "%s:%d: %s\n", FILENAME, FNR, trim($0)
        inblock = 1
        next
      }
      if (inblock && index($0, "&&")) printf "%s:%d: %s\n", FILENAME, FNR, trim($0)
    }
  ' "$f")
  if [ -n "$found" ]; then
    echo "$found"
    status=1
  fi
done

if [ "$status" -ne 0 ]; then
  cat >&2 <<'MSG'

`&&` in a CI job line: set -e is ignored for every command of an AND-OR list but
the last, so a failure above will not stop the job — it will surface later,
somewhere that does not name it (ffmpeg-wasi#68).

Use one command per line, or `if ...; then ...; fi`.
MSG
fi
exit "$status"
