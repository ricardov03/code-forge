package main

// B610: the OS keychain part of createDefaultKeyStore (src/keys/store.mjs)
// in Go, through the macOS `security` CLI exactly as Node's fallback backend
// does (src/keys/backends/security-cli.mjs): service "code-forge"
// (backends/constants.mjs SERVICE, the same pair @napi-rs/keyring uses in
// backends/keychain.mjs), account = the key name.
//
// The backend's name follows Node's chain [keychain, security-cli, file]:
// "keychain" when Node would load @napi-rs/keyring for this platform from the
// package, else "security-cli". Both read the same login-keychain item, so a
// read through `security` gives Node's value under Node's backend name.
// Off macOS, or without a usable `security`, Go does not open the keychain
// and the callers keep their hand-over.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	kcService  = "code-forge"
	kcNotFound = 44 // `security` exits 44 when no matching item exists
)

var (
	// kcTimeout is security-cli.mjs TIMEOUT_MS.
	kcTimeout = 10 * time.Second
	// kcPackageRoot is the npm package root the binary ships in (bin/<os-arch>
	// three levels up, as handover finds bin/code-forge.mjs); tests stand in.
	kcPackageRoot = func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return ""
		}
		return filepath.Join(filepath.Dir(exe), "..", "..", "..")
	}
	// kcArch is Node's process.arch for the binding name.
	kcArch = map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
)

// kcStore is the keychain backend Go reads and writes; name is the backend
// name Node reports for it (source, index rows, `keys test` lines).
type kcStore struct{ name string }

// kcOpen is the keychain half of the default chain: ok false means Go cannot
// stand in for it (not macOS, or `security help` fails like Node's
// available()).
func kcOpen() (kcStore, bool) {
	if keysGOOS != "darwin" || !keysSecurityAvailable() {
		return kcStore{}, false
	}
	if kcNapiKeyring(kcPackageRoot()) {
		return kcStore{"keychain"}, true
	}
	return kcStore{"security-cli"}, true
}

// kcNapiKeyring says whether import('@napi-rs/keyring') from
// src/keys/backends would find the package and its macOS binding (the
// package's index.js: ./keyring.darwin-universal.node, the
// -darwin-universal package, then the same two for process.arch).
func kcNapiKeyring(root string) bool {
	if root == "" {
		return false
	}
	dir := kcResolvePackage(filepath.Join(root, "src", "keys", "backends"), "@napi-rs/keyring")
	if dir == "" {
		return false
	}
	names := []string{"darwin-universal"}
	if kcArch != "" {
		names = append(names, "darwin-"+kcArch)
	}
	for _, name := range names {
		if kcIsFile(filepath.Join(dir, "keyring."+name+".node")) || kcResolvePackage(dir, "@napi-rs/keyring-"+name) != "" {
			return true
		}
	}
	return false
}

// kcResolvePackage is Node's node_modules lookup from dir upward: the
// package directory whose main file (package.json "main", else index.js)
// exists, or "".
func kcResolvePackage(dir, pkg string) string {
	for {
		candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(pkg))
		if data, err := os.ReadFile(filepath.Join(candidate, "package.json")); err == nil {
			var meta struct {
				Main string `json:"main"`
			}
			_ = json.Unmarshal(data, &meta)
			entry := meta.Main
			if entry == "" {
				entry = "index.js"
			}
			if kcIsFile(filepath.Join(candidate, entry)) || kcIsFile(filepath.Join(candidate, entry+".js")) {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func kcIsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// kcExec is security-cli.mjs's exec call: the absolute binary, a new
// session, the 10 s budget, the child in the run's pid registry.
func kcExec(input *string, args ...string) childResult {
	return runChildInput(append([]string{keysSecurityBin}, args...), kcTimeout, lrRegisterChild, input, true)
}

// get is backend.get: the stored text, found false for no item (exit 44) or
// an empty password; ok false is Node's throw (any other exit, a timeout).
func (kc kcStore) get(name string) (raw string, found, ok bool) {
	res := kcExec(nil, "find-generic-password", "-s", kcService, "-a", name, "-w")
	if res.timedOut || res.code == nil {
		return "", false, false
	}
	switch {
	case *res.code == kcNotFound:
		return "", false, true
	case res.ok:
		value := strings.TrimSuffix(nodeUTF8(res.stdout), "\n")
		return value, value != "", true
	}
	return "", false, false
}

// set is backend.set: the value goes in on stdin twice (never in argv), then
// is read back; a stored value that differs is deleted and the write fails.
// The error text is the reason Node's reasonOf gives (the error class).
func (kc kcStore) set(name, value string) string {
	if strings.ContainsAny(value, "\r\n") {
		return "TypeError"
	}
	input := value + "\n" + value + "\n"
	if res := kcExec(&input, "add-generic-password", "-U", "-s", kcService, "-a", name, "-w"); !res.ok {
		return "Error"
	}
	got, found, ok := kc.get(name)
	if !ok {
		return "Error"
	}
	if !found || got != value {
		_, _ = kc.delete(name)
		return "Error"
	}
	return ""
}

// delete is backend.delete: true when an item was removed, false when none
// was there; ok false is Node's throw.
func (kc kcStore) delete(name string) (removed, ok bool) {
	res := kcExec(nil, "delete-generic-password", "-s", kcService, "-a", name)
	if res.timedOut || res.code == nil {
		return false, false
	}
	switch {
	case *res.code == kcNotFound:
		return false, true
	case res.ok:
		return true, true
	}
	return false, false
}
