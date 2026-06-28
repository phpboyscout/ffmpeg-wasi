/* wasi-compat.c — implementations of POSIX functions wasi-libc omits (WASI lacks
 * the underlying syscalls) but that FFmpeg's portable code references. Compiled
 * into the engine by build/driver.sh. MIT.
 *
 * Declarations live in build/wasi-compat.h (force-included during the libav*
 * build); this file provides the symbols the final link needs. */
#include <errno.h>

/* WASI has no dup(2) and no way to allocate a fresh lowest-available fd, so this
 * is a stub: it satisfies the link (libavformat's file.o references dup) and
 * fails cleanly if ever called. The regular "file" protocol — reading/writing
 * real files over the mounted filesystem — does not dup; only the pipe/fd
 * protocols would, and ffmpeg-wasi's file-based I/O does not use those. */
int dup(int oldfd) {
    (void)oldfd;
    errno = ENOSYS;
    return -1;
}
