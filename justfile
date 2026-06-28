set dotenv-load

# Build a variant locally (needs Docker). VARIANT=lgpl|gpl
[default]
build variant="lgpl":
    docker build -f build/Dockerfile --build-arg VARIANT={{variant}} --target artifact -o dist .

# Run the built engine under wazero (prints the Phase-A capability report)
run variant="lgpl":
    go run ./tools/run dist/ffmpeg-wasi-{{variant}}.wasm

# Lint the build scripts
lint:
    shellcheck build/*.sh

# Serve the docs site locally
docs-serve ARGS="":
    zensical serve {{ARGS}}
