#!/bin/sh
# build/deps.sh — build the external codec libraries a variant needs into $PREFIX,
# for the libav* build to link against. Build orchestration only; MIT.
#   VARIANT=gpl sh build/deps.sh
set -eu
HERE_DEPS="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091  # sourced at build time
. "$HERE_DEPS/toolchain.sh"

: "${VARIANT:=lgpl}"
: "${PROFILE:=lean}"                # lean | intermediate — spec 0022; 0018 libs are intermediate
# x264 has no release tags; pin an exact commit for a reproducible build (fetched
# by SHA — the branch is only a hint). Bump X264_COMMIT to advance it.
: "${X264_COMMIT:=b35605ace3ddf7c1a5d67a2eb553f034aef41d55}"
: "${ZLIB_VERSION:=v1.3.1}"
: "${OPENH264_VERSION:=v2.6.0}"     # H.264 encode for both variants (BSD source)
# External LGPL/BSD encoder libs (spec 0018) — intermediate profile only. Each
# tarball is pinned by SHA-256 (verified in fetch_tarball); bump version + digest
# together.
: "${OPUS_VERSION:=1.5.2}"          # libopus (BSD) — Opus encode
OPUS_SHA256=65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1
: "${LAME_VERSION:=3.100}"          # libmp3lame (LGPL) — MP3 encode
LAME_SHA256=ddfe36cab873794038ae2c1210557ad34857a4b6bdc515785d1da9e175b1da1e
: "${OGG_VERSION:=1.3.5}"           # libogg (BSD) — Vorbis container dependency
OGG_SHA256=0eb4b4b9420a0f51db142ba3f9c64b333f826532dc0f48c6410ae51f4799b664
: "${VORBIS_VERSION:=1.3.7}"        # libvorbis (BSD) — Vorbis encode
VORBIS_SHA256=0e982409a9c3fc82ee06e08205b1355e5c6aa4c36bca58146ef399621b0ce5ab
: "${WEBP_VERSION:=1.4.0}"          # libwebp (BSD) — WebP encode
WEBP_SHA256=61f873ec69e3be1b99535634340d5bde750b2e4447caa1db9f61be3fd49ab1e5
: "${VPX_VERSION:=v1.14.1}"         # libvpx (BSD) — VP8/VP9 encode (the heavyweight)
# Text/subtitle burn-in libs (spec 0019, via the meson toolchain of spec 0029).
: "${FREETYPE_VERSION:=2.13.3}"     # freetype (FTL) — glyph rasteriser (drawtext + libass)
FREETYPE_SHA256=5c3a8e78f7b24c20b25b54ee575d6daa40007a5f4eea2845861c3409b3021747
: "${HARFBUZZ_VERSION:=8.5.0}"      # harfbuzz (MIT) — shaping; FFmpeg 8 drawtext requires it
HARFBUZZ_SHA256=77e4f7f98f3d86bf8788b53e6832fb96279956e1c3961988ea3d4b7ca41ddc27
: "${FRIBIDI_VERSION:=1.0.16}"      # fribidi (LGPL) — bidi; libass requires it
FRIBIDI_SHA256=1b1cde5b235d40479e91be2f0e88a309e3214c8ab470ec8a2744d82a5a9ea05c
: "${LIBASS_VERSION:=0.17.3}"       # libass (ISC) — subtitles/ass burn-in
LIBASS_SHA256=da7c348deb6fa6c24507afab2dee7545ba5dd5bbf90a137bfe9e738f7df68537

mkdir -p "$PREFIX"

# fetch_tarball <destdir> <sha256> <url...> — download, verify, and extract a
# release tarball (which ships a pre-generated ./configure, so the image needs no
# autoconf/automake). Tries each mirror URL in turn with retries (release CDNs —
# SourceForge, xiph — 502/stall often enough that a single URL makes the build
# flaky), and rejects any download whose SHA-256 doesn't match the pinned digest,
# so a compromised/altered mirror can't slip tainted source into the artifact.
fetch_tarball() {
  dest="$1"; sha="$2"; shift 2
  mkdir -p "$dest"
  for url in "$@"; do
    if curl -fsSL --retry 4 --retry-all-errors --retry-delay 3 -o /tmp/fetch.tgz "$url"; then
      got=$(sha256sum /tmp/fetch.tgz | cut -d' ' -f1)
      if [ "$got" != "$sha" ]; then
        echo "fetch: $url sha256 mismatch (got $got, want $sha) — trying next mirror" >&2
        rm -f /tmp/fetch.tgz
        continue
      fi
      # tar auto-detects the compression (gzip / xz — harfbuzz + fribidi ship .tar.xz).
      tar xf /tmp/fetch.tgz -C "$dest" --strip-components=1 && rm -f /tmp/fetch.tgz && return 0
    fi
    echo "fetch: $url failed — trying next mirror" >&2
  done
  echo "fetch: all mirrors failed (or checksum mismatched) for $dest" >&2
  return 1
}

