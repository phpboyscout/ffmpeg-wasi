// Parity — spec 0037 phase D1. Do two artefacts built from the same src/ give
// the same answers?
//
// Phases A–C already run every property against every artefact, and each of
// those tests asserts locally and throws the reply away. Nothing compares the
// replies to each other, so an engine where both targets work and *disagree*
// passes the entire suite. Spec 0038 shipped the n9.0.1 bump with that stated as
// a known blind spot; this closes it.
//
// # Why this records instead of running its own jobs
//
// The engine work is already being done (0037 §6 OQ3). A standalone parity layer
// would define its own jobs and run a second full pass across ten artefacts —
// paying twice to execute the same jobs, in order to avoid touching the tests
// that already execute them. So runJob records what it already has, and the
// comparison happens once at the end. No additional engine invocation.
//
// The cost, accepted and worth watching: a phase C test is no longer entirely
// self-contained. Recording is deliberately *additive* — every local assertion
// stays exactly where it was, so a phase C test still fails on its own terms —
// and the key a reply is filed under is the job spec itself, so a reader can see
// what is being compared without a registry to consult.
//
// # Why the comparison is not itself a Test function
//
// It needs every recording to exist before it can run, and Go offers no barrier
// between top-level parallel tests. TestMain is the only place that is reliably
// after all of them. The cost is that failures are printed rather than reported
// through *testing.T, so the output below does the work a test failure message
// normally would.
package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"gitlab.com/phpboyscout/ffmpeg-wasi/internal/engine"
)

// parity collects one reply per (job, artefact). The job spec is the key, which
// is what makes the comparison meaningful without any test declaring an
// intention: two artefacts that ran the same job are, by construction, comparable.
var parity = struct {
	mu sync.Mutex
	// job spec (paths normalised) -> artefact -> replies seen, in order.
	seen map[string]map[string][]string
}{seen: map[string]map[string][]string{}}

// recordForParity files a reply under the job that produced it. Called from
// runJob, which is the single point every behavioural job passes through.
//
// Both the key and the value have paths normalised, for different reasons. In
// the KEY, so the same logical job filed by two targets lands in one bucket —
// the workspace hands WASM "/in.wav" and native "in.wav" for the same file. In
// the VALUE, because `path` echoes the request (spec 0037 D11).
func recordForParity(a engine.Artifact, spec, reply []byte) {
	key, ok := canonicalKey(spec)
	if !ok {
		return // not JSON; nothing to compare it with
	}
	val, ok := canonical(reply)
	if !ok {
		val = strings.TrimSpace(string(reply))
	}

	parity.mu.Lock()
	defer parity.mu.Unlock()
	if parity.seen[key] == nil {
		parity.seen[key] = map[string][]string{}
	}
	parity.seen[key][a.String()] = append(parity.seen[key][a.String()], val)
}

// canonical renders JSON with map keys sorted and the fields spec 0037 D11
// excludes removed, so two renderings are comparable as strings.
func canonical(b []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", false
	}
	out, err := json.Marshal(scrub(v))
	if err != nil {
		return "", false
	}
	return string(out), true // encoding/json sorts map keys on marshal
}

// excluded names the fields dropped before comparison, each with the reason it
// carries no information about whether the targets agree (spec 0037 D11).
//
// This is a fixed list, not a rule that learns what to ignore from what
// currently differs. A mechanism that adapted would have silently absorbed the
// errno divergence in 0037 §2.4, which is exactly the thing worth catching.
var excluded = map[string]string{
	"path": "echoes the request; the workspace addresses files differently per target " +
		"(\"/in.wav\" vs \"in.wav\"), so it differs by construction",
}

// scrub removes the excluded fields anywhere they appear, and strips the leading
// slash from any remaining path-shaped string so a key groups across targets.
func scrub(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if _, drop := excluded[k]; drop {
				continue
			}
			out[k] = scrub(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrub(val)
		}
		return out
	default:
		return v
	}
}

// canonicalKey is scrub plus path normalisation: the KEY keeps paths (they say
// which file the job touched) but must spell them the same way on both targets.
func canonicalKey(b []byte) (string, bool) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", false
	}
	out, err := json.Marshal(normalisePaths(v))
	if err != nil {
		return "", false
	}
	return string(out), true
}

func normalisePaths(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "path" {
				if s, ok := val.(string); ok {
					out[k] = strings.TrimPrefix(s, "/")
					continue
				}
			}
			out[k] = normalisePaths(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalisePaths(val)
		}
		return out
	default:
		return v
	}
}

