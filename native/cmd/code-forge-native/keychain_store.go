package main

// B610: the OS keychain part of createDefaultKeyStore (src/keys/store.mjs)
// in Go, through the macOS `security` CLI exactly as Node's fallback backend
// does (src/keys/backends/security-cli.mjs): service "code-forge"
// (backends/constants.mjs SERVICE, the same pair @napi-rs/keyring uses in
// backends/keychain.mjs), account = the key name.
//
// B690b: Node is gone, so the backend no longer depends on whether Node
// could load @napi-rs/keyring. On macOS the keychain is the login keychain
// through `security`, named "keychain" (the name Node's npm installs used);
// off macOS and Linux, or without a usable `security`, Go does not open it.
//
// B679, B690b: on Linux the keychain is the Secret Service store
// (keychain_secretservice.go) when it connects and answers the probe; else the
// chain is the file store alone (kcChain). Node's kernel keyutils fallback is
// not served: a key kept only there has to be set again.

import (
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
)

// kcStore is the keychain backend Go reads and writes; name is the backend
// name Node reports for it (source, index rows, `keys test` lines).
type kcStore struct {
	name string
	ss   *ssStore // B679: the Secret Service store on Linux; nil is `security`
}

// kcOpen is the keychain half of the default chain: ok false means Go has no
// keychain here (macOS without a usable `security`, like Node's available(),
// or another OS; on Linux a Secret Service that does not connect or answer).
func kcOpen() (kcStore, bool) {
	if keysGOOS == "linux" {
		return kcOpenLinux()
	}
	if keysGOOS != "darwin" || !keysSecurityAvailable() {
		return kcStore{}, false
	}
	return kcStore{name: "keychain"}, true
}

// kcOpenLinux is the keychain backend on Linux: the Secret Service connects
// and the probe read does not fail.
func kcOpenLinux() (kcStore, bool) {
	api, err := ssConnect()
	if err != nil {
		return kcStore{}, false
	}
	store := &ssStore{api: api}
	if !store.probe() {
		return kcStore{}, false
	}
	return kcStore{name: "keychain", ss: store}, true
}

// kcChain is the keychain part of the default chain for the callers that
// also read the file store: kc nil with ok true is a chain with no keychain
// backend (Linux without a working Secret Service: the file store alone); ok
// false keeps the hand-over (macOS without a usable `security`).
func kcChain() (*kcStore, bool) {
	store, ok := kcOpen()
	if !ok {
		if keysGOOS == "linux" {
			return nil, true
		}
		return nil, false
	}
	return &store, true
}

// kcExec is security-cli.mjs's exec call: the absolute binary, a new
// session, the 10 s budget, the child in the run's pid registry.
func kcExec(input *string, args ...string) childResult {
	return runChildInput(append([]string{keysSecurityBin}, args...), kcTimeout, lrRegisterChild, input, true)
}

// get is backend.get: the stored text, found false for no item (exit 44) or
// an empty password; ok false is Node's throw (any other exit, a timeout).
func (kc kcStore) get(name string) (raw string, found, ok bool) {
	if kc.ss != nil {
		value, found, err := kc.ss.get(name)
		return value, found, err == nil
	}
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
	if kc.ss != nil {
		// a napi-rs error from an anyhow error has code "GenericFailure"
		if kc.ss.set(name, value) != nil {
			return "GenericFailure"
		}
		return ""
	}
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
	if kc.ss != nil {
		removed, err := kc.ss.delete(name)
		return removed, err == nil
	}
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
