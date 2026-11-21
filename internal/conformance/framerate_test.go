package conformance

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// outputs[].frame_rate, afmpeg spec 0025 §4.2 (ffmpeg-wasi#69).
//
// The field is defined as the hand-written `fps` / `aresample=async=1` pattern
// applied for the caller, so each lane is asserted three ways: against the
// arithmetic, against that hand-written graph, and against a passthrough, which
// must differ or the fixture could not have told the two apart.

// slowRate is the rate the PNG sequence is read at: 25 images over 2.5s.
const slowRate = 10

// rateFrames runs the PNG sequence at slowRate through filter into one PNG per
// frame, with extra merged into the output, and counts the files.
func rateFrames(t *testing.T, a engine.Artifact, filter string, extra map[string]any) int {
	t.Helper()

	ws := workspaceFor(t, a)
	in := writeSequence(t, ws, seqFrames)
	out := map[string]any{"path": ws.Path("out_%03d.png"), "map": []any{"[v]"}, "video_codec": "png"}
	for k, v := range extra {
		out[k] = v
	}
	// A job without the field says v10, so the passthrough baseline is also a
	// v10 spec running unchanged on the v11 engine.
	version := 10
	if extra != nil {
		version = 11
	}
	runJob(t, ws, a, map[string]any{
		"op":      "process",
		"version": version,
		"inputs": []any{map[string]any{
			"path": in, "format": "image2",
			"options": map[string]any{"framerate": fmt.Sprint(slowRate)},
		}},
		"outputs": []any{out},
		"filter":  filter,
	})

	got, err := ws.Glob("out_*.png")
	if err != nil {
		t.Fatalf("%s: counting output frames: %v", a, err)
	}
	return len(got)
}

func TestFrameRateHoldsTheVideoAtTheRequestedRate(t *testing.T) {
	cases := []struct {
		rate string
		// The source spans seqFrames/slowRate = 2.5s; fps emits one frame per
		// output tick across it, rounding the half-tick at the end up.
		want int
		hand string
	}{
		{"5", 13, "fps=5"}, // below the source rate, so fps drops frames
		{"25", 63, "fps=25"},
		{"30000/1001", 75, "fps=30000/1001"},
		{"ntsc", 75, "fps=30000/1001"},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			if got := rateFrames(t, a, "[0:v]null[v]", nil); got != seqFrames {
				t.Fatalf("%s: the passthrough gives %d frames, want the %d the sequence holds", a, got, seqFrames)
			}
			for _, c := range cases {
				field := rateFrames(t, a, "[0:v]null[v]", map[string]any{"frame_rate": c.rate})
				hand := rateFrames(t, a, "[0:v]"+c.hand+"[v]", nil)
				if field != c.want || hand != c.want {
					t.Errorf("%s: frame_rate %q gives %d frames and the hand-written %s gives %d, want %d for both",
						a, c.rate, field, c.hand, hand, c.want)
				}
			}
		})
	}
}

// holeFixture is 60ms PCM packets at 60ms spacing with one 560ms gap in the
// middle: a hole long enough to pass aresample's 0.1s min_hard_comp, so
// async=1 has to fill it, which the jittered fixture's few ms of drift never do.
func holeFixture() (gaps []int, packets int) {
	const half = 16
	for range half {
		gaps = append(gaps, 60)
	}
	gaps = append(gaps, 560)
	for range half {
		gaps = append(gaps, 60)
	}
	return gaps, len(gaps) + 1
}

// audioSamples runs the hole fixture beside the PNG sequence into a Matroska
// file, copies its audio out to WAV, and returns how many sample frames the WAV
// holds. Counting the samples, not the duration, is the point: the container
// spans the hole either way, and only the samples say whether it was filled.
func audioSamples(t *testing.T, a engine.Artifact, filter string, extra map[string]any) int {
	t.Helper()

	ws := workspaceFor(t, a)
	seq := writeSequence(t, ws, seqFrames)
	gaps, packets := holeFixture()
	mkv, err := fixture.MatroskaJitteredAudio(sampleRate, channels, jitterPacketSamples, packets, gaps)
	if err != nil {
		t.Fatalf("building the hole fixture: %v", err)
	}
	if _, err := ws.Write("hole.mkv", mkv); err != nil {
		t.Fatalf("%s: %v", a, err)
	}

	out := map[string]any{
		"path": ws.Path("synced.mkv"), "map": []any{"[v]", "[a]"},
		"video_codec": "png", "audio_codec": "pcm_s16le",
	}
	for k, v := range extra {
		out[k] = v
	}
	runJob(t, ws, a, map[string]any{
		"op":      "process",
		"version": 11,
		"inputs": []any{
			map[string]any{"path": seq, "format": "image2", "options": map[string]any{"framerate": fmt.Sprint(slowRate)}},
			map[string]any{"path": ws.Path("hole.mkv")},
		},
		"outputs": []any{out},
		"filter":  filter,
	})
	runJob(t, ws, a, map[string]any{
		"op":      "process",
		"version": 2,
		"inputs":  []any{map[string]any{"path": ws.Path("synced.mkv")}},
		"outputs": []any{map[string]any{"path": ws.Path("synced.wav"), "map": []any{"0:a"}, "audio_codec": "copy"}},
	})

	wav, err := ws.Read("synced.wav")
	if err != nil {
		t.Fatalf("%s: reading the extracted audio: %v", a, err)
	}
	const bytesPerSample = 2
	return wavDataBytes(t, wav) / (bytesPerSample * channels)
}

