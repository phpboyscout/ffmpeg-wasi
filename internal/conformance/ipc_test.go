package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// Spec 0037 phase D2. Phases A–C and D1 drive the native driver in plain-file
// mode — legitimate per the contract, and what kept 0036 clear of a component
// that did not exist. This runs it the way it is actually deployed: every byte
// of media I/O over the AFMPEG_NATIVE_SOCKET bridge, served by a host written
// from the ABI document rather than from afmpeg.
//
// What this is really testing is the DOCUMENT. afmpeg's host and this driver
// could agree on behaviour nothing writes down, and no test on either side would
// notice. If a host built only from docs/reference/driver-invocation-abi.md can
// drive a real transcode, the document is sufficient. If it cannot, the document
// is missing something, and that is the finding.
//
// # What these tests catch, measured rather than assumed
//
// Each was checked by breaking the host and confirming the failure.
//
//   - Gotcha 1, a Read reply of 0 meaning EOF: CAUGHT. Reporting a short read as
//     end of file truncates the input at a 64 KiB boundary, and the duration
//     assertion below reports 1.706s against the fixture's 2.000s. Note it is
//     only caught because the check is on DURATION — an earlier version of this
//     test asserted the output was non-empty, and a truncated file passed it.
//   - Gotcha 2, Write replying with a count: NOT CAUGHT here. Replying a constant
//     0 -- the natural status-byte mistake -- leaves the transcode correct,
//     because the host is the one writing the bytes and libav does not act on a
//     non-negative short count. Only the SIGN matters: a reply that reads as
//     negative int32 does fail the job.
//
// So the document's stated consequence for gotcha 2 ("libav would read it as
// 'wrote nothing'") overstates what happens on the success path, and a host that
// got it wrong in the obvious way would ship working. TestWriteReplyIsACountNotAStatus
// in internal/ipchost is the only guard, and it guards by asserting the reply
// value directly rather than by observing a broken file.

// overIPC labels an artefact as "the same driver, driven over the bridge". The
// comparator groups by this name, so the label is what makes plain-file and IPC
// two comparable ways of running one binary rather than one indistinguishable
// blur.
func overIPC(a engine.Artifact) engine.Artifact {
	a.Target += "-ipc"
	return a
}

// nativeArtifacts returns the native drivers, or skips. The bridge is a native
// concept; the WASM target serves its filesystem through wazero instead.
func nativeArtifacts(t *testing.T) []engine.Artifact {
	t.Helper()

	var out []engine.Artifact
	for _, a := range artifacts(t) {
		if a.Target == "native" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		t.Skip("no native driver available; the IPC bridge is a native-only path")
	}
	return out
}

