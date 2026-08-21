# Independent architectural review — ffmpeg-wasi native confinement (REVIEWER B)

## 1. Is the architecture sound?

**Mostly yes as an engine; no as a confinement design.**

The engine itself — one JSON entry point, four ops, three explicit lanes (graph / copy / subtitle) in a single `Ctx`, exit-code vocabulary, `--capabilities` by iteration rather than probing — is a good shape. The 2,050 lines of process.c are dense but they are dense with *libav's* irreducible ceremony, not accidental complexity. I would not restructure the processing code.

The confinement seam is the unsound part, and the defects were **not** incidental. `afio_*` is a *call-site convention*. libav reaches the filesystem through at least four channels, and the seam sees only one and a half:

1. **`AVFormatContext.pb`** — covered.
2. **`io_open`/`io_close2` child opens** — covered ad hoc, per context, after the fact — whack-a-mole; #14 was exactly a mole.
3. **The URL-protocol layer** (`avio_check`, `avio_open` inside components, `ff_rename`/`avpriv_io_move`) — *structurally invisible* to the seam. P3's image2 probe escape and the HLS `.tmp`-rename gap both live here. Two of P3's three gaps are the same channel.
4. **Plain libc I/O inside components** — `fopen` in lut3d/curves/libx264 stats, libass reading files and scanning `fontsdir`, fontconfig's ambient `/etc/fonts` — *also invisible*. All eight of P1's vectors live here or in channel 3.

On wasm, confinement is sound because the seam is the **process boundary**. On native, the design placed the seam at "our call sites" and hoped the traffic below it didn't exist. A property that must be re-earned by vigilance on every upgrade is not a property; it is a trend line, and the 37-defect week is what the trend line looks like.

The 37 defects are **two orthogonal families**, and no single seam fixes both: **escapes** (I/O below the seam) and **swallowed failures** (a discarded return turning failure into exit 0). The second family has nothing to do with where the I/O seam sits.

## 2. The refactor

**Layer A — move the functional seam from per-context AVIO hooks down to libavformat's URL-protocol layer.** On the native build, patch the `file` protocol (the project already carries `build/ffmpeg-concat-ioopen.patch`, so this is an established tool) so that when the bridge is active, `file_open/read/write/seek/close/check/move/delete` speak the bridge.

- `avio_check` (image2 pattern expansion) resolves against the caller's fs — P3's third gap fixed *without* the measured sandbox escape, because that escape came precisely from probe (channel 3, host) and open (channel 2, bridge) disagreeing about which filesystem exists. Put them on one seam and they cannot disagree.
- `ff_rename` maps to a bridge Rename frame — P3's second gap.
- `afio_open_input/output` collapse to plain libav calls; `afio_install_muxer_io`, the concat in-memory-playlist machinery, and the per-context teardown special cases all become unnecessary. nativeio.c gets *smaller* and stops being a list of remembered exceptions.

It does **not** prevent channel 4. Hence **Layer B — an OS-enforced floor (Landlock)**, so anything below Layer A fails loudly. Confinement stops being emergent the moment there is a mechanism that does not depend on the C being correct.

Cost: one carried patch (~200 lines, against a stable URLProtocol interface); protocol v2 across three hosts; Landlock is Linux-only (native is already linux-only).

## 3. Greenfield, P1–P4

### P1 — host-side staging driven by an explicit classification table, with the sandbox as the failure mode for what the table misses.

