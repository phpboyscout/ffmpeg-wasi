// src/nativeio.c — see nativeio.h. The afio_* entry points are compiled in both
// targets; the seekable-AVIO-over-IPC implementation is native-only (-DAFMPEG_NATIVE).
#include "nativeio.h"

#include <stdio.h>

#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <libavutil/error.h>
#include <libavutil/mem.h>

// concat_quote_len is how many bytes escaping name into an ffconcat `file '...'`
// argument will take. Inside single quotes ffconcat treats everything literally
// until the next quote, so a quote is written by closing, escaping and reopening:
// '\'' -- four bytes for one. Backslash is escaped too.
static size_t concat_quote_len(const char *name) {
    size_t len = 0;
    for (const char *p = name; *p; p++) len += (*p == '\'' || *p == '\\') ? 4 : 1;
    return len;
}

// concat_quote writes name into dst escaped for an ffconcat file argument, and
// returns how many bytes it wrote. dst must have concat_quote_len(name) room.
static size_t concat_quote(char *dst, const char *name) {
    size_t o = 0;
    for (const char *p = name; *p; p++) {
        if (*p == '\'' || *p == '\\') {
            dst[o++] = '\''; dst[o++] = '\\'; dst[o++] = *p; dst[o++] = '\'';
        } else {
            dst[o++] = *p;
        }
    }
    return o;
}

// concat_name_is_safe rejects a segment name that cannot be represented on one
// ffconcat line at all. A newline would start a fresh line, and the concat
// demuxer honours directives there -- so a caller-chosen filename would become
// control over what the engine opens (ffmpeg-wasi#25). Escaping cannot help:
// there is no way to spell a newline inside an ffconcat argument.
static int concat_name_is_safe(const char *name) {
    return strchr(name, '\n') == NULL && strchr(name, '\r') == NULL;
}

#ifdef AFMPEG_NATIVE
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>

// The framed IPC protocol (mirrors afmpeg pkg/afmpeg/native): a 1-byte version
// preamble, then Open('O',mode,u32 nameLen,name)→status(1); Read('R',u32)→u32 n,
// data; Write('W',u32,data)→u32 n; Seek('S',u64,whence)→u64; Size('Z')→u64;
// Close('C'). All integers little-endian. One connection per opened file.
#define AFIO_PROTO_VERSION 1
#define AFIO_BUF (64 * 1024)

// How long one read or write on the IPC socket may block before the engine gives
// up on the host. Blocking forever is not a defence: a host that stops answering
// -- or answers with a byte count slightly larger than it actually sends -- hangs
// the job with no output and no upper bound (ffmpeg-wasi#31). #24's bounds check
// cannot catch the small lie, because near end-of-file a short reply is normal
// and the overstated count still fits the buffer.
//
// The default is generous: this bounds a single read() or write() syscall, not a
// whole job, and a host serving a large seek may legitimately take a while.
// AFMPEG_NATIVE_TIMEOUT overrides it in seconds.
//
// It cannot be switched off. `0` used to mean block forever, for a host that
// wanted it; no such host ever turned up, and under afmpeg spec 0043 the engine
// runs behind a Landlock floor whose denials a component can sit on -- so
// block-forever became a documented way to turn a sandbox denial into an
// indefinite hang. It was the only setting that removed the sole bound on a
// blocked bridge (afmpeg spec 0044 D5, ffmpeg-wasi#60).
#define AFIO_TIMEOUT_SECS 120

// afio_set_timeouts applies the send/receive deadline to a connected socket.
// Returns 0, or a negative AVERROR when the caller asked for no deadline at all.
// Best-effort: a kernel that refuses the option leaves the socket blocking, which
// is what it did before, so this never fails an otherwise-working job.
static int afio_set_timeouts(int fd) {
    long secs = AFIO_TIMEOUT_SECS;
    const char *env = getenv("AFMPEG_NATIVE_TIMEOUT");
    if (env && *env) {
        char *end = NULL;
        long v = strtol(env, &end, 10);
        if (end != env && *end == 0 && v >= 0) secs = v;
    }
    if (secs <= 0) {
        // Refused, not quietly replaced with the default. A caller who wrote 0
        // believes there is no deadline; leaving them believing something untrue
        // about their own job is the shape of half the defects this engine has
        // had (afmpeg spec 0044 D5).
        fprintf(stderr,
                "ffmpeg-wasi: AFMPEG_NATIVE_TIMEOUT=%s disables the bridge deadline "
                "entirely, and that is no longer supported: a host that stops "
                "answering would hang the job with no upper bound. Set a positive "
                "number of seconds instead.\n",
                env);
        return AVERROR(EINVAL);
    }

    struct timeval tv;
    tv.tv_sec = secs;
    tv.tv_usec = 0;
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);
    return 0;
}

