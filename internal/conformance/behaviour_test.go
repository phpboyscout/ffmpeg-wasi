package conformance

// Phase C — behavioural properties. Where phases A and B ask what the engine
// CARRIES and how it ANSWERS, this asks whether probe, process and frames
// actually do the thing.
//
// Two rules govern everything here.
//
// PROPERTIES, NOT GOLDENS (spec 0036 D7). Stream counts, durations within
// tolerance, dimensions, pixel formats, frames landing on disk. No byte-exact
// checksums anywhere: a golden suite goes red on every FFmpeg bump, which would
// make the instrument useless at the exact moment it is needed.
//
// THE FIXTURES DO NOT COME FROM THE ENGINE. internal/fixture builds WAV and PNG
// in pure Go, so "the engine read 2.0 seconds" is checked against arithmetic
// rather than against the engine's own earlier output. Where a test reads the
// engine's output back, it prefers Go's own decoder over a second engine call for
// the same reason.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Fixture geometry. Named so an assertion reads against the number that produced
// the media rather than a literal repeated at the call site.
const (
	sampleRate  = 48000
	channels    = 2
	sampleCount = sampleRate * 2 // exactly 2.0s
	frameW      = 160
	frameH      = 120
	frameCount  = 30
	frameRate   = 10 // so the PNG sequence is ~3.0s
)

// durationTolerance is how far a reported duration may sit from the arithmetic.
//
// It is not slop to make a flaky test pass. A container stores duration in its
// own timebase, a frame sequence of N frames at R fps spans (N-1)/R between first
// and last presentation times, and a muxer may round. 200ms accommodates all of
// that while still failing an engine that lost a second, halved a rate, or
// reported nothing.
const durationTolerance = 0.2

