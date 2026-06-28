# Build FFmpeg's libav* + the libav-direct engine → a wasm32-wasi module.
# Usage: docker build -f build/Dockerfile --build-arg VARIANT=lgpl --target artifact -o dist .
FROM ghcr.io/webassembly/wasi-sdk:wasi-sdk-30 AS build
ARG FFMPEG_VERSION=n8.1.2
ARG VARIANT=lgpl
RUN apt-get update && apt-get install -y --no-install-recommends \
        git make pkg-config ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /work
# Build external codec dependencies (per variant) and libav* first — cached
# across engine edits, so iterating on the driver reuses both.
COPY build/toolchain.sh build/deps.sh build/libav.sh build/wasi-compat.h /work/build/
RUN VARIANT="$VARIANT" sh build/deps.sh
RUN FFMPEG_VERSION="$FFMPEG_VERSION" VARIANT="$VARIANT" sh build/libav.sh
COPY build/ /work/build/
COPY src/ /work/src/
RUN OUT="/dist/ffmpeg-wasi-${VARIANT}.wasm" sh build/driver.sh

# Export just the artifact(s).
FROM scratch AS artifact
COPY --from=build /dist/ /
