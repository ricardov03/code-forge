#!/bin/sh
# code-forge install check (B569): `sh <package>/scripts/install.sh`
# Finds the Go binary this package ships for the host OS/arch
# (native/bin/<platform>-<arch>/code-forge-native), restores a lost execute bit, runs
# `<binary> --version` and `<binary> internal version`, and compares the binary's version with
# package.json. Prints one OK line (exit 0), or the problem and one `fix:` line (exit 1).
# Usage errors exit 2. Local checks only: no network, no sudo, and no writes outside the
# package's native/bin. npm runs no install script; run this after `npm i -g` or to repair.

usage='usage: sh scripts/install.sh\n'
case "${1:-}" in
  --help|-h) printf "$usage"; exit 0 ;;
esac
if [ "$#" -gt 0 ]; then
  printf "$usage" >&2
  exit 2
fi

# The package root is the folder above this script, through any symlinks.
self=$0
case "$self" in
  /*) ;;
  *) self=$PWD/$self ;;
esac
while [ -L "$self" ]; do
  self_dir=$(CDPATH= cd -P "${self%/*}" && pwd) || exit 1
  self=$(readlink "$self") || exit 1
  case "$self" in
    /*) ;;
    *) self=$self_dir/$self ;;
  esac
done
pkg=$(CDPATH= cd -P "${self%/*}/.." && pwd) || exit 1

fail() {
  printf 'code-forge install: %s\nfix: %s\n' "$1" "$2" >&2
  exit 1
}

manifest=$pkg/package.json
[ -f "$manifest" ] || fail "cannot read $manifest" "npm i -g @codedology/code-forge"
field() {
  sed -n 's/^  "'"$1"'": *"\([^"]*\)".*/\1/p' "$manifest" | head -n 1
}
name=$(field name)
version=$(field version)
[ -n "$name" ] && [ -n "$version" ] || fail "cannot read name and version from $manifest" "npm i -g @codedology/code-forge"
reinstall="npm i -g $name@$version"
if [ -f "$pkg/native/go.mod" ]; then
  reinstall="node scripts/build-native.mjs --host"
fi

# Keep this mapping the same as bin/code-forge.
sys=$(uname -s)
mach=$(uname -m)
case "$sys" in
  Darwin) platform=darwin ;;
  Linux) platform=linux ;;
  *) platform=$(printf '%s' "$sys" | tr '[:upper:]' '[:lower:]') ;;
esac
case "$mach" in
  x86_64|amd64) arch=x64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) arch=$mach ;;
esac
target=$platform-$arch
binary=$pkg/native/bin/$target/code-forge-native

case "$target" in
  darwin-arm64|darwin-x64|linux-arm64|linux-x64) ;;
  *) fail "no Go binary is built for $target (built: darwin-arm64, darwin-x64, linux-arm64, linux-x64): $binary" \
       "use macOS or Linux on x64 or arm64" ;;
esac
[ -f "$binary" ] || fail "Go binary is missing: $binary" "$reinstall"
if [ ! -x "$binary" ]; then
  chmod +x "$binary" 2>/dev/null
  [ -x "$binary" ] || fail "Go binary is not executable and its mode could not be fixed: $binary" "chmod 755 '$binary'"
fi

# The checks must prove Go runs: no hand-over to Node, no hand-over or error log rows.
run_binary() {
  CODE_FORGE_NATIVE=1 CODE_FORGE_NODE=/nonexistent-code-forge-node CODE_FORGE_HANDOVER_LOG=0 \
    CODE_FORGE_NO_ERROR_LOG=1 "$binary" "$@" 2>/dev/null
}
out=$(run_binary --version)
status=$?
[ "$status" -eq 0 ] && [ "$out" = "$name $version" ] || \
  fail "Go binary failed to run (--version: exit $status): $binary" "$reinstall"
out=$(run_binary internal version)
status=$?
reported=$(printf '%s' "$out" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
[ "$status" -eq 0 ] && [ -n "$reported" ] || \
  fail "Go binary failed to run (internal version: exit $status): $binary" "$reinstall"
[ "$reported" = "$version" ] || fail "Go binary reports v$reported, package is v$version: $binary" "$reinstall"
printf 'code-forge install: OK Go binary %s v%s (%s)\n' "$target" "$version" "$binary"
