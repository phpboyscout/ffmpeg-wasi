// ffmpeg-wasi — the OS-enforced filesystem floor (afmpeg spec 0043 D2).
// SPDX-License-Identifier: MIT
//
// # Why the kernel and not a seam of ours
//
// libav reaches the filesystem through four channels. The bridge covers
// AVFormatContext.pb and, ad hoc, io_open child opens. It cannot see the URL
// protocol layer, and it certainly cannot see a plain fopen inside libass, a
// lut3d file, or libx264's stats writer. No seam in this codebase can: those are
// libc calls inside third-party C. Only the kernel is below all of them.
//
// # Why only when the bridge is active
//
// This is a refinement spec 0043 D2 does not spell out, and it is forced rather
// than chosen. In plain-file mode the caller hands the engine real host paths and
// there is no containment claim to enforce — sandboxing it would break the
// ordinary way the driver is invoked, and granting the paths from the job spec
// would be the classification table D5 rejects.
//
// The guarantee exists when the bridge is serving I/O. That is exactly when the
// engine has no legitimate business on the filesystem at all, which is what makes
// a deny-everything floor possible without an enumeration.
#ifndef _GNU_SOURCE
#define _GNU_SOURCE // O_PATH
#endif

#include "sandbox.h"

#ifndef AFMPEG_NATIVE

void sandbox_install(void) {}
const char *sandbox_state(void) { return "unavailable"; }

#else

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <unistd.h>

#include <linux/landlock.h>

// The build container's linux/landlock.h can predate the kernel the artefact will
// run on, so the ABI bits are defined here when the header lacks them. The kernel
// ABI is stable and additive; the header vintage is an accident of the image.
#ifndef LANDLOCK_ACCESS_FS_REFER
#define LANDLOCK_ACCESS_FS_REFER (1ULL << 13)
#endif
#ifndef LANDLOCK_ACCESS_FS_TRUNCATE
#define LANDLOCK_ACCESS_FS_TRUNCATE (1ULL << 14)
#endif
#ifndef O_PATH
#define O_PATH 010000000
#endif

static const char *state = "unavailable";

const char *sandbox_state(void) { return state; }

static inline int lk_create(const struct landlock_ruleset_attr *attr, size_t size, __u32 flags) {
    return (int)syscall(__NR_landlock_create_ruleset, attr, size, flags);
}

static inline int lk_add(int fd, enum landlock_rule_type type, const void *attr, __u32 flags) {
    return (int)syscall(__NR_landlock_add_rule, fd, type, attr, flags);
}

static inline int lk_restrict(int fd, __u32 flags) {
    return (int)syscall(__NR_landlock_restrict_self, fd, flags);
}

// grant opens one path and allows exactly the given access beneath it.
//
// A path that does not exist is not an error: /dev/urandom is the only grant and
// a container without it is a container where Matroska muxing was already going
// to fail, loudly, for its own reasons.
static void grant(int ruleset, const char *path, __u64 allowed) {
    struct landlock_path_beneath_attr pb;
    memset(&pb, 0, sizeof pb);
    pb.allowed_access = allowed;
    pb.parent_fd = open(path, O_PATH | O_CLOEXEC);
    if (pb.parent_fd < 0) return;
    (void)lk_add(ruleset, LANDLOCK_RULE_PATH_BENEATH, &pb, 0);
    close(pb.parent_fd);
}

void sandbox_install(void) {
    // Nothing to contain when the caller gave us host paths on purpose.
    const char *sock = getenv("AFMPEG_NATIVE_SOCKET");
    if (!sock || !*sock) { state = "disabled"; return; }

    // Explicitly disableable, for a deployment with its own confinement that does
    // not want a second one (spec 0043 D2).
    const char *off = getenv("AFMPEG_NO_SANDBOX");
    if (off && strcmp(off, "1") == 0) { state = "disabled"; return; }

    int abi = lk_create(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
    if (abi < 1) {
        // A kernel that cannot do this runs UNCONFINED and says so, rather than
        // refusing to start. afmpeg is a library inside someone else's process, and
        // refusing turns a security improvement into an outage on upgrade (0043
        // OQ1). D3's reporting contract is what makes that defensible: a host that
        // requires confinement asserts the state instead of assuming it.
        state = "unavailable";
        return;
    }

    // Handle every access this ABI knows about, then grant back only what is
    // needed. Masking to the ABI matters: naming a bit the running kernel does not
    // know makes the ruleset creation fail outright, which would report
    // "unavailable" on a kernel that is merely older than the build host.
    __u64 handled = LANDLOCK_ACCESS_FS_EXECUTE | LANDLOCK_ACCESS_FS_WRITE_FILE |
                    LANDLOCK_ACCESS_FS_READ_FILE | LANDLOCK_ACCESS_FS_READ_DIR |
                    LANDLOCK_ACCESS_FS_REMOVE_DIR | LANDLOCK_ACCESS_FS_REMOVE_FILE |
                    LANDLOCK_ACCESS_FS_MAKE_CHAR | LANDLOCK_ACCESS_FS_MAKE_DIR |
                    LANDLOCK_ACCESS_FS_MAKE_REG | LANDLOCK_ACCESS_FS_MAKE_SOCK |
                    LANDLOCK_ACCESS_FS_MAKE_FIFO | LANDLOCK_ACCESS_FS_MAKE_BLOCK |
                    LANDLOCK_ACCESS_FS_MAKE_SYM;
    if (abi >= 2) handled |= LANDLOCK_ACCESS_FS_REFER;
    if (abi >= 3) handled |= LANDLOCK_ACCESS_FS_TRUNCATE;

    struct landlock_ruleset_attr attr;
    memset(&attr, 0, sizeof attr);
    attr.handled_access_fs = handled;

    int ruleset = lk_create(&attr, sizeof attr, 0);
    if (ruleset < 0) { state = "unavailable"; return; }

    // The one grant. Matroska asks av_get_random_seed for a segment UID, and
    // without a readable /dev/urandom that call does not fail — it BLOCKS, which
    // is how mkv output once hung under wasi.
    grant(ruleset, "/dev/urandom", LANDLOCK_ACCESS_FS_READ_FILE);

    // The AF_UNIX connect that carries every byte of media needs no grant: Landlock
    // ABI 4 does not govern it. That is what makes a deny-everything floor
    // compatible with a bridge that serves all I/O over a socket.
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) {
        close(ruleset);
        state = "unavailable";
        return;
    }
    if (lk_restrict(ruleset, 0) < 0) {
        close(ruleset);
        state = "unavailable";
        return;
    }
    close(ruleset);
    state = "landlock";
}

#endif
