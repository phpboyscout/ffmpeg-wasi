---
title: The licensing model
description: Why the tooling is MIT, the artifacts are LGPL or GPL, and shipping both is clean.
date: 2026-06-28
tags: [explanation, licensing]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# The licensing model

Media tooling licensing is where most projects get vague. ffmpeg-wasi keeps three licences
**deliberately distinct**, so you always know exactly what you're holding.

!!! note "Not legal advice"
    This explains the project's intent and structure. It is not legal advice; a formal
    licence review precedes any tagged release.

## Three licences, kept apart

### 1. This repository's source — **MIT**

The build tooling (`build/`) and the engine (`src/driver.c`) are [MIT](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/blob/main/LICENSE).
Two facts keep it that way:

- **It vendors no FFmpeg source.** FFmpeg (and any GPL dependency such as x264) is *cloned
  at build time*, never committed here.
- **It links nothing.** The tooling *orchestrates* a build — clone, configure, compile,
  package. It is not a derivative work of anything GPL, so it stays MIT, and it is yours to
  reuse.

That MIT pipeline — the reference "FFmpeg → WASI, libav-direct" build — is the valuable,
reusable part we own.

### 2. The released artifacts — **LGPL or GPL** (your choice)

FFmpeg's libraries are **LGPL-2.1-or-later** by default. The artifact's licence is set
entirely by *what we enable*:

| Variant | Built with | Artifact licence |
|---|---|---|
| **LGPL** (default) | stock `libav*`, no `--enable-gpl`, + openh264 (BSD) for H.264 encode | LGPL-2.1-or-later |
| **GPL** (opt-in) | `--enable-gpl` + libx264 | GPL-2.0-or-later |

openh264's BSD-2-Clause source is LGPL-compatible and needs no `--enable-gpl`, so it does not move
the LGPL floor — but it does carry an AVC **patent** caveat, covered [below](#h264-encode-and-the-avc-patent-pool).

**LGPL is the floor.** We cannot relicense `libav*` below it — it's the FFmpeg project's
code, not ours. But LGPL is proprietary-compatible (you may use it in closed software,
subject to its relink provision), which is what matters for adoption.

### 3. Your code that *consumes* the artifact — **unaffected**

[afmpeg](https://gitlab.com/phpboyscout/afmpeg), or whatever loads the module, just downloads
a `.wasm`. The LGPL/GPL obligation attaches to *that artifact* and whoever distributes it —
never to the consuming source. afmpeg stays permissive throughout.

## H.264 encode and the AVC patent pool

Both variants can encode H.264 — the **LGPL** variant via [openh264](https://github.com/cisco/openh264)
(BSD-2-Clause source), the **GPL** variant via libx264. Those are the *copyright* licences. The
**patent** position is identical for both, and it is worth stating plainly.

!!! danger "Self-compiled openh264 is outside Cisco's patent grant"
    H.264/AVC is covered by a patent pool administered by **[Via LA](https://www.via-la.com/licensing-2/avc-h-264/)**
    (formerly MPEG LA). Cisco's well-known *royalty-free* grant for openh264 covers **only the binary
    modules Cisco itself builds and distributes** — it does **not** travel with the source. ffmpeg-wasi
    compiles openh264 from source, so our `.wasm` is **not** under Cisco's umbrella. The GPL variant's
    libx264 is no different: GPL is a copyright licence and never conveys patent rights.

    We ship H.264 encode regardless, on a deliberate and bounded basis:

    - **Volume.** The AVC licence is royalty-free beneath the pool's annual unit threshold (the first
      100,000 units/year). Our expected distribution sits well within it — and that headroom is the
      *only* reason shipping a self-compiled AVC encoder is tenable here.
    - **Comply on demand.** If Via LA or any AVC rights-holder asks us to stop distributing the
      encoder, **we will pull it** — promptly, from both variants — rather than contest it.
    - **Sunset.** The obligation is winding down. The last U.S. AVC essential patent in the pool
      (US 7,826,532) **expires 2027-11-29**; after that the standard is royalty-free in the U.S. and
      this caveat lapses. Other jurisdictions track a similar horizon.

    This states the project's intent; it is not legal advice, and a formal review precedes any tagged
    release. For the authoritative, maintained patent roster see Via LA's
    [AVC/H.264 patent list](https://www.via-la.com/licensing-2/avc-h-264/) (the "Attachment 1" PDF,
    updated periodically) rather than any copy we could let go stale here.

## HEVC & AV1 encode — the *full* native profile only

The heavy next-generation encoders ship **only in the native driver's `full` profile** (spec 0023);
they are absent from every `.wasm` and from the lean/intermediate profiles. Their licence posture
differs sharply from each other.

- **HEVC/H.265 via [libx265](https://bitbucket.org/multicoreware/x265_git)** — **GPL-2.0-or-later**,
  so it enters the **gpl variant only** (behind `--enable-gpl`, exactly like libx264), never the LGPL
  default. And its **patent** exposure is *harder than AVC's*, not softer.

    !!! danger "HEVC patents are active and pooled — with no sunset"
        H.265/HEVC is covered by **multiple, fragmented patent pools** (Via LA, Access Advance, plus
        unaffiliated holders) with **no expiry on the horizon** — unlike the AVC pool that sunsets
        2027-11-29. We compile libx265 from source, so — as with x264 — **GPL conveys no patent
        rights** and our binary sits under no pool's grant. We mirror the AVC posture (self-compiled,
        low volume, **comply-on-demand — we pull HEVC encode promptly from the gpl full driver if any
        HEVC rights-holder asks**), but because the pools are active with no sunset, **HEVC encode is
        the most patent-exposed thing we ship** and is offered only on deliberate, bounded demand.
        Decode is unaffected (the native `hevc` decoder needs no pool licence to *play* HEVC).

- **AV1 via [SVT-AV1](https://gitlab.com/AOMediaCodec/SVT-AV1)** — **BSD-3-Clause-Clear + the Alliance
  for Open Media patent grant**. It is LGPL-clean, so it rides **both variants** of the full profile,
  and AV1 is **royalty-free by design** (AOMedia's patent commitment) — **no patent caveat applies**.
  This is the decisive advantage that makes AV1 the comfortable heavy codec to ship.

## Why shipping both variants is clean

Every release publishes **both** `ffmpeg-wasi-lgpl.wasm` and `ffmpeg-wasi-gpl.wasm`, so you
pick the licence that fits and never have to build. Distributing two separate, independent
artifacts together is **mere aggregation** (GPLv3 §5): the GPL artifact does not "infect"
the LGPL artifact, the MIT tooling, or your code. They simply coexist.

## Obligations we meet

- **Per-asset labelling** — each artifact states its licence; the provenance manifest records
  variant + licence.
- **Corresponding source** — for every released binary, the source is the *pinned upstream
  FFmpeg/x264/openh264* plus *this public repository* (our scripts + engine). Anyone can rebuild.
- **LGPL relink** — because the build is public and the FFmpeg version pinned, a user can
  relink the LGPL artifact against a modified `libav*`.

## In one sentence

**MIT is what we own; LGPL is the floor for the artifact; GPL is opt-in for libx264; and your
code stays yours.**
