package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// Per-encoder option maps — afmpeg spec 0045.
//
// An output's `options` was one dictionary handed to every encoder that output
// opens. On an output with both a video and an audio codec that meant `crf`, a
// libx264-private option, was also offered to `aac` — and since ffmpeg-wasi#54
// made an unconsumed option fail the job rather than vanish, the whole job was
// refused. Measured on the released n9.0.1-2 driver:
//
//	libx264 + aac, options {"crf":"23"}   exit 1   encoder aac does not have option crf
//	the same on n8.1.2                    exit 0   crf reached x264, dropped at aac
//
// The old exit 0 is not a pass. The option was silently dropped, and the caller
// had no way to learn that.
//
// # Why per-kind maps rather than a smarter check
//
// The strict check is a NAME check. `avcodec_open2` resolves a name against the
// encoder's AVClass and never consults AV_OPT_FLAG_VIDEO_PARAM / AUDIO_PARAM,
// and every encoder's class chains to the generic AVCodecContext table — so 56
// of its 74 encoder-settable options are accepted by an encoder that silently
// ignores them (0045 OQ3, verified against the released driver: `g`, `bf`,
// `keyint_min` and `refs` all exit 0 on `aac`).
//
// Nothing downstream can infer which encoder an option was meant for. The job
// spec is the only place it can be said, which is what these maps are.

// perKindOpts names an option private to one encoder in the pair under test —
// private, because a generic one would be accepted by both and prove nothing.
type perKindOpts struct {
	videoCodec string
	videoOnly  [2]string // an option the video encoder has and the audio one does not
	audioOnly  [2]string // and the reverse
}

// optionPairFor picks a video encoder the artifact carries and an option private
// to it, or skips. `aac_coder` is the audio side throughout: it belongs to aac's
// own AVClass, so no video encoder has it.
func optionPairFor(t *testing.T, a engine.Artifact) perKindOpts {
	t.Helper()

	caps, err := engine.Query(context.Background(), a.Runner())
	if err != nil {
		t.Fatalf("%s: querying capabilities: %v", a, err)
	}
	if !slices.Contains(caps.Encoders, "aac") {
		t.Skipf("%s carries no aac encoder, so it has no audio-private option to address", a)
	}

	switch {
	case slices.Contains(caps.Encoders, "libx264"):
		return perKindOpts{"libx264", [2]string{"crf", "23"}, [2]string{"aac_coder", "twoloop"}}
	case slices.Contains(caps.Encoders, "libopenh264"):
		// openh264's own AVClass; aac has no such option.
		return perKindOpts{"libopenh264", [2]string{"allow_skip_frames", "1"}, [2]string{"aac_coder", "twoloop"}}
	}
	t.Skipf("%s carries neither libx264 nor libopenh264, so it has no video-private option to address", a)
	return perKindOpts{}
}

// avJob is one video+audio output built from the standard fixtures, with the
// three option dictionaries the caller wants set on it.
func avJob(ws *engine.Workspace, o perKindOpts, out string, dicts map[string]any) map[string]any {
	outSpec := map[string]any{
		"path":        ws.Path(out),
		"map":         []any{"[v]", "[a]"},
		"video_codec": o.videoCodec,
		"audio_codec": "aac",
	}
	for k, v := range dicts {
		outSpec[k] = v
	}
	return map[string]any{
		"op": "process",
		"inputs": []any{
			map[string]any{"path": ws.Path("f%03d.png"), "options": map[string]any{"framerate": "25"}},
			map[string]any{"path": ws.Path("in.wav")},
		},
		"filter":  "[0:v]null,format=yuv420p[v];[1:a]anull[a]",
		"outputs": []any{outSpec},
	}
}

// runRaw runs a job that is expected to fail, so it cannot use runJob (which
// fails the test on a non-zero exit).
func runRaw(t *testing.T, ws *engine.Workspace, spec any) engine.Result {
	t.Helper()

	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshalling the job spec: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	return runAndCheck(t, ws.Runner(), ctx, string(body), "the job")
}

