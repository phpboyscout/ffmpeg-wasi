---
title: Choose & verify a variant
description: Pick the LGPL or GPL artifact for your licensing needs and verify its checksum.
date: 2026-06-28
tags: [how-to, releases, licensing]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Choose & verify a variant

Every release ships **four** modules — two **licence variants**, each in two capability
**profiles**. Pick by **licence** and **profile**, then **verify** before you trust it.

## Which one?

```mermaid
graph TD
    A[Do you need libx264-quality H.264 encoding?] -->|No| L[ffmpeg-wasi-lgpl.wasm]
    A -->|Yes| B[Can your distribution accept GPL?]
    B -->|Yes| G[ffmpeg-wasi-gpl.wasm]
    B -->|No| L
```

- **`ffmpeg-wasi-lgpl.wasm`** — the default. LGPL-2.1+, proprietary-compatible. H.264 encode
  via openh264 (BSD); everything else in the [baseline](../reference/variants.md).
- **`ffmpeg-wasi-gpl.wasm`** — LGPL plus `--enable-gpl` + libx264 for best-in-class H.264
  encoding. The artifact is GPL-2.0+.

When in doubt, start with **LGPL**. See [the licensing model](../explanation/licensing.md) for
the full picture (and why shipping both together is clean).

## Which profile?

Each variant comes in two profiles (spec [0022](https://afmpeg.phpboyscout.uk/development/specs/0022-build-size-matrix/)):

- **lean** (default, `ffmpeg-wasi-<variant>.wasm`) — web-delivery essentials at the smallest size.
- **intermediate** (`ffmpeg-wasi-intermediate-<variant>.wasm`) — lean **+ every practical software
  codec/format/filter**: the LGPL encoders (Opus/MP3/Vorbis/VP8-9/WebP), the native codec and
  container batches, and text/subtitle burn-in. Larger, but no separate build.

Start with **lean**; reach for **intermediate** when you need a codec, container, or filter it
doesn't carry — see the [capability tables](../reference/variants.md#profiles-capability-classes).

!!! warning "H.264 and AVC patents"
    Both variants encode H.264, and both are self-compiled — so neither rides under Cisco's
    openh264 binary patent grant. We ship encode under the AVC pool's royalty-free volume tier and
    will pull it on request; the obligation sunsets when the last AVC essential patent expires
    (2027-11-29 in the U.S.). Full detail in
    [the licensing model](../explanation/licensing.md#h264-encode-and-the-avc-patent-pool).

## Verify the checksum

Each release includes `checksums.txt`. Always verify the module you downloaded:

```sh
# Download the module + checksums.txt from the release, then:
sha256sum -c checksums.txt --ignore-missing
# ffmpeg-wasi-lgpl.wasm: OK
```

Or check a single file against the published value:

```sh
sha256sum ffmpeg-wasi-lgpl.wasm
```

## Pin it in a consumer

When loading the module from Go via [afmpeg](https://gitlab.com/phpboyscout/afmpeg), pin both
the URL **and** the SHA-256 so an unexpected artifact is rejected:

```go
rt, _ := afmpeg.New(ctx, afmpeg.WithModuleURL(
    "https://gitlab.com/api/v4/projects/83847809/packages/generic/ffmpeg-wasi/n8.1.2-1/ffmpeg-wasi-lgpl.wasm",
    afmpeg.WithSHA256("0f338dac4ed1be3819aaf26f1cdeef119e817b43103f1460ca19354ea56bacc9"),
))
```

Each [release](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases) lists every asset's URL
and `checksums.txt`. For `n8.1.2-1` the GPL module's SHA-256 is
`093c9e084fa82780e7247cd7457c3742e398fc3075ba803eef6924cc72512586`. For exactly what went into
a build (FFmpeg version, build tag, commit, and per-variant licence/encoder/profile), read its
`provenance.json`.
