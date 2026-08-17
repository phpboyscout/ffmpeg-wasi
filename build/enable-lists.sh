#!/bin/sh
# build/enable-lists.sh — the component allowlist: which FFmpeg components each
# (PROFILE, VARIANT) claims.
#
# Sourced by build/libav.sh, which passes the result to ./configure. Also read
# directly by the capability-conformance suite (spec 0036 D3):
#
#   PROFILE=intermediate VARIANT=lgpl PRINT_COMPONENT_FLAGS=1 sh build/enable-lists.sh
#
# It lives in its own file so those two consumers share ONE definition. A test
# that re-implemented this composition in another language would drift from the
# build silently, and a drifting conformance check is worse than none — it would
# assert the allowlist someone believed in rather than the one that was built.
#
# Pure: no clone, no toolchain, no side effects. Safe to source or to run.
set -eu

: "${VARIANT:=lgpl}"    # lgpl (default) | gpl
: "${PROFILE:=lean}"    # lean (default) | intermediate | full — spec 0022 (full is native-only)
: "${TARGET:=wasm}"     # wasm (default) | native — set by build/toolchain.sh in a real build

# The only GPL trigger in our set is libx264 (the GPL variant). The LGPL variant
# enables no GPL components, so libav* stays LGPL. With --disable-everything the
# encoder must be enabled explicitly, not just the library.
GPL_FLAGS=""
[ "$VARIANT" = "gpl" ] && GPL_FLAGS="--enable-gpl --enable-libx264 --enable-encoder=libx264"

# openh264 (BSD, built in build/deps.sh) gives BOTH variants an LGPL-clean H.264
# encoder. It needs no --enable-gpl/--enable-nonfree; like the GPL trigger above,
# --disable-everything means the encoder must be named explicitly, not just the lib.
OPENH264_FLAGS="--enable-libopenh264 --enable-encoder=libopenh264"

# --- Lean profile (spec 0022 §3/§6) ----------------------------------------
# Web-delivery essentials: the codecs/containers/filters that cover the great
# majority of real jobs at the smallest size (roughly what shipped before 0022).
# A general, dep-free native baseline; external deps (zlib, openh264, x264, …)
# extend it in build/deps.sh.
#
# The image_*_pipe demuxers are not redundant with image2. image2 is AVFMT_NOFILE
# — it opens files by name itself and ignores a custom AVIOContext — so it can
# only be reached on the wasm target, where afio_open_input passes the real
# (WASI-mounted) path. The native target routes media through the IPC AVIO and so
# passes a NULL filename (src/nativeio.c), leaving demuxer selection to content
# probing; without a stream-based image demuxer to probe into, *no* still image
# opens on Backend B at all. Keep one _pipe demuxer per image decoder the profile
# enables.
LEAN_ENABLE="--enable-decoder=h264,hevc,vp8,vp9,mjpeg,png,aac,mp3,opus,vorbis,flac,pcm_s16le,pcm_f32le,rawvideo \
--enable-encoder=mjpeg,png,aac,flac,pcm_s16le \
--enable-demuxer=mov,matroska,webm,mp3,wav,ogg,aac,flac,image2,concat,rawvideo,pcm_s16le,pcm_f32le \
--enable-demuxer=image_png_pipe,image_jpeg_pipe \
--enable-muxer=mp4,mov,matroska,webm,mp3,wav,image2 \
--enable-filter=null,anull,split,asplit,scale,crop,pad,format,fps,settb,asettb,setsar,setpts,asetpts,trim,atrim,loop,transpose,overlay,concat,xfade,amix,adelay,volume,afade,aresample,aformat,alimiter \
--enable-bsf=h264_mp4toannexb,hevc_mp4toannexb,aac_adtstoasc,extract_extradata \
--enable-protocol=file,pipe"