// afio_fail marks a session unusable and returns the error.
//
// Any failure mid-frame leaves the connection desynchronised: bytes belonging to
// the abandoned reply are still queued, so the NEXT request reads them as its own
// header. That turns one diagnosable failure into a series of undiagnosable ones
// -- a bogus seek result, a read returning the wrong bytes, a hang for the length
// of the socket timeout (ffmpeg-wasi#47).
//
// #24 established the principle for an over-long count; it applies to every
// failure, not just that one.
static int afio_fail(int fd) {
    shutdown(fd, SHUT_RDWR);
    return AVERROR(EIO);
}

static int rd_full(int fd, void *p, size_t n) {
    uint8_t *b = p;
    size_t got = 0;
    while (got < n) {
        ssize_t r = read(fd, b + got, n - got);
        if (r <= 0) return -1;
        got += (size_t)r;
    }
    return 0;
}

// send() with MSG_NOSIGNAL rather than write(), because a host that closes the
// connection would otherwise raise SIGPIPE and the default disposition KILLS THE
// PROCESS. Measured: a host vanishing mid-file left the driver dead of a broken
// pipe -- exit status -1, signalled -- instead of returning an error the caller
// could read.
//
// That also made a test lie. The regression test for a transport failure asserts
// "the exit code is not zero", and a signalled process reports -1, so it passed
// on a corpse. #15 was recorded as "not reproduced" on the strength of it.
//
// MSG_NOSIGNAL is preferred over ignoring SIGPIPE globally: it is local to this
// socket, so it cannot surprise anything else in the process.
static int wr_full(int fd, const void *p, size_t n) {
    const uint8_t *b = p;
    size_t put = 0;
    while (put < n) {
        ssize_t r = send(fd, b + put, n - put, MSG_NOSIGNAL);
        if (r <= 0) return -1; // EPIPE arrives here now, instead of as a signal
        put += (size_t)r;
    }
    return 0;
}

static void put32(uint8_t *b, uint32_t v) { for (int i = 0; i < 4; i++) b[i] = (uint8_t)(v >> (8 * i)); }
static void put64(uint8_t *b, uint64_t v) { for (int i = 0; i < 8; i++) b[i] = (uint8_t)(v >> (8 * i)); }
static uint32_t get32(const uint8_t *b) { uint32_t v = 0; for (int i = 0; i < 4; i++) v |= (uint32_t)b[i] << (8 * i); return v; }
static uint64_t get64(const uint8_t *b) { uint64_t v = 0; for (int i = 0; i < 8; i++) v |= (uint64_t)b[i] << (8 * i); return v; }

static int afio_active(void) { return getenv("AFMPEG_NATIVE_SOCKET") != NULL; }

// afio_dial connects, sends the version + Open frame, and returns the fd on a 0
// status (or -1). mode is 'r' or 'w'.
static int afio_dial(const char *name, char mode) {
    const char *sock = getenv("AFMPEG_NATIVE_SOCKET");
    if (!sock || !name) return -1;

    int fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return -1;
    if (afio_set_timeouts(fd) < 0) { close(fd); return -1; }

    struct sockaddr_un a;
    memset(&a, 0, sizeof a);
    a.sun_family = AF_UNIX;
    // Refuse rather than truncate. A silently shortened path can NAME A DIFFERENT
    // SOCKET, and the engine would then hand another process the caller's media
    // paths and data (ffmpeg-wasi#55).
    if (strlen(sock) >= sizeof(a.sun_path)) {
        fprintf(stderr, "ffmpeg-wasi: AFMPEG_NATIVE_SOCKET is %zu bytes; the limit is %zu\n",
                strlen(sock), sizeof(a.sun_path) - 1);
        close(fd);
        return -1;
    }
    strncpy(a.sun_path, sock, sizeof(a.sun_path) - 1);
    if (connect(fd, (struct sockaddr *)&a, sizeof a) < 0) { close(fd); return -1; }

    uint8_t ver = AFIO_PROTO_VERSION;
    size_t nl = strlen(name);
    uint8_t hdr[6];
    hdr[0] = 'O';
    hdr[1] = (uint8_t)mode;
    put32(hdr + 2, (uint32_t)nl);

    uint8_t st;
    if (wr_full(fd, &ver, 1) < 0 || wr_full(fd, hdr, sizeof hdr) < 0 ||
        wr_full(fd, name, nl) < 0 || rd_full(fd, &st, 1) < 0 || st != 0) {
        close(fd);
        return -1;
    }
    return fd;
}

