// src/nativeio.h — the engine's media-I/O abstraction across both build targets.
//
// The wasm build opens media on real (WASI-mounted) paths, exactly as before. The
// native build (spec 0028 Backend B) routes every media open through a custom
// *seekable* AVIO over the afmpeg native host's IPC socket (AFMPEG_NATIVE_SOCKET),
// so all input/output stays in the caller's afero.Fs — no host-disk file — and the
// muxer's backward seeks (e.g. the non-fragmented MP4 moov/mdat patch) work over a
// real seek_cb. The socket/IPC code is compiled only under -DAFMPEG_NATIVE; for
// wasm these entry points are thin passthroughs to the libav* functions.
#ifndef AFMPEG_NATIVEIO_H
#define AFMPEG_NATIVEIO_H

#include <stddef.h>
#include <stdint.h>

struct AVFormatContext;
struct AVInputFormat;
struct AVDictionary;

// afio_open_input opens an input format context for path: a custom AVIO over IPC
// when the native bridge is active, else avformat_open_input on the real path.
int afio_open_input(struct AVFormatContext **out, const char *path,
                    const struct AVInputFormat *ifmt, struct AVDictionary **opts);

// afio_open_concat opens the concat demuxer over `n` segment paths joined as one
// continuous input (spec 0013 §3.2). Native bridge active: the playlist is built in
// memory and every segment open is routed through IPC (so nothing touches host disk);
// otherwise it materialises an ffconcat list in the /tmp scratch overlay and opens
// each segment on its real path. Closed by afio_close_input like any input.
int afio_open_concat(struct AVFormatContext **out, const char *const *segments,
                     int n, int idx);

// afio_close_input closes a context opened by afio_open_input or afio_open_concat,
// freeing the custom AVIO if one was installed.
void afio_close_input(struct AVFormatContext **out);

// afio_open_output installs the muxer's pb for writing (custom AVIO over IPC when
// the native bridge is active, else avio_open).
int afio_open_output(struct AVFormatContext *ofmt, const char *path);

// afio_install_muxer_io routes a multi-file muxer's CHILD opens (an hls/dash
// playlist and its segments, an image2 numbered sequence) over the same bridge as
// the primary pb. Those go through AVFormatContext.io_open rather than the pb, so
// without this they reach the host filesystem directly. No-op on wasm, and on a
// muxer that writes a single file.
void afio_install_muxer_io(struct AVFormatContext *ofmt);

// afio_close_output closes the muxer's pb (no-op when none is open).
void afio_close_output(struct AVFormatContext *ofmt);

// afio_bridge_active reports whether the native IPC bridge is in use, so a
// component that opens something OUTSIDE afio_* can refuse to. Always 0 on wasm,
// where the wazero mount is the boundary and a plain open is correct.
int afio_bridge_active(void);

// afio_write_file writes len bytes to path in one shot — the frames op's still
// image output (custom AVIO over IPC when active, else fopen/fwrite).
int afio_write_file(const char *path, const uint8_t *data, size_t len);

#endif

// The URL-protocol layer (afmpeg spec 0043 D1), called from the carried patch to
// libavformat/file.c on the native build. Channel 3: the URLs libav opens on its
// own initiative, below every hook this engine installs.
int afmpeg_url_active(void);
int afmpeg_url_open(const char *filename, int flags);
int afmpeg_url_read(int fd, unsigned char *buf, int size);
int afmpeg_url_write(int fd, const unsigned char *buf, int size);
int64_t afmpeg_url_seek(int fd, int64_t pos, int whence);
int afmpeg_url_close(int fd);
int afmpeg_url_check(const char *filename, int mask);
int afmpeg_url_move(const char *src, const char *dst);
int afmpeg_url_delete(const char *filename);
