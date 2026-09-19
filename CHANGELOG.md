# Changelog

## [n9.0.1-7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-7)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-6...n9.0.1-7)

### Notes

- Modules, checksums, provenance and the signature are now published to https://pkg.phpboyscout.uk/ffmpeg-wasi/<tag>/ and the release links point there; GitLab's package registry no longer receives new releases.

- A release now offers every artefact the signature covers. n9.0.1-6 and earlier linked seventeen files while twenty-three were built, signed and uploaded — every gzipped native driver was published to the package registry and never offered on the release page. The upload now derives its list from checksums.txt, and a check refuses a release whose links and signed set disagree.

### Features

- **release**: publish modules to the release store, not the package registry ([665f8af](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/665f8af1e7e169f078f0e09370a23b0c22615995))

### Bug Fixes

- **release**: link every artefact the signature covers ([25488e5](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/25488e5d2bc73744927f4123bacb457559483c12))

## [n9.0.1-6](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-6)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-5...n9.0.1-6)

### Notes

- The release path works again. The first pipeline after adopting colophon failed on the job that cuts the tag, because of a defect in the colophon version the image carried; this pins the image that fixes it. No change to how releases are named or proposed.

- The release object is now created by colophon rather than by release-cli, carrying the same seventeen artefacts. Each one is verified to resolve before the release is published, which the previous job did not do — a release can no longer appear with a link to a file the upload failed to place.

- Releases are now cut by merging a release merge request rather than by pushing a tag by hand, and this repository has a CHANGELOG.md for the first time — seeded with all seventeen releases back to n8.1.2-1. Versions are unchanged in shape and meaning: n9.0.1-6 is still the sixth build of FFmpeg n9.0.1, the ordinal still restarts when the FFmpeg version moves, and build/ffmpeg-version.txt is still the one place that version is written. The release description now links to the Variants & artifacts reference page instead of repeating it.

### Features

- **build**: a `just ci` that predicts the pipeline, and a lint that can run ([847fedf](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/847fedf693f2381c0d85336e0e4ecc3de78c5ea2))

### Bug Fixes

- **docs**: quote the descriptions YAML cannot read ([7fd2dba](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7fd2dba6859a70732ba1f5ffa7e454f9fe0b2d41))
- **build**: name the missing tool instead of retrying the clone that needed it ([058bf5c](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/058bf5cba1d5fca826a10150f33326d413e5a4de))
- **build**: name meson's pkg-config binary by its canonical key ([73ac79e](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/73ac79e707523e7fc21044386ea1579158a0d568))

## [n9.0.1-5](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-5)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-4...n9.0.1-5)

### Bug Fixes

- **process**: time the audio frames a filter hands over without a pts ([6e35ef6](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/6e35ef6fe07e005b33405de4f29aa85c2ec94185))
- **process**: clamp a non-advancing dts at the muxer instead of failing the job ([1ace304](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/1ace304019c036d2f216931c4430300a62c2ef4b))
- **process**: take the audio encoder's time base from the sink ([10da701](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/10da7014fd3d37fe26bf5388fad5352ed13bfc9a))

## [n9.0.1-4](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-4)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-3...n9.0.1-4)

### Bug Fixes

- **process**: rescale decoded audio timestamps into the buffersrc's time base ([2b25142](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/2b251420f94b84eaf8acde23d9e8a2cc0127bdc6))

## [n9.0.1-3](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-3)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-2...n9.0.1-3)

### Features

- **engine**: address an option to one encoder rather than every one ([4890d52](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/4890d525356138eac4fe8c8a0c1cec8dc7f6f674))

## [n9.0.1-2](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-2)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n9.0.1-1...n9.0.1-2)

### Features

- **engine**: route the file URL protocol through the bridge ([3af8d9d](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/3af8d9de179e2f9f58c5b1f68edabbc25741e3a1))
- **engine**: put a Landlock floor under the bridge ([8495b80](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/8495b8052f27ae5f2173a87c045e8315b426eaf3))
- **engine**: speak IPC protocol v2, and fail a read the host says failed ([4f2bbe8](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/4f2bbe85596dec226bdf69f9afe0c4063e805724))
- **ipchost**: serve the driver over the AFMPEG_NATIVE_SOCKET bridge ([31d0cdf](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/31d0cdf7932e5746bb49299bba471d1db95a4661))

### Bug Fixes

