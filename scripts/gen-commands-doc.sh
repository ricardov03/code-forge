#!/bin/sh
# B648: docs/reference/commands.md comes from the Go help table. Builds the Go binary from
# native/ and runs its hidden `help --commands-doc`: no arguments writes the page,
# `--check [<file>]` exits 1 when the page (or that copy) is stale. Not shipped in the package.
set -e
repo=$(cd "$(dirname "$0")/.." && pwd)
bin="$repo/node_modules/.cache/code-forge/gen-commands-doc"
mkdir -p "$(dirname "$bin")"
CGO_ENABLED=0 go -C "$repo/native" build -o "$bin" ./cmd/code-forge-native
exec "$bin" help --commands-doc --root "$repo" "$@"