The eight vectors are channel-4 traffic, so neither the bridge nor any libav seam can serve them. The *host* (afmpeg's Go side, which owns the afero.Fs) maintains `(filter, option) → {read | write | read-dir}`. Before dispatch it stages reads into a per-job private tempdir (0700, `O_EXCL`), rewrites the option values, runs the job, harvests writes back, deletes the tempdir. Landlock grants exactly that tempdir.

- **Does not guarantee** the table is complete — that residual is covered by the sandbox: a missed vector becomes EACCES → exit 1, not a silent host read.
- **Cost** is bounded and *gateable*: add a conformance rule that **every filter in the `--capabilities` dump must appear in the classification table** (with path options, or an explicit "none"). A newly enabled filter nobody classified fails the build.
- **Tests:** content, not exit codes. A 3D LUT mapping everything to solid green → assert output pixels are green. Then the differential: plant the **same path on host disk with different content** (host LUT = solid red) and assert the output is green. A corpse, a segfault, or wrong staging all fail it.

### P2 — Landlock, on by default, with a detectable downgrade.

Install after argv parsing (libraries already loaded); deny all fs except the staging tempdir and `/dev/urandom`; `no_new_privs`; AF_UNIX still permitted. Report `"sandbox":"landlock"|"none"` in `--capabilities` and the result JSON so hosts and CI can *assert* it. `AFMPEG_NO_SANDBOX=1` opts out loudly.

- **Tests:** a paired canary — a job whose only difference is an unstaged host-file access must **fail with the sandbox on and succeed with it off**. The pair proves causation. Assert `WIFEXITED && code==1` explicitly, never "non-zero". False-positive direction: a Matroska mux job (needs `/dev/urandom`) must still pass sandboxed.

### P3 — protocol v2, hard cut.

Read reply becomes signed: `n>0` data, `0` EOF, `n<0` an AVERROR code. `'M'` rename → status. `'E'` exists/stat → status + size. Engine and Go host ship in lockstep inside afmpeg releases and the test host is in-repo, so a compatibility window is ceremony without a beneficiary. Does **not** add directory listing (libass `fontsdir` stays channel 4 → P1 staging).

- **Tests:** *Exists:* an image2 sequence existing only in the caller fs (5 files) with a **different count** (8) planted at the same host paths; assert exactly 5 frames decode. Discriminates bridge-probe from host-probe by an integer that cannot coincide.

### P4 — fontconfig out permanently; hls/dash not until the refactor lands.

Fontconfig's entire design is ambient host state. It is unserveable over a caller fs, it would make output differ between host machines (breaking the both-targets-identical property), and it is channel-4 traffic with no path in the spec to stage. libass resolves font *names* against a `fontsdir` without fontconfig, so the answer is "supply `fontsdir` in your fs" — staged by P1, deterministic, identical on both targets.

hls/dash: after Layer A + Landlock, an absolute path in a manifest resolves inside the caller's own filesystem or dies at the kernel — the security objection dissolves into a product question.

## 4. Which are the same problem?

P1, P3, and the enforcement half of P2 are one problem: **filesystem traffic below the seam the design assumed was total**. P4 is not a separate problem — it is the *policy file* for two components generating that same traffic. Solve together, in order: Landlock floor → Layer-A seam + IPC v2 → P1 staging → P4 policy. The swallowed-failure family is genuinely different and should not be welded to this programme.

## 5. Making the defect class visible

1. **A must-check wrapper header + `-Werror=unused-result` + a banned-symbol lint.** Wrap the sneaky API shapes at the same seam: `avio_write` is void and parks failure in `pb->error` — its wrapper checks `pb->error` and returns it, converting "error parked in a field nobody reads" into a return value the compiler guards.
2. **Harness-owned postconditions, so a non-discriminating test cannot be written.** Move predicates into the runner as invariants on *every* job: `WIFEXITED` always; the exit code equals the *expected specific* code, never a sign test; stdout exactly one line of JSON or empty; and on exit 0, every declared output **re-probes clean under an independent oracle** — stock ffprobe, not the engine's own probe, which could share the bug. That last one is the structural answer to the entire "exit 0 over truncated output" family: truncation cannot survive a duration check performed by different code.
3. **Fault injection as a first-class test-host feature.**

## 6. Single highest-leverage change

**Landlock on by default, with the sandbox state reported in `--capabilities` and a paired escape-canary test in CI.** The protocol-seam refactor is the most *elegant* change but fixes only channels 2–3 and needs a coordinated protocol rev; the wrapper discipline is the best defect-*prevention* per line but most of that family's instances are already fixed and it protects code paths, not the promise. Landlock is ~150 lines, no ABI change, no libav patch, and it is the only candidate that converts every member of the escape class — including a memory-unsafe parse of hostile media — from a silent wrong into a loud exit 1.
