package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/ipchost"
)

// The no-host-disk guarantee, asserted as a negative — ffmpeg-wasi#14.
//
// docs/reference/driver-invocation-abi.md states that with AFMPEG_NATIVE_SOCKET
// set, the driver touches no host disk. Every other test in this package checks
// that the right bytes came OUT of the bridge. None of them could notice bytes
// going somewhere else as well, because a file written to the wrong place is
// invisible to an assertion that only looks in the right one.
//
// So this one looks in the wrong place. The driver is started with its working
// directory somewhere the host does not serve, and afterwards that directory must
// be empty.
//
// # Why a multi-file muxer
//
// A muxer writing ONE file writes it through the pb the engine installed, which
// has always been wrapped. A muxer writing MANY — image2 with a numbered pattern,
// hls, dash, segment — opens each child through AVFormatContext.io_open instead,
// which the pb wrapping never saw. Every frame of an image sequence was landing
// on host disk, and the caller, whose filesystem is in memory, got nothing back
// and no error.
//
// image2 is the case tested because it is the ordinary one — extracting frames is
// not an exotic job — and because it needs nothing beyond the fix. hls is left
// out deliberately: its playlist is written to a .tmp and renamed, the protocol
// has no rename, and that gap is tracked as #35. A test written against hls today
// would be asserting a bug that belongs to a different ticket.

// TestAMultiFileOutputStaysInsideTheBridge is the regression test for #14.
func TestAMultiFileOutputStaysInsideTheBridge(t *testing.T) {
	t.Parallel()

	const frames = 12

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			served := t.TempDir() // what the host exposes
			offLimits := t.TempDir()

			// A real clip, built without the bridge so the fixture does not depend
			// on the thing under test.
			for i := range frames {
				png, err := fixture.PNG(64, 48, i)
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("f_%02d.png", i)
				if err := os.WriteFile(filepath.Join(served, name), png, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			runIn(t, a, offLimits, nil, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": filepath.Join(served, "f_%02d.png"), "format": "image2",
					"options": map[string]any{"framerate": "12"},
				}},
				"outputs": []any{map[string]any{
					"path": filepath.Join(served, "clip.mkv"), "map": []any{"[v]"},
					"video_codec": "libopenh264",
				}},
				"filter": "[0:v]null[v]",
			})
			// The fixture build ran without a socket, so it legitimately wrote to
			// paths it was given. Clear the slate before the assertion.
			mustBeEmptyAfterClearing(t, offLimits)

			h, sock, err := ipchost.Listen(served, t.TempDir())
			if err != nil {
				t.Fatalf("%s: starting the host: %v", a, err)
			}
			t.Cleanup(func() { _ = h.Close() })

			// The same driver, now bridged, writing a numbered sequence. Relative
			// paths, so anything not routed lands in the working directory.
			code, stderr := runIn(t, a, offLimits, []string{"AFMPEG_NATIVE_SOCKET=" + sock},
				map[string]any{
					"op":     "process",
					"inputs": []any{map[string]any{"path": "clip.mkv"}},
					"outputs": []any{map[string]any{
						"path": "shot_%03d.png", "map": []any{"[v]"}, "video_codec": "png",
					}},
					"filter": "[0:v]null[v]",
				})
			if code != 0 {
				t.Fatalf("%s: the sequence job failed over the bridge (exit %d)\n%s",
					a, code, strings.TrimSpace(stderr))
			}

			// THE ASSERTION: nothing outside the bridge.
			stray, err := os.ReadDir(offLimits)
			if err != nil {
				t.Fatal(err)
			}
			if len(stray) > 0 {
				names := make([]string, len(stray))
				for i, e := range stray {
					names[i] = e.Name()
				}
				t.Errorf("%s: %d file(s) written to host disk with the bridge active "+
					"(ffmpeg-wasi#14): %s\n"+
					"driver-invocation-abi.md promises the driver touches no host disk when "+
					"AFMPEG_NATIVE_SOCKET is set. A multi-file muxer opens each child through "+
					"io_open rather than the wrapped pb, so a caller whose filesystem is in "+
					"memory receives nothing and is told the job succeeded.",
					a, len(stray), strings.Join(names, ", "))
			}

			// HLS is the other multi-file muxer #14 named, and an engine that
			// special-cased image2 would pass everything above while its segments
			// still escaped. Its CONTAINMENT is asserted here; its usability is not,
			// because the playlist is written to a .tmp and renamed and the protocol
			// has no rename — that gap is #35, and a test demanding a working
			// playlist today would be failing for that instead of this.
			if hasMuxer(t, a, "hls") {
				mustBeEmptyAfterClearing(t, offLimits)
				runIn(t, a, offLimits, []string{"AFMPEG_NATIVE_SOCKET=" + sock},
					map[string]any{
						"op":     "process",
						"inputs": []any{map[string]any{"path": "clip.mkv"}},
						"outputs": []any{map[string]any{
							"path": "stream.m3u8", "map": []any{"[v]"}, "format": "hls",
							"video_codec": "libopenh264",
							"format_options": map[string]any{
								"hls_time": "1", "hls_segment_filename": "seg_%03d.ts",
								"hls_list_size": "0",
							},
						}},
						"filter": "[0:v]null[v]",
					})
				if stray, err := os.ReadDir(offLimits); err == nil && len(stray) > 0 {
					names := make([]string, len(stray))
					for i, e := range stray {
						names[i] = e.Name()
					}
					t.Errorf("%s: an HLS output put %d file(s) on host disk with the bridge "+
						"active (ffmpeg-wasi#14): %s", a, len(stray), strings.Join(names, ", "))
				}
			}

			// And the positive half: the frames really did arrive through the host.
			// Without this, a fix that simply stopped writing anything would pass.
			got, err := filepath.Glob(filepath.Join(served, "shot_*.png"))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != frames {
				t.Errorf("%s: the served filesystem holds %d frames, want %d — the output must "+
					"arrive over the bridge, not merely stay off host disk", a, len(got), frames)
			}
		})
	}
}

