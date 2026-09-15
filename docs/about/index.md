---
title: About ffmpeg-wasi
description: Why ffmpeg-wasi exists, where it runs, and how one engine ships as both a sandboxed WASI module and a native driver.
tags: [about, overview, wasi, native]
---

# About ffmpeg-wasi

ffmpeg-wasi is current FFmpeg, built to run inside a Go program without CGO. It exists
because the two available answers to "FFmpeg from Go" were both wrong: shell out to a
binary you have to install and trust, or link a C library and give up the single static
cross-compiled binary. This page is the short version of why, where and how. The long
versions are under [Explanation](../explanation/index.md).

## Why it exists

Every other FFmpeg-in-WebAssembly project runs into the same two walls. From FFmpeg 7.0
the `ffmpeg` command-line tool is built around a multithreaded scheduler and cannot be
configured without threads, and a pure-Go WASI runtime such as [wazero](https://wazero.io/)
has no way to spawn one. The one WASI build that sidestepped this did so by pinning FFmpeg
5.1, which is end-of-life, and an unmaintained decoder for untrusted media is not a
trade-off anybody should be making.

ffmpeg-wasi goes under both walls. The threading lives in the command-line tool, not in the
libraries, so it skips the tool: it compiles the `libav*` libraries single-threaded and
drives them with a small engine of its own. That is why the repository carries an engine
rather than a build script, and it is what lets the project track the latest stable FFmpeg
rather than a frozen one. [Why libav-direct](../explanation/why-libav-direct.md) has the
full argument.

## Where it runs

It was built for [afmpeg](https://afmpeg.phpboyscout.uk), the pure-Go binding that runs
media jobs over an in-memory filesystem, and afmpeg is where most people will meet it. But
the module is not tied to it. Anything that can host a WASI runtime can load the `.wasm`
and drive it with the same [job spec](../reference/job-spec.md): servers, edge workers,
embedded Go, anywhere the deploy is a single static binary and a native FFmpeg install is
not on the table.

It is a server-side project, not a browser one. The well-known `ffmpeg.wasm` is an
emscripten build for the browser, and ffmpeg-wasi is the other end of the same idea: a
WASI build for the back end, with no DOM, no JavaScript and no host filesystem.

## Two targets, one engine

The engine is one C source tree built two ways, and the choice between them is a
performance and security-posture decision rather than a capability one.

**The WASI module** is the sandboxed target. The libraries and the engine compile to
`wasm32-wasi`, single-threaded, and the result is one `.wasm` file that a Go program embeds
and runs under wazero. Every input and output crosses the sandbox boundary as a WASI call,
so the engine never sees a host disk, and a memory bug in a codec corrupts the guest's
linear memory and nothing else. It is the default, and the safe choice for parsing media
you did not produce.

**The native driver** is the same engine compiled for the host with real threads, SIMD and
hand-written assembly enabled, published as a Linux amd64 ELF alongside the modules. It
serves its file I/O over an IPC bridge instead of WASI syscalls, so the caller's filesystem
is still the only one it can reach, but it runs out of process rather than in a sandbox.
Software encode runs roughly 50× faster with openh264 and 170× faster with libx264, and a
third profile adds the HEVC and AV1 encoders that are impractical in WebAssembly. afmpeg
drives it as its native backend. [The build](../explanation/the-build.md) covers how one
build system produces both.

Each target ships in two licence variants, LGPL and GPL, and in profiles that add codecs
additively, so a consumer can move between the WASI module and the native driver without
a capability change until it reaches the encoders only the native full profile carries.
[Variants and artifacts](../reference/variants.md) lists what each release contains.

## Who is behind it

ffmpeg-wasi is built and maintained by Matt Cockayne as part of the
[phpboyscout](https://phpboyscout.uk) estate. The build tooling and the engine are MIT;
the modules and drivers carry the licence of the FFmpeg variant they were built from, and
[the licensing model](../explanation/licensing.md) keeps the three deliberately distinct.
Every release is [signed](../explanation/signing.md), and the [branding](branding.md) page
has the mark and the palette if you need them.
