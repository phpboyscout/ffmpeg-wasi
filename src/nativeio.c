// src/nativeio.c — see nativeio.h. The afio_* entry points are compiled in both
// targets; the seekable-AVIO-over-IPC implementation is native-only (-DAFMPEG_NATIVE).
#include "nativeio.h"

#include <stdio.h>

#include <libavformat/avformat.h>
#include <libavformat/avio.h>
#include <libavutil/error.h>
#include <libavutil/mem.h>

#ifdef AFMPEG_NATIVE
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/un.h>

// The framed IPC protocol (mirrors afmpeg pkg/afmpeg/native): a 1-byte version
// preamble, then Open('O',mode,u32 nameLen,name)→status(1); Read('R',u32)→u32 n,
// data; Write('W',u32,data)→u32 n; Seek('S',u64,whence)→u64; Size('Z')→u64;
// Close('C'). All integers little-endian. One connection per opened file.
#define AFIO_PROTO_VERSION 1
#define AFIO_BUF (64 * 1024)

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

static int wr_full(int fd, const void *p, size_t n) {
    const uint8_t *b = p;
    size_t put = 0;
    while (put < n) {
        ssize_t r = write(fd, b + put, n - put);
        if (r <= 0) return -1;
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

    struct sockaddr_un a;
    memset(&a, 0, sizeof a);
    a.sun_family = AF_UNIX;
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
    if (wr_full(fd, req, sizeof req) < 0) return AVERROR(EIO);

    uint8_t nb[4];
    if (rd_full(fd, nb, sizeof nb) < 0) return AVERROR(EIO);

    uint32_t n = get32(nb);
    if (n == 0) return AVERROR_EOF;
    if (rd_full(fd, buf, n) < 0) return AVERROR(EIO);
    return (int)n;
}

static int afio_write(void *opaque, const uint8_t *buf, int size) {
    int fd = (int)(intptr_t)opaque;
    uint8_t req[5];
    req[0] = 'W';
    put32(req + 1, (uint32_t)size);
    if (wr_full(fd, req, sizeof req) < 0 || wr_full(fd, buf, (size_t)size) < 0) return AVERROR(EIO);

    uint8_t nb[4];
    if (rd_full(fd, nb, sizeof nb) < 0) return AVERROR(EIO);
    return (int)get32(nb);
}

static int64_t afio_seek(void *opaque, int64_t offset, int whence) {
    int fd = (int)(intptr_t)opaque;

    if (whence == AVSEEK_SIZE) {
        uint8_t req = 'Z';
        uint8_t sz[8];
        if (wr_full(fd, &req, 1) < 0 || rd_full(fd, sz, sizeof sz) < 0) return AVERROR(EIO);
        return (int64_t)get64(sz);
    }

    whence &= ~AVSEEK_FORCE;
    uint8_t req[10];
    req[0] = 'S';
    put64(req + 1, (uint64_t)offset);
    req[9] = (uint8_t)whence;

    uint8_t off[8];
    if (wr_full(fd, req, sizeof req) < 0 || rd_full(fd, off, sizeof off) < 0) return AVERROR(EIO);
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
static void afio_ipc_close(AVIOContext **ppb) {
    if (!ppb || !*ppb) return;

    AVIOContext *pb = *ppb;
    int fd = (int)(intptr_t)pb->opaque;
    uint8_t c = 'C';

    avio_flush(pb);
    (void)wr_full(fd, &c, 1);
    close(fd);
    av_freep(&pb->buffer);
    avio_context_free(&pb);
    *ppb = NULL;
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

void afio_close_input(AVFormatContext **out) {
#ifdef AFMPEG_NATIVE
    if (out && *out && ((*out)->flags & AVFMT_FLAG_CUSTOM_IO) && (*out)->pb) {
        AVIOContext *pb = (*out)->pb;
        (*out)->pb = NULL; // detach so avformat_close_input leaves the custom pb alone
        avformat_close_input(out);
        afio_ipc_close(&pb);
        return;
    }
#endif
    avformat_close_input(out);
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
    if (afio_active()) { afio_ipc_close(&ofmt->pb); return; }
#endif
    avio_closep(&ofmt->pb);
}

int afio_write_file(const char *path, const uint8_t *data, size_t len) {
#ifdef AFMPEG_NATIVE
    if (afio_active()) {
        AVIOContext *pb = afio_ipc_open(path, 1);
        if (!pb) return AVERROR(EIO);
        avio_write(pb, data, (int)len);
        afio_ipc_close(&pb);
        return 0;
    }
#endif
    FILE *fp = fopen(path, "wb");
    if (!fp) return AVERROR(EIO);
    fwrite(data, 1, len, fp);
    fclose(fp);
    return 0;
}
