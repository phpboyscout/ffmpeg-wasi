// Package buildlist reads the component allowlist the build claims for a given
// (profile, variant).
//
// It does NOT reimplement the composition. It runs build/enable-lists.sh, which
// is the same file build/libav.sh sources to configure FFmpeg, and parses the
// flags that come back. A Go reimplementation of that shell would drift from the
// build silently, and a conformance check comparing an artifact against a stale
// idea of what was requested asserts nothing useful (spec 0036 D3).
package buildlist

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Claim is one component the build asked configure for.
type Claim struct {
	Kind string // decoder | encoder | muxer | demuxer | filter | bsf | protocol | parser
	Name string
}

// String renders the claim as the configure flag it came from.
func (c Claim) String() string { return "--enable-" + c.Kind + "=" + c.Name }

// RuntimeNames returns every name this claim may legitimately appear under in a
// built artifact's --capabilities output.
//
// FFmpeg has TWO NAMESPACES for formats and they do not always agree. configure
// identifies a component by its source symbol — ff_<name>_demuxer — while the
// running library reports AVInputFormat.name, the public name you would pass to
// -f. For most components these coincide; for two families they systematically
// do not:
//
//	--enable-demuxer=image_png_pipe  → libavformat/img2dec.c  .name = "png_pipe"
//	--enable-demuxer=pcm_s16le       → libavformat/pcmdec.c   .name = "s16le"
//
// All four such components in the lean profile are genuinely present; a check
// comparing configure names against runtime names called them missing. Left
// unhandled that is fatal to the whole exercise: a conformance check that is
// permanently red for a reason everyone knows about is a check that gets
// switched off.
//
// These are expressed as RULES rather than as a table of aliases deliberately. A
// rule describes the naming convention and cannot excuse anything outside it; a
// hand-maintained alias table is one typo away from silently excusing a
// component that really did disappear, which is the failure this check exists to
// catch.
func (c Claim) RuntimeNames() []string {
	names := []string{c.Name}
	if c.Kind != "demuxer" && c.Kind != "muxer" {
		return names
	}
	// image_<x>_pipe → <x>_pipe
	if rest, ok := strings.CutPrefix(c.Name, "image_"); ok && strings.HasSuffix(rest, "_pipe") {
		names = append(names, rest)
	}
	// pcm_<x> → <x>  (the raw-PCM (de)muxers, not the pcm_* codecs, which keep
	// their prefix — hence the kind guard above.)
	if rest, ok := strings.CutPrefix(c.Name, "pcm_"); ok {
		names = append(names, rest)
	}
	return names
}

// kinds are the --enable-<kind>= flags that name an individual component. Other
// --enable-* flags (--enable-gpl, --enable-libx264, --enable-small) switch on a
// library or a build option rather than claiming a named component, so they are
// not conformance claims and are ignored.
var kinds = map[string]bool{
	"decoder": true, "encoder": true, "muxer": true, "demuxer": true,
	"filter": true, "bsf": true, "protocol": true, "parser": true,
}

// Load runs build/enable-lists.sh for one (profile, variant, target) and returns
// every component it claims.
//
// repoRoot is the repository root; target is "wasm" or "native" and matters
// because the full profile is native-only.
func Load(repoRoot, profile, variant, target string) ([]Claim, error) {
	cmd := exec.Command("sh", "build/enable-lists.sh")
	cmd.Dir = repoRoot
	cmd.Env = append(cmd.Environ(),
		"PROFILE="+profile,
		"VARIANT="+variant,
		"TARGET="+target,
		"PRINT_COMPONENT_FLAGS=1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("enable-lists.sh %s/%s/%s: %w: %s",
			target, profile, variant, err, strings.TrimSpace(stderr.String()))
	}

	return parse(stdout.String()), nil
}

// LoadGPLOnly returns the components upstream gates behind --enable-gpl, which
// configure therefore drops from an lgpl build without warning.
//
// Read from build/enable-lists.sh for the same reason as Load: the annotation
// belongs beside the list that would otherwise mislead its next reader, not in a
// second list here that could drift from it.
func LoadGPLOnly(repoRoot string) (map[Claim]bool, error) {
	cmd := exec.Command("sh", "build/enable-lists.sh")
	cmd.Dir = repoRoot
	cmd.Env = append(cmd.Environ(), "PRINT_GPL_ONLY_COMPONENTS=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("enable-lists.sh gpl-only: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out := map[Claim]bool{}
	for _, field := range strings.Fields(stdout.String()) {
		kind, name, ok := strings.Cut(field, "=")
		if !ok || !kinds[kind] || name == "" {
			return nil, fmt.Errorf("enable-lists.sh gpl-only: cannot parse entry %q (want <kind>=<name>)", field)
		}
		out[Claim{Kind: kind, Name: name}] = true
	}
	return out, nil
}

// parse pulls the component claims out of a configure flag string.
//
// One flag can name several components — --enable-decoder=h264,hevc,vp8 — so a
// claim is one (kind, name) pair, not one flag.
func parse(flags string) []Claim {
	var out []Claim
	seen := map[Claim]bool{}

	for _, field := range strings.Fields(flags) {
		spec, ok := strings.CutPrefix(field, "--enable-")
		if !ok {
			continue
		}
		kind, names, ok := strings.Cut(spec, "=")
		if !ok || !kinds[kind] {
			continue
		}
		for _, name := range strings.Split(names, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			c := Claim{Kind: kind, Name: name}
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}
