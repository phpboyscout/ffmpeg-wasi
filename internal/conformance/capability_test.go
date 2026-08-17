// Package conformance holds spec 0036's engine test suite.
//
// Phase A — capability conformance: every component build/enable-lists.sh claims
// for a (profile, variant) must actually be present in the artifact built from
// it. This is the permanent, mechanical answer to "do the profile variants still
// make sense", and it catches a silently dropped component on ANY FFmpeg bump,
// not just the n9 one it was written for.
//
// Tests SKIP when no artifacts are available. Running them needs
// FFMPEG_WASI_ARTIFACTS pointing at a directory of built engines; nobody should
// need a two-hour FFmpeg build to run `go test ./...` (spec 0036 D6).
package conformance

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/buildlist"
	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// repoRoot locates the repository from this file's own path, so the tests find
// build/enable-lists.sh regardless of the working directory `go test` ran in.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// artifacts discovers what is available, or skips.
func artifacts(t *testing.T) []engine.Artifact {
	t.Helper()
	found, err := engine.Discover()
	if err != nil {
		t.Fatalf("discovering artifacts: %v", err)
	}
	if len(found) == 0 {
		t.Skipf("no engine artifacts: set %s to a directory of built artifacts "+
			"(e.g. dist/ after `just build`, or the build stage's artifacts in CI)", engine.ArtifactsEnv)
	}
	return found
}

// TestCapabilityConformance asserts every claimed component is present.
//
// The assertion is one-directional by design. A component the build asked for
// and did not get is a FAILURE — that is a capability silently lost. A component
// present but never asked for is only NOTED: FFmpeg pulls dependencies in of its
// own accord, so treating those as failures would make the check noisy enough to
// be ignored (spec 0036 D3).
func TestCapabilityConformance(t *testing.T) {
	root := repoRoot(t)

	gplOnly, err := buildlist.LoadGPLOnly(root)
	if err != nil {
		t.Fatalf("loading the gpl-gated component list: %v", err)
	}

	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			claims, err := buildlist.Load(root, a.Profile, a.Variant, a.Target)
			if err != nil {
				t.Fatalf("loading the build's claims: %v", err)
			}
			if len(claims) == 0 {
				t.Fatalf("the build claims no components at all for %s — the allowlist reader is broken, "+
					"which would make this check vacuously pass", a)
			}

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("querying capabilities: %v", err)
			}

			present := map[buildlist.Claim]bool{}
			for kind, names := range caps.ByKind() {
				for _, n := range names {
					present[buildlist.Claim{Kind: kind, Name: n}] = true
				}
			}

			var missing, gated []string
			for _, c := range claims {
				// A claim may appear under more than one name: configure and the
				// running library disagree about what some formats are called.
				found := false
				for _, n := range c.RuntimeNames() {
					if present[buildlist.Claim{Kind: c.Kind, Name: n}] {
						found = true
						break
					}
				}
				if found {
					continue
				}
				// Upstream gates some components behind --enable-gpl, so their
				// absence from an lgpl build is correct rather than a regression.
				if a.Variant == "lgpl" && gplOnly[c] {
					gated = append(gated, c.String())
					continue
				}
				missing = append(missing, c.String())
			}

			if len(gated) > 0 {
				sort.Strings(gated)
				t.Logf("%d component(s) absent because upstream gates them on --enable-gpl, as expected for %s: %v",
					len(gated), a.Variant, gated)
			}

			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("%s: the build claims %d component(s) the artifact does not carry:\n  %v\n"+
					"Each is either a capability lost in this change, or a component upstream now gates on a "+
					"licence — in which case add it to GPL_ONLY_COMPONENTS in build/enable-lists.sh with the "+
					"upstream reason, rather than removing this assertion.",
					a, len(missing), missing)
			}
		})
	}
}

// TestArtifactIdentity pins what an artifact says it is. It is nearly free —
// --capabilities already reports both — and it turns "which engine did that test
// actually run against?" from an assumption into a recorded fact, which is the
// question every ambiguous result in this programme has started with.
func TestArtifactIdentity(t *testing.T) {
	for _, a := range artifacts(t) {
		t.Run(a.String(), func(t *testing.T) {
			t.Parallel()

			caps, err := engine.Query(context.Background(), a.Runner())
			if err != nil {
				t.Fatalf("querying capabilities: %v", err)
			}
			if caps.FFmpegVersion == "" {
				t.Error("the engine reports no ffmpeg_version")
			}
			if caps.VocabVersion <= 0 {
				t.Errorf("vocab_version is %d, want a positive version", caps.VocabVersion)
			}
			t.Logf("%s: FFmpeg %s, job-spec vocabulary %d", a, caps.FFmpegVersion, caps.VocabVersion)
		})
	}
}