# --- Intermediate profile (spec 0022 §3/§6) --------------------------------
# lean + every practical *software* codec/format/filter (no hardware, no heavy
# thread-hungry encoders). Filled additively by the native flag batches:
# containers [0015], decoders/native-encoders [0016], filters [0017] — each
# appends its own --enable-* group here. No new external lib enters here; those
# (0018) ride build/deps.sh.
#
# Container batch [0015] — native (de)muxers, no licence delta. mpegts/hls/dash
# are the web-delivery marquee (hls muxes TS segments → needs mpegts; dash muxes
# fragmented mp4 → the lean mp4 muxer); the segment muxer generalises segmenting;
# flv/avi/gif and the audio containers (adts/caf/aiff/au) are breadth. gif rides
# with its codec (gif enc/dec) so animated GIF is a complete, testable unit.
#
# Filter batch [0017] — native (in-tree) libavfilter filters, LGPL-clean, no
# external lib, no vocabulary change (they extend the `filter` string's reach).
# Grouped per spec 0017 §3 so the allowlist and the reference matrix stay in step.
# (drawtext/subtitles pull freetype/libass → spec 0019, excluded here.)
# Codec batch [0016] — native (in-tree) libavcodec decoders/encoders, all
# LGPL-clean (no --enable-gpl / --enable-nonfree), flag-only, no external lib and
# no vocabulary change (more valid video_codec/audio_codec strings + demuxable
# inputs). Decoders: broadcast audio (ac3/eac3/dca), lossless/legacy audio
# (alac/wmav2), the PCM tail, images (bmp/tiff), editing intermediates
# (prores/dnxhd/dv), and legacy/broadcast video (mpeg2/mpeg4/vc1/wmv3/theora).
# Encoders: ac3, alac, the PCM tail, and bmp/tiff (so those images round-trip).
# AV1/HEVC and the external-lib encoders are elsewhere (0023 / 0018). AV1 *decode*
# needs libdav1d (FFmpeg's in-tree `av1` decoder is hwaccel-only); it is added below
# for BOTH targets (deps.sh builds dav1d single-threaded for wasm too).
# The image_*_pipe demuxers track this profile's added image decoders (gif/bmp/
# tiff) — see the lean block for why image2 alone leaves Backend B unable to open
# any still image. No image_webp_pipe: libwebp is enabled encode-only below, so
# webp is not decodable here.
INTERMEDIATE_ENABLE="\
--enable-demuxer=mpegts,flv,avi,gif,caf,aiff,au \
--enable-demuxer=image_gif_pipe,image_bmp_pipe,image_tiff_pipe \
--enable-muxer=mpegts,hls,dash,flv,avi,gif,ogg,adts,caf,aiff,au,segment,stream_segment \
--enable-decoder=gif,ac3,eac3,dca,alac,wmav2 \
--enable-decoder=pcm_s24le,pcm_s32le,pcm_f64le,pcm_u8,pcm_s16be,pcm_s24be,pcm_mulaw,pcm_alaw \
--enable-decoder=prores,dnxhd,dvvideo,mpeg2video,mpeg4,vc1,wmv3,theora,bmp,tiff \
--enable-encoder=gif,ac3,alac,bmp,tiff \
--enable-encoder=pcm_s24le,pcm_s32le,pcm_f32le,pcm_mulaw,pcm_alaw \
--enable-filter=fade,hue,colorbalance,curves,colorchannelmixer,lut,lut3d \
--enable-filter=unsharp,gblur,boxblur,hstack,vstack,xstack,blend,tile \
--enable-filter=select,thumbnail,framestep,palettegen,paletteuse \
--enable-filter=yadif,bwdif,chromakey,colorkey,rotate,hflip,vflip,reverse \
--enable-filter=drawbox,drawgrid,vignette \
--enable-filter=loudnorm,dynaudnorm,acompressor,compand \
--enable-filter=highpass,lowpass,equalizer,atempo,aecho,silenceremove,afftdn \
--enable-filter=pan,channelsplit,channelmap,join,aselect,areverse \
--enable-filter=cropdetect,blackdetect,signalstats,silencedetect,ebur128,astats \
--enable-libopus --enable-encoder=libopus \
--enable-libmp3lame --enable-encoder=libmp3lame \
--enable-libvorbis --enable-encoder=libvorbis \
--enable-libwebp --enable-encoder=libwebp \
--enable-libvpx --enable-encoder=libvpx_vp8,libvpx_vp9 \
--enable-libfreetype --enable-libharfbuzz --enable-libass \
--enable-filter=drawtext,subtitles,ass \
--enable-decoder=subrip,ass,webvtt,movtext \
--enable-encoder=srt,ass,webvtt,movtext \
--enable-muxer=srt,webvtt,ass \
--enable-demuxer=srt,ass,webvtt \
--enable-protocol=file,pipe"

