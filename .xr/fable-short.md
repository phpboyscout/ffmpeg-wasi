# The other reviewer's load-bearing claims (condensed)

**C1 — libav reaches the filesystem through FOUR channels, and the engine's `afio_*` abstraction
sees one and a half:**
1. `AVFormatContext.pb` — covered.
2. `io_open`/`io_close2` child opens — covered ad hoc, per context, after the fact. Whack-a-mole.
3. The URL-protocol layer (`avio_check`, `avio_open` inside components, `ff_rename`) —
   STRUCTURALLY INVISIBLE to the abstraction.
4. Plain libc I/O inside components (`fopen` in lut3d/curves/libx264 stats, libass, fontconfig) —
   also invisible.
All eight measured path-option vectors live in channel 3 or 4. Two of the three protocol gaps are
both channel 3.

**C2 — the image2 sandbox escape had a specific cause:** probe (`avio_check`, channel 3, resolving
on the host) and open (`io_open`, channel 2, resolving over the bridge) DISAGREED ABOUT WHICH
FILESYSTEM EXISTS. Put them on one seam and the disagreement is impossible.

**C3 — the refactor is two layers.** Layer A: patch FFmpeg's `file` URL protocol on the native
build so `file_open/read/write/seek/close/check/move/delete` speak the bridge. This fixes channels
1–3 in one place, dissolves the image2 escape, gives `ff_rename` somewhere to land, and DELETES
code — `afio_install_muxer_io`, the concat in-memory playlist, the per-context teardown special
cases. The repo already carries a build patch, so patch-carrying is an established tool. Layer B:
an OS-enforced floor for channel 4, which no libav seam can see.

**C4 — P1 answer:** host-side staging into a per-job tempdir, driven by a hand-maintained
`(filter, option)` table. The table's incompleteness is covered by making it a BUILD GATE: every
filter in the `--capabilities` dump must appear in the table, or the build fails.

**C5 — highest leverage is Landlock on by default**, because it is the only mechanism that
converts every escape — including a memory-unsafe parse of hostile media, which needs no option
vector at all — into a loud exit 1. ~150 lines, no ABI change, no libav patch.

**C6 — fontconfig never.** It is ambient host state by design; it would make output differ between
host machines, breaking the documented "both targets produce identical results" property. libass
resolves font names against a `fontsdir` WITHOUT fontconfig, so the honest answer is to narrow the
promise: "supply a fontsdir".

**C7 — a harness-owned postcondition kills the swallowed-failure family:** on exit 0, every
declared output must RE-PROBE CLEAN UNDER AN INDEPENDENT ORACLE — stock ffprobe in the test image,
not the engine's own probe, which could share the bug. Truncation cannot survive a duration check
performed by different code.

**C8 — the two defect families are orthogonal** and should be separate programmes: escapes (below
the seam) and swallowed failures (error propagation).
