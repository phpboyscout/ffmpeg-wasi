/* wasi-compat.h — declarations for POSIX functions wasi-libc omits from its
 * headers (WASI lacks them) but that FFmpeg's portable code references. Force-
 * included during the libav* build (build/libav.sh) so they compile; where a
 * symbol is actually called at link time, build/wasi-compat.c provides a stub.
 * MIT. */
#ifndef FFMPEG_WASI_COMPAT_H
#define FFMPEG_WASI_COMPAT_H
int dup(int oldfd);
char *tempnam(const char *dir, const char *pfx);
#endif
