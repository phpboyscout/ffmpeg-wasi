package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// How the engine behaves when the HOST misbehaves — ffmpeg-wasi#15 and #24.
//
// These exist because of spec 0037 D4's second argument for writing our own IPC
// host: afmpeg's host is correct, so nothing in this estate could previously ask
// the engine what it does when a host is not. A host that overstates a read, or
// vanishes mid-file, is a case the reference implementation will never produce.
//
// A truncated *file* is deliberately not the test here. The ffmpeg CLI also
// exits 0 on a truncated Matroska, so the engine matching it is not a defect —
// checking that first is what stopped #15 being written against the wrong
// scenario. What is being asserted is narrower and defensible: a transport
// failure is not an end of stream.

// mp4Fixture builds a small real video in the workspace and returns its name.
// Matroska, because MP4 carried its own defect (#12) until recently and a
// fixture should not depend on a fix.
func mp4Fixture(t *testing.T, ws *engine.Workspace, a engine.Artifact) string {
	t.Helper()

	for i := range 25 {
		png, err := fixture.PNG(64, 48, i)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ws.Write(fmt.Sprintf("f_%02d.png", i), png); err != nil {
			t.Fatal(err)
		}
	}

	runJob(t, ws, a, map[string]any{
		"op": "process",
		"inputs": []any{map[string]any{
			"path": ws.Path("f_%02d.png"), "format": "image2",
			"options": map[string]any{"framerate": "25"},
		}},
		"outputs": []any{map[string]any{
			"path": ws.Path("clip.mkv"), "map": []any{"[v]"}, "video_codec": "libopenh264",
		}},
		"filter": "[0:v]null[v]",
	})

	return "clip.mkv"
}

