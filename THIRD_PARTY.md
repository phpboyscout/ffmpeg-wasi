# Third-party components

ffmpeg-wasi vendors a small amount of third-party source, all under permissive
licences compatible with this repository's [MIT](LICENSE) licence.

| Component | Version | Licence | Location | Used for |
|---|---|---|---|---|
| [cJSON](https://github.com/DaveGamble/cJSON) | v1.7.18 | MIT | `src/third_party/cJSON/` | parsing the engine's JSON job spec |

The FFmpeg `libav*` libraries are **not** vendored — they are cloned at build time
and linked into the released `.wasm` artifacts, which therefore carry the LGPL (or
GPL) licence. See the [README](README.md) and
[docs/explanation/licensing.md](docs/explanation/licensing.md) for the full model.