// TestAPerKindOptionReachesOnlyItsOwnEncoder is the defect keryx hit, plus the
// three ways a naive fix goes wrong.
func TestAPerKindOptionReachesOnlyItsOwnEncoder(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			o := optionPairFor(t, a)
			ws := mediaWorkspace(t, a)

			// 1. The fix itself. A video-private option addressed to the video
			//    encoder must not reach the audio one. Red before 0045.
			spec := avJob(ws, o, "perkind.mp4", map[string]any{
				"video_options": map[string]any{o.videoOnly[0]: o.videoOnly[1]},
			})
			if res := runRaw(t, ws, spec); res.ExitCode != 0 {
				t.Errorf("%s: video_options{%s} was refused (exit %d), but it names an option "+
					"%s has — it must not be offered to the audio encoder.\nstderr: %s",
					a, o.videoOnly[0], res.ExitCode, o.videoCodec, strings.TrimSpace(res.Stderr))
			}

			// And it must have TAKEN EFFECT. Exit 0 is also what an engine that
			// ignores the dictionary entirely produces — which is exactly what a
			// pre-0045 engine does with an unknown outputs[] key, so without this
			// the assertion above passes against the defect it exists to catch.
			sizeAt := func(out, bitrate string) int {
				runJob(t, ws, a, avJob(ws, o, out, map[string]any{
					"video_options": map[string]any{"b": bitrate},
				}))
				body, err := ws.Read(out)
				if err != nil {
					t.Fatalf("%s: reading %s: %v", a, out, err)
				}
				return len(body)
			}
			if lo, hi := sizeAt("vlow.mp4", videoBitrateLow), sizeAt("vhigh.mp4", videoBitrateHigh); lo >= hi {
				t.Errorf("%s: video_options{b} does not change the output (%s → %d bytes, %s → %d) — "+
					"the dictionary is being accepted and dropped rather than applied",
					a, videoBitrateLow, lo, videoBitrateHigh, hi)
			}

			// 2. `options` keeps its v9 meaning — every encoder the output opens.
			//    Without this the suite passes against a fix that quietly redefined
			//    the common dictionary as video-only, which would change what an
			//    existing job means without saying so (0045 D2).
			spec = avJob(ws, o, "common.mp4", map[string]any{
				"options": map[string]any{o.videoOnly[0]: o.videoOnly[1]},
			})
			res := runRaw(t, ws, spec)
			if res.ExitCode == 0 {
				t.Errorf("%s: options{%s} exited 0. The common dictionary still reaches every "+
					"encoder, so aac must refuse it — a pass here means `options` silently "+
					"changed meaning.", a, o.videoOnly[0])
			} else if !strings.Contains(res.Stderr, "aac") {
				t.Errorf("%s: options{%s} was refused, but stderr does not name aac: %q",
					a, o.videoOnly[0], strings.TrimSpace(res.Stderr))
			}

			// 3. Both directions of the reverse: an option addressed to the wrong
			//    kind must still be refused, and the diagnostic must name the
			//    encoder that refused it. Without these the dictionaries could be
			//    merged back together and everything above would still pass.
			for _, tc := range []struct{ dict, key, val, wantEncoder string }{
				{"audio_options", o.videoOnly[0], o.videoOnly[1], "aac"},
				{"video_options", o.audioOnly[0], o.audioOnly[1], o.videoCodec},
			} {
				spec := avJob(ws, o, fmt.Sprintf("wrong_%s.mp4", tc.dict), map[string]any{
					tc.dict: map[string]any{tc.key: tc.val},
				})
				res := runRaw(t, ws, spec)
				if res.ExitCode == 0 {
					t.Errorf("%s: %s{%s} exited 0 — %s does not have that option, so the "+
						"dictionaries are being merged rather than addressed",
						a, tc.dict, tc.key, tc.wantEncoder)
				} else if !strings.Contains(res.Stderr, tc.wantEncoder) {
					t.Errorf("%s: %s{%s} was refused, but stderr does not name %s: %q",
						a, tc.dict, tc.key, tc.wantEncoder, strings.TrimSpace(res.Stderr))
				}
			}
		})
	}
}

// The bitrate pairs are far enough apart that the encoded sizes cannot be
// confused, and each is a value its encoder accepts.
const (
	audioBitrateHigh = "128000"
	audioBitrateLow  = "16000"
	videoBitrateHigh = "800000"
	videoBitrateLow  = "40000"
)

// audioOnlyJob encodes the WAV fixture to aac with the given dictionaries.
func audioOnlyJob(ws *engine.Workspace, out string, dicts map[string]any) map[string]any {
	outSpec := map[string]any{
		"path": ws.Path(out), "map": []any{"[a]"}, "audio_codec": "aac",
	}
	for k, v := range dicts {
		outSpec[k] = v
	}
	return map[string]any{
		"op":      "process",
		"inputs":  []any{map[string]any{"path": ws.Path("in.wav")}},
		"filter":  "[0:a]anull[a]",
		"outputs": []any{outSpec},
	}
}

// encodedSize runs an audio-only job and returns the bytes it wrote.
func encodedSize(t *testing.T, ws *engine.Workspace, a engine.Artifact, out string, dicts map[string]any) int {
	t.Helper()

	runJob(t, ws, a, audioOnlyJob(ws, out, dicts))
	body, err := ws.Read(out)
	if err != nil {
		t.Fatalf("%s: reading %s: %v", a, out, err)
	}
	return len(body)
}