// The AVIO callbacks; opaque carries the socket fd.
static int afio_read(void *opaque, uint8_t *buf, int size) {
    int fd = (int)(intptr_t)opaque;
    uint8_t req[5];
    req[0] = 'R';
    put32(req + 1, (uint32_t)size);
    if (wr_full(fd, req, sizeof req) < 0) return afio_fail(fd);

    uint8_t nb[4];
    if (rd_full(fd, nb, sizeof nb) < 0) return afio_fail(fd);

    uint32_t n = get32(nb);
    if (n == 0) return AVERROR_EOF;
    // The host is the counterparty, not an authority. A count larger than the
    // buffer we asked to fill would have us write past its end (ffmpeg-wasi#24).
    //
    // Refusing is not enough on its own: the reply's payload is still queued on
    // the socket, so the next frame would parse those bytes as a header and the
    // session would desynchronise -- which shows up as the engine blocking
    // forever rather than failing. The session is unrecoverable once a reply is
    // malformed, so tear the connection down and let every later call fail fast.
    if (n > (uint32_t)size) return afio_fail(fd);
    if (rd_full(fd, buf, n) < 0) return afio_fail(fd);
    return (int)n;
}

static int afio_write(void *opaque, const uint8_t *buf, int size) {
    int fd = (int)(intptr_t)opaque;
    uint8_t req[5];
    req[0] = 'W';
    put32(req + 1, (uint32_t)size);
    if (wr_full(fd, req, sizeof req) < 0 || wr_full(fd, buf, (size_t)size) < 0) return afio_fail(fd);

    uint8_t nb[4];
    if (rd_full(fd, nb, sizeof nb) < 0) return afio_fail(fd);

    // The count must be EXACTLY what we handed over.
    //
    // Too many was already refused (#24): a host cannot have written more than it
    // was given, and an inflated count corrupts libav's accounting. Too FEW is the
    // one that mattered in practice, because libavformat never resends the
    // remainder -- flush_buffer calls this once and records an error only on a
    // negative return. A short count therefore lost the tail of the buffer in
    // silence, and the job still exited 0 (ffmpeg-wasi#45).
    //
    // There is no useful middle ground: the engine cannot act on a partial write,
    // so a host that manages one has failed, and saying so is the only honest
    // reply.
    uint32_t w = get32(nb);
    if (w != (uint32_t)size) return afio_fail(fd);
    return (int)w;
}

static int64_t afio_seek(void *opaque, int64_t offset, int whence) {
    int fd = (int)(intptr_t)opaque;

    if (whence == AVSEEK_SIZE) {
        uint8_t req = 'Z';
        uint8_t sz[8];
        if (wr_full(fd, &req, 1) < 0 || rd_full(fd, sz, sizeof sz) < 0) return afio_fail(fd);
        return (int64_t)get64(sz);
    }

    whence &= ~AVSEEK_FORCE;
    uint8_t req[10];
    req[0] = 'S';
    put64(req + 1, (uint64_t)offset);
    req[9] = (uint8_t)whence;

    uint8_t off[8];
    if (wr_full(fd, req, sizeof req) < 0 || rd_full(fd, off, sizeof off) < 0) return afio_fail(fd);
    return (int64_t)get64(off);
}

// afio_ipc_open builds a seekable AVIOContext backed by an IPC connection to name.
static AVIOContext *afio_ipc_open(const char *name, int write) {
    int fd = afio_dial(name, write ? 'w' : 'r');
    if (fd < 0) return NULL;

    uint8_t *buf = av_malloc(AFIO_BUF);
    if (!buf) { close(fd); return NULL; }

    AVIOContext *pb = avio_alloc_context(buf, AFIO_BUF, write, (void *)(intptr_t)fd,
                                         write ? NULL : afio_read,
                                         write ? afio_write : NULL,
                                         afio_seek);
    if (!pb) { av_free(buf); close(fd); return NULL; }
    return pb;
}