- **engine**: degrade to protocol v1 instead of refusing an older host ([8f9592b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/8f9592b025738a152992bd08c1a06c9b0368489e))
- **engine**: decide a copy cutoff on decode order, anchored on presentation ([a945d5b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/a945d5bc62307a1097eb036e63d08dae7e0d6545))
- **engine**: let the encoder choose its own picture types ([801b8be](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/801b8be6cac0a51de1457df7481a3f6035283d1e))
- **engine**: say why an input stopped, and finish the liveness corpus ([73b7a39](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/73b7a394ec889305b447d161ff6a4a397ba13428))
- **nativeio**: stop letting a caller disable the bridge deadline ([1b755ea](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/1b755eaa37028baf766668656ae31110a7e24d4d))
- **engine**: bound the job-spec options that make a component iterate ([f7471bc](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/f7471bcab527455878f6c806e4851822def5830d))
- **engine**: give a copied packet the duration its demuxer withheld ([c6bdc69](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/c6bdc69b0c7936312b39b8c2214f0f1de37e145d))
- **engine**: finish a job whose length is set by the graph, not the input ([ca62798](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/ca62798b51d9ecdcfdd5e468fb83810a8310b7c0))
- **engine**: refuse an option or a version the engine cannot honour ([e4cd438](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/e4cd438bf5ed8db25216063b5d6aee409f32ae3e))
- what the re-review found, including a bug the last round introduced ([0ee3f5a](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/0ee3f5aa446dbbc664ed097e92510c6c768ad9c5))
- **engine**: saturate timestamp arithmetic instead of wrapping ([e405b63](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/e405b6381c463a11894e797af397319cbfa9ee9d))
- **engine**: refuse a request instead of half-honouring it ([991dad6](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/991dad6a328a9ff69af4ac0941bcd8abd5506075))
- **engine**: carry metadata to the subtitle lane, and put chapters in the window ([4b22c89](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/4b22c89df361833e777a81b26620475dd3bcb08f))
- **frames**: refuse an output path that does not fit instead of truncating it ([3ffd54a](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/3ffd54a52827bddcfae44490ca398bb6cda32edb))
- **progress**: stop opening a host path when the bridge is active ([7d00736](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7d007368bb0414c28abe530ae1352b6540c27bba))
- **engine**: report a flush that failed, and flush the lane nobody was flushing ([062c9f3](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/062c9f35d739352d1d5d937e52bf3befd66ee2cd))
- **nativeio**: stop reporting success for writes that did not happen ([392db0b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/392db0bf0d912eccf5adecd378a23915c26c718e))
- **ipchost**: make the test host hold the engine to the document ([84779b2](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/84779b288c5b0e35739a19c466b16bf60f014d46))
- **engine**: tell a failed read from an ended one, and stop dying of SIGPIPE ([e3c1d31](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/e3c1d31d57b6585ab3482db8072ff4d2205b65d3))
- **engine**: drain a finished sink instead of abandoning it ([2fb754c](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/2fb754c15248e694aca9c5f33b1e7f7087cb58b5))
- **nativeio**: route a multi-file muxer's children over the bridge ([3db44ee](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/3db44ee1a3842298f9c57a10618e0abea2901190))
- **nativeio**: give up on a host that stops answering ([7bf711a](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7bf711ac0d8a5001001536fc5167736da06a891c))
- **engine**: let a bounded output bound the work ([8bde176](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/8bde1761ea9ee82a11558262da9f8332ecfecadf))
- **engine**: answer a bad job spec instead of guessing at it ([0cbe80f](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/0cbe80fc46c7f7ed0b498cefd7bc30d22590442b))
- **engine**: give the subtitle lane the right stream and the right window ([1571364](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/15713648eefd833104bf69b6803fe0f305782863))
- **engine**: stop discarding three failures the caller needs ([f6074a7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/f6074a7d569efc8bc444695837151168321e4420))
- **frames**: tell an end of stream apart from a failure ([91bbbd2](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/91bbbd243ebf4a05d64fc01b8ebb1d3b8bb51a45))
- **engine**: constrain the audio sink to the encoder's rates and layouts ([cd6c749](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/cd6c74935659fe0ff6e8dff560816ed18fbb7b1d))
- **nativeio**: escape segment names when building a concat playlist ([d2d6ed7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/d2d6ed7370e3d2cfc0e653b2ae9db4514a47a524))
- **engine**: give a pure-copy output its streams when map is omitted ([b292ff6](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/b292ff6bb1c5251ac76a2f9a784998db821c9968))
- **nativeio**: refuse a read count larger than the buffer it fills ([1755bf7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/1755bf73f27214ea9a27693bcbbd16f6d5d21082))
- **engine**: report a failed trailer instead of exiting 0 ([bb5944f](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/bb5944fdcbcaf9caefe80e0b61f6a488b36601e3))
- **engine**: give every encoded packet a duration ([ab68726](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/ab68726d3d1a861ce3f0ddfd1e785da8b726b3ee))
- **engine**: let a filter graph finish before its input does ([079eb4b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/079eb4b5a594c70c0558f3e1abcdc2803dd2d157))
- **parity**: stop the guard firing on a filtered run, and the table racing ([dff29f9](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/dff29f940934a99ce984b433e9c055e325c3f91d))

