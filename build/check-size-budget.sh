#!/bin/sh
# build/check-size-budget.sh — flag per-artifact size regressions (spec 0022).
#
# Reads build/size-budget.txt (`<artifact-name> <max-bytes>` lines, `#` comments)
# and, for every release artifact present in the working directory, prints its size
# and headroom against its budget. A budgeted artifact over its ceiling is a failure
# (exit 1); an artifact with no budget line is reported, not failed (so a new
# artifact surfaces its size to seed a budget). Missing artifacts are skipped.
#
# The gate is deliberately generous — it catches a profile ballooning (a dep bloating
# a build), not a few percent of drift. Bump a budget in build/size-budget.txt, in
# review, when a real increase lands.
set -eu

HERE="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
BUDGET="${SIZE_BUDGET_FILE:-$HERE/size-budget.txt}"
[ -f "$BUDGET" ] || { echo "size-budget: no budget file at $BUDGET" >&2; exit 2; }

# file size in bytes, portable (GNU stat -c, BSD/macOS stat -f).
fsize() { stat -c %s "$1" 2>/dev/null || stat -f %z "$1" 2>/dev/null; }

fail=0
printf '%-52s %12s %12s %8s\n' "ARTIFACT" "SIZE" "BUDGET" "USED"

# Iterate the budgeted artifacts in declaration order; check the ones present.
while read -r name max _rest; do
  case "$name" in ''|\#*) continue ;; esac
  [ -f "$name" ] || continue

  size=$(fsize "$name")
  [ -n "$size" ] || { echo "size-budget: cannot stat $name" >&2; fail=1; continue; }

  pct=$(( size * 100 / max ))
  flag=""
  if [ "$size" -gt "$max" ]; then flag=" OVER"; fail=1; fi
  printf '%-52s %12s %12s %7s%%%s\n' "$name" "$size" "$max" "$pct" "$flag"
done < "$BUDGET"

# Report any present artifact that has no budget line (advisory — seed a budget).
for f in ffmpeg-wasi-*.wasm ffmpeg-wasi-driver-linux-amd64-*; do
  [ -f "$f" ] || continue
  case "$f" in *.gz) continue ;; esac
  if ! grep -qE "^[[:space:]]*${f}[[:space:]]" "$BUDGET"; then
    printf '%-52s %12s %12s %8s\n' "$f" "$(fsize "$f")" "(none)" "n/a"
    echo "size-budget: note — $f has no budget line (add one to $BUDGET)" >&2
  fi
done

[ "$fail" -eq 0 ] || echo "size-budget: FAIL — an artifact exceeded its budget (above)" >&2
exit "$fail"
