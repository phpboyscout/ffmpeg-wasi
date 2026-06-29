#!/bin/sh
# build/sign-release.sh — assemble the release manifest and KMS-sign it.
#
# From the built ffmpeg-wasi-*.wasm(.gz) assets in the working directory it
# produces three files:
#   provenance.json    — what went into the build (versions, per-variant licence)
#   checksums.txt      — SHA-256 of every published asset, provenance.json included
#   checksums.txt.sig  — a detached RSASSA-PSS-SHA256 signature over checksums.txt
#                        from the ffmpeg-wasi release-signing KMS key, wrapped in a
#                        small JSON envelope naming the signing key-id.
#
# Run by the tag-gated `sign` CI job, which supplies AWS web-identity
# credentials (AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE) so `aws kms` resolves
# the signer role automatically. afmpeg verifies the signature offline against
# its pinned public key (afmpeg spec 0010). Build orchestration only; MIT.
set -eu

: "${CI_COMMIT_TAG:?set CI_COMMIT_TAG, e.g. n8.1.2-2}"
: "${CI_COMMIT_SHA:?set CI_COMMIT_SHA}"
: "${SIGNING_KEY_ALIAS:?set SIGNING_KEY_ALIAS, e.g. alias/ffmpeg-wasi-release-signing-v1}"
: "${AWS_REGION:?set AWS_REGION}"

FFMPEG_VERSION="${CI_COMMIT_TAG%-*}"

# 1. Provenance manifest. (Kept in lock-step with afmpeg's Provenance schema.)
cat > provenance.json <<PROV
{
  "ffmpeg_version": "${FFMPEG_VERSION}",
  "build_tag": "${CI_COMMIT_TAG}",
  "commit": "${CI_COMMIT_SHA}",
  "variants": {
    "lgpl": { "file": "ffmpeg-wasi-lgpl.wasm", "license": "LGPL-2.1-or-later", "h264_encode": "openh264" },
    "gpl":  { "file": "ffmpeg-wasi-gpl.wasm",  "license": "GPL-2.0-or-later",  "h264_encode": "libx264" }
  },
  "h264_patent_note": "Both variants encode H.264 from self-compiled sources (openh264/libx264), outside Cisco's binary patent grant; shipped under the AVC pool's royalty-free volume tier. See docs/explanation/licensing.md.",
  "tooling_license": "MIT"
}
PROV

# 2. Checksums over every published asset — provenance.json included, so the one
#    signature over checksums.txt transitively certifies the whole release set.
sha256sum ffmpeg-wasi-lgpl.wasm ffmpeg-wasi-lgpl.wasm.gz \
  ffmpeg-wasi-gpl.wasm ffmpeg-wasi-gpl.wasm.gz \
  provenance.json > checksums.txt

# 3. The key-id is the hex SHA-256 of the key's SubjectPublicKeyInfo DER, derived
#    from the live key so it can never drift from what afmpeg pins.
key_id=$(aws kms get-public-key --key-id "$SIGNING_KEY_ALIAS" --region "$AWS_REGION" \
  --query PublicKey --output text | base64 -d | sha256sum | cut -d' ' -f1)

# 4. Detached signature over checksums.txt. MessageType=RAW: KMS hashes the
#    (small) file with SHA-256 then applies PSS — the same digest afmpeg verifies.
sig=$(aws kms sign --key-id "$SIGNING_KEY_ALIAS" --region "$AWS_REGION" \
  --message fileb://checksums.txt --message-type RAW \
  --signing-algorithm RSASSA_PSS_SHA_256 --query Signature --output text)

printf '{"key_id":"%s","algorithm":"RSASSA_PSS_SHA_256","signature":"%s"}\n' \
  "$key_id" "$sig" > checksums.txt.sig

echo "signed checksums.txt -> checksums.txt.sig (key_id ${key_id})"
