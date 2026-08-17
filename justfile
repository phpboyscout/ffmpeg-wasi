set dotenv-load

# Build a variant locally (needs Docker). variant=lgpl|gpl, profile=lean|intermediate (spec 0022)
[default]
build variant="lgpl" profile="lean":
    docker build -f build/Dockerfile --build-arg VARIANT={{variant}} --build-arg PROFILE={{profile}} --target artifact -o dist .

# Run a built engine under wazero (prints the capability report). profile=lean
# keeps the legacy artifact name; any other profile carries it in the name.
run variant="lgpl" profile="lean":
    #!/bin/sh
    if [ "{{profile}}" = lean ]; then M="dist/ffmpeg-wasi-{{variant}}.wasm"
    else M="dist/ffmpeg-wasi-{{profile}}-{{variant}}.wasm"; fi
    go run ./tools/run "$M"

# Run the engine test suite (spec 0036). Skips every artifact-backed test unless
# `artifacts` names a directory of built engines — `go test ./...` alone should
# never require a two-hour FFmpeg build.
test artifacts="dist":
    #!/bin/sh
    # An artifacts directory that does not exist means "none built yet", so run
    # without the variable and let the tests skip. A directory that DOES exist is
    # passed through and any problem reading it is a hard error -- pointing the
    # suite at the wrong place must never look like a pass.
    if [ -d "{{artifacts}}" ]; then
      FFMPEG_WASI_ARTIFACTS="$(cd "{{artifacts}}" && pwd)" go test ./... -v
    else
      echo "note: {{artifacts}}/ does not exist -- artifact-backed tests will skip" >&2
      go test ./... -v
    fi

# Print what a built artifact actually carries, by component kind. The same
# ground truth `just test` asserts the build's allowlist against.
@capabilities artifact:
    #!/bin/sh
    case "{{artifact}}" in
      *.wasm) go run ./tools/run "{{artifact}}" --capabilities ;;
      *)      "{{artifact}}" --capabilities ;;
    esac

# Show the components a (profile, variant) claims — the other side of `just test`
@show-claims profile="lean" variant="lgpl" target="wasm":
    PROFILE={{profile}} VARIANT={{variant}} TARGET={{target}} PRINT_COMPONENT_FLAGS=1 sh build/enable-lists.sh

# Lint the build scripts
lint:
    shellcheck build/*.sh

# Serve the docs site locally
docs-serve ARGS="":
    zensical serve {{ARGS}}
