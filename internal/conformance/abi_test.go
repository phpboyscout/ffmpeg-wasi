package conformance

// Phase B — ABI conformance. These tests pin the invocation surface afmpeg (and
// any other host) depends on, as documented in docs/reference/driver-invocation-abi.md
// and docs/reference/errors.md: the four ops dispatch, a malformed request exits
// `2`, a too-new vocabulary exits `3`, stdout carries exactly one line of JSON and
// nothing else, and `version` reports the pair this repository declares.
//
// EVERY SPEC HERE IS REJECTED, OR ANSWERED, WITHOUT OPENING A MEDIA FILE. That is
// what lets phase B run with no fixtures, no mount and no working directory: the
// validation paths return before any I/O, `version` needs no input at all, and the
// one case that does reach for a file is asserted to fail. Media that actually
// decodes is phase C's business (spec 0036 D7).
//
// Every assertion here is asserted green. Phase B shipped with two known-failing
// exceptions, both since fixed in the engine; the assertions they were written
// against are now plain — see TestProcessRejectsAMalformedOutputEntry.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/buildlist"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// invoke runs one artifact with a single argument and fails the test if the
// engine could not be run at all. A non-zero exit is a result, not an error —
// the exit code is the contract under test.
func invoke(t *testing.T, a engine.Artifact, arg string) engine.Result {
	t.Helper()
	res, err := a.Runner().Run(context.Background(), arg)
	if err != nil {
		t.Fatalf("%s: invoking with %s: %v", a, arg, err)
	}
	return res
}

// wantOneJSONLine asserts the ABI's stdout clause: "one line of unformatted JSON
// … nothing else is written there". It returns the decoded object so a caller can
// go on to assert its contents.
//
// The line count is checked as well as the parse, because encoding/json would
// happily accept pretty-printed or trailing-garbage-free multi-line output. A
// host reading the engine line by line would not.
func wantOneJSONLine(t *testing.T, a engine.Artifact, res engine.Result, what string) map[string]any {
	t.Helper()

	if !strings.HasSuffix(res.Stdout, "\n") {
		t.Errorf("%s: %s: stdout is not newline-terminated: %q", a, what, res.Stdout)
	}
	body := strings.TrimSuffix(res.Stdout, "\n")
	if body == "" {
		t.Fatalf("%s: %s: stdout is empty, want one line of JSON", a, what)
	}
	if strings.Contains(body, "\n") {
		t.Errorf("%s: %s: stdout carries %d lines, want exactly one:\n%s",
			a, what, strings.Count(res.Stdout, "\n"), res.Stdout)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%s: %s: stdout is not JSON (%v): %q", a, what, err, body)
	}
	return out
}

// wantSilentStdout asserts the other half of the same clause: a failed
// invocation writes its message to stderr and leaves stdout untouched. A host
// that reads stdout on a non-zero exit must find nothing to misparse.
func wantSilentStdout(t *testing.T, a engine.Artifact, res engine.Result, what string) {
	t.Helper()
	if res.Stdout != "" {
		t.Errorf("%s: %s: exited %d but wrote to stdout, which must carry only a result: %q",
			a, what, res.ExitCode, res.Stdout)
	}
	if strings.TrimSpace(res.Stderr) == "" {
		t.Errorf("%s: %s: exited %d with nothing on stderr; a failure must say why",
			a, what, res.ExitCode)
	}
}

