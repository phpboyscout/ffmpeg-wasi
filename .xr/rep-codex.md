## 1. Is the architecture sound?

Not completely.

The portable-engine/per-target-I/O split is the right functional seam: sharing the FFmpeg pipeline while adapting file access for WASI and native avoids duplicating the most complicated code. `afio_*` is a reasonable transport abstraction.

It is not a sound confinement boundary on native. WASI confinement is enforced outside the engine; native confinement depends on every present and future access remembering to use `afio_*`. Libav complicates that further because some muxers and demuxers open child resources through different paths, such as `io_open`. The need for `afio_install_muxer_io()` and `afio_bridge_active()` is evidence that the abstraction is advisory rather than complete. A forgotten call becomes host-filesystem access.

Therefore:

- The shared portable engine is not the architectural mistake.
- Most of the 37 error-handling defects were systemic implementation defects, not consequences of sharing the engine.
- The native claim that “all media stays confined” is architecturally unsound because it rests on call-site discipline.
- The path-escape defects were not incidental; they were predictable consequences of treating an I/O abstraction as a security boundary.

## 2. What is the refactor?

Keep `afio_*`, but demote it from security boundary to transport mechanism. Put the actual native security boundary around the process.

The native host should:

1. Establish the IPC connection before confinement and pass an already-open descriptor to the driver.
2. Start the driver inside an OS-enforced filesystem sandbox.
3. Deny subsequent ambient filesystem access except for an explicit, minimal device set, if genuinely required.
4. Have `nativeio` use the inherited descriptor and fail closed if the native runtime capability is absent.

On Linux this could be implemented with Landlock, a mount namespace, or a carefully staged seccomp policy. The precise facility is secondary; the invariant is that an accidental `open`, `openat`, libav protocol open, child-muxer open, or `fopen` cannot reach the host filesystem.

The internal seam should become a job runtime capability, responsible for:

- Primary input and output opens
- Libav child opens
- Concat materialization
- Frame-file writes
- Scratch storage
- Device/progress access

WASI supplies that runtime through its mounted environment. Native supplies it through IPC. Native should not have a plain-filesystem fallback while operating as the confined backend; missing IPC must be an initialization failure, not a different mode selected implicitly by an absent environment variable.

This would have prevented the path escapes, including forgotten HLS/DASH/image-sequence child opens and future raw file calls. Such calls would fail at the kernel boundary even if the programmer forgot `afio_*`.

It would not prevent ignored failures from being reported as success. That is a separate structural problem.

The costs are meaningful:

- Linux-specific sandbox setup and maintenance
- More complicated process startup and descriptor passing
- Handling dynamic loading and required devices before or within the sandbox
- Additional implementations if native targets expand beyond Linux
- A likely IPC/runtime contract change
- Tests for the sandbox policy itself

That cost is justified if host-filesystem confinement remains a promised property.

## 3. What would make this class of defect visible?

Introduce a mandatory checked-result layer for fallible libav and I/O operations, enforced by the build.

A practical C design is:

- Fallible wrappers return an opaque `AfResult` struct, not a bare `int`.
- Wrapper functions are marked `warn_unused_result`.
- A small set of macros or functions requires callers to explicitly propagate, translate, or intentionally discard the result.
- `-Werror=unused-result` makes omission a build failure.
- Direct use of the wrapped libav entry points is forbidden in engine translation units by a build check or static-analysis rule.

Using a struct matters: it cannot accidentally be treated as an ordinary integer or silently returned as the operation’s exit code. Intent should look structurally distinct:

```c
AF_TRY(af_mux_write(...));
AF_TRY(af_write_trailer(...));
AF_IGNORE(af_optional_cleanup(...), "cleanup after earlier failure");
```

The operation should also default to failure and reach success only through an explicit final commit after encoder flush, trailer write, output close, and result serialization have succeeded. Cleanup must preserve the first failure rather than overwrite it.

This directly attacks the reported pattern: every conversion from “libav failed” to “job succeeded” must contain an explicit, compiler-visible decision.

Tests should additionally assert the expected exit code and result, not merely “non-zero.” A signal should be classified as a crash and fail every ordinary error-path test. Sanitizers help find the corpse, but checked results prevent most of these failures from being buried in the first place.

## 4. What is the single highest-leverage change?

Build-enforced checked results for every fallible libav and engine-I/O call.

The OS sandbox is essential for making the native confinement promise true, but it addresses the smaller path-escape subset. A checked-result facade addresses the dominant pattern across `process`, `frames`, probing, mux finalization, IPC, and output writing: failure silently becoming success.

It is also cheaper and more portable than redesigning the entire engine or rewriting it in a safer language. It converts a recurring review concern into a compile-time obligation while retaining the sound part of the existing design—the shared engine and target-specific I/O transport.
# Greenfield recommendation