// afio_ipc_close flushes, sends Close, closes the socket, and frees the context.
// Returns 0, or the error the final flush or Close recorded. The FINAL FLUSH is
// where a write-mode AVIO actually pushes its last buffer, so discarding this
// left an hls/dash/segment/image2 CHILD truncated with the muxer and the job
// both reporting success (ffmpeg-wasi#54) -- distinct from the trailer and
// afio_write_file cases, because the error was destroyed inside io_close2.
static int afio_ipc_close(AVIOContext **ppb) {
    if (!ppb || !*ppb) return 0;

    AVIOContext *pb = *ppb;
    int fd = (int)(intptr_t)pb->opaque;
    uint8_t c = 'C';

    avio_flush(pb);
    int err = pb->error; // where the flush leaves a failed write
    if (wr_full(fd, &c, 1) < 0 && err >= 0) err = AVERROR(EIO);
    close(fd);
    av_freep(&pb->buffer);
    avio_context_free(&pb);
    *ppb = NULL;
    return err < 0 ? err : 0;
}

// --- concat over IPC ------------------------------------------------------
// The concat demuxer opens each segment through AVFormatContext.io_open; we route
// that to a per-file IPC AVIO. The playlist itself is read from an in-memory buffer
// (plsrc) via a custom AVIO, so no scratch file touches the caller's fs.

struct plsrc { uint8_t *data; int len; int pos; };

static int pl_read(void *o, uint8_t *buf, int n) {
    struct plsrc *m = o;
    int k = m->len - m->pos;
    if (k <= 0) return AVERROR_EOF;
    if (k > n) k = n;
    memcpy(buf, m->data + m->pos, (size_t)k);
    m->pos += k;
    return k;
}

static int64_t pl_seek(void *o, int64_t off, int whence) {
    struct plsrc *m = o;
    if (whence == AVSEEK_SIZE) return m->len;
    int64_t np = whence == SEEK_CUR ? m->pos + off : whence == SEEK_END ? m->len + off : off;
    if (np < 0 || np > m->len) return AVERROR(EINVAL);
    m->pos = (int)np;
    return np;
}

static int concat_io_open(AVFormatContext *s, AVIOContext **pb, const char *url,
                          int flags, AVDictionary **options) {
    (void)s; (void)flags; (void)options;
    AVIOContext *c = afio_ipc_open(url, 0);
    if (!c) return AVERROR(EIO);
    *pb = c;
    return 0;
}

static int concat_io_close(AVFormatContext *s, AVIOContext *pb) {
    (void)s;
    // A read session has nothing to flush, so a close error carries no
    // information the caller could act on — unlike the muxer side (#54).
    (void)afio_ipc_close(&pb);
    return 0;
}

// afio_concat_is_mem reports whether a context is one we opened with an in-memory
// playlist (so afio_close_input frees the plsrc AVIO instead of an IPC close).
static int afio_concat_is_mem(AVFormatContext *ic) {
    return ic && ic->iformat && ic->iformat->name &&
           strcmp(ic->iformat->name, "concat") == 0 &&
           ic->io_open == concat_io_open;
}

// afio_open_concat_ipc builds the in-memory ffconcat playlist, wires the memory
// playlist AVIO + the per-segment IPC io_open, and opens the concat demuxer.
static int afio_open_concat_ipc(AVFormatContext **out, const AVInputFormat *cf,
                                const char *const *segments, int n) {
    // Size the "ffconcat version 1.0\n" header + "file '<seg>'\n" per segment.
    size_t len = strlen("ffconcat version 1.0\n");
    for (int i = 0; i < n; i++) {
        if (!concat_name_is_safe(segments[i])) {
            fprintf(stderr, "ffmpeg-wasi: process: concat segment %d contains a newline, "
                            "which cannot be written to a playlist\n", i);
            return AVERROR(EINVAL);
        }
        len += strlen("file '") + concat_quote_len(segments[i]) + strlen("'\n");
    }

    uint8_t *text = av_malloc(len + 1);
    if (!text) return AVERROR(ENOMEM);

    size_t off = 0;
    off += (size_t)snprintf((char *)text + off, len + 1 - off, "ffconcat version 1.0\n");
    for (int i = 0; i < n; i++) {
        off += (size_t)snprintf((char *)text + off, len + 1 - off, "file '");
        off += concat_quote((char *)text + off, segments[i]);
        off += (size_t)snprintf((char *)text + off, len + 1 - off, "'\n");
    }

    struct plsrc *src = av_mallocz(sizeof *src);
    uint8_t *iobuf = av_malloc(4096);
    AVFormatContext *ic = avformat_alloc_context();
    if (!src || !iobuf || !ic) { av_free(text); av_free(src); av_free(iobuf); if (ic) avformat_free_context(ic); return AVERROR(ENOMEM); }

    src->data = text;
    src->len = (int)off;
    ic->pb = avio_alloc_context(iobuf, 4096, 0, src, pl_read, NULL, pl_seek);
    if (!ic->pb) { av_free(text); av_free(src); av_free(iobuf); avformat_free_context(ic); return AVERROR(ENOMEM); }

    ic->flags |= AVFMT_FLAG_CUSTOM_IO;
    ic->io_open = concat_io_open;
    ic->io_close2 = concat_io_close;

    AVDictionary *opts = NULL;
    av_dict_set(&opts, "safe", "0", 0);
    *out = ic;
    int rc = avformat_open_input(out, NULL, cf, &opts);
    av_dict_free(&opts);
    return rc;
}
#endif // AFMPEG_NATIVE

