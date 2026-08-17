package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Artifact is one built engine binary, together with the (profile, variant) it
// was built for.
//
// The pair matters and is not decoration: the same enable list legitimately
// produces different components under lgpl and gpl — upstream carries
// cropdetect_filter_deps="gpl", so that filter is absent from every lgpl build
// while its neighbours on the same line are present. A conformance check that
// knew only the profile would have to call that a regression (spec 0036 D3).
type Artifact struct {
	Path    string
	Profile string // lean | intermediate | full
	Variant string // lgpl | gpl
	Target  string // wasm | native
}

// Runner builds the right invoker for this artifact's target.
func (a Artifact) Runner() Runner {
	if a.Target == "native" {
		return Native{Path: a.Path}
	}
	return WASM{Path: a.Path}
}

// String names the artifact as "<target>/<profile>/<variant>", which is what
// subtests are named after, so a failure says which of the ten it was.
func (a Artifact) String() string {
	return fmt.Sprintf("%s/%s/%s", a.Target, a.Profile, a.Variant)
}

// ArtifactsEnv names the directory holding built artifacts. A test that finds it
// unset SKIPS — nobody should need a two-hour FFmpeg build to run `go test`
// (spec 0036 D6).
const ArtifactsEnv = "FFMPEG_WASI_ARTIFACTS"

// Discover lists the artifacts in the directory named by FFMPEG_WASI_ARTIFACTS,
// deriving each one's profile, variant and target from its filename.
//
// Reading the identity off the name rather than taking it from more environment
// variables is what stops a test being pointed at an artifact that is not what
// the caller believes it is — the failure mode afmpeg's resolvers (!152/!153)
// were raised to fix. A file that does not parse is ignored rather than guessed
// at, for the same reason.
//
// Returns an empty slice when the variable is unset; the caller skips.
func Discover() ([]Artifact, error) {
	dir := os.Getenv(ArtifactsEnv)
	if dir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s=%s: %w", ArtifactsEnv, dir, err)
	}

	var out []Artifact
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		a, ok := parseArtifactName(e.Name())
		if !ok {
			continue
		}
		a.Path = filepath.Join(dir, e.Name())
		out = append(out, a)
	}
	return out, nil
}

// parseArtifactName recovers (target, profile, variant) from a release artifact
// name. The naming is spec 0022's: the lean profile keeps the legacy name with
// no profile segment, and anything else carries the profile before the variant.
//
//	ffmpeg-wasi-lgpl.wasm                             → wasm   lean         lgpl
//	ffmpeg-wasi-intermediate-gpl.wasm                 → wasm   intermediate gpl
//	ffmpeg-wasi-driver-linux-amd64-lgpl               → native lean         lgpl
//	ffmpeg-wasi-driver-linux-amd64-full-gpl           → native full         gpl
func parseArtifactName(name string) (Artifact, bool) {
	if strings.HasSuffix(name, ".gz") {
		return Artifact{}, false // the compressed copies track the raw ones
	}

	var a Artifact
	rest := name
	switch {
	case strings.HasPrefix(rest, "ffmpeg-wasi-driver-linux-amd64-"):
		a.Target = "native"
		rest = strings.TrimPrefix(rest, "ffmpeg-wasi-driver-linux-amd64-")
	case strings.HasPrefix(rest, "ffmpeg-wasi-") && strings.HasSuffix(rest, ".wasm"):
		a.Target = "wasm"
		rest = strings.TrimSuffix(strings.TrimPrefix(rest, "ffmpeg-wasi-"), ".wasm")
	default:
		return Artifact{}, false
	}

	// What remains is "<variant>" (lean) or "<profile>-<variant>".
	switch rest {
	case "lgpl", "gpl":
		a.Profile, a.Variant = "lean", rest
		return a, true
	}
	profile, variant, ok := strings.Cut(rest, "-")
	if !ok {
		return Artifact{}, false
	}
	switch profile {
	case "intermediate", "full":
	default:
		return Artifact{}, false
	}
	switch variant {
	case "lgpl", "gpl":
	default:
		return Artifact{}, false
	}
	a.Profile, a.Variant = profile, variant
	return a, true
}

// Capabilities is the engine's --capabilities reply: what this binary actually
// carries, by component kind, plus its identity.
type Capabilities struct {
	VocabVersion  int      `json:"vocab_version"`
	FFmpegVersion string   `json:"ffmpeg_version"`
	Encoders      []string `json:"encoders"`
	Decoders      []string `json:"decoders"`
	Muxers        []string `json:"muxers"`
	Demuxers      []string `json:"demuxers"`
	Filters       []string `json:"filters"`
	BSFs          []string `json:"bsfs"`
	Protocols     []string `json:"protocols"`
	Parsers       []string `json:"parsers"`
}

// ByKind indexes the component lists by the configure flag kind that claims
// them, so a caller can look up "--enable-decoder=" entries without a switch.
// The keys match build/enable-lists.sh's --enable-<kind>= spelling.
func (c Capabilities) ByKind() map[string][]string {
	return map[string][]string{
		"encoder":  c.Encoders,
		"decoder":  c.Decoders,
		"muxer":    c.Muxers,
		"demuxer":  c.Demuxers,
		"filter":   c.Filters,
		"bsf":      c.BSFs,
		"protocol": c.Protocols,
		"parser":   c.Parsers,
	}
}

// Query runs --capabilities against an artifact and parses the reply.
func Query(ctx context.Context, r Runner) (Capabilities, error) {
	res, err := r.Run(ctx, "--capabilities")
	if err != nil {
		return Capabilities{}, fmt.Errorf("%s: %w", r.Describe(), err)
	}
	if res.ExitCode != 0 {
		return Capabilities{}, fmt.Errorf("%s: --capabilities exited %d: %s",
			r.Describe(), res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var c Capabilities
	if err := json.Unmarshal([]byte(res.Stdout), &c); err != nil {
		return Capabilities{}, fmt.Errorf("%s: parse --capabilities: %w", r.Describe(), err)
	}
	return c, nil
}