# The vendored config.sub (build/config.sub, committed to the repo) is a modern
# one that knows the wasm32-wasi triple — the older autotools libs' bundled copies
# predate wasm and reject it ("machine wasm32 not recognized"). Vendoring it (vs
# fetching a moving gcc master each build) makes the build deterministic + offline.
FRESH_CONFIG_SUB="$HERE_DEPS/config.sub"
fetch_config_sub() {
  [ -f "$FRESH_CONFIG_SUB" ] || { echo "deps: vendored config.sub missing at $FRESH_CONFIG_SUB" >&2; return 1; }
}

# The autotools cross-compile shape shared by the 0018 audio libs: a fake host
# triple to force cross mode (CC/CFLAGS from toolchain.sh do the real wasm32-wasi
# targeting), asm off, static only, and LD routed through clang (mirrors x264).
# Drops the fresh config.sub in first so the wasm host triple canonicalises.
autotools_configure() {
  fetch_config_sub
  cp "$FRESH_CONFIG_SUB" config.sub
  LD="$CC" \
  LDFLAGS="--target=wasm32-wasip1 --sysroot=$WASI_SYSROOT $WASI_EMULATED_LIBS" \
  ./configure --host=wasm32-wasi --prefix="$PREFIX" \
    --enable-static --disable-shared "$@"
}

# meson_list turns its arguments into a meson string-list body ('a','b',…), single-
# quoting each and escaping any embedded single quote, so a flag value can't break
# the meson parse. (Each argument is one list element — a flag must not itself
# contain a space; the toolchain's paths are space-free by construction.)
meson_list() {
  out=""
  for w in "$@"; do
    esc=$(printf '%s' "$w" | sed "s/'/'\\\\''/g")
    out="$out'$esc',"
  done
  printf '%s' "${out%,}"
}

# write_meson_cross emits a meson cross-file for wasm32-wasi (spec 0029) from the
# toolchain's $CFLAGS — harfbuzz + fribidi are meson-only. c_args/cpp_args carry the
# exact same wasm target flags as the rest of the build. needs_exe_wrapper=true: the
# sandbox can't run the wasm output during the build.
write_meson_cross() {
  # shellcheck disable=SC2086  # $CFLAGS is deliberately split into per-flag args
  cargs=$(meson_list $CFLAGS)
  # shellcheck disable=SC2086
  ldargs=$(meson_list --target=wasm32-wasip1 --sysroot="$WASI_SYSROOT" $WASI_EMULATED_LIBS)
  cat > /tmp/wasi-cross.txt <<EOF
[binaries]
c = 'clang'
cpp = 'clang++'
ar = 'llvm-ar'
strip = 'llvm-strip'
pkg-config = 'pkg-config'

[host_machine]
system = 'wasi'
cpu_family = 'wasm32'
cpu = 'wasm32'
endian = 'little'

[properties]
needs_exe_wrapper = true

[built-in options]
c_args = [$cargs]
c_link_args = [$ldargs]
cpp_args = [$cargs]
cpp_link_args = [$ldargs]
EOF
}

