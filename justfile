set dotenv-load

# Build a variant locally (needs Docker). variant=lgpl|gpl, profile=lean|intermediate (spec 0022)
[default]
build variant="lgpl" profile="lean":
    docker build -f build/Dockerfile --build-arg VARIANT={{variant}} --build-arg PROFILE={{profile}} --target artifact -o dist .

# Run the built lean engine under wazero (prints the capability report)
run variant="lgpl":
    go run ./tools/run dist/ffmpeg-wasi-{{variant}}.wasm

# Lint the build scripts
lint:
    shellcheck build/*.sh

# Serve the docs site locally
docs-serve ARGS="":
    zensical serve {{ARGS}}
