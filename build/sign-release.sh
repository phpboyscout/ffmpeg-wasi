#!/bin/sh
# build/sign-release.sh — assemble the release manifest and OpenPGP-sign it with
# the org signing toolchain (gtb), so afmpeg verifies it via gitlab.com/phpboyscout/signing.
#
# From the built ffmpeg-wasi-*.wasm(.gz) assets in the working directory it produces:
#   provenance.json    — what went into the build (versions, per-variant licence)
#   checksums.txt      — SHA-256 of every published asset, provenance.json included
#   checksums.txt.sig  — an ASCII-armored OpenPGP detached signature over checksums.txt
#   release.asc        — the OpenPGP public key (for the operator to publish via WKD)
#
# Signing uses the dedicated ffmpeg-wasi KMS key via gtb's aws-kms backend, which
# resolves credentials from the OIDC web-identity env (AWS_ROLE_ARN +
# AWS_WEB_IDENTITY_TOKEN_FILE) — only this project's tag pipeline can sign.
# `gtb` must be on PATH (the `sign` CI job installs it). MIT orchestration.
set -eu

: "${CI_COMMIT_TAG:?set CI_COMMIT_TAG, e.g. n8.1.2-4}"
: "${CI_COMMIT_SHA:?set CI_COMMIT_SHA}"
: "${SIGNING_KEY_ALIAS:?set SIGNING_KEY_ALIAS, e.g. alias/ffmpeg-wasi-release-signing-v1}"
: "${AWS_REGION:?set AWS_REGION}"
# The OpenPGP UID + creation time are FIXED so every mint reproduces the same key
# fingerprint — the one afmpeg pins. Do not change without rotating the pinned key.
: "${SIGNING_KEY_NAME:?set SIGNING_KEY_NAME}"
: "${SIGNING_KEY_EMAIL:?set SIGNING_KEY_EMAIL}"
: "${SIGNING_KEY_CREATED:?set SIGNING_KEY_CREATED (RFC3339)}"

FFMPEG_VERSION="${CI_COMMIT_TAG%-*}"

# 1. Provenance manifest. (Kept in lock-step with afmpeg's Provenance schema.)
cat > provenance.json <<PROV
{
  "ffmpeg_version": "${FFMPEG_VERSION}",
  "build_tag": "${CI_COMMIT_TAG}",
  "commit": "${CI_COMMIT_SHA}",
  "variants": {
    "lgpl": { "file": "ffmpeg-wasi-lgpl.wasm", "license": "LGPL-2.1-or-later", "h264_encode": "openh264", "profile": "lean" },
    "gpl":  { "file": "ffmpeg-wasi-gpl.wasm",  "license": "GPL-2.0-or-later",  "h264_encode": "libx264",  "profile": "lean" },
    "intermediate-lgpl": { "file": "ffmpeg-wasi-intermediate-lgpl.wasm", "license": "LGPL-2.1-or-later", "h264_encode": "openh264", "profile": "intermediate" },
    "intermediate-gpl":  { "file": "ffmpeg-wasi-intermediate-gpl.wasm",  "license": "GPL-2.0-or-later",  "h264_encode": "libx264",  "profile": "intermediate" },
    "driver-linux-amd64-lgpl": { "file": "ffmpeg-wasi-driver-linux-amd64-lgpl", "license": "LGPL-2.1-or-later", "h264_encode": "openh264", "profile": "lean" },
    "driver-linux-amd64-gpl":  { "file": "ffmpeg-wasi-driver-linux-amd64-gpl",  "license": "GPL-2.0-or-later",  "h264_encode": "libx264",  "profile": "lean" }
  },
  "h264_patent_note": "Both variants encode H.264 from self-compiled sources (openh264/libx264), outside Cisco's binary patent grant; shipped under the AVC pool's royalty-free volume tier. See docs/explanation/licensing.md.",
  "tooling_license": "MIT"
}
PROV

# 2. Checksums over every published asset — provenance.json included, so the one
#    signature over checksums.txt transitively certifies the whole release set
#    (the wasm modules AND the native Backend-B drivers, spec 0028 D-0028-D).
sha256sum ffmpeg-wasi-lgpl.wasm ffmpeg-wasi-lgpl.wasm.gz \
  ffmpeg-wasi-gpl.wasm ffmpeg-wasi-gpl.wasm.gz \
  ffmpeg-wasi-intermediate-lgpl.wasm ffmpeg-wasi-intermediate-lgpl.wasm.gz \
  ffmpeg-wasi-intermediate-gpl.wasm ffmpeg-wasi-intermediate-gpl.wasm.gz \
  ffmpeg-wasi-driver-linux-amd64-lgpl ffmpeg-wasi-driver-linux-amd64-lgpl.gz \
  ffmpeg-wasi-driver-linux-amd64-gpl ffmpeg-wasi-driver-linux-amd64-gpl.gz \
  provenance.json > checksums.txt

# 3. Mint the OpenPGP public key from KMS. Fixed name/email/created → deterministic
#    fingerprint matching afmpeg's embedded key.
gtb keys mint --backend aws-kms --key-id "$SIGNING_KEY_ALIAS" --kms-region "$AWS_REGION" \
  --name "$SIGNING_KEY_NAME" --email "$SIGNING_KEY_EMAIL" --created "$SIGNING_KEY_CREATED" \
  --output release.asc

# 4. Detached OpenPGP signature over checksums.txt (RSA-4096 via KMS, in CI only).
gtb sign checksums.txt --backend aws-kms --key-id "$SIGNING_KEY_ALIAS" --kms-region "$AWS_REGION" \
  --public-key release.asc --output checksums.txt.sig

echo "signed checksums.txt -> checksums.txt.sig (${SIGNING_KEY_EMAIL})"
