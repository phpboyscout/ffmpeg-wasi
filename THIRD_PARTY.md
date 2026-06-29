# Third-party components

ffmpeg-wasi vendors a small amount of third-party source, all under permissive
licences compatible with this repository's [MIT](LICENSE) licence.

| Component | Version | Licence | Location | Used for |
|---|---|---|---|---|
| [cJSON](https://github.com/DaveGamble/cJSON) | v1.7.18 | MIT | `src/third_party/cJSON/` | parsing the engine's JSON job spec |

## Built at build time, not vendored

The codec libraries are **not** vendored — they are cloned at build time and linked
into the released `.wasm` artifacts:

| Component | Licence | Pinned in | Used for |
|---|---|---|---|
| [FFmpeg](https://github.com/FFmpeg/FFmpeg) `libav*` | LGPL-2.1+ (GPL with x264) | `FFMPEG_VERSION` | the engine's codecs/filters/muxers |
| [openh264](https://github.com/cisco/openh264) | BSD-2-Clause | `OPENH264_VERSION` | H.264 encode (both variants) |
| [x264](https://www.videolan.org/developers/x264.html) | GPL-2.0+ | `X264_BRANCH` | H.264 encode (GPL variant only) |
| [zlib](https://github.com/madler/zlib) | Zlib | `ZLIB_VERSION` | FFmpeg's native PNG codec |

The artifact therefore carries the LGPL (or GPL) licence; openh264's self-compiled
H.264 encode adds an AVC **patent** caveat. See the [README](README.md) and
[docs/explanation/licensing.md](docs/explanation/licensing.md) for the full model
(including the [patent posture](docs/explanation/licensing.md#h264-encode-and-the-avc-patent-pool)).
