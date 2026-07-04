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
ENGINE_SRC="$DRIVER_SRC $SRC_DIR/process.c $SRC_DIR/frames.c $SRC_DIR/meta.c $SRC_DIR/third_party/cJSON/cJSON.c $HERE/wasi-compat.c"

# External codec libraries the libav* archives depend on (e.g. libx264 in the GPL
# variant), discovered from $PREFIX and linked AFTER libav* so their symbols
# resolve. Empty for a dependency-free build.
#
# wasm-ld resolves archives strictly in command order (it has no --start-group),
# and the audio libs interdepend: libvorbisenc → libvorbis → libogg. So a provider
# must come AFTER every consumer. dep_rank orders them (lower = earlier); leaf
# providers (ogg, z) sort last, unranked libs sit in the middle.
dep_rank() {
  case "$1" in
    webpmux|webpdemux) echo 10 ;;   # → webp
    vorbisenc)         echo 10 ;;   # → vorbis → ogg
    vorbis)            echo 20 ;;
    webp)              echo 30 ;;   # → sharpyuv
    sharpyuv)          echo 70 ;;   # leaf provider (libwebp)
    ogg)               echo 80 ;;   # leaf provider (libvorbis)
    z)                 echo 90 ;;   # leaf provider
    *)                 echo 50 ;;
  esac
}
DEP_LIBS=""
for n in $(for a in "$PREFIX"/lib/lib*.a; do
    [ -e "$a" ] || continue
    b="$(basename "$a" .a)"; b="${b#lib}"
    printf '%s:%s\n' "$(dep_rank "$b")" "$b"
  done | sort -n -t: -k1 | cut -d: -f2); do
  DEP_LIBS="$DEP_LIBS -l$n"
done

# clang links a WASI command (crt1/_start) itself for wasm32-wasip1 + main();
# no wasm-ld-only flags here. -lc++/-lc++abi resolve openh264's C++ runtime (it is
# the one C++ dependency; the engine and libav* are C), linked after the codec libs.
# -lsetjmp resolves the __wasm_setjmp/__wasm_longjmp that clang's SjLj lowering
# emits for libvpx's encoder (its setjmp/longjmp use); harmless when unreferenced.
# shellcheck disable=SC2086  # $ENGINE_SRC/$DEP_LIBS/$WASI_EMULATED_LIBS are deliberately split
$CC $CFLAGS -I"$FFMPEG_SRC" $ENGINE_SRC -o "$OUT" \
  -L"$FFMPEG_SRC/libavformat" -L"$FFMPEG_SRC/libavcodec" -L"$FFMPEG_SRC/libavfilter" \
  -L"$FFMPEG_SRC/libavutil" -L"$FFMPEG_SRC/libswresample" -L"$FFMPEG_SRC/libswscale" \
  -lavformat -lavcodec -lavfilter -lavutil -lswresample -lswscale \
  -L"$PREFIX/lib" $DEP_LIBS \
  -lc++ -lc++abi -lsetjmp \
  $WASI_EMULATED_LIBS
ls -la "$OUT"
echo "engine linked → $OUT"
