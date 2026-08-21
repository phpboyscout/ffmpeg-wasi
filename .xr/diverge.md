# Where the two reviews DIVERGE

You were both given the same brief and the same code, independently. You agreed on more than
expected: the engine split is sound, `afio_*` is a transport abstraction being asked to be a
security boundary, the escapes were predictable rather than incidental, the 37 defects are two
orthogonal families, an OS-enforced boundary is required, a build-enforced checked-result
discipline is the answer to the swallowed-failure family, and tests must assert specific exit
codes and content rather than "non-zero".

These four points are where you did not agree. For EACH one:

  - state which position you now hold, and whether reading the other changed it
  - if you still disagree, say precisely what evidence or measurement would settle it
  - if a compromise exists that is genuinely better than either, say so — but do not manufacture
    one to be agreeable. "No compromise; A is right and here is why B fails" is a fine answer.

## D1 — the P1 mechanism

**A (codex):** a private mount namespace with FUSE over the caller's filesystem. Absolute host
paths simply do not exist, so no classification table is needed at all. Costs FUSE and
mount-namespace lock-in, a real filesystem adapter, per-I/O context switches.

**B (fable):** host-side staging into a per-job private tempdir, driven by a hand-maintained
`(filter, option)` classification table, gated so that any filter appearing in `--capabilities`
but missing from the table fails the build. The sandbox covers what the table misses.

Note: a separate review of the existing draft spec found that a table-based approach CANNOT fail
closed on its own, because FFmpeg exposes no "this option is a path" type — an unclassified path
option is indistinguishable from any other string option. B's build-gate is a response to that;
A removes the problem entirely.

## D2 — the single highest-leverage change

**A (codex):** build-enforced checked results for every fallible libav call — it attacks the
dominant defect family (failure becoming success) across every op, and is cheaper and more
portable than a sandbox.

**B (fable):** Landlock on by default — it is the only mechanism that converts every member of
the escape class, including a memory-unsafe parse of hostile media, into a loud failure; ~150
lines, no ABI change, shippable immediately.

## D3 — fontconfig

**A (codex):** enable it HERMETICALLY — sealed configuration scanning only a bundled font set and
caller fonts, per-job tmpfs cache, cleared environment, and a strict mode that fails when a family
cannot be resolved without substitution.

**B (fable):** never enable it. Narrow the promise instead: libass resolves font names against a
`fontsdir` without fontconfig, so the answer is "supply a fontsdir", which is deterministic and
identical on both targets. Fontconfig is ambient host state by design and would make output differ
between machines.

## D4 — where the functional seam belongs

**A (codex):** pass an already-open descriptor to a driver started inside an OS sandbox; the
engine's internal seam becomes a "job runtime capability". Does not propose patching FFmpeg.

**B (fable):** patch FFmpeg's `file` URL protocol on the native build so probe and open share one
seam — arguing the image2 escape existed precisely because `avio_check` (host) and `io_open`
(bridge) disagreed about which filesystem exists, and that one seam makes that disagreement
impossible. Notes the repo already carries a build patch.
