#!/bin/sh
# build/toolchain.sh — the compile environment for FFmpeg's libav* libraries and
# our engine. Two targets share one build system (spec 0028 D-0028-B):
#
#   TARGET=wasm   (default) → wasm32-wasi via the wasi-sdk/clang cross toolchain
#   TARGET=native           → the host toolchain, with threads + SIMD, for the
#                             native driver (spec 0028 Backend B)
#
# This is build orchestration only; it links nothing GPL. MIT-licensed. Source it
# before configure/make:  . build/toolchain.sh
set -eu

: "${TARGET:=wasm}"
: "${PREFIX:=/opt/vendor}"                 # where built dependencies install
export PREFIX

if [ "$TARGET" = native ]; then
  # --- TARGET=native : the host toolchain (spec 0028) ------------------------
  # No wasi-sdk, no cross flags, no setjmp lowering, no emulated shims — a plain
  # clang/gcc build with real threads and SIMD (the point of going native). The
  # wasm-only variables are exported empty so the shared scripts, which reference
  # them under `set -u`, stay defined; each script's native branch simply omits
  # the wasm-specific flags.
  : "${CC:=cc}"
  : "${CXX:=c++}"
  export CC CXX
  export AR=ar NM=nm RANLIB=ranlib STRIP=strip LD=ld
  export CFLAGS="-O2 -g0 -fPIC -I$PREFIX/include"
  export CXXFLAGS="$CFLAGS"
  export SJLJ=""
  export WASI_SYSROOT=""
  export WASI_EMULATED_LIBS=""
  # Prepend our deps but do NOT pin LIBDIR: native libav's probes (e.g. zlib) must
  # still resolve against the host's system pkg-config paths.
  export PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig"
else
  # --- TARGET=wasm (default) : the wasi-sdk cross toolchain ------------------
  : "${WASI_SDK:=/opt/wasi-sdk}"
  WASI_SYSROOT="$WASI_SDK/share/wasi-sysroot"
  export WASI_SDK WASI_SYSROOT
  export PATH="$WASI_SDK/bin:$PATH"

  # LLVM cross toolchain targeting wasm32-wasi.
  export CC=clang CXX=clang++ LD=wasm-ld AR=llvm-ar NM=llvm-nm RANLIB=llvm-ranlib STRIP=true

  # The WebAssembly features afmpeg's runtime enables (kept in lock-step with
  # spec 0004 R-0004-9), plus clang's native setjmp/longjmp lowering — which emits
  # the env.__wasm_setjmp / __wasm_longjmp imports the host provides. -Oz for size.
  WASM_FEATURES="-mtail-call -mbulk-memory -msimd128 -mextended-const \
-mnontrapping-fptoint -msign-ext -mmutable-globals -mreference-types"
  SJLJ="-mllvm=-wasm-enable-sjlj -mllvm=-wasm-use-legacy-eh=false"
  export SJLJ

  export CFLAGS="--target=wasm32-wasip1 --sysroot=$WASI_SYSROOT -Oz -g0 \
$WASM_FEATURES $SJLJ -I$WASI_SYSROOT/include/wasm32-wasip1 -I$PREFIX/include \
-D_WASI_EMULATED_MMAN -D_WASI_EMULATED_PROCESS_CLOCKS -D_WASI_EMULATED_SIGNAL -D_WASI_EMULATED_GETPID \
-Wno-implicit-function-declaration -Wno-int-conversion -Wno-incompatible-function-pointer-types \
-Wno-error=implicit-function-declaration -Wno-error=int-conversion -Wno-error=incompatible-function-pointer-types"
  export CXXFLAGS="$CFLAGS"

  # wasi-libc ships these as opt-in emulated shims FFmpeg's portable paths expect.
  export WASI_EMULATED_LIBS="-lwasi-emulated-mman -lwasi-emulated-process-clocks -lwasi-emulated-signal -lwasi-emulated-getpid"

  # pkg-config resolves only our cross-built dependencies in $PREFIX (LIBDIR pins it
  # there so it never picks up host libraries into the wasm build).
  export PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig"
  export PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig"
fi
