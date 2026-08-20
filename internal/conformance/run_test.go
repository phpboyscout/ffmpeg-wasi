package conformance

import (
	"context"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// One rule, in one place: A SIGNALLED PROCESS IS NOT A RESULT.
//
// This exists because the same mistake has now been made three times, by three
// different tests, and each time it hid a real defect:
//
//   - #15's test asserted "the exit code is not zero". The engine was being
//     killed by SIGPIPE, Go reports that as -1, and the ticket was recorded as
//     NOT REPRODUCED on the strength of a test passing against a corpse.
//   - #49's test did the same and was satisfied by a SEGFAULT — exit 139 — on a
//     probe with no `path`.
//   - #20's first test read the harness killing a hung job as the engine
//     reporting an error, and passed against the unfixed engine.
//
// Every one of those was written by someone who knew about the previous one. So
// the check does not belong in the tests; it belongs on the path all of them take.
//
// runAndCheck is that path. It fails the test on a signal, naming it, before any
// caller gets the chance to interpret an exit code. Prefer it to calling Run
// directly — a new test that reaches for the runner itself is re-opening this.
func runAndCheck(t *testing.T, r engine.Runner, ctx context.Context, spec, what string) engine.Result {
	t.Helper()

	res, err := r.Run(ctx, spec)
	if ctx.Err() != nil {
		t.Fatalf("%s: %s did not finish — it HUNG rather than failing, so any assertion "+
			"about its exit code would be about the kill signal, not the engine.\nspec: %s",
			r.Describe(), what, spec)
	}
	if err != nil {
		t.Fatalf("%s: %s: invoking: %v", r.Describe(), what, err)
	}
	if res.Signal != "" {
		t.Fatalf("%s: %s was KILLED BY %s.\nThat is not the engine reporting anything — Go "+
			"reports a signalled process as a non-zero code, so a check spelled \"it did not "+
			"exit 0\" would be satisfied by this corpse.\nspec: %s",
			r.Describe(), what, res.Signal, spec)
	}
	return res
}