// mediaWorkspace prepares a workspace with the standard fixtures already in it.
func mediaWorkspace(t *testing.T, a engine.Artifact) *engine.Workspace {
	t.Helper()

	ws, err := engine.NewWorkspace(t.TempDir(), a)
	if err != nil {
		t.Fatalf("%s: preparing the workspace: %v", a, err)
	}

	wav, err := fixture.WAV(sampleRate, channels, sampleCount)
	if err != nil {
		t.Fatalf("building the WAV fixture: %v", err)
	}
	if _, err := ws.Write("in.wav", wav); err != nil {
		t.Fatalf("%s: %v", a, err)
	}

	for i := range frameCount {
		img, err := fixture.PNG(frameW, frameH, i)
		if err != nil {
			t.Fatalf("building PNG fixture %d: %v", i, err)
		}
		if _, err := ws.Write(fmt.Sprintf("f%03d.png", i), img); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	return ws
}

// jobTimeout bounds a single invocation.
//
// It exists because THE FAILURE MODE HERE IS A HANG, not an error. Matroska seeds
// itself from av_get_random_seed, and on the WASM target with no /dev/urandom that
// call never returns — verified by running this suite's mkv job against a mount
// with no /dev, where it had to be killed. Without a deadline that regression
// would stall the whole `go test` run until Go's package timeout and report
// nothing useful about why.
//
// Generous against the real numbers: the slowest legitimate invocation here is a
// WASM module compile plus a two-second encode, well under two seconds.
const jobTimeout = 60 * time.Second

// runJob invokes a job spec and fails unless it succeeded. stderr is NOT required
// to be empty: libav writes advisory warnings there on wholly successful jobs
// (swscaler pixel-format notes, "changing frame properties on the fly"), and
// treating those as failures would make the suite red for no defect.
func runJob(t *testing.T, ws *engine.Workspace, a engine.Artifact, spec any) []byte {
	t.Helper()

	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshalling the job spec: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	res, err := ws.Runner().Run(ctx, string(body))
	if ctx.Err() != nil {
		t.Fatalf("%s: the job did not finish within %s — it HUNG rather than failed.\n"+
			"The known cause is a muxer waiting on entropy the mount does not serve; check that the "+
			"workspace still creates /dev/urandom (spec 0036 D5).\nspec: %s", a, jobTimeout, body)
	}
	if err != nil {
		t.Fatalf("%s: invoking: %v", a, err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("%s: job exited %d, want 0.\nspec:   %s\nstderr: %s",
			a, res.ExitCode, body, strings.TrimSpace(res.Stderr))
	}

	out := []byte(strings.TrimSpace(res.Stdout))

	// File the reply under the job that produced it, so the parity comparison can
	// ask whether two artefacts running the same job answered the same way (spec
	// 0037 D1). Purely additive: every assertion in the caller still applies, and
	// a caller that fails still fails for its own reason.
	recordForParity(a, body, out)

	return out
}

// probeReply is the subset of a probe reply these tests assert on.
type probeReply struct {
	Inputs []struct {
		Path        string  `json:"path"`
		Format      string  `json:"format"`
		DurationSec float64 `json:"duration_sec"`
		Error       string  `json:"error"`
		Chapters    []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Title string  `json:"title"`
		} `json:"chapters"`
		Streams []struct {
			Index      int    `json:"index"`
			Type       string `json:"type"`
			Codec      string `json:"codec"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			SampleRate int    `json:"sample_rate"`
			Channels   int    `json:"channels"`
			PixFmt     string `json:"pix_fmt"`
			Language   string `json:"language"`
		} `json:"streams"`
	} `json:"inputs"`
}

// probe runs the probe op over one path and returns the single input's entry.
func probe(t *testing.T, ws *engine.Workspace, a engine.Artifact, path string) probeReply {
	t.Helper()

	out := runJob(t, ws, a, map[string]any{
		"op":     "probe",
		"inputs": []any{map[string]any{"path": path}},
	})

	var reply probeReply
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatalf("%s: parsing the probe reply: %v\n%s", a, err, out)
	}
	if len(reply.Inputs) != 1 {
		t.Fatalf("%s: probe of %s reported %d inputs, want 1", a, path, len(reply.Inputs))
	}
	if reply.Inputs[0].Error != "" {
		t.Fatalf("%s: probe could not open %s: %s", a, path, reply.Inputs[0].Error)
	}
	return reply
}

// wantDuration asserts a reported duration against what the fixture arithmetic
// says it must be.
func wantDuration(t *testing.T, a engine.Artifact, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > durationTolerance {
		t.Errorf("%s: %s is %.3fs, want %.3fs ±%.1f — the fixture is that long by construction",
			a, what, got, want, durationTolerance)
	}
}

// TestProbeReportsWhatTheFixtureContains is the base of phase C: if probe cannot
// describe media whose contents are known by construction, nothing built on top
// of it means anything.
func TestProbeReportsWhatTheFixtureContains(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			t.Run("wav", func(t *testing.T) {
				in := probe(t, ws, a, ws.Path("in.wav")).Inputs[0]

				if !strings.Contains(in.Format, "wav") {
					t.Errorf("%s: format is %q, want it to name wav", a, in.Format)
				}
				wantDuration(t, a, "the WAV duration", in.DurationSec, fixture.WAVDuration(sampleRate, sampleCount))

				if len(in.Streams) != 1 {
					t.Fatalf("%s: the WAV reports %d streams, want 1", a, len(in.Streams))
				}
				s := in.Streams[0]
				if s.Type != "audio" {
					t.Errorf("%s: stream type is %q, want audio", a, s.Type)
				}
				if s.Codec != "pcm_s16le" {
					t.Errorf("%s: codec is %q, want pcm_s16le — the fixture is 16-bit PCM", a, s.Codec)
				}
				if s.SampleRate != sampleRate {
					t.Errorf("%s: sample rate is %d, want %d", a, s.SampleRate, sampleRate)
				}
				if s.Channels != channels {
					t.Errorf("%s: channel count is %d, want %d", a, s.Channels, channels)
				}
			})

			t.Run("png", func(t *testing.T) {
				in := probe(t, ws, a, ws.Path("f000.png")).Inputs[0]

				if len(in.Streams) != 1 {
					t.Fatalf("%s: the PNG reports %d streams, want 1", a, len(in.Streams))
				}
				s := in.Streams[0]
				if s.Type != "video" {
					t.Errorf("%s: stream type is %q, want video", a, s.Type)
				}
				if s.Codec != "png" {
					t.Errorf("%s: codec is %q, want png", a, s.Codec)
				}
				if s.Width != frameW || s.Height != frameH {
					t.Errorf("%s: dimensions are %dx%d, want %dx%d", a, s.Width, s.Height, frameW, frameH)
				}
			})
		})
	}
}

// TestProcessTranscodesAudio asserts the transcode actually happened: the output
// carries the codec that was asked for, and the same amount of time as the input.
func TestProcessTranscodesAudio(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("out.mkv"), "audio_codec": "flac",
				}},
			})

			in := probe(t, ws, a, ws.Path("out.mkv")).Inputs[0]
			if len(in.Streams) != 1 {
				t.Fatalf("%s: the output has %d streams, want 1", a, len(in.Streams))
			}
			if got := in.Streams[0].Codec; got != "flac" {
				t.Errorf("%s: output codec is %q, want flac — the transcode did not happen", a, got)
			}
			wantDuration(t, a, "the transcoded duration", in.DurationSec, fixture.WAVDuration(sampleRate, sampleCount))
		})
	}
}

// TestProcessStreamCopyPreservesTheCodec asserts the copy sentinel does NOT
// re-encode. Duration and codec both surviving is what distinguishes a copy from
// a transcode that happens to pick the same codec.
func TestProcessStreamCopyPreservesTheCodec(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("copy.mkv"), "map": []any{"0:a"}, "audio_codec": "copy",
				}},
			})

			in := probe(t, ws, a, ws.Path("copy.mkv")).Inputs[0]
			if len(in.Streams) != 1 {
				t.Fatalf("%s: the copy has %d streams, want 1", a, len(in.Streams))
			}
			if got := in.Streams[0].Codec; got != "pcm_s16le" {
				t.Errorf("%s: copied stream is %q, want pcm_s16le — a copy must not re-encode", a, got)
			}
			wantDuration(t, a, "the copied duration", in.DurationSec, fixture.WAVDuration(sampleRate, sampleCount))
		})
	}
}

// TestVideoRoundTripPreservesGeometry runs an image sequence through the encoder
// and asserts the geometry survives.
//
// Duration is asserted against (N-1)/rate, not N/rate: a sequence of N frames at
// R fps spans that between the first and last presentation timestamps, and the
// tolerance is not wide enough to paper over the difference at these numbers.
func TestVideoRoundTripPreservesGeometry(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": ws.Path("f%03d.png"), "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(frameRate)},
				}},
				"outputs": []any{map[string]any{
					"path": ws.Path("seq.mp4"), "video_codec": "mjpeg",
				}},
			})

			in := probe(t, ws, a, ws.Path("seq.mp4")).Inputs[0]
			if len(in.Streams) != 1 {
				t.Fatalf("%s: the output has %d streams, want 1", a, len(in.Streams))
			}
			s := in.Streams[0]
			if s.Codec != "mjpeg" {
				t.Errorf("%s: output codec is %q, want mjpeg", a, s.Codec)
			}
			if s.Width != frameW || s.Height != frameH {
				t.Errorf("%s: output is %dx%d, want %dx%d — geometry was not preserved",
					a, s.Width, s.Height, frameW, frameH)
			}
			wantDuration(t, a, "the encoded sequence", in.DurationSec, float64(frameCount-1)/frameRate)
		})
	}
}

// TestMatroskaMuxesWithoutStalling is a REGRESSION TEST with a specific history.
//
// Matroska seeds itself from av_get_random_seed. On the WASM target with no
// /dev/urandom that call does not fail — it HANGS, and an mkv job never returns.
// This is the first test in the suite that would catch it: phases A and B never
// mux anything, and the workspace's synthetic /dev (spec 0036 D5) is what makes
// the job completable here at all.
//
// On the native target the device is the host's real one, so this asserts the
// muxer rather than the harness. Both are worth having: the failure is a hang, and
// a hang looks the same whichever side caused it.
func TestMatroskaMuxesWithoutStalling(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("entropy.mkv"), "audio_codec": "flac",
				}},
			})

			out, err := ws.Read("entropy.mkv")
			if err != nil {
				t.Fatalf("%s: %v", a, err)
			}
			if len(out) == 0 {
				t.Fatalf("%s: the muxer exited 0 but wrote an empty file", a)
			}
		})
	}
}

// TestFramesLandOnDisk asserts the frames op against the filesystem and against
// Go's own PNG decoder.
//
// Decoding host-side rather than probing the frames back through the engine is
// deliberate: an engine that wrote 3 unreadable files would satisfy a reply-only
// assertion, and probing its own output back asks the engine to confirm itself.
// image/png agrees or it does not.
func TestFramesLandOnDisk(t *testing.T) {
	type framesReply struct {
		Frames []struct {
			Path      string  `json:"path"`
			Index     int     `json:"index"`
			Timestamp float64 `json:"timestamp"`
		} `json:"frames"`
		Count int `json:"count"`
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			// A source with real duration to select within.
			runJob(t, ws, a, map[string]any{
				"op": "process",
				"inputs": []any{map[string]any{
					"path": ws.Path("f%03d.png"), "format": "image2",
					"options": map[string]any{"framerate": fmt.Sprint(frameRate)},
				}},
				"outputs": []any{map[string]any{"path": ws.Path("seq.mp4"), "video_codec": "mjpeg"}},
			})

			for _, c := range []struct {
				name    string
				select_ map[string]any
				prefix  string
				want    int
			}{
				{"explicit timestamps", map[string]any{"timestamps": []any{0.5, 1.5}}, "ts", 2},
				{"an interval", map[string]any{"interval": 1.0}, "iv", 3},
			} {
				out := runJob(t, ws, a, map[string]any{
					"op":     "frames",
					"inputs": []any{map[string]any{"path": ws.Path("seq.mp4")}},
					"select": c.select_,
					"path":   ws.Path(c.prefix + "%02d.png"),
					"codec":  "png",
				})

				var reply framesReply
				if err := json.Unmarshal(out, &reply); err != nil {
					t.Fatalf("%s: %s: parsing the frames reply: %v\n%s", a, c.name, err, out)
				}
				if reply.Count != c.want || len(reply.Frames) != c.want {
					t.Errorf("%s: %s: reply says count=%d with %d entries, want %d",
						a, c.name, reply.Count, len(reply.Frames), c.want)
				}

				// The reply is a claim; the filesystem is the fact.
				written, err := ws.Glob(c.prefix + "*.png")
				if err != nil {
					t.Fatalf("%s: %v", a, err)
				}
				if len(written) != c.want {
					t.Errorf("%s: %s: %d files on disk, want %d — the reply and the filesystem disagree",
						a, c.name, len(written), c.want)
				}

				for _, name := range written {
					body, err := ws.Read(name)
					if err != nil {
						t.Fatalf("%s: %v", a, err)
					}
					img, err := png.Decode(bytes.NewReader(body))
					if err != nil {
						t.Errorf("%s: %s: %s is not a decodable PNG: %v", a, c.name, name, err)
						continue
					}
					if b := img.Bounds(); b.Dx() != frameW || b.Dy() != frameH {
						t.Errorf("%s: %s: %s is %dx%d, want %dx%d",
							a, c.name, name, b.Dx(), b.Dy(), frameW, frameH)
					}
				}
			}
		})
	}
}

// TestProgressSideChannel asserts the engine streams progress records when asked
// (spec 0032), which the workspace's /dev/afmpeg-progress captures.
//
// WASM ONLY, and not from squeamishness: the ABI reference says the native driver
// opens /dev/afmpeg-progress against the HOST filesystem rather than the caller's,
// so on that target the emitter is inert by design and there is nothing to read.
// Asserting it there would be asserting a documented absence.
func TestProgressSideChannel(t *testing.T) {
	for _, a := range artifacts(t) {
		if a.Target != "wasm" {
			continue
		}
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()
			ws := mediaWorkspace(t, a)

			runJob(t, ws, a, map[string]any{
				"op":       "process",
				"inputs":   []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs":  []any{map[string]any{"path": ws.Path("prog.mkv"), "audio_codec": "flac"}},
				"progress": true,
			})

			raw, err := ws.Read("dev/afmpeg-progress")
			if err != nil {
				t.Fatalf("%s: %v", a, err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(raw) == 0 || len(lines) == 0 {
				t.Fatalf("%s: a job with progress:true wrote nothing to the side-channel", a)
			}

			var last float64 = -1
			for i, line := range lines {
				var rec struct {
					OutTimeUS  float64 `json:"out_time_us"`
					DurationUS float64 `json:"duration_us"`
					TotalSize  int     `json:"total_size"`
				}
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("%s: progress line %d is not JSON: %v\n%s", a, i, err, line)
				}
				// Monotonic: a progress bar that goes backwards is worse than none.
				if rec.OutTimeUS < last {
					t.Errorf("%s: progress went backwards at line %d: %.0fus after %.0fus",
						a, i, rec.OutTimeUS, last)
				}
				last = rec.OutTimeUS
			}

			wantUS := fixture.WAVDuration(sampleRate, sampleCount) * 1e6
			if math.Abs(last-wantUS) > durationTolerance*1e6 {
				t.Errorf("%s: the final progress record is at %.0fus, want ~%.0fus — progress did not reach the end",
					a, last, wantUS)
			}
		})
	}
}

// TestWebMMuxesWithoutStalling is the matroska regression's sibling, on the
// container that shares its entropy path.
//
// It is CAPABILITY-GATED rather than profile-gated. WebM accepts only VP8/VP9/AV1
// video and Vorbis/Opus audio, and the lean profile carries none of those as
// encoders — so on lean there is no legal way to write a WebM file and the test
// has nothing to say. Asking the artifact what it can do, via phase A's
// --capabilities, is better than hardcoding "intermediate and above": the answer
// stays right when a profile's contents change, which is the whole reason that
// machinery exists.
//
// A skip here means "this build cannot make a WebM", not "this was not checked".
func TestWebMMuxesWithoutStalling(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			const audioCodec = "libopus"
			if !slices.Contains(caps.Encoders, audioCodec) {
				t.Skipf("%s carries no %s encoder, so it cannot write a WebM at all "+
					"(WebM takes only VP8/VP9/AV1 video and Vorbis/Opus audio)", a, audioCodec)
			}

			ws := mediaWorkspace(t, a)
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("out.webm"), "audio_codec": audioCodec,
				}},
			})

			in := probe(t, ws, a, ws.Path("out.webm")).Inputs[0]
			if len(in.Streams) != 1 {
				t.Fatalf("%s: the WebM has %d streams, want 1", a, len(in.Streams))
			}
			if got := in.Streams[0].Codec; got != "opus" {
				t.Errorf("%s: WebM audio codec is %q, want opus", a, got)
			}
			wantDuration(t, a, "the WebM duration", in.DurationSec, fixture.WAVDuration(sampleRate, sampleCount))
		})
	}
}
