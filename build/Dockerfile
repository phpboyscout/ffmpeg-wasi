# Build FFmpeg's libav* + the libav-direct engine → a wasm32-wasi module.
# Usage: docker build -f build/Dockerfile --build-arg VARIANT=lgpl \
#          --build-arg PROFILE=lean --target artifact -o dist .
FROM ghcr.io/webassembly/wasi-sdk:wasi-sdk-30 AS build
ARG FFMPEG_VERSION=n8.1.2
ARG VARIANT=lgpl
ARG PROFILE=lean
RUN apt-get update && apt-get install -y --no-install-recommends \
        git make pkg-config ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /work
# Build external codec dependencies (per variant) and libav* first — cached
# across engine edits, so iterating on the driver reuses both. deps.sh is not
# PROFILE-aware yet (the intermediate codec batches are flag-only, in libav.sh;
# external encoder libs arrive with spec 0018), so it caches across profiles.
COPY build/toolchain.sh build/deps.sh build/libav.sh build/wasi-compat.h \
     build/openh264-wasi.patch build/openh264-threads.c /work/build/
RUN VARIANT="$VARIANT" sh build/deps.sh
RUN FFMPEG_VERSION="$FFMPEG_VERSION" VARIANT="$VARIANT" PROFILE="$PROFILE" sh build/libav.sh
COPY build/ /work/build/
COPY src/ /work/src/
# Artifact name: the lean profile keeps the legacy ffmpeg-wasi-<variant>.wasm
# (the WASM-lean row of the 0022 matrix, unchanged for existing consumers);
# other profiles carry the profile in the name.
RUN if [ "$PROFILE" = lean ]; then OUT="/dist/ffmpeg-wasi-${VARIANT}.wasm"; \
    else OUT="/dist/ffmpeg-wasi-${PROFILE}-${VARIANT}.wasm"; fi; \
    OUT="$OUT" sh build/driver.sh

# Export just the artifact(s).
FROM scratch AS artifact
COPY --from=build /dist/ /