// wavDataBytes walks the RIFF chunks to the `data` chunk's size; lavf's WAV
// muxer writes a LIST chunk ahead of it, so the header is not a fixed 44 bytes.
func wavDataBytes(t *testing.T, wav []byte) int {
	t.Helper()
	for at := 12; at+8 <= len(wav); {
		size := int(binary.LittleEndian.Uint32(wav[at+4:]))
		if string(wav[at:at+4]) == "data" {
			return size
		}
		at += 8 + size + size%2
	}
	t.Fatalf("the extracted WAV has no data chunk")
	return 0
}

func TestFrameRateFillsAnAudioHole(t *testing.T) {
	gaps, packets := holeFixture()
	carried := packets * jitterPacketSamples
	spanned := int(fixture.MatroskaJitteredAudioDuration(sampleRate, jitterPacketSamples, packets, gaps) * sampleRate)

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			const graph = "[0:v]null[v];[1:a]anull[a]"
			if got := audioSamples(t, a, graph, nil); got != carried {
				t.Fatalf("%s: the passthrough holds %d samples, want the %d the packets carry; "+
					"the fixture cannot show a hole being filled", a, got, carried)
			}
			field := audioSamples(t, a, graph, map[string]any{"frame_rate": "25"})
			hand := audioSamples(t, a, "[0:v]fps=25[v];[1:a]aresample=async=1[a]", nil)
			// async=1 compensates to within one packet of the timeline, not to the sample.
			if field != hand || field < spanned-jitterPacketSamples || field > spanned {
				t.Errorf("%s: frame_rate gives %d samples and the hand-written aresample=async=1 gives %d; "+
					"want them equal and within one packet of the %d the timeline spans",
					a, field, hand, spanned)
			}
		})
	}
}

// TestFrameRateIsRefusedWhereItCannotApply covers the rejections spec 0025
// §4.2 names. Each is a malformed request, so exit 2 and a diagnostic naming
// the field.
func TestFrameRateIsRefusedWhereItCannotApply(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			ws := mediaWorkspace(t, a)
			wav := ws.Path("in.wav")
			seq := writeSequence(t, ws, seqFrames)
			image2 := map[string]any{"path": seq, "format": "image2"}

			cases := []struct {
				name string
				job  map[string]any
				want string
			}{
				{"an unparseable rate", map[string]any{
					"inputs": []any{image2}, "filter": "[0:v]null[v]",
					"outputs": []any{map[string]any{"path": ws.Path("a.mkv"), "map": []any{"[v]"}, "video_codec": "png", "frame_rate": "fast"}},
				}, `"fast" on`},
				{"a zero rate", map[string]any{
					"inputs": []any{image2}, "filter": "[0:v]null[v]",
					"outputs": []any{map[string]any{"path": ws.Path("b.mkv"), "map": []any{"[v]"}, "video_codec": "png", "frame_rate": "0/1"}},
				}, "is not a positive frame rate"},
				{"a rate that is not a string", map[string]any{
					"inputs": []any{image2}, "filter": "[0:v]null[v]",
					"outputs": []any{map[string]any{"path": ws.Path("c.mkv"), "map": []any{"[v]"}, "video_codec": "png", "frame_rate": 25}},
				}, "must be a string"},
				{"a copied video stream", map[string]any{
					"inputs":  []any{image2},
					"outputs": []any{map[string]any{"path": ws.Path("d.mkv"), "map": []any{"0:v"}, "video_codec": "copy", "frame_rate": "25"}},
				}, "cannot apply to a copied video stream"},
				{"an output with no video codec", map[string]any{
					"inputs": []any{map[string]any{"path": wav}}, "filter": "[0:a]anull[a]",
					"outputs": []any{map[string]any{"path": ws.Path("e.mkv"), "map": []any{"[a]"}, "audio_codec": "pcm_s16le", "frame_rate": "25"}},
				}, "needs a video_codec"},
				{"an output that maps no video pad", map[string]any{
					"inputs": []any{map[string]any{"path": wav}}, "filter": "[0:a]anull[a]",
					"outputs": []any{map[string]any{"path": ws.Path("f.mkv"), "map": []any{"[a]"},
						"video_codec": "png", "audio_codec": "pcm_s16le", "frame_rate": "25"}},
				}, "maps no encoded video stream"},
			}

			for _, c := range cases {
				c.job["op"], c.job["version"] = "process", 11
				spec, err := json.Marshal(c.job)
				if err != nil {
					t.Fatalf("marshalling %s: %v", c.name, err)
				}
				res := runAndCheck(t, ws.Runner(), t.Context(), string(spec), c.name)
				if res.ExitCode != 2 {
					t.Errorf("%s: %s: exited %d, want 2 for a malformed request\nstderr: %s",
						a, c.name, res.ExitCode, strings.TrimSpace(res.Stderr))
					continue
				}
				if !strings.Contains(res.Stderr, c.want) {
					t.Errorf("%s: %s: refused, but stderr does not carry %q: %q",
						a, c.name, c.want, strings.TrimSpace(res.Stderr))
				}
			}
		})
	}
}

// TestAnEmptyFrameRateChangesNothing is the zero value the Go binding sends
// when no rate is set: it must run as a v10 spec does.
func TestAnEmptyFrameRateChangesNothing(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			counts := []int{
				rateFrames(t, a, "[0:v]null[v]", map[string]any{"frame_rate": ""}),
				rateFrames(t, a, "[0:v]null[v]", map[string]any{"frame_rate": nil}),
			}
			if slices.ContainsFunc(counts, func(n int) bool { return n != seqFrames }) {
				t.Errorf("%s: an empty or null frame_rate gives %v frames, want %d for each", a, counts, seqFrames)
			}
		})
	}
}
