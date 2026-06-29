#!/bin/sh
# build/toolchain.sh — wasi-sdk cross-compile environment for building FFmpeg's
# libav* libraries (and our engine) to wasm32-wasi.
#
# This is build orchestration only: it configures the wasi-sdk/clang toolchain
# and links nothing GPL. MIT-licensed. Source it before configure/make:
#   . build/toolchain.sh
set -eu

: "${WASI_SDK:=/opt/wasi-sdk}"
: "${PREFIX:=/opt/vendor}"                 # where wasm-built dependencies install
WASI_SYSROOT="$WASI_SDK/share/wasi-sysroot"
export WASI_SDK PREFIX WASI_SYSROOT
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
# there so it never picks up host libraries).
export PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig"
export PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig"
