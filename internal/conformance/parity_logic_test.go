package conformance

import (
	"bytes"
	"strings"
	"testing"
)

// The comparison in parity_test.go runs once, after every other test, and
// reports through stderr rather than *testing.T. That makes it the one piece of
// this suite whose own correctness nothing else would notice: if it silently
// compared nothing, or compared everything to itself, the run would look exactly
// like a clean one.
//
// So these tests are pointed at the comparator, not at the engine. They need no
// artefact and never skip.
//
// The tolerance table is passed in rather than read from package state. An
// earlier version had the tolerance test assign to the package-level
// `tolerances` and restore it with t.Cleanup, which raced the other parallel
// tests here and made TestParityCatchesADisagreement fail intermittently.

// twoArtifactsDisagreeing is the minimal shape the comparator exists to catch.
func twoArtifactsDisagreeing() map[string]map[string][]string {
	return map[string]map[string][]string{
		`{"op":"probe"}`: {
			"wasm/lean/lgpl":   {`{"duration_sec":2}`},
			"native/lean/lgpl": {`{"duration_sec":3}`},
		},
	}
}

func TestParityCatchesADisagreement(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	bad := compareParity(&out, twoArtifactsDisagreeing(), nil)

	if !bad {
		t.Fatal("two artefacts answered the same job differently and the comparison passed" +
			" — the instrument does not work")
	}
	for _, want := range []string{"FAIL: parity", "disagree", `duration_sec":2`, `duration_sec":3`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the failure report does not mention %q, so it does not say what diverged:\n%s",
				want, out.String())
		}
	}
}

func TestParityPassesWhenArtifactsAgree(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	agreeing := map[string]map[string][]string{
		`{"op":"probe"}`: {
			"wasm/lean/lgpl":   {`{"duration_sec":2}`},
			"native/lean/lgpl": {`{"duration_sec":2}`},
		},
	}
	if compareParity(&out, agreeing, nil) {
		t.Fatalf("identical replies were reported as a divergence:\n%s", out.String())
	}
}

// A job only one artefact ran has nothing to be compared against. Reporting it
// would be noise; counting it as compared would be a lie about coverage.
func TestParityIgnoresAJobOnlyOneArtifactRan(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	single := map[string]map[string][]string{
		`{"op":"probe"}`: {"wasm/lean/lgpl": {`{"duration_sec":2}`}},
	}
	if compareParity(&out, single, nil) {
		t.Fatal("a job run by a single artefact was reported as a divergence")
	}
	if !strings.Contains(out.String(), "0 cross-comparison(s)") {
		t.Errorf("a single-artefact job was counted as compared:\n%s", out.String())
	}
}

// Instability within one artefact is a different fault from a disagreement
// between two, and the report has to say which it found.
func TestParityCatchesOneArtifactAnsweringInconsistently(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	unstable := map[string]map[string][]string{
		`{"op":"probe"}`: {
			"wasm/lean/lgpl":   {`{"duration_sec":2}`, `{"duration_sec":9}`},
			"native/lean/lgpl": {`{"duration_sec":2}`},
		},
	}
	if !compareParity(&out, unstable, nil) {
		t.Fatal("an artefact answered the same job two different ways and the comparison passed")
	}
	if !strings.Contains(out.String(), "instability") {
		t.Errorf("the report does not distinguish instability from a target disagreement:\n%s",
			out.String())
	}
}

func TestToleranceExcusesADivergenceAndSaysWhy(t *testing.T) {
	t.Parallel()

	tols := []tolerance{{
		Job:      `"op":"probe"`,
		Reason:   "a fabricated tolerance, for this test only",
		Observed: "parity_logic_test.go",
	}}

	var out bytes.Buffer
	if compareParity(&out, twoArtifactsDisagreeing(), tols) {
		t.Fatalf("a divergence covered by a tolerance still failed the run:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "EXCUSED") || !strings.Contains(out.String(), "fabricated tolerance") {
		t.Errorf("an excused divergence was hidden rather than reported with its reason:\n%s",
			out.String())
	}
}

// D11: `path` echoes the request, so the two targets differ on it by
// construction. Excluding it is what lets everything else be compared.
func TestPathIsExcludedFromTheComparedValue(t *testing.T) {
	t.Parallel()

	wasm, ok := canonical([]byte(`{"inputs":[{"path":"/in.wav","format":"wav"}]}`))
	if !ok {
		t.Fatal("canonical rejected valid JSON")
	}
	native, ok := canonical([]byte(`{"inputs":[{"path":"in.wav","format":"wav"}]}`))
	if !ok {
		t.Fatal("canonical rejected valid JSON")
	}

	if wasm != native {
		t.Errorf("path was not excluded, so every comparison would diverge:\n wasm:   %s\n native: %s",
			wasm, native)
	}
	if strings.Contains(wasm, "path") {
		t.Errorf("the compared value still carries a path field: %s", wasm)
	}
	if !strings.Contains(wasm, "wav") {
		t.Errorf("excluding path removed more than it should have: %s", wasm)
	}
}

// The KEY keeps paths — they say which file the job touched — but must spell
// them identically across targets, or the same job files under two keys and
// nothing is ever compared. That failure would look like a pass.
func TestJobKeyGroupsTheSameJobAcrossTargets(t *testing.T) {
	t.Parallel()

	wasm, _ := canonicalKey([]byte(`{"op":"probe","inputs":[{"path":"/in.wav"}]}`))
	native, _ := canonicalKey([]byte(`{"op":"probe","inputs":[{"path":"in.wav"}]}`))

	if wasm != native {
		t.Errorf("the same job files under two keys, so it would never be compared:\n"+
			" wasm:   %s\n native: %s", wasm, native)
	}
	if !strings.Contains(wasm, "in.wav") {
		t.Errorf("the key dropped the path, so different jobs would collide into one bucket: %s", wasm)
	}

	other, _ := canonicalKey([]byte(`{"op":"probe","inputs":[{"path":"other.wav"}]}`))
	if wasm == other {
		t.Error("jobs against different files share a key — their replies would be compared" +
			" against each other and diverge for the wrong reason")
	}
}
