# ffmpeg-wasi

**Current FFmpeg, as a sandboxed WebAssembly module — for the server, not the browser.**

`ffmpeg-wasi` builds FFmpeg's media libraries (`libav*`) to `wasm32-wasi` and drives
them with a small purpose-built engine, producing a single `.wasm` artifact that runs
media pipelines **anywhere a WASI runtime does** — with no native FFmpeg install, no C
toolchain at deploy time, and no shelling out to a binary.

It is designed to run under [**wazero**](https://wazero.io/), the zero-dependency,
**pure-Go** WebAssembly runtime — so a Go program can transcode, filter, and mux media
**embedded, CGO-free, and sandboxed**, cross-compiling to a single static binary.

## Why this exists

Every other "FFmpeg in WebAssembly" project hits the same two walls. We went under them.

- **It's not the browser one.** The well-known `ffmpeg.wasm` is an *emscripten* build for
  the browser. `ffmpeg-wasi` is the opposite end: a **WASI** build for servers, edge, and
  embedded Go — a different runtime, a different target, a different job.
- **It's current, not EOL.** The existing WASI-capable build pins **FFmpeg 5.1**, which is
  **end-of-life** — no security backports for a library whose entire job is parsing
  untrusted media. `ffmpeg-wasi` tracks **current, maintained FFmpeg**.
- **It went under the threading wall.** FFmpeg 7.0+ rewrote its *command-line tool* to be
  multithreaded, which pure-Go runtimes can't run (no `wasi-threads` thread-spawn). So
  instead of the CLI, **we link the libraries directly** — the libraries build
  single-threaded with no trouble — and drive them with our own engine. That's the trick
  nobody else has done, and it's what makes *current* FFmpeg work CGO-free.

The result: the **reference server-side FFmpeg for WebAssembly** — current, sandboxed,
pure-Go-embeddable.

> **Status: it transcodes.** Current FFmpeg (n8.1.2) compiles to `wasm32-wasi` and runs
> under wazero, and the engine does **real in-memory transcodes** — verified end-to-end:
> WAV → AAC, and H.264 → scaled → H.264 (libx264, GPL variant). Both `probe` and `process`
> (single input → single output) work today; the full multi-pad `filter_complex` and
> multi-output muxing are next. Design:
> [spec 0007](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0007-libav-direct-engine.md).

## What you get

Each release publishes two ready-to-use artifacts — **pick the licence that fits and skip
building**:

| Variant | Licence | H.264 encode | For |
|---|---|---|---|
| **`ffmpeg-wasi-lgpl.wasm`** | LGPL-2.1+ | openh264 (BSD) | the default — proprietary-compatible |
| **`ffmpeg-wasi-gpl.wasm`** | GPL-2.0+ | libx264 (best quality) | when you want x264 and accept GPL |

Plus a checksum manifest (`checksums.txt`), a **detached KMS signature** over it
(`checksums.txt.sig` — signable only by this project's tag pipeline via GitLab OIDC, verified
offline by afmpeg), and a provenance manifest (the exact FFmpeg + dependency + toolchain
versions). Pin by URL + SHA-256. Both variants encode H.264; the self-compiled openh264 in the
LGPL variant carries an AVC **patent** caveat — see [licensing](docs/explanation/licensing.md#h264-encode-and-the-avc-patent-pool).

## Quick start (consuming it from Go)

With [afmpeg](https://gitlab.com/phpboyscout/afmpeg) — the pure-Go binding that runs this
module over an in-memory filesystem:

```go
rt, _ := afmpeg.New(ctx, afmpeg.WithModuleURL(
    "https://gitlab.com/api/v4/projects/83847809/packages/generic/ffmpeg-wasi/n8.1.2-1/ffmpeg-wasi-lgpl.wasm",
    afmpeg.WithSHA256("0f338dac4ed1be3819aaf26f1cdeef119e817b43103f1460ca19354ea56bacc9"),
))
// ... run a media job entirely in memory ...
```

(That's the LGPL module from [`n8.1.2-1`](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-1).
The LGPL module encodes H.264 via openh264; swap `lgpl` → `gpl` for libx264 instead. Every release
lists each asset's URL + SHA-256.)

## Licensing — clean, and yours to choose

- **This repository's source — the build tooling and the engine (`src/driver.c`) — is
  [MIT](LICENSE).** It *orchestrates* the build and links nothing GPL, so it stays MIT and
  is yours to reuse.
- **The released `.wasm` artifacts** carry the licence their contents demand: the **LGPL**
  variant (default libav\*) and the **GPL** variant (with libx264). Shipping both in one
  release is mere aggregation — neither affects the other, nor this MIT source.
- Corresponding source for every release is the pinned upstream FFmpeg/x264/openh264 plus this
  public repository — anyone can rebuild or relink.

FFmpeg is a trademark of its respective owners; this project builds and redistributes
FFmpeg's libraries and is not affiliated with the FFmpeg project.

## Documentation

Full docs (tutorials / how-to / reference / explanation) live in [`docs/`](docs/index.md).