# --- Full profile (spec 0022 §3/§6, spec 0023) -----------------------------
# intermediate + the heavy native-only encoders. Threads + SIMD make these viable,
# so full is Native only (0022 §4 — there is no WASM-full). SVT-AV1 (BSD, both
# variants — royalty-free) + x265/HEVC (GPL → gpl variant only, riding the
# --enable-gpl gate GPL_FLAGS already sets). The HW-accel encoders (nvenc/vaapi/
# videotoolbox/qsv) are the remaining full members — deferred until a device exists.
FULL_ENABLE="--enable-libsvtav1 --enable-encoder=libsvtav1"
[ "$VARIANT" = "gpl" ] && FULL_ENABLE="$FULL_ENABLE --enable-libx265 --enable-encoder=libx265"

# AV1 software decode (spec 0023 D-0023-C): libdav1d. FFmpeg's in-tree `av1` decoder
# is hwaccel-only, so software AV1 decode needs the lib. LGPL-clean → both variants;
# rides intermediate + full. The lib is built single-threaded for wasm (deps.sh), so
# both runtimes decode AV1. Deps come from build/deps.sh.
AV1_DECODE="--enable-libdav1d --enable-decoder=libdav1d --enable-parser=av1"

case "$PROFILE" in
  lean)         ENABLE="$LEAN_ENABLE" ;;
  intermediate) ENABLE="$LEAN_ENABLE $INTERMEDIATE_ENABLE $AV1_DECODE" ;;
  full)
    [ "$TARGET" = native ] || { echo "enable-lists.sh: PROFILE=full is native-only (0022 §4 — no WASM-full)" >&2; exit 2; }
    ENABLE="$LEAN_ENABLE $INTERMEDIATE_ENABLE $FULL_ENABLE $AV1_DECODE" ;;
  *) echo "enable-lists.sh: unknown PROFILE '$PROFILE' (want lean|intermediate|full)" >&2; exit 2 ;;
esac
# COMPONENT_FLAGS is every flag that CLAIMS a component, in exactly the
# combination ./configure receives it. This is what spec 0036 D3 reconciles
# against the built artifact's --capabilities output.
COMPONENT_FLAGS="$ENABLE $OPENH264_FLAGS $GPL_FLAGS"

# --- Components upstream gates behind --enable-gpl --------------------------
#
# These are named in the lists above because the GPL variant genuinely carries
# them, but FFmpeg's configure drops them from an LGPL build WITHOUT WARNING.
# The lists therefore read as though lgpl carries them, and it does not.
#
# The conformance check (spec 0036 D3) reads this to tell a licence-driven
# omission from a regression. Without it the check would be permanently red on
# every lgpl artifact, which is how a check gets switched off.
#
#   cropdetect — upstream sets cropdetect_filter_deps="gpl" (in both n8.1.2 and
#   n9.0.1), while its immediate neighbours on the same --enable-filter line,
#   blackdetect and signalstats, build fine. Nothing warns. This cost real time:
#   it surfaced as an afmpeg test failing with "No such filter: 'cropdetect'",
#   which reads as a product defect rather than a build-list inaccuracy.
#
# Format: <kind>=<name>, space-separated. Add an entry ONLY for a component
# upstream gates on a licence — never to silence a component that has genuinely
# gone missing, which is the failure this check exists to catch.
GPL_ONLY_COMPONENTS="filter=cropdetect filter=boxblur"

if [ "${PRINT_COMPONENT_FLAGS:-0}" = 1 ]; then
  printf '%s\n' "$COMPONENT_FLAGS"
fi

if [ "${PRINT_GPL_ONLY_COMPONENTS:-0}" = 1 ]; then
  printf '%s\n' "$GPL_ONLY_COMPONENTS"
fi