## [n9.0.1-1](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n9.0.1-1)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-12...n9.0.1-1)

### Features

- **build**: build the engine on FFmpeg n9.0.1 ([8f365e7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/8f365e705fc318117d555f8a5cefefd5d147f4ad))
- **ci**: run the engine conformance suite after the build stage ([ffdbf21](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/ffdbf213ef3eab53e3acca91a5a6e58e77c893e7))
- **engine**: report every linked component via --capabilities ([0097f71](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/0097f71b9c3d6d492aab03d55ce946772979b6ae))
- **ci**: build the engine on a merge request, one compile at a time ([1f5995a](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/1f5995a4ffb652d5889768a6933981b154e3477e))
- **build**: make build/ffmpeg-version.txt the source of truth for FFMPEG_VERSION ([c2800cc](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/c2800cc89cc35f79177dba4e6eda716245d9fe68))

### Bug Fixes

- **engine**: exit 2 when a process output entry is malformed ([d068ccf](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/d068ccf6969a41ca0056f509eb6721b13b0b6d64))
- **ci**: run the conformance suite when the suite itself changes ([7e4f158](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7e4f158da0014fa51d96aaf77822f8780b8bfc7e))
- **tools**: enable wasm exception-handling so intermediate modules load ([e9f7845](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/e9f78454f71eaa2c0109ee3e8b9ba24036763217))
- **conformance**: handle the ad-hoc configure/libav name differences ([cdc3668](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/cdc3668e011ef85afaa74a8abb5cf5d09613ca90))

### Other

- **build**: extract the component allowlist into build/enable-lists.sh ([521abda](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/521abda79860ba38a944e5cf8a2cd6e7b099b3e1))

## [n8.1.2-12](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-12)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-11...n8.1.2-12)

### Features

- **ci**: announce releases to Discord ([a3e3dd9](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/a3e3dd9ea2dce2aad8287b2bcdbfd00637ba39e3))

### Bug Fixes

- **engine**: read encoder formats via avcodec_get_supported_config ([723b0cd](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/723b0cd8534f936b6e2bac1c51f4eb2270bb55da))
- **build**: enable the piped image demuxers so Backend B can open stills ([82315e1](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/82315e1536f092f01e85605cb4d6c7849fb45e2b))
- **deps**: require go 1.26.6 for the stdlib advisories ([f9b420b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/f9b420b3017418d44390d23651e65d8f3626a326))
- **ci**: bump the cicd components to v0.36.0 for Go 1.26.6 ([ca6229d](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/ca6229d56c885b828dac3ba73f892820e592b97b))
- **docs**: correct the build and third-party records ([52a5a49](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/52a5a4901f0cee82af6f4d4bacc740fd6e54fb05))
- **docs**: report the engine contract as driver.c implements it ([6499a3d](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/6499a3dc1dcf069219217e0b0008d11da2416bcb))

## [n8.1.2-11](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-11)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-10...n8.1.2-11)

## [n8.1.2-10](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-10)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-9...n8.1.2-10)

### Features

- **0032**: emit duration_us in progress records for host Fraction ([7298621](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7298621faa68a746c25ce0d17734f99c9e5ce397))

## [n8.1.2-9](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-9)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-8...n8.1.2-9)

### Features

- **0032**: emit progress side-channel from the process op (vocab v9) ([12e1586](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/12e15862cd0fa4eeadb339451a638b0770f6cb06))

## [n8.1.2-8](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-8)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-7...n8.1.2-8)

### Features

- **0023**: AV1 decode in WASM too — single-threaded libdav1d ([1dd730b](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/1dd730bf00910675e7585c537a15798228bbd4c9))
- **0022**: CI size-budget gate ([c2e0d59](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/c2e0d59d4c12bf7b7af8054a03ae6e3a04a79711))
- **0017**: structured analysis-filter output ([eec9a76](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/eec9a764082534107e7d0db91240c43f24de7a27))
- **0023,0028**: native AV1 decode (dav1d) + concat-over-IPC ([f47c992](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/f47c9920a7b1649c78424d15954bad9109248ba8))

## [n8.1.2-7](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-7)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-6...n8.1.2-7)

### Features

- **0023**: native full profile — HEVC (x265) + AV1 (SVT-AV1) encode ([b342550](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/b342550b87e301ac7fc54c4bbd48e9a4ecf52dec))
- **0028**: native intermediate profile — full software-codec batch, native ([fa5a488](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/fa5a488e3d5a19967748d2884b3abe8c13f1c7fb))
- **0028**: publish + sign the native Backend-B driver in releases ([7dfde85](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/7dfde856f2c6db93d53474e46579e663c116480b))
- **0028**: native software H.264 encode (openh264/x264) + pixel_formats fix ([d041e90](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/d041e90cebe8e1c9556feab9bdc5c0f8effebd96))
- **0028**: seekable AVIO-over-IPC in the engine (native media I/O) ([9e5bf53](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/9e5bf53b0251dd4c421f83b246ebbd0de31bee9e))
- **0028**: native driver build (TARGET=native) ([fecbbbf](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/fecbbbf5597774dab814c38a120169973740121e))

