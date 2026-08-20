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

# Engine sources: the driver + operations, the I/O abstraction, and the vendored
# JSON parser. The wasi compat shim is wasm-only (native has a real libc).
ENGINE_SRC="$DRIVER_SRC $SRC_DIR/process.c $SRC_DIR/frames.c $SRC_DIR/meta.c $SRC_DIR/nativeio.c $SRC_DIR/progress.c $SRC_DIR/third_party/cJSON/cJSON.c"
[ "$TARGET" = native ] || ENGINE_SRC="$ENGINE_SRC $HERE/wasi-compat.c"

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
    ass)               echo 15 ;;   # → freetype, fribidi, harfbuzz
    vorbisfile)        echo 15 ;;   # → vorbis → ogg (unreferenced today, ordered anyway)
    vorbis)            echo 20 ;;
    webp)              echo 30 ;;   # → sharpyuv
    harfbuzz)          echo 40 ;;   # → freetype (consumed by ass + drawtext)
    freetype)          echo 70 ;;   # leaf provider (harfbuzz/libass/drawtext)
    fribidi)           echo 70 ;;   # leaf provider (libass)
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

LIBAV_L="-L$FFMPEG_SRC/libavformat -L$FFMPEG_SRC/libavcodec -L$FFMPEG_SRC/libavfilter \
-L$FFMPEG_SRC/libavutil -L$FFMPEG_SRC/libswresample -L$FFMPEG_SRC/libswscale"
LIBAV_l="-lavformat -lavcodec -lavfilter -lavutil -lswresample -lswscale"

# Warnings for OUR code only — CFLAGS is shared with the vendored dependencies,
# which are not ours to make noisy.
#
# -Werror=return-type is the one that earns its place. Falling off the end of a
# non-void function is never intentional, and it is undefined behaviour rather
# than a style point: the caller reads whatever happens to be in the return
# register. It shipped here, in probe_input, and every test still passed because
# that register usually held zero. A whole-source review caught it; a compiler
# flag would have caught it in the same second it was written.
ENGINE_WARNINGS="-Wall -Werror=return-type"

if [ "$TARGET" = native ]; then
  # Native driver (spec 0028): a real ELF executable. libc/setjmp/threads are the
  # host's, so no wasm shims, no 8 MB stack bump, no -lsetjmp. --start-group lets
  # the linker resolve the libav* interdependencies without hand-ordering (native
  # ld has it; wasm-ld does not). -lstdc++ covers openh264's C++ runtime when the
  # external encoders land; -lm/-lpthread/-lz are libav*'s system deps.
  # -DAFMPEG_NATIVE turns on the seekable AVIO-over-IPC media I/O (src/nativeio.c),
  # so the driver serves inputs/outputs through the afmpeg native host's socket.
  # $DEP_LIBS are the native external encoders from deps.sh (openh264, + x264 on
  # gpl); --start-group covers the libav*↔codec interdependencies.
  # shellcheck disable=SC2086  # $ENGINE_SRC/$LIBAV_*/$DEP_LIBS are deliberately split
  $CC $CFLAGS $ENGINE_WARNINGS -DAFMPEG_NATIVE -I"$FFMPEG_SRC" $ENGINE_SRC -o "$OUT" \
    $LIBAV_L -L"$PREFIX/lib" \
    -Wl,--start-group $LIBAV_l $DEP_LIBS -Wl,--end-group \
    -lz -lm -lpthread -lstdc++
else
  # clang links a WASI command (crt1/_start) itself for wasm32-wasip1 + main();
  # no wasm-ld-only flags here. -lc++/-lc++abi resolve openh264's C++ runtime (it is
  # the one C++ dependency; the engine and libav* are C), linked after the codec libs.
  # -lsetjmp resolves the __wasm_setjmp/__wasm_longjmp that clang's SjLj lowering
  # emits for libvpx's encoder (its setjmp/longjmp use); harmless when unreferenced.
  #
  # -z stack-size lifts the wasm data stack from wasi-sdk's 64 KB default to 8 MB:
  # the engine's op_process holds a large Ctx and FFmpeg's native encoders recurse
  # deeply (the mpegvideo/mjpeg path most of all), so 64 KB overflows into a trap.
  # shellcheck disable=SC2086  # $ENGINE_SRC/$DEP_LIBS/$WASI_EMULATED_LIBS are deliberately split
  $CC $CFLAGS $ENGINE_WARNINGS -Wl,-z,stack-size=8388608 -I"$FFMPEG_SRC" $ENGINE_SRC -o "$OUT" \
    $LIBAV_L $LIBAV_l \
    -L"$PREFIX/lib" $DEP_LIBS \
    -lc++ -lc++abi -lsetjmp \
    $WASI_EMULATED_LIBS
fi
ls -la "$OUT"
echo "engine linked → $OUT ($TARGET)"
