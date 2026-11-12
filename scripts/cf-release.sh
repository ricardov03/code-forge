#!/bin/sh
# Maintainer release tooling in Go (B570): builds native/cmd/cf-release from source and runs it
# from the current folder, so `npm run release|release:site|docs-block|changelog` keep their
# arguments, output and exit codes. Not shipped in the package.
set -e
repo=$(cd "$(dirname "$0")/.." && pwd)
bin="$repo/node_modules/.cache/code-forge/cf-release"
mkdir -p "$(dirname "$bin")"
CGO_ENABLED=0 go -C "$repo/native" build -o "$bin" ./cmd/cf-release
exec "$bin" "$@"
