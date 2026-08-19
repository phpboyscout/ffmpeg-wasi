package conformance

import (
	"context"
	"slices"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/fixture"
)

// Sample-rate negotiation — ffmpeg-wasi#17.
//
// An encoder with a restricted set of sample rates must not be handed a rate it
// cannot take: the graph should resample, as ffmpeg does. libopus is the case
// that matters, because it accepts 48kHz and a few others but NOT 44.1 — which
// is what all consumer audio is.
//
// # Why this fixture is 44.1kHz and not the suite's usual 48kHz
//
// The behavioural fixtures are 48000 Hz, and TestWebMMuxesWithoutStalling
// already encodes them through libopus. It passes, and it could not have failed:
// 48kHz is exactly the rate libopus wants, so the missing resample was never
// exercised. The rate here is deliberately one the encoder must convert away
// from — the test is worthless at any rate the encoder already accepts.
const oddSampleRate = 44100 // the commonest consumer rate, and unsupported by libopus

func TestAnEncoderGetsARateItSupports(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			const enc = "libopus"
			if !slices.Contains(caps.Encoders, enc) {
				t.Skipf("%s carries no %s encoder, so it cannot exercise a restricted "+
					"rate set at all", a, enc)
			}

			ws := workspaceFor(t, a)

			// One second of 44.1kHz audio: a rate libopus refuses outright.
			wav, err := fixture.WAV(oddSampleRate, channels, oddSampleRate)
			if err != nil {
				t.Fatalf("building the %dHz fixture: %v", oddSampleRate, err)
			}
			in, err := ws.Write("in441.wav", wav)
			if err != nil {
				t.Fatal(err)
			}

			// runJob fails the test on a non-zero exit, which is the #17 symptom:
			// "Specified sample rate 44100 is not supported by the libopus encoder".
			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": in}},
				"outputs": []any{map[string]any{
					"path": ws.Path("out.webm"), "map": []any{"[a]"}, "audio_codec": enc,
				}},
				"filter": "[0:a]anull[a]",
			})

			// And the result really is audio the encoder accepted, not an empty file.
			got := probe(t, ws, a, ws.Path("out.webm")).Inputs[0]
			if len(got.Streams) != 1 {
				t.Fatalf("%s: the output has %d streams, want 1", a, len(got.Streams))
			}
			if got.Streams[0].Codec != "opus" {
				t.Errorf("%s: output codec is %q, want opus", a, got.Streams[0].Codec)
			}
			// The graph must have resampled to a rate libopus takes.
			if sr := got.Streams[0].SampleRate; sr == oddSampleRate {
				t.Errorf("%s: the output is still %dHz — libopus cannot encode that, so this "+
					"file should not exist", a, sr)
			}
		})
	}
}
