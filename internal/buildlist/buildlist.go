// Package buildlist reads what this repository declares a build should produce:
// the component allowlist for a given (profile, variant), the FFmpeg version, and
// the job-spec vocabulary version.
//
// It does NOT reimplement any of those. It runs build/enable-lists.sh and
// build/ffmpeg-version.sh — the same files the build itself runs — and reads
// AFMPEG_VOCAB_VERSION out of src/driver.c. A Go reimplementation of that shell
// would drift from the build silently, and a conformance check comparing an
// artifact against a stale idea of what was requested asserts nothing useful
// (spec 0036 D3).
//
// Everything here is one side of a conformance assertion: the declaration. The
// other side is the artifact, read through internal/engine.
package buildlist

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
// Where the difference follows a convention it is expressed as a RULE, because a
// rule cannot excuse anything outside the convention it describes. Where upstream
// simply spells a component differently, there is no rule to write and the entry
// goes in configureAliases below.
func (c Claim) RuntimeNames() []string {
	names := []string{c.Name}
	if alias, ok := configureAliases[c]; ok {
		names = append(names, alias)
	}
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

// configureAliases maps a configure component name to the name libav actually
// registers it under, for the cases where the two namespaces differ ad hoc
// rather than by a convention.
//
// This is a table where RuntimeNames' image_*_pipe and pcm_* handling is a rule,
// and the distinction is deliberate. Those two families follow a pattern that can
// be written as a transformation; these simply spell the component differently
// upstream, and there is no rule to write. The first version of this check tried
// to hold the line at rules only. The intermediate profile showed that was not
// tenable.
//
// An entry asserts that a component has two names. It cannot hide a component
// that has genuinely gone, because the check still requires it to be present
// under one of them. What a WRONG entry can do is map a claim onto an unrelated
// component that happens to be present — so every entry cites the upstream
// source it was read from, verified against FFmpeg n8.1.2, and a new one needs
// the same. Do not add an entry to make a red check green without opening the
// source and confirming the two names are the same component.
var configureAliases = map[Claim]string{
	{Kind: "decoder", Name: "movtext"}:    "mov_text",   // libavcodec/movtextdec.c:595
	{Kind: "encoder", Name: "movtext"}:    "mov_text",   // libavcodec/movtextenc.c:707
	{Kind: "encoder", Name: "libvpx_vp8"}: "libvpx",     // libavcodec/libvpxenc.c:2085
	{Kind: "encoder", Name: "libvpx_vp9"}: "libvpx-vp9", // libavcodec/libvpxenc.c:2184
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

// FFmpegVersion returns the FFmpeg version this repository builds, by running
// build/ffmpeg-version.sh — the resolver both Dockerfiles and every pipeline job
// use, reading build/ffmpeg-version.txt (spec 0035 D3).
//
// Running the script rather than parsing the file keeps one resolver: the file
// carries comments and the script decides what counts as the version, so a second
// reader here could disagree with the build about its own version.
//
// CI_COMMIT_TAG is cleared for the child, because the script also enforces that a
// release tag agrees with the file. That guard belongs to the pipeline; a test
// asking "what does this repo build" must not fail on a mismatched tag, or the
// suite would go red for a reason that has nothing to do with any artifact.
func FFmpegVersion(repoRoot string) (string, error) {
	cmd := exec.Command("sh", "build/ffmpeg-version.sh")
	cmd.Dir = repoRoot
	cmd.Env = append(cmd.Environ(), "CI_COMMIT_TAG=")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg-version.sh: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	version := strings.TrimSpace(stdout.String())
	if version == "" {
		return "", fmt.Errorf("ffmpeg-version.sh printed no version")
	}
	return version, nil
}

// vocabDefine matches the engine's vocabulary-version declaration in
// src/driver.c. Anchored to a #define at the start of a line so a mention in a
// comment cannot satisfy it.
var vocabDefine = regexp.MustCompile(`(?m)^#define[ \t]+AFMPEG_VOCAB_VERSION[ \t]+([0-9]+)`)

// VocabVersion returns the job-spec vocabulary version src/driver.c declares.
//
// There is no script to run for this one — the declaration is a #define compiled
// into the engine — so this reads the C source. That is still the build's own
// statement of the answer rather than a copy of it: a constant repeated in Go
// would have to be remembered on every vocabulary bump, and forgetting it would
// make the check pass while comparing the artifact against the wrong number.
func VocabVersion(repoRoot string) (int, error) {
	path := filepath.Join(repoRoot, "src", "driver.c")
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	m := vocabDefine.FindSubmatch(src)
	if m == nil {
		return 0, fmt.Errorf("%s declares no AFMPEG_VOCAB_VERSION", path)
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, fmt.Errorf("%s: AFMPEG_VOCAB_VERSION %q is not a number: %w", path, m[1], err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s: AFMPEG_VOCAB_VERSION is %d, want a positive version", path, n)
	}
	return n, nil
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