// runIn invokes an artifact with a chosen working directory and returns its
// result without judging it.
func runIn(t *testing.T, a engine.Artifact, dir string, env []string, spec any) (int, string) {
	t.Helper()

	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	res, err := engine.Native{Path: a.Path, Dir: dir, Env: env}.Run(ctx, string(body))
	if ctx.Err() != nil {
		t.Fatalf("%s: the job hung", a)
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	return res.ExitCode, res.Stderr
}

// mustBeEmptyAfterClearing empties dir, so a later "nothing here" assertion is
// about the run under test rather than about setup.
func mustBeEmptyAfterClearing(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
}

// hasMuxer reports whether an artifact carries a muxer by name, so a test can
// cover it where present and stay quiet where the profile leaves it out.
func hasMuxer(t *testing.T, a engine.Artifact, name string) bool {
	t.Helper()
	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	return slices.Contains(caps.Muxers, name)
}

// TestAnImageSequenceCannotReachHostDisk is the regression test for the mistake
// I made trying to fix #36 — and it is why that ticket is still open.
//
// #36 is that a numbered image sequence cannot be READ over the bridge: a pattern
// is not a file, so handing `f_%02d.png` to the host as a single name fails. The
// obvious fix is the one #14 used on the muxer side — give the context io_open
// and let the demuxer ask for each frame by name.
//
// THAT FIX IS A SANDBOX ESCAPE. The image2 demuxer does not only OPEN files; it
// PROBES for the sequence first, with avio_check, and that goes through the URL
// protocol layer where io_open has no say. So the demuxer looks for the pattern
// on the real filesystem, and where the frames happen to exist there it reads
// them. Measured, host serving an empty directory with the frames in the driver's
// working directory:
//
//	before the change   contained (could not open input)
//	after the change    READ HOST DISK — duration 0.4
//
// So this asserts the CONTAINMENT that currently holds, not the capability that
// does not. A future attempt at #36 has to keep this green, which the io_open
// route cannot.
func TestAnImageSequenceCannotReachHostDisk(t *testing.T) {
	t.Parallel()

	for _, a := range nativeArtifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// The host serves an EMPTY directory. The frames exist ONLY on host
			// disk, in the directory the driver runs in.
			served := t.TempDir()
			cwd := t.TempDir()
			for i := range 8 {
				png, err := fixture.PNG(64, 48, i)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, fmt.Sprintf("f_%02d.png", i)), png, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			h, sock, err := ipchost.Listen(served, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = h.Close() })

			// A PROBE, not a process job. The escaping build fails a process job
			// anyway, at the filter stage, for a reason unrelated to the input —
			// so "the job failed" would have read as containment while the demuxer
			// was happily reading host disk. Probe reports what it managed to open
			// and nothing else, which is exactly the question.
			job, err := json.Marshal(map[string]any{
				"op": "probe",
				"inputs": []any{map[string]any{
					"path": "f_%02d.png", "format": "image2",
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
			defer cancel()
			res, err := engine.Native{Path: a.Path, Dir: cwd,
				Env: []string{"AFMPEG_NATIVE_SOCKET=" + sock}}.Run(ctx, string(job))
			if err != nil {
				t.Fatalf("%s: invoking: %v", a, err)
			}

			var reply probeReply
			if res.ExitCode == 0 {
				if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &reply); err != nil {
					t.Fatalf("%s: parsing the probe reply: %v\n%s", a, err, res.Stdout)
				}
			}
			if len(reply.Inputs) > 0 && reply.Inputs[0].Error == "" {
				t.Errorf("%s: an image sequence the host does not serve was READ ANYWAY "+
					"(ffmpeg-wasi#36).\nThe host served an empty directory; the only copies of "+
					"those frames are on host disk in the driver's working directory, and the "+
					"probe reports %.3fs of media — so the demuxer went round the bridge to "+
					"find them.", a, reply.Inputs[0].DurationSec)
			}
		})
	}
}
