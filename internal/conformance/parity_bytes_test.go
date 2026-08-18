package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// fixedEntropy is what makes this test possible. Matroska seeds its SegmentUID
// from av_get_random_seed, so output is non-deterministic in the ordinary
// workspace, whose /dev/urandom is a fresh block of crypto/rand bytes. Pin the
// device and the WASM engine becomes byte-deterministic (spec 0037 §2.1).
//
// The value is arbitrary; only its fixedness matters.
var fixedEntropy = bytes.Repeat([]byte{0xA5}, 64<<10)

// TestWASMOutputIsIdenticalAcrossArtifacts is spec 0037 D10a — the one place
// this suite can assert *bytes* rather than reported semantics.
//
// It is deliberately narrow. It says nothing about the native target and must
// not be read as parity in D9's sense: the native driver reads the host's real
// /dev/urandom through libavutil's own open(), outside the AVIO bridge, so
// nothing here can pin it and its output is random per run. Cross-target
// comparison lives in parity_test.go and works on what the engine reports.
//
// Within WASM, though, the assertion is free and much stricter than a semantic
// one: three modules built from the same src/ with different codec allowlists
// should encode the same input to the same file, and if they ever stop doing so
// that is worth knowing on exactly the grounds the rest of this layer exists for.
func TestWASMOutputIsIdenticalAcrossArtifacts(t *testing.T) {
	t.Parallel()

	var wasm []engine.Artifact
	for _, a := range artifacts(t) {
		if a.Target == "wasm" {
			wasm = append(wasm, a)
		}
	}
	if len(wasm) < 2 {
		t.Skipf("need two or more wasm artifacts to compare, found %d", len(wasm))
	}

	digests := map[string]string{}

	for _, a := range wasm {
		t.Run(a.String(), func(t *testing.T) {
			ws := mediaWorkspace(t, a)

			// Overwrite the workspace's random device before the job runs. Without
			// this the test would fail on its first run for a reason that has
			// nothing to do with the engine.
			if _, err := ws.Write("dev/urandom", fixedEntropy); err != nil {
				t.Fatalf("%s: pinning /dev/urandom: %v", a, err)
			}

			runJob(t, ws, a, map[string]any{
				"op":     "process",
				"inputs": []any{map[string]any{"path": ws.Path("in.wav")}},
				"outputs": []any{map[string]any{
					"path": ws.Path("out.mkv"), "audio_codec": "flac",
				}},
			})

			body, err := ws.Read("out.mkv")
			if err != nil {
				t.Fatalf("%s: reading the output: %v", a, err)
			}
			sum := sha256.Sum256(body)
			digests[a.String()] = hex.EncodeToString(sum[:])
		})
	}

	// Subtests above are sequential (no t.Parallel inside), so every digest is
	// present here.
	var first, firstName string
	for name, d := range digests {
		if first == "" {
			first, firstName = d, name
			continue
		}
		if d != first {
			t.Errorf("two WASM modules built from the same src/ encoded the same input to "+
				"different bytes:\n  %s: %s\n  %s: %s\n"+
				"Both were given an identical /dev/urandom, so this is not entropy. Either a codec "+
				"allowlist changed what the encoder picks, or the engine has diverged between "+
				"profiles (spec 0037 D10a).", firstName, first, name, d)
		}
	}

	if len(digests) < 2 {
		t.Fatalf("only %d artifact(s) produced a digest, so nothing was compared", len(digests))
	}
	t.Logf("%d wasm artifacts agree byte-for-byte: %s", len(digests), first)
}