// TestOpsDispatch asserts all four ops are reachable in every artifact.
//
// Reachability is asserted through each op's OWN response — a probe reply, a
// version reply, an error naming "process:" or "frames:" — rather than through
// the exit code, because the unknown-op branch exits `2` as well. A spec that
// never reached its op would report `unknown op`, and that is precisely the
// failure this catches: an op dropped from the dispatch table, or a build that
// compiled one out.
func TestOpsDispatch(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// probe and version answer without touching a file, so both are
			// asserted all the way to a reply. An empty inputs array is a
			// legitimate probe: nothing to open, so nothing to report.
			for _, c := range []struct{ op, spec string }{
				{"probe", `{"op":"probe","inputs":[]}`},
				{"version", `{"op":"version"}`},
			} {
				res := invoke(t, a, c.spec)
				if res.ExitCode != 0 {
					t.Errorf("%s: %s: exited %d, want 0: %s", a, c.op, res.ExitCode, strings.TrimSpace(res.Stderr))
					continue
				}
				wantOneJSONLine(t, a, res, c.op)
			}

			// process and frames need media to succeed, so dispatch is asserted
			// by the op-specific rejection: the message prefix proves control
			// reached op_process / op_frames rather than the unknown-op branch.
			for _, c := range []struct{ op, spec string }{
				{"process", `{"op":"process","inputs":[],"outputs":[]}`},
				{"frames", `{"op":"frames","inputs":[]}`},
			} {
				op, spec := c.op, c.spec
				res := invoke(t, a, spec)
				if want := "ffmpeg-wasi: " + op + ":"; !strings.Contains(res.Stderr, want) {
					t.Errorf("%s: %s did not dispatch — stderr does not begin %q, so the spec never reached the op: %q",
						a, op, want, strings.TrimSpace(res.Stderr))
				}
				if res.ExitCode != 2 {
					t.Errorf("%s: %s with an empty spec exited %d, want 2 (a malformed request)", a, op, res.ExitCode)
				}
				wantSilentStdout(t, a, res, op+" with an empty spec")
			}
		})
	}
}

// TestVersionOpReportsTheDeclaredEngine asserts `version` answers with the pair
// this repository declares — build/ffmpeg-version.txt via build/ffmpeg-version.sh,
// and AFMPEG_VOCAB_VERSION from src/driver.c.
//
// This is the assertion that makes an FFmpeg bump reviewable on evidence rather
// than trust (spec 0035): bumping the version file is a one-line change whose own
// merge request builds the artifacts, and this test is what proves the artifact
// really is the version the file now names. It is equally the check that catches
// the reverse — a suite pointed at a stale artifact, which is where every
// ambiguous result in this programme has started.
func TestVersionOpReportsTheDeclaredEngine(t *testing.T) {
	root := repoRoot(t)

	wantFFmpeg, err := buildlist.FFmpegVersion(root)
	if err != nil {
		t.Fatalf("reading the declared FFmpeg version: %v", err)
	}
	wantVocab, err := buildlist.VocabVersion(root)
	if err != nil {
		t.Fatalf("reading the declared vocabulary version: %v", err)
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			res := invoke(t, a, `{"op":"version"}`)
			if res.ExitCode != 0 {
				t.Fatalf("%s: version exited %d, want 0: %s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}
			if strings.TrimSpace(res.Stderr) != "" {
				t.Errorf("%s: version succeeded but wrote to stderr, which carries only failures: %q", a, res.Stderr)
			}

			reply := wantOneJSONLine(t, a, res, "version")

			if got, ok := reply["ffmpeg_version"].(string); !ok || got != wantFFmpeg {
				t.Errorf("%s: version reports ffmpeg_version %v, but build/ffmpeg-version.txt declares %q.\n"+
					"Either this artifact was built from a different version of the repository — check what "+
					"%s points at — or the build did not pick the version file up.",
					a, reply["ffmpeg_version"], wantFFmpeg, engine.ArtifactsEnv)
			}
			// JSON numbers decode as float64; the engine emits an integer.
			if got, ok := reply["vocab_version"].(float64); !ok || int(got) != wantVocab {
				t.Errorf("%s: version reports vocab_version %v, but src/driver.c declares %d.\n"+
					"A vocabulary bump has to reach the artifact before a host can rely on it.",
					a, reply["vocab_version"], wantVocab)
			}
		})
	}
}

