#!/bin/sh
# build/driver.sh — compile + link the libav-direct engine to a .wasm module.
#   OUT=/dist/ffmpeg-wasi-lgpl.wasm sh build/driver.sh
set -eu
HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
. "$HERE/toolchain.sh"

: "${FFMPEG_SRC:=/ffmpeg}"
: "${DRIVER_SRC:=$HERE/../src/driver.c}"
: "${OUT:=/dist/ffmpeg-wasi.wasm}"
mkdir -p "$(dirname -- "$OUT")"

# clang links a WASI command (crt1/_start) itself for wasm32-wasip1 + main();
# no wasm-ld-only flags here.
# shellcheck disable=SC2086
$CC $CFLAGS -I"$FFMPEG_SRC" "$DRIVER_SRC" "$HERE/wasi-compat.c" -o "$OUT" \
  -L"$FFMPEG_SRC/libavformat" -L"$FFMPEG_SRC/libavcodec" -L"$FFMPEG_SRC/libavfilter" \
  -L"$FFMPEG_SRC/libavutil" -L"$FFMPEG_SRC/libswresample" -L"$FFMPEG_SRC/libswscale" \
  -lavformat -lavcodec -lavfilter -lavutil -lswresample -lswscale \
  $WASI_EMULATED_LIBS
ls -la "$OUT"
echo "engine linked → $OUT"
