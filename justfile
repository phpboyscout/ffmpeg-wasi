set dotenv-load

# Build a variant locally (needs Docker). variant=lgpl|gpl, profile=lean|intermediate (spec 0022)
[default]
build variant="lgpl" profile="lean":
    docker build -f build/Dockerfile --build-arg VARIANT={{variant}} --build-arg PROFILE={{profile}} --target artifact -o dist .

# Run a built engine under wazero (prints the capability report). profile=lean
# keeps the legacy artifact name; any other profile carries it in the name.
[doc("Run a built engine under wazero (prints the capability report)")]
run variant="lgpl" profile="lean":
    #!/bin/sh
    if [ "{{profile}}" = lean ]; then M="dist/ffmpeg-wasi-{{variant}}.wasm"
    else M="dist/ffmpeg-wasi-{{profile}}-{{variant}}.wasm"; fi
    go run ./tools/run "$M"

# Run the engine test suite (spec 0036). Skips every artifact-backed test unless
# `artifacts` names a directory of built engines — `go test ./...` alone should
# never require a two-hour FFmpeg build.
[doc("Run the engine test suite (spec 0036)")]
test artifacts="dist":
    #!/bin/sh
    # An artifacts directory that does not exist means "none built yet", so run
    # without the variable and let the tests skip. A directory that DOES exist is
    # passed through and any problem reading it is a hard error -- pointing the
    # suite at the wrong place must never look like a pass.
    if [ -d "{{artifacts}}" ]; then
      dir="$(cd "{{artifacts}}" && pwd)"
      # Say which binaries these are and when they were built. Nothing in a test
      # result reveals an artefact's age, and a matrix built at different times is
      # not one matrix: the parity layer compares them against each other, so a
      # months-old artefact beside a fresh one reports as an engine defect. That
      # is a real failure to have chased, and this line is what would have
      # answered it in a glance.
      echo "testing against $dir:" >&2
      ls -lt --time-style=long-iso "$dir" | awk 'NR > 1 { print "  " $6 " " $7 "  " $8 }' >&2
      FFMPEG_WASI_ARTIFACTS="$dir" go test ./... -v
    else
      echo "note: {{artifacts}}/ does not exist -- artifact-backed tests will skip" >&2
      go test ./... -v
    fi

# Print what a built artifact actually carries, by component kind. The same
# ground truth `just test` asserts the build's allowlist against.
[doc("Print what a built artifact actually carries, by component kind")]
@capabilities artifact:
    #!/bin/sh
    case "{{artifact}}" in
      *.wasm) go run ./tools/run "{{artifact}}" --capabilities ;;
      *)      "{{artifact}}" --capabilities ;;
    esac

# Show the components a (profile, variant) claims — the other side of `just test`
@show-claims profile="lean" variant="lgpl" target="wasm":
    PROFILE={{profile}} VARIANT={{variant}} TARGET={{target}} PRINT_COMPONENT_FLAGS=1 sh build/enable-lists.sh

# Lint the build scripts.
#
# Through the image .gitlab-ci.yml pins, not whatever shellcheck is on PATH: a
# local version that disagrees with CI's makes `just ci` predict the wrong
# answer, which is the one thing it exists not to do. The tag is read out of the
# pipeline rather than repeated here, because Renovate tracks it there and a
# second copy would drift the moment it bumps.
[doc("Lint the build scripts, with the shellcheck CI pins")]
lint:
    #!/bin/sh
    set -eu
    image="$(sed -n 's|^[[:space:]]*name: \(koalaman/shellcheck-alpine:[^[:space:]]*\)[[:space:]]*$|\1|p' .gitlab-ci.yml | head -n 1)"
    [ -n "$image" ] || { echo "lint: found no shellcheck image in .gitlab-ci.yml" >&2; exit 1; }
    docker run --rm -v "$PWD:/mnt" -w /mnt --entrypoint shellcheck "$image" build/*.sh

# Resolve the FFmpeg version this build targets, the way CI does before anything
# expensive runs (spec 0035 D3).
[doc("Print the FFmpeg version this build targets")]
@version:
    sh build/ffmpeg-version.sh

# Everything CI gates on, in the order CI runs it, so a green run here predicts a
# green pipeline rather than merely suggesting one.
#
# It is deliberately no MORE than CI gates on. A local target that fails where the
# pipeline would pass teaches people to skip it, and one that passes where the
# pipeline would fail is worse than nothing. `test` is the same recipe CI's job
# runs, so artefact-backed tests skip here exactly as they do there unless
# `artifacts` names built engines.
[doc("Everything CI gates on: lint, version, tests")]
ci artifacts="dist": lint version (test artifacts)
    @echo "ci: shellcheck, ffmpeg-version and the test suite all passed"

# Serve the docs site locally
docs-serve ARGS="":
    zensical serve {{ARGS}}