// TestMalformedRequestExitsTwo walks the documented exit-`2` surface.
//
// Exit `2` means "fixing the spec" — it is the code a host uses to decide the
// request itself is wrong, as against `1` (a processing failure, not retryable
// unchanged). Every case here is drawn from docs/reference/errors.md, so a change
// that renumbers one of them shows up as a failure rather than as a documentation
// drift nobody notices.
func TestMalformedRequestExitsTwo(t *testing.T) {
	cases := []struct {
		name       string
		arg        string
		wantStderr string
	}{
		{"not JSON at all", `this is not json`, "invalid job spec JSON"},
		{"JSON, but not an object", `[1,2,3]`, "unknown op"},
		{"an unknown op", `{"op":"transcode"}`, "unknown op transcode"},
		{"no op at all", `{}`, "unknown op (none)"},
		{"op is not a string", `{"op":7}`, "unknown op (none)"},

		{"probe without inputs", `{"op":"probe"}`, `probe: "inputs" must be an array`},
		{"probe whose inputs is not an array", `{"op":"probe","inputs":{"path":"a.mp4"}}`, `probe: "inputs" must be an array`},

		{"process with no inputs or outputs", `{"op":"process","inputs":[],"outputs":[]}`, "need at least one input and one output"},
		{"process with an input but no output", `{"op":"process","inputs":[{"path":"a.mp4"}],"outputs":[]}`, "need at least one input and one output"},

		{"frames with no inputs", `{"op":"frames","inputs":[]}`, "need exactly one input"},
		{"frames with two inputs", `{"op":"frames","inputs":[{"path":"a.mp4"},{"path":"b.mp4"}],"select":{"every":1},"path":"f.png"}`, "need exactly one input"},
		{"frames without a select", `{"op":"frames","inputs":[{"path":"a.mp4"}]}`, "`select` object required"},
		{"frames without a path template", `{"op":"frames","inputs":[{"path":"a.mp4"}],"select":{"every":1}}`, "`path` template required"},
		{"frames with two template tokens", `{"op":"frames","inputs":[{"path":"a.mp4"}],"select":{"every":1},"path":"f%03d-%d.png"}`, "zero or one integer template token"},
		{"frames with an unknown codec", `{"op":"frames","inputs":[{"path":"a.mp4"}],"select":{"every":1},"path":"f.png","codec":"definitely-not-a-codec"}`, "unknown or non-image codec"},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			for _, c := range cases {
				res := invoke(t, a, c.arg)
				if res.ExitCode != 2 {
					t.Errorf("%s: %s: exited %d, want 2 (docs/reference/errors.md). stderr: %q",
						a, c.name, res.ExitCode, strings.TrimSpace(res.Stderr))
				}
				if !strings.Contains(res.Stderr, c.wantStderr) {
					t.Errorf("%s: %s: stderr does not carry %q, so the rejection came from somewhere else: %q",
						a, c.name, c.wantStderr, strings.TrimSpace(res.Stderr))
				}
				wantSilentStdout(t, a, res, c.name)
			}
		})
	}
}