I would replace the native bridge with a private POSIX filesystem view and make a mandatory sandbox part of the native backend.

The Go supervisor would expose the caller’s `afero.Fs` through FUSE, mount it inside a new mount namespace, add a read-only runtime plus tmpfs scratch, and launch FFmpeg with no view of the host filesystem. The C engine would use ordinary file operations; the kernel/FUSE boundary would carry reads, errors, metadata operations, and renames.

This deliberately narrows the promise to:

> No job-controlled path can access the host filesystem namespace. Job data is accessible only through explicitly mounted caller filesystems and ephemeral scratch space.

It does not claim that no host filesystem activity occurs while loading the executable or libraries, nor that data cannot reach disk when the caller itself supplied a disk-backed filesystem.

## P1 — file-valued filter and codec options

### Mechanism

Give every job a private filesystem namespace:

- `/in/...`: caller-supplied, read-only assets.
- `/out/...`: writable caller output filesystem.
- `/scratch/...`: private tmpfs, deleted after the job.
- `/runtime/...`: sealed, read-only libraries and configuration.
- No host root, home directory, `/etc`, or general `/dev`.

Expose `/in` and `/out` through a FUSE adapter over the caller’s filesystem. Job-spec paths and typed asset references are rendered as paths in that namespace:

- `subtitles=/in/subtitles/movie.ass`
- `fontsdir=/in/fonts`
- `curves=plot=/out/curve.png`
- `stats=/out/pass`

Advanced raw FFmpeg options may contain paths, but those paths have the same private namespace semantics. Absolute host paths simply do not exist.

I would make inputs read-only and outputs writable rather than mounting the caller’s entire filesystem read-write. The caller chooses the capability roots supplied to each job.

### Guarantees

This guarantees that:

- Known and future file-opening filters see caller assets without special interception.
- Writes from codec and filter options go only to writable job mounts or scratch.
- A path such as `/etc/passwd` cannot resolve to the host’s `/etc/passwd`.
- The solution does not depend on maintaining a list of FFmpeg options that happen to contain paths.

It does not guarantee that:

- Every arbitrary filter option will work; it must refer to a valid virtual path.
- Writes are confined to declared output filenames. A component can write anywhere within the writable output capability.
- Caller data remains off physical disk if the caller supplied a disk-backed filesystem.
- A named font or LUT produces the intended visual result; that is separate from filesystem mediation.

### Cost

- Linux/FUSE and mount-namespace lock-in for the native backend.
- `/dev/fuse` or an appropriate rootless mount facility must be available. Where it is not, native execution should be unavailable and the library should use WASM or return a clear backend error.
- A real filesystem adapter must correctly implement handles, offsets, permissions, metadata, rename, and cancellation.
- Extra context switches on I/O. Large FFmpeg reads, write buffering, readahead, and concurrent FUSE workers should keep this tolerable, but it needs load benchmarks.
- Mount lifecycle, crash cleanup, quotas, and descriptor leaks become ongoing operational responsibilities.

### Discriminating tests

For each of the eight measured vectors:

1. Put an asset with distinctive content in the caller filesystem.
2. Put a different decoy at the corresponding host path.
3. Run the operation and assert output derived from the caller version, not merely that the job succeeded.
4. For write cases, assert the expected files and bytes appear under `/out`, while host decoys remain byte-for-byte unchanged.
5. Assert the FUSE backend observed the expected opens.

Examples should use visibly different subtitle text, fonts with a unique glyph, LUTs producing different colors, and x264 statistics with identifiable contents.

A generic namespace canary should run through the same launcher and attempt to read and write a known host sentinel. It must continue executing, report `ENOENT` or `EACCES` for each attempt, and successfully access `/in` and `/out`. A killed canary is a test failure, not evidence of confinement.

### Deliberately not

I would not:

- Build a blacklist of path-valued FFmpeg options. FFmpeg provides no complete type information, and the list will become stale.
- Copy only recognized assets into temporary host files. It has the same completeness problem and weakens the no-host-path guarantee.
- Interpose `open()` with `LD_PRELOAD`; direct syscalls, static libraries, alternate libc APIs, and future FFmpeg behavior make that brittle.
- Claim “no host disk access” literally. The capability-oriented promise above is narrower and defensible.

## P2 — mandatory OS sandboxing

### Mechanism

Sandboxing should be mandatory for native jobs that process untrusted media.

A trusted supervisor should establish confinement before the engine parses media:

- Private user, mount, PID, and network namespaces where supported.
- Private filesystem layout described under P1.
- Landlock as a second filesystem boundary, allowing only job mounts, runtime assets, scratch, and the required random source.
- `no_new_privs`, dropped groups and capabilities.
- Seccomp denying network creation/connect, mount operations, namespace changes, `ptrace`, `bpf`, `perf_event_open`, keyrings, and further `exec`.
- Inherited descriptors for stdin/stdout/progress; no need for an engine-created Unix socket after moving to FUSE.
- Cgroup or equivalent memory, PID, CPU, and wall-time limits.
- Output and scratch quotas.

If the required primitives cannot be installed, the default must be WASM fallback or a clear refusal to run native. It must not silently run unsandboxed.

### Guarantees

This substantially limits a compromised decoder to:

- Reading declared job inputs and sealed runtime assets.
- Writing the output capability and scratch.
- Consuming resources within configured limits.
- Communicating through inherited descriptors and filesystem operations intentionally provided to it.

It does not guarantee:

- Memory safety or correct output.
- Protection against kernel vulnerabilities.
- Prevention of all timing or resource side channels.
- Preservation of writable job outputs after compromise.
- Perfect availability; limits bound abuse but cannot make pathological inputs cheap.

The argument that current demuxers cannot name files is insufficient. A memory-corruption exploit can issue filesystem or network syscalls directly, and future FFmpeg changes can invalidate today’s route analysis.

### Cost

- Significant Linux-specific launcher and deployment work.
- Seccomp maintenance as FFmpeg, libc, threading, and hardware acceleration needs change.
- Sandboxing failures become a compatibility concern in restricted containers.
- Resource limits require product-level configuration rather than one universal value.
- Debugging crashes is harder; a development-only unsafe mode may be useful, but must be explicit and must void the untrusted-input guarantee.

### Discriminating tests

Use a hostile probe executable launched by the production sandbox, not a media file that merely happens not to escape. It should attempt every forbidden operation, record the exact errno for each, and then exit normally with a success bitmask. It should also prove that intended `/in`, `/out`, scratch, and randomness access still work.

Additional tests should verify:

- With Landlock/seccomp unavailable, default native execution does not begin.
- Network and unrelated Unix-socket connects fail even when a listening target exists.
- Attempts to mount, execute a second binary, or access the host sentinel fail.
- Memory and CPU bombs are classified by the supervisor as resource-limit failures.
- The harness rejects signalled processes. Negative tests must require a normal exit and an exact expected result, never merely “exit code was nonzero.”

Fuzzing and exploit corpora remain valuable, but they test parser robustness rather than proving containment.

### Deliberately not

I would not:

- Make the sandbox best-effort.
- Use Landlock alone and call the process sandboxed.
- Install confinement only after parsing the job or opening media.
- Rely on the current demuxer/filter set as a security boundary.
- Allow arbitrary socket creation merely because the old bridge used AF_UNIX.

## P3 — inadequate IPC semantics

### Mechanism

I would remove the private file IPC protocol rather than add three more messages to it.

The C engine should see a normal POSIX filesystem through FUSE. The adapter must implement at least:

- lookup/getattr/access and negative `ENOENT`
- open/create
- positioned read and write
- truncate and size
- flush/fsync/release
- atomic rename
- directory enumeration where applicable
- unlink and directory operations required by supported muxers

Errors must remain errors. In particular, a zero-length successful read means EOF, while a failed read returns an errno. If a backend returns both partial data and a non-EOF error, the adapter must return the data and retain a pending error at the resulting offset so the next relevant read observes it rather than seeing false EOF.

For HLS output, support should require the output backend to declare atomic rename capability. If it cannot, fail before starting the muxer. Rename atomicity cannot honestly be emulated over every `afero.Fs`.

Because `avio_check`, filters, codecs, and libc all see the same private filesystem, image-sequence probes and component-level file opens no longer cross different interception layers.

The C engine, Go library, and conformance host consequently stop sharing an application-specific wire protocol. The C engine consumes POSIX behavior; FUSE protocol negotiation is between the kernel and filesystem server.

### Guarantees

This provides:

- Deliverable read, write, seek, and metadata errors.
- Correct existence probing for image sequences.
- Rename semantics for HLS when the backing filesystem supports them.
- One mediation boundary for both libav URL access and direct component filesystem calls.
- No possibility that installing a default `io_open` callback accidentally exposes the host namespace.

It does not provide:

- Atomic rename when the underlying caller filesystem lacks it.
- Crash durability without meaningful `fsync` and directory-sync support from the backend.
- A transaction spanning an entire multi-file output.
- Identical behavior across all `afero.Fs` implementations. Backend capabilities must be explicit.

### Discriminating tests

**Read error:** Use a fault-injecting filesystem that returns valid bytes through a chosen offset and then `EIO`, with more valid media logically remaining afterward. Assert:

- The fault point was reached.
- The process exited normally with the exact I/O-failure exit code and structured error stage.
- No success JSON or committed output exists.

An implementation translating `EIO` to zero would falsely succeed or truncate and therefore fail this test.

**HLS rename:** Use a backend that records operations and rejects direct creation of the final playlist name. The only successful route is create `.tmp`, write it, and call rename. Assert the rename occurred exactly once, the final playlist exists, the temporary name does not, and every referenced segment exists.

**Image sequence:** Put differently colored numbered frames only in the caller filesystem and conflicting decoys on the host. Assert exact decoded pixel values and assert the adapter received lookup/open operations for all expected frame names. A host probe or an EOF-like false negative cannot satisfy both assertions.

**Filesystem conformance:** Run randomized operation traces against the adapter and a POSIX reference filesystem, comparing results and errno values. Include concurrent rename/open and injected failures, not only successful reads and writes.

### Deliberately not

I would not:

- Add only `ReadError`, `Rename`, and `Exists` to the current protocol. The next direct filesystem behavior would reopen the architectural gap.
- Treat short reads or backend errors as EOF.
- Install FFmpeg’s default `io_open` as a fallback.
- Claim atomicity based solely on the presence of a `Rename` method.

## P4 — fontconfig and adaptive demuxers

These are two separate product decisions and should not share one answer.

### Fontconfig: enable hermetically

Compile fontconfig, but never expose the system font configuration.

Ship a sealed configuration that scans only:

- A documented, versioned bundled font set.
- Caller-provided font assets under `/in/fonts`.
- Optionally, fonts embedded in the input container.

Use a per-job tmpfs cache for caller fonts and a prebuilt read-only cache for bundled fonts. Clear fontconfig-related environment variables and explicitly set the configuration path. Offer a strict subtitle mode that fails when a requested family cannot be resolved without substitution.

This guarantees deterministic resolution against the documented font universe. It does not guarantee that “Arial” is the same proprietary Arial used by the subtitle author; exact rendering still requires the actual font asset.

Costs include binary size, font scanning, cache handling, font licensing, deterministic bundle updates, and more native code to patch. Under load, bundled caches and hashing caller font sets are necessary to avoid rescanning everything for every frame.

A discriminating test should provide a font with a unique family name and visibly unique glyph, while installing a conflicting host decoy with the same family. An ASS file should name the family without a `fontfile`. Assert exact rendered pixels and that only the job font was opened. In strict mode, removing that font must cause a normal, specifically classified unresolved-font failure—not fallback rendering and not a crash.

I would not use `/etc/fonts`, host caches, inherited fontconfig environment variables, or promise exact typography from a name alone.

### HLS/DASH demuxers: do not enable in the baseline

I would keep these demuxers absent until local adaptive input is an explicit supported product requirement. Their absence is not a correctness defect for the currently stated operations; enabling them increases nested-resource parsing, encryption/key handling, path resolution, and fuzzing obligations.

The stable behavior should be an explicit “adaptive manifest input unsupported” result, documented in the engine’s capability inventory.

A discriminating absence test should submit a valid local manifest whose first segment is a trap file. It must exit normally with the exact unsupported-feature result, and the filesystem backend must report zero segment or key opens. A compile-time symbol check alone is not sufficient.

If local adaptive input later becomes worth supporting, the private filesystem and mandatory sandbox are prerequisites. The security promise should initially be job-scoped: a manifest may access any read-only input capability deliberately supplied to that job, but nothing outside it. Per-manifest-directory isolation would require a capability-preserving URL protocol or a separately sandboxed manifest-demux stage; pre-scanning and rewriting manifests would not be sufficient.

## Relationship between the four

P1 and P3 are the same architectural problem: interception was placed at `AVFormatContext`, while FFmpeg is actually a general filesystem client. They should be solved together by presenting the engine with a complete private filesystem rather than continually extending hooks and protocols.

P2 is related but not interchangeable. The virtual filesystem provides intended mediation; the OS sandbox contains implementation mistakes and native-code compromise. Both belong in the same runner, but they are separate security layers and should have separate tests.

P4 is feature policy built on top of those foundations:

- Hermetic fontconfig becomes reasonable once direct filesystem access is safely namespaced.
- HLS/DASH demuxing becomes containable, but containment alone does not make the additional feature surface worthwhile.

My priority order would therefore be:

1. Replace the bridge with the private POSIX filesystem.
2. Make the sandbox mandatory and fail closed.
3. Enable hermetic fontconfig with deterministic/strict semantics.
4. Leave HLS/DASH input unsupported until there is concrete demand.

That removes two recurring classes of defects instead of repairing individual manifestations, while deliberately declining the one expansion that currently lacks enough value to justify its maintenance and attack surface.