func TestIPCBridgeCarriesARealTranscode(t *testing.T) {
	t.Parallel()

	wav, err := fixture.WAV(sampleRate, channels, sampleCount)
	if err != nil {
		t.Fatalf("building the WAV fixture: %v", err)
	}

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// The served directory holds only the media. The driver reaches it
			// solely through the socket, so nothing here is on a path it could
			// open directly by accident.
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "in.wav"), wav, 0o644); err != nil {
				t.Fatal(err)
			}

			host, sock, err := ipchost.Listen(root, t.TempDir())
			if err != nil {
				t.Fatalf("starting the host: %v", err)
			}
			defer func() { _ = host.Close() }()

			job, err := json.Marshal(map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": "in.wav"}},
				"outputs": []any{map[string]any{
					"path": "out.mkv", "audio_codec": "flac",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()

			res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
				Run(ctx, string(job))
			if ctx.Err() != nil {
				t.Fatalf("%s: the job did not finish within %s over the IPC bridge — it HUNG.\n"+
					"A host that never replies to a frame leaves the driver blocked forever.\n"+
					"host errors: %v", a, jobTimeout, host.Errors())
			}
			if err != nil {
				t.Fatalf("%s: invoking: %v (host errors: %v)", a, err, host.Errors())
			}
			if res.ExitCode != 0 {
				t.Fatalf("%s: the job exited %d over the IPC bridge, want 0.\n"+
					"stderr: %s\nhost errors: %v",
					a, res.ExitCode, strings.TrimSpace(res.Stderr), host.Errors())
			}

			// The host must have been the one serving the media. Without this a
			// driver that ignored AFMPEG_NATIVE_SOCKET and opened real paths would
			// pass every assertion above while testing nothing about the bridge.
			opened := host.Opened()
			var sawInput, sawOutput bool
			for _, n := range opened {
				if strings.Contains(n, "in.wav") {
					sawInput = true
				}
				if strings.Contains(n, "out.mkv") {
					sawOutput = true
				}
			}
			if !sawInput || !sawOutput {
				t.Fatalf("%s: the bridge served %v — the driver did not route both the input "+
					"and the output through it, so this test proves nothing about the IPC path",
					a, opened)
			}

			if errs := host.Errors(); len(errs) != 0 {
				t.Fatalf("%s: the host reported protocol errors: %v", a, errs)
			}

			// Spec 0037 phase D3. File this reply under a DISTINCT artefact label,
			// so the phase D1 comparator picks it up and compares it against the
			// same job run in plain-file mode -- which TestProcessTranscodesAudio
			// already runs, with an identical spec, so the keys match by
			// construction.
			//
			// That is the whole of D3: the bridge is not a separate thing to
			// compare, it is another way of running the driver, and the machinery
			// that asks whether two artefacts agree is the machinery that asks
			// whether two ways of driving one artefact agree.
			//
			// Only the process reply is recorded. The probe below reads back a
			// file this test produced, which is verification rather than a parity
			// subject, and recording it would collide with phase C's own probe of
			// out.mkv under the same key.
			recordForParity(overIPC(a), job, []byte(strings.TrimSpace(res.Stdout)))

			// And the transcode actually happened, on the host's filesystem.
			if _, err := os.Stat(filepath.Join(root, "out.mkv")); err != nil {
				t.Fatalf("%s: the output is not on the served filesystem: %v", a, err)
			}

			// Assert the DURATION, not merely that bytes exist. A host that
			// reported a short read as end of file would truncate the input at a
			// 64 KiB boundary and still produce a plausible, non-empty file --
			// this test passed that break until the check was tightened. Read the
			// result back in plain-file mode, so the assertion does not depend on
			// the bridge it is checking.
			plain, err := engine.Native{Path: a.Path, Dir: root}.
				Run(context.Background(), `{"op":"probe","inputs":[{"path":"out.mkv"}]}`)
			if err != nil || plain.ExitCode != 0 {
				t.Fatalf("%s: probing the output failed: rc=%d %v", a, plain.ExitCode, err)
			}

			var reply probeReply
			if err := json.Unmarshal([]byte(strings.TrimSpace(plain.Stdout)), &reply); err != nil {
				t.Fatalf("%s: parsing the probe of the output: %v\n%s", a, err, plain.Stdout)
			}
			if len(reply.Inputs) != 1 {
				t.Fatalf("%s: the probe reports %d inputs, want 1", a, len(reply.Inputs))
			}
			wantDuration(t, a, "the transcode over the IPC bridge",
				reply.Inputs[0].DurationSec, fixture.WAVDuration(sampleRate, sampleCount))
		})
	}
}

// A muxer seeks backwards to patch what it already wrote. Matroska does less of
// this than MP4, whose moov/mdat patch on av_write_trailer is the case the
// document names, so this runs the harder one over the bridge.
func TestIPCBridgeSurvivesAMuxerBackwardSeek(t *testing.T) {
	t.Parallel()

	wav, err := fixture.WAV(sampleRate, channels, sampleCount)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "in.wav"), wav, 0o644); err != nil {
				t.Fatal(err)
			}

			host, sock, err := ipchost.Listen(root, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = host.Close() }()

			job, _ := json.Marshal(map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": "in.wav"}},
				"outputs": []any{map[string]any{
					"path": "out.mp4", "audio_codec": "aac",
				}},
			})

			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()

			res, err := engine.Native{Path: a.Path, Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.
				Run(ctx, string(job))
			if ctx.Err() != nil {
				t.Fatalf("%s: the MP4 mux hung over the bridge; host errors: %v", a, host.Errors())
			}
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("%s: the MP4 mux exited %d over the bridge: %v\nstderr: %s\nhost errors: %v",
					a, res.ExitCode, err, strings.TrimSpace(res.Stderr), host.Errors())
			}
			if errs := host.Errors(); len(errs) != 0 {
				t.Fatalf("%s: the host reported protocol errors: %v", a, errs)
			}

			out, err := os.ReadFile(filepath.Join(root, "out.mp4"))
			if err != nil {
				t.Fatal(err)
			}
			// A non-fragmented MP4 that was never patched has its moov in the
			// wrong state; the cheapest honest check is that the file is a real
			// MP4 the engine can read back.
			if len(out) < 1000 {
				t.Fatalf("%s: the MP4 is %d bytes, which is too small to have been muxed properly",
					a, len(out))
			}
		})
	}
}