// TestVersionGateExitsThree asserts the vocabulary gate, including where it sits
// in the order of operations.
//
// Exit `3` exists so a caller can tell "upgrade the engine" from "fix the job"
// without parsing a message, and the gate deliberately runs BEFORE dispatch on
// every spec. Both halves matter to a host: the code, and the fact that a
// too-new spec is rejected as too-new rather than as anything else.
func TestVersionGateExitsThree(t *testing.T) {
	root := repoRoot(t)
	vocab, err := buildlist.VocabVersion(root)
	if err != nil {
		t.Fatalf("reading the declared vocabulary version: %v", err)
	}

	// N+1 is the assertion that matters: it is the smallest version the engine
	// must refuse, so it cannot pass by accident the way a large number could.
	tooNew := strconv.Itoa(vocab + 1)

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			// A spec stamped with the engine's own version is accepted: the gate
			// rejects what is NEWER, not what is equal.
			res := invoke(t, a, `{"op":"version","version":`+strconv.Itoa(vocab)+`}`)
			if res.ExitCode != 0 {
				t.Errorf("%s: a spec stamped with the engine's own vocabulary (%d) exited %d, want 0: %s",
					a, vocab, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			for _, c := range []struct{ name, arg string }{
				{"one version too new", `{"op":"probe","inputs":[],"version":` + tooNew + `}`},
				{"far too new", `{"op":"probe","inputs":[],"version":9999}`},
				// The gate runs before dispatch, so an unknown op does not get
				// the chance to turn this into a 2. A host that saw 2 here would
				// tell its user to fix a spec that only needs a newer engine.
				{"too new AND an unknown op", `{"op":"transcode","version":9999}`},
			} {
				res := invoke(t, a, c.arg)
				if res.ExitCode != 3 {
					t.Errorf("%s: %s: exited %d, want 3 (version-too-new). stderr: %q",
						a, c.name, res.ExitCode, strings.TrimSpace(res.Stderr))
				}
				if !strings.Contains(res.Stderr, "upgrade ffmpeg-wasi") {
					t.Errorf("%s: %s: stderr does not tell the caller to upgrade the engine: %q",
						a, c.name, strings.TrimSpace(res.Stderr))
				}
				wantSilentStdout(t, a, res, c.name)
			}
		})
	}
}

// TestProcessingFailureExitsOne pins the `1`/`2` boundary from the other side.
//
// A well-formed spec naming an input that will not open is a PROCESSING failure:
// the request was valid, the work could not be done, and the caller cannot fix it
// by editing the spec. Collapsing this to `2` — or to `0` — would tell a host the
// wrong thing about whether to retry, which is the whole reason the codes are
// distinguished.
func TestProcessingFailureExitsOne(t *testing.T) {
	cases := []struct{ name, arg string }{
		{"process with an input that will not open",
			`{"op":"process","inputs":[{"path":"no-such-input.mp4"}],"outputs":[{"path":"out.mp4","video_codec":"mpeg4"}]}`},
		{"frames with an input that will not open",
			`{"op":"frames","inputs":[{"path":"no-such-input.mp4"}],"select":{"every":1},"path":"f%03d.png"}`},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			for _, c := range cases {
				res := invoke(t, a, c.arg)
				if res.ExitCode != 1 {
					t.Errorf("%s: %s: exited %d, want 1 (a processing failure). stderr: %q",
						a, c.name, res.ExitCode, strings.TrimSpace(res.Stderr))
				}
				wantSilentStdout(t, a, res, c.name)
			}
		})
	}
}

// TestProbeToleratesAnUnopenableInput pins INTENDED behaviour that looks like the
// process defects below but is not.
//
// probe reports an unopenable input inside an otherwise normal reply and exits
// `0`, so one bad file in a batch does not lose the results for the others
// (docs/reference/errors.md, the probe section). The resemblance is only skin
// deep: those two exited `0` on a spec they had REJECTED, with empty stdout and
// nothing for a caller to act on. Here stdout carries the reply, the failure is
// named inside it, and exiting `0` is the whole point.
//
// Asserted explicitly so nobody "fixes" this into a non-zero exit on the strength
// of that resemblance — which was a live risk while the two sat side by side, and
// is why they were never lumped together.
func TestProbeToleratesAnUnopenableInput(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			res := invoke(t, a, `{"op":"probe","inputs":[{"path":"no-such-input.mp4"}]}`)
			if res.ExitCode != 0 {
				t.Fatalf("%s: probe of an unopenable input exited %d, want 0 — a bad file in a batch "+
					"must not lose the other results. stderr: %q", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}

			reply := wantOneJSONLine(t, a, res, "probe of an unopenable input")
			inputs, ok := reply["inputs"].([]any)
			if !ok || len(inputs) != 1 {
				t.Fatalf("%s: probe reply carries %v, want one entry under \"inputs\"", a, reply["inputs"])
			}
			entry, ok := inputs[0].(map[string]any)
			if !ok {
				t.Fatalf("%s: probe reply's input entry is %v, want an object", a, inputs[0])
			}
			if msg, ok := entry["error"].(string); !ok || msg == "" {
				t.Errorf("%s: probe exited 0 for an unopenable input without reporting an \"error\" on it, "+
					"so the failure is invisible to the caller: %v", a, entry)
			}
		})
	}
}

