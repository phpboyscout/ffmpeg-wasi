#!/bin/sh
# build/deps.sh — build the external codec libraries a variant needs into $PREFIX,
# for the libav* build to link against. Build orchestration only; MIT.
#   VARIANT=gpl sh build/deps.sh
set -eu
HERE_DEPS="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091  # sourced at build time
. "$HERE_DEPS/toolchain.sh"

: "${VARIANT:=lgpl}"
: "${X264_BRANCH:=stable}"          # x264 has no release tags; pin a commit for releases
: "${ZLIB_VERSION:=v1.3.1}"

mkdir -p "$PREFIX"

# build_zlib cross-compiles zlib (permissive) to wasm32-wasi, static. FFmpeg's
# native PNG codec needs it, so both variants get it.
build_zlib() {
  git clone https://github.com/madler/zlib.git --depth=1 --branch "$ZLIB_VERSION" /zlib
  cd /zlib
  # zlib's configure uses CC/CFLAGS/AR from toolchain.sh; clang cross-links its tests.
  ./configure --prefix="$PREFIX" --eprefix="$PREFIX" --static \
    || { echo "zlib configure failed"; cat configure.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "zlib built → $PREFIX"
}

# build_x264 cross-compiles libx264 (GPL) to wasm32-wasi, static, no asm/cli.
build_x264() {
  git clone https://code.videolan.org/videolan/x264.git --depth=1 --branch "$X264_BRANCH" /x264
  cd /x264
  # x264's configure wants a host triple to cross-compile; "x86-gnu" + --disable-asm
  # selects the portable C path. CC/CFLAGS come from toolchain.sh; clang links the
  # test programs (no raw wasm-ld), so unset LD for x264's checks.
  LD="$CC" \
  CFLAGS="$CFLAGS -D_WASI_EMULATED_SIGNAL -include $HERE_DEPS/wasi-compat.h" \
  LDFLAGS="--target=wasm32-wasip1 --sysroot=$WASI_SYSROOT -lwasi-emulated-signal" \
  ./configure \
    --host=x86-gnu --prefix="$PREFIX" --exec-prefix="$PREFIX" \
    --enable-static --disable-cli --disable-asm --disable-opencl \
    --disable-avs --disable-gpac --disable-lsmash --bit-depth=8 \
    || { echo "x264 configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  # wasm has no malloc.h.
  sed -i 's|#define HAVE_MALLOC_H.*|#define HAVE_MALLOC_H 0|' config.h || true
  make -j"$(nproc)" install-lib-static
  echo "libx264 built → $PREFIX"
}

build_zlib   # both variants (PNG)

case "$VARIANT" in
  gpl)  build_x264 ;;
  lgpl) echo "deps: lgpl needs no GPL libraries (openh264 added later)" ;;
  *)    echo "deps: unknown VARIANT $VARIANT" >&2; exit 2 ;;
esac