## [n8.1.2-6](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-6)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-5...n8.1.2-6)

### Features

- **0019**: subtitle-stream lane — extract/convert/copy subtitle tracks ([6d0dafa](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/6d0dafa4ecccad69b8ccb58113d685640304de78))
- **0029,0019**: meson toolchain + text/subtitle burn-in filters ([57c4d90](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/57c4d90035f5fe086d1ec338019609f8f1f88190))
- **0020**: metadata & chapters — probe read + process write ([cc7f282](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/cc7f2829d9289108919ca0ef28f840f95ea0411f))
- **0021**: frames op — extract stills by timestamp/interval/scene ([77d67ad](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/77d67adfad2f8cda9a87a282abaf4ef5c00dbbc5))
- **0018**: cross-compile LGPL encoder libs into the intermediate profile ([a77f0ef](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/a77f0efe99821ee2cffff42523ed78126ff15c46))
- **build**: native codec batch → intermediate profile (spec 0016) ([4b92150](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/4b921506df7946c80a258829c220c5a8fe324118))
- **build**: native filter batch → intermediate profile (spec 0017) ([9e3d965](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/9e3d96549be42618d7cc98ac3c852c3d5c2f4905))
- **engine**: container coverage — mpegts/hls/dash/… + muxer options (spec 0015) ([9103c05](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/9103c050bf35ea1c236794330add6d77d0c3abe5))
- **build**: PROFILE build-arg (lean|intermediate) — spec 0022 machinery ([daff473](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/daff473bfadbe0a96657e28ea1cd194505468db9))
- **engine**: input demuxer options, forced/raw formats & N:v:K (spec 0024) ([bcef6fa](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/bcef6fab3cd272ad4dc041d21d7f730f5185bfee))
- **engine**: seeking & time ranges (spec 0014, vocab v3) ([225d29f](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/225d29fd0fe80e978629fe4dcd4e416773d95cbb))
- **engine**: stream copy, bitstream filters & concat demuxer (spec 0013) ([90f6939](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/90f693968652ebcc3a848578edc36dd20ec77b48))
- **engine**: job-spec vocabulary version gate + op:version ([5d9fff8](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/5d9fff83d9d24212236fa1c4f1a4b5fb41f9f67f))

### Bug Fixes

- review hardening — engine safety, subtitle timing, reproducible build ([bffaab1](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/bffaab1af3f23e7bc214a1c7869cee9fce64d3b4))
- **process**: guard non-object options before iterating (spec 0027 4C) ([0170d17](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/0170d1786492cc16c80e3e21ec76cfd758ccb728))

## [n8.1.2-5](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-5)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-4...n8.1.2-5)

### Features

- **engine**: multi-output muxing + split/asplit (spec 0007) ([267918e](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/267918ee15e62fd80999406825299c6b8832cef6))

## [n8.1.2-4](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-4)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-3...n8.1.2-4)

### Breaking Changes

- **release**: sign releases as OpenPGP via gtb (org signing model) ([240c8fa](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/240c8fa964cd158a3872cc3c9589cab9f2af6d42))

## [n8.1.2-3](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-3)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-2...n8.1.2-3)

## [n8.1.2-2](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-2)

[Compare to previous version](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/compare/n8.1.2-1...n8.1.2-2)

### Features

- **build**: openh264 H.264 encode in both variants ([164163e](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/164163ecac85930bfd972faf48ba3ce536bf41c8))

## [n8.1.2-1](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/releases/n8.1.2-1)

### Features

- **build**: zlib + native PNG codec (both variants) ([de317e5](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/de317e5b077637cb5293a13dbec1eeeb669b700d))
- **engine**: process v2 — the full filter_complex (multi-input) ([faa5f3c](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/faa5f3c92d66914e0a5daf65233dc42e0ec71a74))
- **engine**: the process operation — real in-memory transcode ([c02847d](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/c02847dc6e4048933b1c7afc7f6c403706b33465))
- **build**: the GPL variant — cross-compile libx264 for H.264 encode ([f081f0e](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/f081f0e1c406b2b1a9b0ea3fe9049a4e134bbe05))
- **engine**: job-spec dispatch + the probe operation (Phase B) ([b16447c](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/b16447ca4914c319a14ca007ab27ba9a96584295))
- scaffold ffmpeg-wasi — current FFmpeg as a sandboxed WASI module ([c7e7d13](https://gitlab.com/phpboyscout/ffmpeg-wasi/-/commit/c7e7d134de3da40995d687f210532616b1d8fbc3))
