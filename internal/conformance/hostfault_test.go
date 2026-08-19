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
func runOverIPC(t *testing.T, a engine.Artifact, h *ipchost.Host, sock, in string) (int, string) {
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

	res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
		Run(ctx, string(job))
	if ctx.Err() != nil {
		t.Fatalf("%s: the job hung rather than failing; host errors: %v", a, h.Errors())
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	return res.ExitCode, res.Stderr
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

// TestATransportFailureIsNotAnEndOfStream asserts that a host disappearing
// mid-file fails the job. It was written for #15 and IT HAS NEVER BEEN OBSERVED
// RED -- run against the released engine, before any fix in this branch, it
// passes 6/6.
//
// That is recorded here on purpose. A passing test is evidence of a property
// holding, not evidence that the fault it names was ever present, and reading it
// the second way is precisely what let #12 hide for the life of the project. So
// either #15 was never real, or this check is looser than the fault it was
// written for, and nothing here distinguishes those.
//
// It is kept because the property is worth holding either way. Reopen #15 on a
// concrete host fault that yields truncated output and exit 0; until then, do not
// cite this test as the thing that fixed anything.
func TestATransportFailureIsNotAnEndOfStream(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// Control first: an honest host completes the job. Without this a
			// failure below could be the fixture rather than the fault.
			good, sock, name := hostFaultSetup(t, a)
			if code, stderr := runOverIPC(t, a, good, sock, name); code != 0 {
				t.Fatalf("%s: the control job failed over an honest host (exit %d) — "+
					"the fixture or the bridge is wrong before any fault is injected.\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			// Now the same job, with the host vanishing part-way through the file.
			bad, sock2, name2 := hostFaultSetup(t, a)
			bad.CloseAfterReads = 2

			code, stderr := runOverIPC(t, a, bad, sock2, name2)
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

			code, stderr := runOverIPC(t, a, h, sock, name)
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
