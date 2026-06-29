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
: "${OPENH264_VERSION:=v2.6.0}"     # H.264 encode for both variants (BSD source)

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

# build_openh264 cross-compiles Cisco's openh264 (BSD-2-Clause) to wasm32-wasi,
# static, no asm, single-threaded. Both variants get it for H.264 encode. The
# artifact's AVC *patent* posture (self-compiled → outside Cisco's binary grant)
# is covered in docs/explanation/licensing.md.
build_openh264() {
  git clone https://github.com/cisco/openh264.git --depth=1 --branch "$OPENH264_VERSION" /openh264
  cd /openh264
  # Teach WelsThreadLib about wasip1: no <sys/sysctl.h>, no SCHED_FIFO, CPU count = 1.
  git apply "$HERE_DEPS/openh264-wasi.patch"
  # OS=linux only steers the Makefile's platform .mk — not the C preprocessor, which
  # never sees __linux__ for wasm. ARCH=generic + USE_ASM=No takes the portable C
  # path; USE_STACK_PROTECTOR=No drops __stack_chk_* the sysroot won't link;
  # -fno-exceptions/-fno-rtti keeps the archive free of unwind tables.
  make -j"$(nproc)" V=No \
    OS=linux ARCH=generic USE_ASM=No USE_STACK_PROTECTOR=No \
    CC="$CC" CXX="$CXX" AR="$AR" \
    CFLAGS="$CFLAGS" CXXFLAGS="$CXXFLAGS -fno-exceptions -fno-rtti" \
    libopenh264.a \
    || { echo "openh264 build failed"; exit 1; }
  # wasip1 has no pthreads; fold the single-threaded shim into the archive so it
  # resolves both FFmpeg's configure link-probe and the final engine link.
  # shellcheck disable=SC2086  # $CFLAGS is deliberately split into args
  "$CC" $CFLAGS -c "$HERE_DEPS/openh264-threads.c" -o openh264-threads.o
  "$AR" rs libopenh264.a openh264-threads.o
  # Install lib + public headers + a pkg-config file FFmpeg's configure can use.
  # (openh264's own .pc emits -lstdc++/-lpthread, neither present on wasi.)
  mkdir -p "$PREFIX/lib/pkgconfig" "$PREFIX/include/wels"
  cp libopenh264.a "$PREFIX/lib/"
  cp codec/api/wels/*.h "$PREFIX/include/wels/"
  cat > "$PREFIX/lib/pkgconfig/openh264.pc" <<EOF
prefix=$PREFIX
libdir=\${prefix}/lib
includedir=\${prefix}/include

Name: OpenH264
Description: Cisco OpenH264 — H.264 codec (wasm32-wasi, single-threaded)
Version: ${OPENH264_VERSION#v}
Libs: -L\${libdir} -lopenh264
Libs.private: -lc++ -lc++abi
Cflags: -I\${includedir}
EOF
  echo "openh264 built → $PREFIX"
}

build_zlib       # both variants (PNG)
build_openh264   # both variants (H.264 encode)

case "$VARIANT" in
  gpl)  build_x264 ;;   # GPL-only: best-in-class H.264 encode (libx264)
  lgpl) : ;;            # openh264 (above) is the LGPL variant's H.264 encoder
  *)    echo "deps: unknown VARIANT $VARIANT" >&2; exit 2 ;;
esac
