# ffmpeg-wasi

**Current FFmpeg, as a sandboxed WebAssembly module — for the server, not the
browser.** FFmpeg's media libraries are built to `wasm32-wasi` and driven by a
small purpose-built engine, producing a single `.wasm` artifact that runs media
pipelines anywhere a WASI runtime does: no native FFmpeg install, no C toolchain
at deploy time, and no shelling out to a binary.

> **This is a read-only mirror. The canonical repository is on GitLab:**
> **https://gitlab.com/phpboyscout/ffmpeg-wasi**
>
> Issues and merge requests are handled there.

## Getting it

Signed release artifacts, including the driver builds and a checksum manifest,
are published on the
[GitLab releases page](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases).
Both a full GPL and a full LGPL build are produced, because the licence you can
ship depends on which one you take.

To drive it from Go, the usual route is
[afmpeg](https://gitlab.com/phpboyscout/afmpeg), the pure-Go binding that runs
this module over an in-memory filesystem:

```
go get gitlab.com/phpboyscout/afmpeg
```

Anything that can host a WASI runtime can use the module directly, in any
language. `go get github.com/phpboyscout/ffmpeg-wasi` will not work — the module
path is the GitLab one, and this mirror is for browsing and reference only.

## Documentation

Full documentation: **https://ffmpeg-wasi.phpboyscout.uk**

Background on how it is built and what the sandbox does and does not buy you:
[Introducing afmpeg and ffmpeg-wasi](https://phpboyscout.uk/introducing-afmpeg-and-ffmpeg-wasi/).
