package conformance

import "testing"

// TestJudgeSandboxState pins the gate both sandbox tests share, without a driver,
// so it is exercised on every pipeline rather than only those that build one.
func TestJudgeSandboxState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state    string
		required bool
		want     sandboxVerdict
	}{
		{"landlock", false, confined},
		{"landlock", true, confined},
		{"unavailable", false, unconfinedHere},
		{"unavailable", true, unconfinedOnARequiringRunner},
		{"", false, unreported},
		{"", true, unreported},
		{"disabled", false, unreported},
		{"disabled", true, unreported},
	}
	for _, c := range cases {
		if got := judgeSandboxState(c.state, c.required); got != c.want {
			t.Errorf("judgeSandboxState(%q, %v) = %d, want %d", c.state, c.required, got, c.want)
		}
	}
}

// TestTheRunnerOffersLandlock asks the kernel directly, so the job log says
// whether this runner can prove containment even when no native driver was built.
func TestTheRunnerOffersLandlock(t *testing.T) {
	t.Parallel()

	// Read the requirement first, so a malformed value fails on a Landlock host too.
	required := landlockRequired(t)

	abi, err := landlockABI()
	if err == nil {
		t.Logf("this host offers Landlock ABI v%d", abi)
		return
	}

	if required {
		t.Fatalf("this host offers no Landlock (%v), yet %s says this runner has it",
			err, requireLandlockEnv)
	}
	t.Skipf("this host offers no Landlock (%v), so native drivers run unconfined here", err)
}