// tolerance is a permitted divergence: a difference that is known, legitimate,
// and explained (spec 0037 D12).
//
// The table starts EMPTY and stays that way until a real divergence is observed.
// Every row cites the observation that produced it, and a row without a Reason is
// not accepted — the point of the table is the reasoning, not the suppression.
//
// Note what did NOT need a row: 0037 §2.5 measured a real WASM/native difference
// (three FLAC frames, 13 bytes) and it is invisible here, because at this
// comparison's level the two are indistinguishable. A tolerance is for something
// this comparison can actually see.
type tolerance struct {
	Job      string // substring of the canonical job key it applies to; "" = any
	Reason   string // why this divergence is legitimate — required
	Observed string // where it was first seen
}

var tolerances []tolerance

func (t tolerance) applies(job string) bool {
	return t.Job == "" || strings.Contains(job, t.Job)
}

// TestMain runs the suite, then compares everything phase C recorded.
func TestMain(m *testing.M) {
	code := m.Run()
	parity.mu.Lock()
	seen := parity.seen
	parity.mu.Unlock()
	if bad := compareParity(os.Stderr, seen); bad && code == 0 {
		code = 1
	}
	os.Exit(code)
}

// compareParity reports every job whose artefacts did not agree, and returns
// whether any of them was a failure. It writes to w rather than a *testing.T
// because there is no test still running by the time it can be correct.
func compareParity(w io.Writer, seen map[string]map[string][]string) bool {
	if len(seen) == 0 {
		return false // nothing ran; the artefact-backed tests skipped
	}

	type finding struct{ job, detail string }
	var findings, excused []finding
	compared, jobs := 0, 0

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, job := range keys {
		byArtifact := seen[job]
		if len(byArtifact) < 2 {
			continue // only one artefact ran this job; nothing to compare against
		}
		jobs++

		names := make([]string, 0, len(byArtifact))
		for n := range byArtifact {
			names = append(names, n)
		}
		sort.Strings(names)

		// An artefact that answered the same job differently on two runs is a
		// finding in its own right, and a different one — it is not a
		// disagreement between targets but instability within one.
		for _, n := range names {
			replies := byArtifact[n]
			for i := 1; i < len(replies); i++ {
				if replies[i] != replies[0] {
					findings = append(findings, finding{job, fmt.Sprintf(
						"%s answered the SAME job differently on two runs — this is instability "+
							"within one artefact, not a target disagreement\n    run 1: %s\n    run 2: %s",
						n, truncate(replies[0]), truncate(replies[i]))})
					break
				}
			}
		}

		base, baseName := byArtifact[names[0]][0], names[0]
		for _, n := range names[1:] {
			compared++
			got := byArtifact[n][0]
			if got == base {
				continue
			}
			f := finding{job, fmt.Sprintf("%s and %s disagree\n    %s: %s\n    %s: %s",
				baseName, n, baseName, truncate(base), n, truncate(got))}

			if excusedBy(job) != "" {
				excused = append(excused, f)
				continue
			}
			findings = append(findings, f)
		}
	}

	fmt.Fprintf(w, "\nparity (spec 0037 D1): %d job(s) run by two or more artefacts, "+
		"%d cross-comparison(s), %d divergence(s), %d excused by the tolerance table\n",
		jobs, compared, len(findings), len(excused))

	for _, f := range excused {
		fmt.Fprintf(w, "  EXCUSED %s\n    %s\n    reason: %s\n", f.job, f.detail, excusedBy(f.job))
	}
	if len(findings) == 0 {
		return false
	}

	fmt.Fprintf(w, "\n--- FAIL: parity\n")
	for _, f := range findings {
		fmt.Fprintf(w, "  job: %s\n    %s\n", f.job, f.detail)
	}
	fmt.Fprintf(w, "\n  Two artefacts built from the same src/ gave different answers.\n"+
		"  If this difference is legitimate, add a tolerance to `tolerances` in\n"+
		"  internal/conformance/parity_test.go WITH the reason it is legitimate and\n"+
		"  where it was observed (spec 0037 D12). If it is not, the engine has a bug\n"+
		"  that only shows up on one target.\n")
	return true
}

func excusedBy(job string) string {
	for _, t := range tolerances {
		if t.applies(job) {
			return t.Reason + " (observed: " + t.Observed + ")"
		}
	}
	return ""
}

func truncate(s string) string {
	const max = 400
	if len(s) <= max {
		return s
	}
	return s[:max] + "… (truncated)"
}
