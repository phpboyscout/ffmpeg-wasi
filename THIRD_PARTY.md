# Third-party components

ffmpeg-wasi vendors a small amount of third-party source, all under permissive
licences compatible with this repository's [MIT](LICENSE) licence.

| Component | Version | Licence | Location | Used for |
|---|---|---|---|---|
| [cJSON](https://github.com/DaveGamble/cJSON) | v1.7.18 | MIT | `src/third_party/cJSON/` | parsing the engine's JSON job spec |

## Built at build time, not vendored

The codec libraries are **not** vendored — they are fetched at build time by
`build/deps.sh` and `build/libav.sh` and linked into the released artifacts. Which
of them a given artifact carries depends on its build profile; see
[docs/reference/build-options.md](docs/reference/build-options.md) for the pins and
[docs/reference/variants.md](docs/reference/variants.md) for the profiles.

### Every build

| Component | Licence | Pinned by | Used for |
|---|---|---|---|
| [FFmpeg](https://github.com/FFmpeg/FFmpeg) `libav*` | LGPL-2.1+ (GPL with x264/x265) | `FFMPEG_VERSION` | the engine's codecs/filters/muxers |
| [openh264](https://github.com/cisco/openh264) | BSD-2-Clause | `OPENH264_VERSION` | H.264 encode (both variants) |
| [x264](https://www.videolan.org/developers/x264.html) | GPL-2.0+ | `X264_COMMIT` | H.264 encode (GPL variant only) |
| [zlib](https://github.com/madler/zlib) | Zlib | `ZLIB_VERSION` | FFmpeg's native PNG codec (WASM; native uses the system zlib) |

### Intermediate and full profiles

| Component | Licence | Pinned by | Used for |
|---|---|---|---|
| [libopus](https://opus-codec.org/) | BSD-3-Clause | `OPUS_VERSION` | Opus encode |
| [LAME](https://lame.sourceforge.io/) | LGPL-2.0+ | `LAME_VERSION` | MP3 encode |
| [libogg](https://xiph.org/ogg/) | BSD-3-Clause | `OGG_VERSION` | Vorbis container dependency |
| [libvorbis](https://xiph.org/vorbis/) | BSD-3-Clause | `VORBIS_VERSION` | Vorbis encode |
| [libwebp](https://chromium.googlesource.com/webm/libwebp) | BSD-3-Clause | `WEBP_VERSION` | WebP encode |
| [libvpx](https://chromium.googlesource.com/webm/libvpx) | BSD-3-Clause | `VPX_VERSION` | VP8/VP9 encode |
| [FreeType](https://freetype.org/) | FTL or GPL-2.0 | `FREETYPE_VERSION` | glyph rasterising for `drawtext`/`subtitles` |
| [HarfBuzz](https://harfbuzz.github.io/) | MIT | `HARFBUZZ_VERSION` | text shaping |
| [FriBidi](https://github.com/fribidi/fribidi) | LGPL-2.1+ | `FRIBIDI_VERSION` | bidirectional text (libass dependency) |
| [libass](https://github.com/libass/libass) | ISC | `LIBASS_VERSION` | `subtitles`/`ass` burn-in |
| [dav1d](https://code.videolan.org/videolan/dav1d) | BSD-2-Clause | `DAV1D_VERSION` | AV1 **decode** |

### Full profile only (native driver)

| Component | Licence | Pinned by | Used for |
|---|---|---|---|
| [SVT-AV1](https://gitlab.com/AOMediaCodec/SVT-AV1) | BSD-3-Clause-Clear + AOM patent grant | `SVTAV1_VERSION` | AV1 encode (both variants) |
| [x265](https://bitbucket.org/multicoreware/x265_git) | GPL-2.0+ | `X265_VERSION` | HEVC encode (GPL variant only) |

The artifact therefore carries the LGPL (or GPL) licence; openh264's self-compiled
H.264 encode adds an AVC **patent** caveat, and x265 a heavier HEVC one. See the
[README](README.md) and [docs/explanation/licensing.md](docs/explanation/licensing.md)
for the full model (including the
[patent posture](docs/explanation/licensing.md#h264-encode-and-the-avc-patent-pool)).
