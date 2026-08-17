#!/bin/sh
# build/libav.sh — configure + build FFmpeg's libav* libraries for wasm32-wasi.
#   FFMPEG_VERSION=n8.1.2 VARIANT=lgpl PROFILE=lean sh build/libav.sh
set -eu
HERE_LIBAV="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091  # sourced at build time
. "$HERE_LIBAV/toolchain.sh"

: "${FFMPEG_VERSION:?set FFMPEG_VERSION, e.g. n8.1.2}"
: "${VARIANT:=lgpl}"                       # lgpl (default) | gpl
: "${PROFILE:=lean}"                        # lean (default) | intermediate | full — spec 0022 (full is native-only)
: "${FFMPEG_SRC:=/ffmpeg}"

git clone https://github.com/FFmpeg/FFmpeg --depth=1 --branch "$FFMPEG_VERSION" "$FFMPEG_SRC"
cd "$FFMPEG_SRC"

# Forward the concat demuxer's custom io_open into its per-segment sub-context so a
# concat segment routes through the native IPC bridge (spec 0028), not the `file`
# protocol. A no-op for wasm (default io_open); see build/ffmpeg-concat-ioopen.patch.
git apply "$HERE_LIBAV/ffmpeg-concat-ioopen.patch"

# The component allowlist per (PROFILE, VARIANT) — extracted to its own file so
# the conformance suite reads the same definition this build uses (spec 0036 D3).
# Sets ENABLE, OPENH264_FLAGS, GPL_FLAGS and COMPONENT_FLAGS.
# shellcheck disable=SC1091  # sourced at build time
. "$HERE_LIBAV/enable-lists.sh"

if [ "$TARGET" = native ]; then
  # --- TARGET=native (spec 0028 Backend B) ---------------------------------
  # The host build: threads + SIMD on (the whole point of native), no cross
  # machinery, no wasi shims, no HAVE_* fixups. Built from the same allowlist as
  # wasm (lean, or lean + the intermediate batch) plus the native openh264 (both
  # variants) / libx264 (gpl) H.264 encoders from deps.sh — encode at native speed.
  # The intermediate profile links the native Opus/MP3/Vorbis/WebP/VP8-9 + subtitle/
  # burn-in libs, matching the wasm intermediate capability (0022 parity).
  # --extra-cflags/--extra-ldflags anchor $PREFIX on the include + link paths.
  # pkg-config libs (openh264/x264/opus/vorbis/webp/vpx/freetype/harfbuzz/fribidi/
  # ass) carry their own -I/-L via .pc, but libmp3lame ships no pkg-config file, so
  # ffmpeg link-tests a bare -lmp3lame — which needs -L$PREFIX/lib to resolve.
  # shellcheck disable=SC2086  # $ENABLE/$OPENH264_FLAGS/$GPL_FLAGS are deliberately split into args
  ./configure \
    --cc="$CC" --cxx="$CXX" --ar="$AR" --ranlib="$RANLIB" \
    --pkg-config-flags=--static \
    --extra-cflags="-I$PREFIX/include" --extra-ldflags="-L$PREFIX/lib" \
    --disable-shared --enable-static --enable-small --disable-stripping \
    --disable-programs --disable-doc --disable-debug --disable-network \
    --enable-zlib \
    --disable-everything $ENABLE $OPENH264_FLAGS $GPL_FLAGS \
    || { echo "configure failed"; tail -40 ffbuild/config.log; exit 1; }
else
  # --- TARGET=wasm (default) -----------------------------------------------
  # Use clang as the linker (--ld=clang), not raw wasm-ld: clang understands
  # --target/--sysroot and links a WASI command (crt1/_start) automatically, so
  # configure's link probe just works. (The engine link in driver.sh is also clang.)
  #
  # --pkg-config-flags=--static: our deps are static archives, so configure's dep
  #   probes need pkg-config's transitive Requires.private libs (e.g. vorbisenc →
  #   vorbis → ogg), which plain --libs omits.
  # --extra-libs: -lsetjmp for libvpx's encoder setjmp/longjmp (clang's wasm SjLj
  #   lowering → __wasm_setjmp/__wasm_longjmp in the sysroot's libsetjmp.a); and
  #   -lc++/-lc++abi so configure's dep probes resolve harfbuzz's C++ symbols
  #   (libass → harfbuzz). Without these the probes fail to link and silently
  #   soft-disable the lib (libav's own code is C).
  # shellcheck disable=SC2086  # $ENABLE/$GPL_FLAGS/$OPENH264_FLAGS are deliberately split into args
  LDFLAGS="--target=wasm32-wasip1 --sysroot=$WASI_SYSROOT $SJLJ -L$PREFIX/lib $WASI_EMULATED_LIBS" \
  ./configure \
    --cc="$CC" --cxx="$CXX" --ld="$CC" --nm="$NM" --ar="$AR" --ranlib="$RANLIB" --strip="$STRIP" \
    --pkg-config-flags=--static \
    --extra-libs="-lsetjmp -lc++ -lc++abi" \
    --extra-cflags="-include $HERE_LIBAV/wasi-compat.h -Wno-error=implicit-function-declaration -Wno-error=int-conversion -Wno-error=incompatible-function-pointer-types" \
    --enable-cross-compile --arch=x86_32 --target-os=none \
    --disable-shared --enable-static --enable-small --disable-stripping \
    --disable-programs --disable-doc --disable-debug --disable-network \
    --disable-pthreads --disable-w32threads --disable-os2threads \
    --disable-runtime-cpudetect --disable-asm --disable-x86asm \
    --enable-zlib \
    --disable-everything $ENABLE $OPENH264_FLAGS $GPL_FLAGS \
    || { echo "configure failed"; tail -40 ffbuild/config.log; exit 1; }

  # wasm has no sysctl/mkstemp/gethrtime/setrlimit — force the portable fallbacks.
  # (Missing function declarations like dup/tempnam are handled by the force-included
  # build/wasi-compat.h, baked into config.mak via --extra-cflags above.)
  for d in HAVE_SYSCTL HAVE_MKSTEMP HAVE_GETHRTIME HAVE_SETRLIMIT; do
    sed -i "s|#define $d 1|#define $d 0|" config.h || true
  done
fi

make -j"$(nproc)"
echo "libav* built ($TARGET, $PROFILE, $VARIANT, FFmpeg $FFMPEG_VERSION)"