// TestProcessRejectsAMalformedOutputEntry asserts the output-entry validation
// rejections, which are malformed requests and exit `2` like every other one.
//
// These were phase B's two known-failing exceptions until the engine was fixed:
// parse_output returned 2 and op_process flattened it with `return rc < 0 ? 1 : 0`,
// so both printed to stderr and then exited `0` with empty stdout — a host keying
// on the exit code read a rejected job as a success that produced no files. Kept as
// its own test rather than folded into TestMalformedRequestExitsTwo, because these
// are the cases where "a malformed request exits 2" was untrue, and a regression
// here deserves to be named rather than to arrive as one row among fifteen.
//
// Three inputs, two defects — missing path and missing codec trip the same check.
//
// Stdout being empty is still asserted. It was the workaround the documentation
// prescribed while the exit code was wrong, and it remains the ABI's rule: a failed
// invocation writes to stderr and leaves stdout alone.
func TestProcessRejectsAMalformedOutputEntry(t *testing.T) {
	cases := []struct{ name, arg, wantStderr string }{
		{
			"an output with no path",
			`{"op":"process","inputs":[{"path":"a.mp4"}],"outputs":[{"video_codec":"mpeg4"}]}`,
			"each output needs path and a video, audio and/or subtitle codec",
		},
		{
			"an output with no codec",
			`{"op":"process","inputs":[{"path":"a.mp4"}],"outputs":[{"path":"out.mp4"}]}`,
			"each output needs path and a video, audio and/or subtitle codec",
		},
		{
			"an output setting both duration and end",
			`{"op":"process","inputs":[{"path":"a.mp4"}],"outputs":[{"path":"out.mp4","video_codec":"mpeg4","duration":5,"end":10}]}`,
			"`duration` and `end` are mutually exclusive",
		},
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			for _, c := range cases {
				res := invoke(t, a, c.arg)
				if res.ExitCode != 2 {
					t.Errorf("%s: %s: exited %d, want 2 (a malformed request). Exiting 0 here is the defect "+
						"this test was written against: the rejection never reaches a host that keys on the "+
						"exit code. stderr: %q", a, c.name, res.ExitCode, strings.TrimSpace(res.Stderr))
				}
				if !strings.Contains(res.Stderr, c.wantStderr) {
					t.Errorf("%s: %s: stderr does not carry %q: %q",
						a, c.name, c.wantStderr, strings.TrimSpace(res.Stderr))
				}
				wantSilentStdout(t, a, res, c.name)
			}
		})
	}
}

// TestReportModeIsNotAJobOp pins the boundary of the one-line-of-JSON rule.
//
// With no argument, or `--report`, the engine prints a human-readable capability
// report instead of dispatching a job — a build smoke test. It is an invocation
// mode, not an op, so it carries no vocabulary implication and is deliberately
// NOT JSON. Stated as an assertion so the stdout clause elsewhere in this file
// reads as the rule it is, with its exceptions known rather than assumed.
func TestReportModeIsNotAJobOp(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			res, err := a.Runner().Run(context.Background(), "--report")
			if err != nil {
				t.Fatalf("%s: invoking --report: %v", a, err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("%s: --report exited %d, want 0: %s", a, res.ExitCode, strings.TrimSpace(res.Stderr))
			}
			if !strings.HasPrefix(res.Stdout, "ffmpeg-wasi engine") {
				t.Errorf("%s: --report does not begin with the engine banner: %q", a, res.Stdout)
			}
			if json.Valid([]byte(strings.TrimSpace(res.Stdout))) {
				t.Errorf("%s: --report printed JSON. The machine-readable dump is --capabilities; "+
					"if the report is now JSON too, say so in docs/reference/driver-invocation-abi.md.", a)
			}
		})
	}
}
