/* openh264-threads.c — single-threaded pthread/semaphore shim for the wasm32-wasi
 * build of openh264. MIT.
 *
 * wasi-libc (wasip1, no `wasm-threads`) ships <pthread.h>/<semaphore.h> but defines
 * none of these symbols — there is no thread spawning under wasip1. openh264 still
 * *references* them from WelsThreadLib/WelsThreadPool, so the link needs definitions.
 *
 * The engine drives the encoder single-threaded (FFmpeg's libav* is configured
 * `--disable-pthreads`, so AVCodecContext.thread_count resolves to 1 → openh264's
 * iMultipleThreadIdc is 1 → it never creates the thread pool). So:
 *   - mutex / cond / sem operations are no-op successes (no contention to guard);
 *   - pthread_create/join are never reached and fail cleanly (ENOSYS) if they ever are.
 *
 * This object is archived into libopenh264.a by build/deps.sh, so it resolves both
 * FFmpeg's configure link-probe and the final engine link. It is the openh264
 * counterpart of build/wasi-compat.c (which covers libav*'s POSIX gaps). */
#include <pthread.h>
#include <semaphore.h>
#include <errno.h>

/* Thread lifecycle — unreachable single-threaded; fail cleanly if ever called. */
int pthread_create(pthread_t *t, const pthread_attr_t *a,
                   void *(*fn)(void *), void *arg) {
    (void)t; (void)a; (void)fn; (void)arg;
    return ENOSYS;
}
int pthread_join(pthread_t t, void **r) { (void)t; (void)r; return 0; }
pthread_t pthread_self(void) { return (pthread_t)0; }
int pthread_attr_init(pthread_attr_t *a) { (void)a; return 0; }
int pthread_attr_destroy(pthread_attr_t *a) { (void)a; return 0; }

/* Mutexes — no contention single-threaded, so every op succeeds. */
int pthread_mutex_init(pthread_mutex_t *m, const pthread_mutexattr_t *a) { (void)m; (void)a; return 0; }
int pthread_mutex_destroy(pthread_mutex_t *m) { (void)m; return 0; }
int pthread_mutex_lock(pthread_mutex_t *m) { (void)m; return 0; }
int pthread_mutex_unlock(pthread_mutex_t *m) { (void)m; return 0; }

/* Condition variables — no waiters to signal single-threaded. */
int pthread_cond_init(pthread_cond_t *c, const pthread_condattr_t *a) { (void)c; (void)a; return 0; }
int pthread_cond_destroy(pthread_cond_t *c) { (void)c; return 0; }
int pthread_cond_wait(pthread_cond_t *c, pthread_mutex_t *m) { (void)c; (void)m; return 0; }
int pthread_cond_timedwait(pthread_cond_t *c, pthread_mutex_t *m, const struct timespec *t) {
    (void)c; (void)m; (void)t; return 0;
}
int pthread_cond_broadcast(pthread_cond_t *c) { (void)c; return 0; }

/* Semaphores — the encoder only posts/waits across worker threads it never spawns. */
int sem_init(sem_t *s, int pshared, unsigned value) { (void)s; (void)pshared; (void)value; return 0; }
int sem_destroy(sem_t *s) { (void)s; return 0; }
int sem_post(sem_t *s) { (void)s; return 0; }
int sem_wait(sem_t *s) { (void)s; return 0; }
int sem_trywait(sem_t *s) { (void)s; return 0; }
int sem_timedwait(sem_t *s, const struct timespec *t) { (void)s; (void)t; return 0; }