# build_libopus — Opus encode (BSD). Autotools; asm/rtcd/intrinsics off for the
# portable C path (spec 0018).
build_libopus() {
  fetch_tarball /opus "$OPUS_SHA256" \
    "https://downloads.xiph.org/releases/opus/opus-${OPUS_VERSION}.tar.gz" \
    "https://ftp.osuosl.org/pub/xiph/releases/opus/opus-${OPUS_VERSION}.tar.gz"
  cd /opus
  autotools_configure --disable-asm --disable-rtcd --disable-intrinsics \
    --disable-doc --disable-extra-programs \
    || { echo "opus configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libopus built → $PREFIX"
}

# build_libmp3lame — MP3 encode (LGPL). Old autotools; force the wasi-compat
# shims and drop the frontend/gtk test.
build_libmp3lame() {
  fetch_tarball /lame "$LAME_SHA256" \
    "https://downloads.sourceforge.net/project/lame/lame/${LAME_VERSION}/lame-${LAME_VERSION}.tar.gz" \
    "https://ftp.osuosl.org/pub/blfs/conglomeration/lame/lame-${LAME_VERSION}.tar.gz"
  cd /lame
  CFLAGS="$CFLAGS -include $HERE_DEPS/wasi-compat.h" \
  autotools_configure --disable-frontend --disable-gtktest --disable-decoder \
    || { echo "lame configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libmp3lame built → $PREFIX"
}

# build_libvorbis — Vorbis encode (BSD), on libogg (BSD). Two static archives.
build_libvorbis() {
  fetch_tarball /ogg "$OGG_SHA256" \
    "https://downloads.xiph.org/releases/ogg/libogg-${OGG_VERSION}.tar.gz" \
    "https://ftp.osuosl.org/pub/xiph/releases/ogg/libogg-${OGG_VERSION}.tar.gz"
  cd /ogg
  autotools_configure \
    || { echo "libogg configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libogg built → $PREFIX"

  fetch_tarball /vorbis "$VORBIS_SHA256" \
    "https://downloads.xiph.org/releases/vorbis/libvorbis-${VORBIS_VERSION}.tar.gz" \
    "https://ftp.osuosl.org/pub/xiph/releases/vorbis/libvorbis-${VORBIS_VERSION}.tar.gz"
  cd /vorbis
  autotools_configure --disable-oggtest \
    || { echo "libvorbis configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libvorbis built → $PREFIX"
}

# build_libwebp — WebP encode (BSD). Autotools (the release tarball ships
# configure); threads off (no pthreads), SIMD auto-disables for the wasm target.
# Installs libwebp + libwebpmux/demux + libsharpyuv and their .pc files.
build_libwebp() {
  fetch_tarball /webp "$WEBP_SHA256" \
    "https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-${WEBP_VERSION}.tar.gz" \
    "https://github.com/webmproject/libwebp/releases/download/v${WEBP_VERSION}/libwebp-${WEBP_VERSION}.tar.gz"
  cd /webp
  autotools_configure --disable-threading \
    || { echo "libwebp configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libwebp built → $PREFIX"
}

# build_libvpx — VP8/VP9 encode (BSD), the heavyweight of the five. libvpx has its
# own configure (not autotools): target=generic-gnu is the portable C path (no
# asm), and wasi has neither threads nor cpuid, so multithread + runtime CPU detect
# are off. VP9 encode is notably slow single-threaded (documented on the codec).
build_libvpx() {
  git clone https://github.com/webmproject/libvpx.git --depth=1 --branch "$VPX_VERSION" /libvpx
  cd /libvpx
  LD="$CC" CROSS='' \
  ./configure --target=generic-gnu --prefix="$PREFIX" \
    --enable-static --disable-shared --enable-vp8 --enable-vp9 \
    --disable-examples --disable-tools --disable-docs --disable-unit-tests \
    --disable-runtime-cpu-detect --disable-multithread \
    || { echo "libvpx configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libvpx built → $PREFIX"
}

# build_freetype — glyph rasteriser (FTL) for drawtext + libass (spec 0019).
# Autotools; every optional dependency (harfbuzz/brotli/png/bz2/zlib) is off — we
# need only core glyph rendering, and harfbuzz would circular-dep with freetype.
# freetype's real configure is in builds/unix, so its config.sub is refreshed too.
build_freetype() {
  fetch_tarball /freetype "$FREETYPE_SHA256" \
    "https://download.savannah.gnu.org/releases/freetype/freetype-${FREETYPE_VERSION}.tar.gz" \
    "https://downloads.sourceforge.net/freetype/freetype-${FREETYPE_VERSION}.tar.gz"
  cd /freetype
  fetch_config_sub
  [ -f builds/unix/config.sub ] && cp "$FRESH_CONFIG_SUB" builds/unix/config.sub
  autotools_configure --without-harfbuzz --without-brotli --without-png \
    --without-bzip2 --without-zlib \
    || { echo "freetype configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "freetype built → $PREFIX"
}

# build_harfbuzz — text-shaping engine (MIT), meson-built (spec 0029). FFmpeg 8's
# drawtext requires it (drawtext_filter_deps="libfreetype libharfbuzz"), and libass
# uses it for shaping. C++ — links libc++ at the final engine link. Everything but
# freetype integration is off. Slow to compile under -Oz (~5-8 min).
build_harfbuzz() {
  fetch_tarball /harfbuzz "$HARFBUZZ_SHA256" \
    "https://github.com/harfbuzz/harfbuzz/releases/download/${HARFBUZZ_VERSION}/harfbuzz-${HARFBUZZ_VERSION}.tar.xz"
  cd /harfbuzz
  # -DHB_NO_MT: single-threaded harfbuzz (no std::atomic). meson pulls the threads
  # dep, so without this clang emits wasm atomic ops (i32.atomic.store) that
  # wazero rejects (the threads feature is off). Safe — the engine is single-threaded.
  CFLAGS="$CFLAGS -DHB_NO_MT" write_meson_cross
  meson setup build --cross-file /tmp/wasi-cross.txt --prefix="$PREFIX" \
    --default-library=static --buildtype=minsize \
    -Dfreetype=enabled -Dglib=disabled -Dgobject=disabled -Dcairo=disabled \
    -Dicu=disabled -Dtests=disabled -Ddocs=disabled -Dutilities=disabled \
    || { echo "harfbuzz setup failed"; tail -40 build/meson-logs/meson-log.txt 2>/dev/null; exit 1; }
  ninja -C build install
  echo "harfbuzz built → $PREFIX"
}

# build_fribidi — Unicode bidi algorithm (LGPL), meson-built (spec 0029). Required
# by libass.
build_fribidi() {
  fetch_tarball /fribidi "$FRIBIDI_SHA256" \
    "https://github.com/fribidi/fribidi/releases/download/v${FRIBIDI_VERSION}/fribidi-${FRIBIDI_VERSION}.tar.xz"
  cd /fribidi
  write_meson_cross
  meson setup build --cross-file /tmp/wasi-cross.txt --prefix="$PREFIX" \
    --default-library=static --buildtype=minsize -Ddocs=false -Dtests=false -Dbin=false \
    || { echo "fribidi setup failed"; tail -40 build/meson-logs/meson-log.txt 2>/dev/null; exit 1; }
  ninja -C build install
  echo "fribidi built → $PREFIX"
}

# build_libass — ASS/SSA + SRT/VTT rasteriser (ISC) for the subtitles/ass burn-in
# filters (spec 0019). Autotools, needs freetype + fribidi (+ harfbuzz for shaping),
# all built above. fontconfig is off — the sandbox has no system fonts, so fonts
# come from the mounted fs by path (D-0019-C); asm off for the portable path.
build_libass() {
  fetch_tarball /libass "$LIBASS_SHA256" \
    "https://github.com/libass/libass/releases/download/${LIBASS_VERSION}/libass-${LIBASS_VERSION}.tar.gz"
  cd /libass
  autotools_configure --disable-fontconfig --disable-require-system-font-provider \
    --disable-libunibreak --disable-asm \
    || { echo "libass configure failed"; tail -40 config.log 2>/dev/null; exit 1; }
  make -j"$(nproc)" install
  echo "libass built → $PREFIX"
}

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
  # Pin to an exact commit (x264 publishes no release tags): shallow-fetch just
  # that SHA so the build is reproducible regardless of where `stable` points.
  mkdir -p /x264 && cd /x264 && git init -q
  git remote add origin https://code.videolan.org/videolan/x264.git
  git fetch -q --depth=1 origin "$X264_COMMIT"
  git checkout -q FETCH_HEAD
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

# The external LGPL/BSD encoder quartet (spec 0018) lands in the intermediate
# profile only (0022 §6) — both licence variants, since they are LGPL-compatible.
if [ "$PROFILE" = intermediate ]; then
  build_libopus
  build_libmp3lame
  build_libvorbis
  build_libwebp
  build_libvpx
  # Text/subtitle burn-in (spec 0019): freetype → harfbuzz → fribidi → libass,
  # in dependency order (harfbuzz + libass link freetype; libass links fribidi).
  build_freetype
  build_harfbuzz
  build_fribidi
  build_libass
  # wasi has no pthreads and every lib here is built single-threaded, but some
  # .pc files still advertise it: libvpx lists -lpthread, harfbuzz (meson) lists
  # -pthread. FFmpeg probes deps with --static pkg-config, so either reaches the
  # wasm link — -lpthread has no archive, and -pthread makes clang emit
  # --shared-memory (which wasm-ld rejects without atomics). Strip both.
  sed -i 's/-lpthread//g; s/-pthread//g' "$PREFIX"/lib/pkgconfig/*.pc
fi
