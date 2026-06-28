#!/bin/sh
# build/driver.sh — compile + link the libav-direct engine to a .wasm module.
#   OUT=/dist/ffmpeg-wasi-lgpl.wasm sh build/driver.sh
set -eu
HERE="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091  # sourced at build time
. "$HERE/toolchain.sh"

: "${FFMPEG_SRC:=/ffmpeg}"
: "${SRC_DIR:=$HERE/../src}"
: "${DRIVER_SRC:=$SRC_DIR/driver.c}"
: "${OUT:=/dist/ffmpeg-wasi.wasm}"
mkdir -p "$(dirname -- "$OUT")"

# Engine sources: the driver + operations, the vendored JSON parser, and the wasi shims.
ENGINE_SRC="$DRIVER_SRC $SRC_DIR/process.c $SRC_DIR/third_party/cJSON/cJSON.c $HERE/wasi-compat.c"

# External codec libraries the libav* archives depend on (e.g. libx264 in the GPL
# variant), discovered from $PREFIX and linked AFTER libav* so their symbols
# resolve. Empty for a dependency-free build.
DEP_LIBS=""
for a in "$PREFIX"/lib/lib*.a; do
  [ -e "$a" ] || continue
  n="$(basename "$a" .a)"
  DEP_LIBS="$DEP_LIBS -l${n#lib}"
done

# clang links a WASI command (crt1/_start) itself for wasm32-wasip1 + main();
# no wasm-ld-only flags here.
# shellcheck disable=SC2086  # $ENGINE_SRC/$DEP_LIBS/$WASI_EMULATED_LIBS are deliberately split
$CC $CFLAGS -I"$FFMPEG_SRC" $ENGINE_SRC -o "$OUT" \
  -L"$FFMPEG_SRC/libavformat" -L"$FFMPEG_SRC/libavcodec" -L"$FFMPEG_SRC/libavfilter" \
  -L"$FFMPEG_SRC/libavutil" -L"$FFMPEG_SRC/libswresample" -L"$FFMPEG_SRC/libswscale" \
  -lavformat -lavcodec -lavfilter -lavutil -lswresample -lswscale \
  -L"$PREFIX/lib" $DEP_LIBS \
  $WASI_EMULATED_LIBS
ls -la "$OUT"
echo "engine linked → $OUT"