int afio_open_input(AVFormatContext **out, const char *path,
                    const AVInputFormat *ifmt, AVDictionary **opts) {
#ifdef AFMPEG_NATIVE
    if (afio_active()) {
        AVFormatContext *ic = avformat_alloc_context();
        if (!ic) return AVERROR(ENOMEM);

        ic->pb = afio_ipc_open(path, 0);
        if (!ic->pb) { avformat_free_context(ic); return AVERROR(EIO); }

        ic->flags |= AVFMT_FLAG_CUSTOM_IO;
        *out = ic;
        return avformat_open_input(out, NULL, ifmt, opts);
    }
#endif
    return avformat_open_input(out, path, ifmt, opts);
}

int afio_open_concat(AVFormatContext **out, const char *const *segments, int n, int idx) {
    const AVInputFormat *cf = av_find_input_format("concat");
    if (!cf) {
        fprintf(stderr, "ffmpeg-wasi: process: concat demuxer not built\n");
        return AVERROR_DEMUXER_NOT_FOUND;
    }

#ifdef AFMPEG_NATIVE
    if (afio_active()) return afio_open_concat_ipc(out, cf, segments, n);
#endif

    // Fallback (wasm, or native without the bridge): an ffconcat list in the /tmp
    // scratch overlay, segments opened on their real (WASI-mounted) paths.
    char listpath[64];
    snprintf(listpath, sizeof listpath, "/tmp/afmpeg-concat-%d.txt", idx);

    FILE *lf = fopen(listpath, "w");
    if (!lf) {
        fprintf(stderr, "ffmpeg-wasi: process: cannot create concat list\n");
        return AVERROR(EIO);
    }
    for (int i = 0; i < n; i++) {
        const char *seg = segments[i][0] == '/' ? segments[i] + 1 : segments[i];
        if (!concat_name_is_safe(seg)) {
            fclose(lf);
            fprintf(stderr, "ffmpeg-wasi: process: concat segment %d contains a newline, "
                            "which cannot be written to a playlist\n", i);
            return AVERROR(EINVAL);
        }
        char *q = av_malloc(concat_quote_len(seg) + 1);
        if (!q) { fclose(lf); return AVERROR(ENOMEM); }
        q[concat_quote(q, seg)] = 0;
        fprintf(lf, "file '/%s'\n", q);
        av_free(q);
    }
    fclose(lf);

    AVDictionary *opts = NULL;
    av_dict_set(&opts, "safe", "0", 0);
    av_dict_set(&opts, "protocol_whitelist", "file,pipe", 0);
    int rc = avformat_open_input(out, listpath, cf, &opts);
    av_dict_free(&opts);
    return rc;
}