// runOverIPC drives the native driver with a host that may be told to misbehave,
// and returns the engine's exit code.
func runOverIPC(t *testing.T, a engine.Artifact, h *ipchost.Host, sock, in string) (code int, stderr, signal string) {
	t.Helper()

	job, err := json.Marshal(map[string]any{
		"op":     "process",
		"inputs": []any{map[string]any{"path": in}},
		"outputs": []any{map[string]any{
			"path": "out.mkv", "map": []any{"[v]"}, "video_codec": "libopenh264",
		}},
		"filter": "[0:v]null[v]",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	// NOT runAndCheck: this helper's callers deliberately reason about the signal
	// themselves — #15's whole point is that being killed is a distinguishable
	// outcome here — so it is returned rather than made fatal.
	res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
		Run(ctx, string(job))
	if ctx.Err() != nil {
		t.Fatalf("%s: the job hung rather than failing; host errors: %v", a, h.Errors())
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	return res.ExitCode, res.Stderr, res.Signal
}

// hostFaultSetup builds the fixture once and serves it, returning the host and
// socket. The caller sets the fault fields before running the job.
func hostFaultSetup(t *testing.T, a engine.Artifact) (*ipchost.Host, string, string) {
	t.Helper()

	build := workspaceFor(t, a)
	name := mp4Fixture(t, build, a)

	body, err := build.Read(name)
	if err != nil {
		t.Fatalf("%s: reading the fixture: %v", a, err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
		t.Fatal(err)
	}

	h, sock, err := ipchost.Listen(root, t.TempDir())
	if err != nil {
		t.Fatalf("%s: starting the host: %v", a, err)
	}
	t.Cleanup(func() { _ = h.Close() })

	return h, sock, name
}

// TestATransportFailureIsNotAnEndOfStream is the regression test for #15 -- and
// the story of how it lied is worth more than the test.
//
// It asserted "the exit code is not zero". Against the released engine it passed
// 6/6, and #15 was recorded as NOT REPRODUCED on that evidence. Both were wrong.
//
// The engine was being KILLED BY SIGPIPE. A host that closes the connection makes
// the driver's next write() raise it, and the default disposition terminates the
// process; Go reports a signalled process as exit -1, which is not zero, so the
// check was satisfied by a corpse. Measured across three builds:
//
//	released          exit=-1  killed by signal: broken pipe
//	MSG_NOSIGNAL      exit=0   truncated output, reported as success  <- this is #15
//	+ read-error fix  exit=1   a failure the caller can read
//
// So the test now asserts the process EXITED, and then that it exited non-zero.
// Without the first half it cannot tell a reported failure from a dead process,
// which is the whole reason it was believed for as long as it was.
func TestATransportFailureIsNotAnEndOfStream(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// Control first: an honest host completes the job. Without this a
			// failure below could be the fixture rather than the fault.
			good, sock, name := hostFaultSetup(t, a)
			if code, stderr, _ := runOverIPC(t, a, good, sock, name); code != 0 {
				t.Fatalf("%s: the control job failed over an honest host (exit %d) — "+
					"the fixture or the bridge is wrong before any fault is injected.\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			// Now the same job, with the host vanishing part-way through the file.
			bad, sock2, name2 := hostFaultSetup(t, a)
			bad.CloseAfterReads = 2

			code, stderr, signal := runOverIPC(t, a, bad, sock2, name2)

			// The signal check comes FIRST, and is the point of this test. A
			// signalled process reports exit -1, which satisfies any "not zero"
			// check — which is how this test was believed for weeks.
			if signal != "" {
				t.Errorf("%s: the host disappeared mid-file and the engine was KILLED BY %s "+
					"rather than reporting a failure (ffmpeg-wasi#15).\nExit -1 from a corpse is "+
					"not a reported error, and a check spelled \"the exit code is not zero\" "+
					"cannot tell the difference.", a, signal)
			}
			if code == 0 {
				t.Errorf("%s: the host disappeared mid-file and the engine exited 0 "+
					"(ffmpeg-wasi#15).\nA transport failure is not an end of stream: the caller "+
					"gets a truncated file and is told it succeeded.\nstderr: %s",
					a, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestAnOverstatedReadIsRefused is the regression test for #24.
//
// The IPC reader used the count the host returned without checking it against
// the buffer it asked to fill, so a host claiming more than the buffer holds had
// the engine write past its end.
//
// The overstatement here deliberately exceeds any plausible AVIO buffer. That is
// not arbitrary: bounds-checking can only catch a count LARGER than the buffer.
// A host that overstates by a little, on a read near end-of-file where the reply
// is short anyway, produces a count that is still within the buffer — the engine
// cannot tell it is a lie and blocks on bytes that never arrive. That residual
// hang is a transport-timeout problem rather than a bounds problem, and it is
// tracked separately; do not weaken this test trying to cover it.
func TestAnOverstatedReadIsRefused(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			h, sock, name := hostFaultSetup(t, a)
			// Larger than the driver's 64 KiB AVIO buffer, so the claim exceeds the
			// buffer whatever the read size happens to be.
			h.OverstateReadBy = 1 << 20

			code, stderr, _ := runOverIPC(t, a, h, sock, name)
			if code == 0 {
				t.Errorf("%s: the host claimed more bytes than the buffer holds and the engine "+
					"exited 0 (ffmpeg-wasi#24).\nThe returned count must be checked against the "+
					"requested size before it is used.\nstderr: %s", a, strings.TrimSpace(stderr))
			}
		})
	}
}

// NOTE on ffmpeg-wasi#20 (frames swallowing read and decode errors).
//
// There is deliberately no test here, and the reason is worth recording so the
// next person does not write the one that fooled me.
//
// A first attempt injected a mid-file host failure and asserted a non-zero exit.
// It PASSED against the unfixed engine — but not because the engine reported the
// error. The job HUNG and the test read the kill signal as a non-zero exit. A
// sweep of fault positions showed the shape: failing early hangs the job (that
// is #31, no socket timeout), and failing late never fires at all because every
// read has already happened. There is no window in between.
//
// A corrupt payload does not reach it either: mangling a third of a Matroska
// file produces demuxer warnings and still extracts the same nine frames as the
// intact control.
//
// The underlying reason is a gap in the protocol rather than a gap in the test:
// a Read reply can express "here are N bytes" or "zero, meaning EOF". It has no
// way to say "I failed". A host that cannot serve a read can only lie or hang,
// so a genuine read failure cannot be delivered to the engine at all. Tracked
// separately.
//
// The fix in src/frames.c stands on reading rather than on a reproduction, and
// the commit says so.

// TestASmallOverstatementDoesNotHangTheEngine is the regression test for #31.
//
// #24's bounds check catches a host claiming MORE than the buffer holds. It
// cannot catch a smaller lie: near end-of-file a short reply is normal, so a
// count overstated by a few kilobytes is still inside the buffer, and the engine
// has no way to know it is waiting for bytes that will never arrive.
//
// Observed while writing #24's test: overstating by 4096 on a small file produced
// a sixty-second hang that ended only when the test's own deadline fired. The
// engine had no defence of its own — afmpeg imposes a deadline on the subprocess,
// but the engine must not depend on its caller for that.
//
// The timeout is set through AFMPEG_NATIVE_TIMEOUT rather than waiting out the
// 120-second default, which also exercises the override a host needs in order to
// tune it.
func TestASmallOverstatementDoesNotHangTheEngine(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			h, sock, name := hostFaultSetup(t, a)
			// Small enough to fit inside the AVIO buffer on a short read, so #24's
			// bounds check cannot fire and the engine genuinely blocks.
			h.OverstateReadBy = 4096

			job, err := json.Marshal(map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": name}},
				"outputs": []any{map[string]any{
					"path": "out.mkv", "map": []any{"[v]"}, "video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})
			if err != nil {
				t.Fatal(err)
			}

			// The engine's own deadline must be well inside the test's, so that a
			// pass means the ENGINE gave up rather than the harness killing it —
			// which is the mistake the note above records for #20.
			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()

			res, err := engine.Native{
				Path: a.Path,
				Env: []string{
					"AFMPEG_NATIVE_SOCKET=" + sock,
					"AFMPEG_NATIVE_TIMEOUT=2",
				},
			}.Run(ctx, string(job))

			if ctx.Err() != nil {
				t.Fatalf("%s: the engine blocked on a host that overstated a read by %d bytes "+
					"and never gave up (ffmpeg-wasi#31).\nIt was killed at %s by the test, not "+
					"stopped by its own timeout; host errors: %v", a, h.OverstateReadBy, jobTimeout, h.Errors())
			}
			if err != nil {
				t.Fatalf("%s: invoking: %v", a, err)
			}
			if res.ExitCode == 0 {
				t.Errorf("%s: the read timed out and the job still reported success "+
					"(ffmpeg-wasi#31 with #15).\nstderr: %s", a, strings.TrimSpace(res.Stderr))
			}
		})
	}
}

// TestAShortWriteAcknowledgementIsAFailure is the regression test for #45.
//
// #24 gave the Read reply an upper bound. The Write reply had no LOWER one, and
// libavformat does not resend what a short write left behind — flush_buffer
// calls the write callback once and records an error only on a negative return.
// So a host acknowledging fewer bytes than it was handed silently lost the tail
// of every buffer, and the job exited 0 with a truncated file.
//
// The host here writes everything and only lies about the count, which separates
// the question under test — does the engine act on the count — from whether the
// data happened to survive.
func TestAShortWriteAcknowledgementIsAFailure(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// Control: an honest host completes the job, so a failure below is the
			// short count and not the fixture.
			good, sock, name := hostFaultSetup(t, a)
			if code, stderr, _ := runOverIPC(t, a, good, sock, name); code != 0 {
				t.Fatalf("%s: the control job failed over an honest host (exit %d)\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			bad, sock2, name2 := hostFaultSetup(t, a)
			bad.UnderstateWriteBy = 512 // less than a buffer, so writes still mostly work

			code, stderr, signal := runOverIPC(t, a, bad, sock2, name2)
			if signal != "" {
				t.Fatalf("%s: the engine was killed by %s rather than reporting a failure", a, signal)
			}
			if code == 0 {
				t.Errorf("%s: the host acknowledged 512 bytes fewer than it was given on every "+
					"write, and the engine exited 0 (ffmpeg-wasi#45).\nlibav does not resend the "+
					"remainder, so those bytes are simply missing from the output — and the caller "+
					"is told the job succeeded.\nstderr: %s", a, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestAFailedImageWriteIsReported is the regression test for #46.
//
// afio_write_file returned 0 whatever happened after the open. avio_write is
// void and leaves failures in pb->error, which nobody read; avio_flush's outcome
// was dropped; the plain-file branch ignored fwrite and fclose too. So a bridge
// that dropped mid-write, or a full disk, produced an empty or truncated image
// and the `frames` reply still listed it as written.
//
// This is the write half of #20, which fixed `frames` swallowing READ errors.
func TestAFailedImageWriteIsReported(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			framesJob := func(h *ipchost.Host, sock, in string) (int, string) {
				job, err := json.Marshal(map[string]any{
					"op":     "frames",
					"inputs": []any{map[string]any{"path": in}},
					"select": map[string]any{"timestamps": []any{0.1, 0.3, 0.5}},
					"path":   "shot_%02d.png",
					"codec":  "png",
				})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
				defer cancel()
				res := runAndCheck(t, engine.Native{Path: a.Path,
					Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}, ctx, string(job), "the frames job")
				return res.ExitCode, res.Stderr
			}

			// Control: an honest host, so a failure below is the fault and not the
			// job spec being wrong for this build.
			good, sock, name := hostFaultSetup(t, a)
			if code, stderr := framesJob(good, sock, name); code != 0 {
				t.Skipf("%s: the control frames job did not run (exit %d) — this build or "+
					"spec cannot exercise the write path.\n%s", a, code, strings.TrimSpace(stderr))
			}

			bad, sock2, name2 := hostFaultSetup(t, a)
			bad.UnderstateWriteBy = 64

			code, stderr := framesJob(bad, sock2, name2)
			if code == 0 {
				t.Errorf("%s: every image write was acknowledged short and the job still "+
					"reported success (ffmpeg-wasi#46).\nThe reply lists images the caller "+
					"cannot rely on.\nstderr: %s", a, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestAReportedReadFailureIsNotAnEndOfStream is the regression test
// ffmpeg-wasi#20 could never have, and afmpeg spec 0041 D2 is what made it
// possible.
//
// Under protocol v1 a Read reply was a count where zero meant end of file, with
// no third answer. A host that could not serve a read could only lie — answer
// zero, which the engine turns into AVERROR_EOF — or hang. So the fault could
// not be delivered, and #20's fix shipped with a note saying exactly that.
//
// v2 makes the count signed. This test injects a host that stays connected and
// reports the failure, which is the case that could not previously exist.
//
// # Why this is not a duplicate of the #15 test above
//
// #15 is the transport DYING mid-file: the connection vanishes and the engine
// has to tell that apart from an end of stream. This is the transport STAYING UP
// and the host saying it cannot serve the read. They are different failures with
// different fixes, and v1 collapsed both into "the count was zero".
//
// # Why this asserts the DIAGNOSIS and not the exit code
//
// Because the exit code cannot discriminate, and assuming it could would have
// shipped a test that passes either way. Built against an engine that ignores the
// signed count entirely, this job still exits 1 — the negative count reads back
// as 0xFFFFFFFF, which #24's over-long-count guard refuses, so the job fails for
// a reason that has nothing to do with what the host said.
//
// Measured, same fixture, same injected fault:
//
//	engine handling the failure form   exit 1, "the host could not serve a read"
//	engine ignoring it                 exit 1, "Input/output error" only
//
// So the value D2 adds here is not that the job fails — it already did — but that
// the failure is ATTRIBUTABLE. A host fault and a malformed host are different
// problems for whoever has to fix one, and reporting them identically is how the
// wrong one gets investigated.
func TestAReportedReadFailureIsNotAnEndOfStream(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// Control: the same job over an honest host must succeed, or the failure
			// below could be the fixture rather than the injected fault.
			good, sock, name := hostFaultSetup(t, a)
			if code, stderr, _ := runOverIPC(t, a, good, sock, name); code != 0 {
				t.Fatalf("%s: the control job failed over an honest host (exit %d) — "+
					"the fixture or the bridge is wrong before any fault is injected.\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			bad, sock2, name2 := hostFaultSetup(t, a)
			bad.FailReadsAfter = 2

			code, stderr, signal := runOverIPC(t, a, bad, sock2, name2)

			// The signal check is first for the same reason as #15's: a signalled
			// process reports exit -1, which satisfies any "not zero" check.
			if signal != "" {
				t.Errorf("%s: the host reported a read failure and the engine was KILLED BY %s "+
					"rather than reporting it (ffmpeg-wasi#20).", a, signal)
			}
			if code == 0 {
				t.Errorf("%s: the host reported a read failure and the engine exited 0 "+
					"(ffmpeg-wasi#20).\nThe caller gets a truncated file and is told it "+
					"succeeded — which is the whole defect, and until protocol v2 there was "+
					"no way to provoke it.\nstderr: %s", a, strings.TrimSpace(stderr))
			}

			// The discriminating half. Without it this test passes against an engine
			// that never looks at the sign, because #24's bounds check refuses the
			// count for an unrelated reason and the job fails anyway.
			if !strings.Contains(stderr, "could not serve a read") {
				t.Errorf("%s: the job failed, but not for the reason the host gave "+
					"(ffmpeg-wasi#20).\nA reported read failure and a malformed count are "+
					"different problems, and an engine that ignores the signed count reports "+
					"them identically.\nstderr: %s", a, strings.TrimSpace(stderr))
			}
		})
	}
}
