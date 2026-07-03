#!/bin/sh
# build/libav.sh — configure + build FFmpeg's libav* libraries for wasm32-wasi.
#   FFMPEG_VERSION=n8.1.2 VARIANT=lgpl PROFILE=lean sh build/libav.sh
set -eu
HERE_LIBAV="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck disable=SC1091  # sourced at build time
. "$HERE_LIBAV/toolchain.sh"

: "${FFMPEG_VERSION:?set FFMPEG_VERSION, e.g. n8.1.2}"
: "${VARIANT:=lgpl}"                       # lgpl (default) | gpl
: "${PROFILE:=lean}"                        # lean (default) | intermediate — spec 0022
: "${FFMPEG_SRC:=/ffmpeg}"

git clone https://github.com/FFmpeg/FFmpeg --depth=1 --branch "$FFMPEG_VERSION" "$FFMPEG_SRC"
cd "$FFMPEG_SRC"

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
LEAN_ENABLE="--enable-decoder=h264,hevc,vp8,vp9,mjpeg,png,aac,mp3,opus,vorbis,flac,pcm_s16le,pcm_f32le,rawvideo \
--enable-encoder=mjpeg,png,aac,flac,pcm_s16le \
--enable-demuxer=mov,matroska,webm,mp3,wav,ogg,aac,flac,image2,concat,rawvideo,pcm_s16le,pcm_f32le \
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
INTERMEDIATE_ENABLE="\
--enable-demuxer=mpegts,flv,avi,gif,caf,aiff,au \
--enable-muxer=mpegts,hls,dash,flv,avi,gif,ogg,adts,caf,aiff,au,segment,stream_segment \
--enable-decoder=gif \
--enable-encoder=gif \
--enable-filter=fade,hue,colorbalance,curves,colorchannelmixer,lut,lut3d \
--enable-filter=unsharp,gblur,boxblur,hstack,vstack,xstack,blend,tile \
--enable-filter=select,thumbnail,framestep,palettegen,paletteuse \
--enable-filter=yadif,bwdif,chromakey,colorkey,rotate,hflip,vflip,reverse \
--enable-filter=drawbox,drawgrid,vignette \
--enable-filter=loudnorm,dynaudnorm,acompressor,compand \
--enable-filter=highpass,lowpass,equalizer,atempo,aecho,silenceremove,afftdn \
--enable-filter=pan,channelsplit,channelmap,join,aselect,areverse \
--enable-filter=cropdetect,blackdetect,signalstats,silencedetect,ebur128,astats \
--enable-protocol=file,pipe"

case "$PROFILE" in
  lean)         ENABLE="$LEAN_ENABLE" ;;
  intermediate) ENABLE="$LEAN_ENABLE $INTERMEDIATE_ENABLE" ;;
  *) echo "libav.sh: unknown PROFILE '$PROFILE' (want lean|intermediate)" >&2; exit 2 ;;
esac

# Use clang as the linker (--ld=clang), not raw wasm-ld: clang understands
# --target/--sysroot and links a WASI command (crt1/_start) automatically, so
# configure's link probe just works. (The engine link in driver.sh is also clang.)
# shellcheck disable=SC2086  # $ENABLE/$GPL_FLAGS/$OPENH264_FLAGS are deliberately split into args
LDFLAGS="--target=wasm32-wasip1 --sysroot=$WASI_SYSROOT $SJLJ -L$PREFIX/lib $WASI_EMULATED_LIBS" \
./configure \
  --cc="$CC" --cxx="$CXX" --ld="$CC" --nm="$NM" --ar="$AR" --ranlib="$RANLIB" --strip="$STRIP" \
  --extra-cflags="-include $HERE_LIBAV/wasi-compat.h -Wno-error=implicit-function-declaration -Wno-error=int-conversion -Wno-error=incompatible-function-pointer-types" \
  --enable-cross-compile --arch=x86_32 --target-os=none \
  --disable-shared --enable-static --enable-small --disable-stripping \
  --disable-programs --disable-doc --disable-debug --disable-network \
  --disable-pthreads --disable-w32threads --disable-os2threads \
  --disable-runtime-cpudetect --disable-asm --disable-x86asm \
  --enable-zlib \
  --disable-everything $ENABLE $OPENH264_FLAGS $GPL_FLAGS \
  || { echo "configure failed"; tail -40 ffbuild/config.log; exit 1; }

# wasm has no sysctl/mkstemp/gethrtime/setrlimit — force the portable fallbacks.
# (Missing function declarations like dup/tempnam are handled by the force-included
# build/wasi-compat.h, baked into config.mak via --extra-cflags above.)
for d in HAVE_SYSCTL HAVE_MKSTEMP HAVE_GETHRTIME HAVE_SETRLIMIT; do
  sed -i "s|#define $d 1|#define $d 0|" config.h || true
done

make -j"$(nproc)"
echo "libav* built ($PROFILE, $VARIANT, FFmpeg $FFMPEG_VERSION)"