void afio_close_input(AVFormatContext **out) {
#ifdef AFMPEG_NATIVE
    if (out && *out && afio_concat_is_mem(*out)) {
        // A concat context we opened with an in-memory playlist: free the plsrc AVIO
        // ourselves (the per-segment IPC AVIOs are closed by concat via io_close2).
        AVIOContext *pb = (*out)->pb;
        (*out)->pb = NULL;
        avformat_close_input(out);
        if (pb) {
            struct plsrc *src = pb->opaque;
            if (src) av_free(src->data);
            av_free(src);
            av_freep(&pb->buffer);
            avio_context_free(&pb);
        }
        return;
    }
    if (out && *out && ((*out)->flags & AVFMT_FLAG_CUSTOM_IO) && (*out)->pb) {
        AVIOContext *pb = (*out)->pb;
        (*out)->pb = NULL; // detach so avformat_close_input leaves the custom pb alone
        avformat_close_input(out);
        (void)afio_ipc_close(&pb); // read side: nothing to report
        return;
    }
#endif
    avformat_close_input(out);
}

#ifdef AFMPEG_NATIVE
// A muxer that writes MORE THAN ONE FILE — hls, dash, segment, and image2 with a
// numbered pattern — does not put those files through its own pb. It opens each
// one through AVFormatContext.io_open, which the primary-pb wrapping never sees,
// so the playlist and every child went straight to host disk with the bridge
// active and the job still reported success (ffmpeg-wasi#14).
//
// Same shape as concat_io_open on the demux side, with the write flag honoured:
// a segmenting muxer reads back what it wrote in some modes.
static int mux_io_open(AVFormatContext *s, AVIOContext **pb, const char *url,
                       int flags, AVDictionary **options) {
    (void)s; (void)options;
    AVIOContext *c = afio_ipc_open(url, (flags & AVIO_FLAG_WRITE) != 0);
    if (!c) return AVERROR(EIO);
    *pb = c;
    return 0;
}

static int mux_io_close(AVFormatContext *s, AVIOContext *pb) {
    (void)s;
    // Propagated, not swallowed: this is the only place a child file's final
    // flush can report anything (ffmpeg-wasi#54).
    return afio_ipc_close(&pb);
}
#endif // AFMPEG_NATIVE

// afio_install_muxer_io routes a muxer's child-file opens over the bridge. Safe on
// every muxer: one that writes a single file through its pb never calls io_open.
void afio_install_muxer_io(AVFormatContext *ofmt) {
#ifdef AFMPEG_NATIVE
    if (!ofmt || !afio_active()) return;
    ofmt->io_open = mux_io_open;
    ofmt->io_close2 = mux_io_close;
#else
    (void)ofmt;
#endif
}

int afio_open_output(AVFormatContext *ofmt, const char *path) {
#ifdef AFMPEG_NATIVE
    if (afio_active()) {
        ofmt->pb = afio_ipc_open(path, 1);
        return ofmt->pb ? 0 : AVERROR(EIO);
    }
#endif
    return avio_open(&ofmt->pb, path, AVIO_FLAG_WRITE);
}

void afio_close_output(AVFormatContext *ofmt) {
    if (!ofmt || !ofmt->pb) return;
#ifdef AFMPEG_NATIVE
    if (afio_active()) { (void)afio_ipc_close(&ofmt->pb); return; }
#endif
    avio_closep(&ofmt->pb);
}

// afio_write_file writes one file in a single shot -- the frames op's still
// images. Every failure after the open used to be discarded: avio_write is void
// and records into pb->error, which nobody read; avio_flush's outcome was
// dropped; and the plain-file branch ignored fwrite and fclose alike. A bridge
// that dropped mid-write, or a full disk, produced an empty or truncated image
// that the reply still listed as written (ffmpeg-wasi#46).
int afio_bridge_active(void) {
#ifdef AFMPEG_NATIVE
    return afio_active();
#else
    return 0;
#endif
}

int afio_write_file(const char *path, const uint8_t *data, size_t len) {
#ifdef AFMPEG_NATIVE
    if (afio_active()) {
        AVIOContext *pb = afio_ipc_open(path, 1);
        if (!pb) return AVERROR(EIO);
        if (len > INT_MAX) return AVERROR(EINVAL); // avio_write takes an int
        avio_write(pb, data, (int)len);
        int err = pb->error;          // where avio_write left a failure
        int cerr = afio_ipc_close(&pb); // the final flush lives in here
        if (err < 0) return err;
        return cerr;
    }
#endif
    FILE *fp = fopen(path, "wb");
    if (!fp) return AVERROR(EIO);
    size_t put = fwrite(data, 1, len, fp);
    // fclose flushes, so it is the last thing that can fail -- and closing a
    // stream whose buffer cannot be written is exactly how a full disk surfaces.
    int closed = fclose(fp);
    return (put != len || closed != 0) ? AVERROR(EIO) : 0;
}