// TestVideoOptionsDoNotReachTheAudioEncoder needs saying separately because the
// ordinary leak is unobservable: a video-flagged GENERIC option set on an audio
// encoder is accepted and silently ignored, so exit 0 proves nothing (0045 OQ3).
//
// `b` is the probe that works, precisely because it is one of the eighteen both
// encoders act on — so if `video_options` leaked, the audio bitrate would move
// and the file size with it.
func TestVideoOptionsDoNotReachTheAudioEncoder(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			if !slices.Contains(caps.Encoders, "aac") {
				t.Skipf("%s carries no aac encoder", a)
			}
			ws := mediaWorkspace(t, a)

			// The control first: `b` must actually move the size, or the assertion
			// below is vacuous and would pass against an engine that ignores every
			// option it is given.
			high := encodedSize(t, ws, a, "high.mp4", map[string]any{
				"audio_options": map[string]any{"b": audioBitrateHigh},
			})
			low := encodedSize(t, ws, a, "low.mp4", map[string]any{
				"audio_options": map[string]any{"b": audioBitrateLow},
			})
			if low >= high {
				t.Fatalf("%s: audio_options{b} does not change the output size "+
					"(%s → %d bytes, %s → %d), so this test cannot detect a leak",
					a, audioBitrateHigh, high, audioBitrateLow, low)
			}

			// Now the assertion. A low bitrate in video_options, on an output with
			// no video encoder at all, must change nothing.
			leaked := encodedSize(t, ws, a, "leak.mp4", map[string]any{
				"audio_options": map[string]any{"b": audioBitrateHigh},
				"video_options": map[string]any{"b": audioBitrateLow},
			})
			if leaked != high {
				t.Errorf("%s: video_options{b:%s} changed an audio-only output from %d to %d bytes — "+
					"it reached the audio encoder", a, audioBitrateLow, high, leaked)
			}
		})
	}
}

// TestAPerKindOptionWinsOverTheCommonOne asserts D1's precedence on a size
// rather than an exit code, because both dictionaries name an option aac has —
// an exit code could not tell which value was applied.
func TestAPerKindOptionWinsOverTheCommonOne(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("%s: querying capabilities: %v", a, err)
			}
			if !slices.Contains(caps.Encoders, "aac") {
				t.Skipf("%s carries no aac encoder", a)
			}
			ws := mediaWorkspace(t, a)

			// Both references use the COMMON dictionary only, so they mean the same
			// thing to an engine with per-kind maps and one without. A reference
			// built from audio_options would be ignored by a pre-0045 engine and
			// land on the encoder default — which is close enough to 128k that the
			// assertion below passed against the very defect it exists to catch.
			low := encodedSize(t, ws, a, "ref_low.mp4", map[string]any{
				"options": map[string]any{"b": audioBitrateLow},
			})
			high := encodedSize(t, ws, a, "ref_high.mp4", map[string]any{
				"options": map[string]any{"b": audioBitrateHigh},
			})
			if low >= high {
				t.Fatalf("%s: options{b} does not change the output size (%s → %d bytes, %s → %d), "+
					"so this test cannot tell which value won",
					a, audioBitrateHigh, high, audioBitrateLow, low)
			}

			collided := encodedSize(t, ws, a, "collide.mp4", map[string]any{
				"options":       map[string]any{"b": audioBitrateHigh},
				"audio_options": map[string]any{"b": audioBitrateLow},
			})
			if collided != low {
				t.Errorf("%s: options{b:%s} + audio_options{b:%s} produced %d bytes, want %d "+
					"(the audio_options value) — got %d for the options value alone, so the "+
					"per-kind dictionary did not win (0045 D1)",
					a, audioBitrateHigh, audioBitrateLow, collided, low, high)
			}
		})
	}
}

// TestTheSubtitleEncoderIsConfigurable closes the third instance of the family.
//
// The subtitle encoder was opened with a NULL dictionary, so an option addressed
// to it was dropped before libav saw it — and the strict check could not fire,
// because there was no leftover dictionary to inspect. It passed, silently, for
// the wrong reason (0045 D3).
func TestTheSubtitleEncoderIsConfigurable(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			subtitleArtifacts(t, a, "webvtt")
			ws, in := subWorkspace(t, a)

			job := func(dicts map[string]any) map[string]any {
				outSpec := map[string]any{
					"path": ws.Path("out.vtt"), "map": []any{"0:s"}, "subtitle_codec": "webvtt",
				}
				for k, v := range dicts {
					outSpec[k] = v
				}
				return map[string]any{
					"op":      "process",
					"inputs":  []any{map[string]any{"path": in}},
					"outputs": []any{outSpec},
				}
			}

			// A name no encoder has must be refused, and the message must name the
			// subtitle encoder. Red before 0045 — it exited 0.
			res := runRaw(t, ws, job(map[string]any{
				"subtitle_options": map[string]any{"not_an_option": "1"},
			}))
			if res.ExitCode == 0 {
				t.Errorf("%s: subtitle_options{not_an_option} exited 0 — the dictionary is not "+
					"reaching the subtitle encoder, so nothing addressed to it can take effect", a)
			} else if !strings.Contains(res.Stderr, "webvtt") {
				t.Errorf("%s: refused, but stderr does not name the subtitle encoder: %q",
					a, strings.TrimSpace(res.Stderr))
			}

			// And the same job without the bad option still works, so the check
			// above is not passing because subtitles are broken outright.
			if res := runRaw(t, ws, job(nil)); res.ExitCode != 0 {
				t.Errorf("%s: the same job without subtitle_options exited %d — the refusal above "+
					"proves nothing.\nstderr: %s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}
		})
	}
}
