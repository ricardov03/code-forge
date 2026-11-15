package main

// B679: the Linux keychain backend in Go. Node's @napi-rs/keyring 2.1.0
// (src/keys/backends/keychain.mjs) builds every Entry on Linux through
// keyring-node src/linux_credential_builder.rs: the Secret Service store
// (crate dbus-secret-service-keyring-store 1.0.x) when it connects, else the
// kernel keyutils store. This file is the Secret Service store's item logic
// (cred.rs Specifier, service.rs Service), written against ssAPI so tests
// drive it with a fake; keychain_linux.go is the D-Bus side.
//
// The items Node writes and finds (cred.rs Specifier::new and
// search_attributes, store.rs build with no modifiers):
//   attributes {"service": "code-forge", "username": <key name>} (no target)
//   label      "keyring:<key name>@code-forge"
//   collection the "default" alias (service.rs util::get_collection), made
//              unlocked first; CreateItem with replace=true and content type
//              "application/octet-stream".

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// ssAPI is the part of org.freedesktop.Secret.Service the store uses. Each
// call that the service answers with a prompt (Unlock, CreateItem, Delete)
// runs the prompt to its end as dbus-secret-service does.
type ssAPI interface {
	// Search is SearchItems: the unlocked and the locked item paths.
	Search(attrs map[string]string) (unlocked, locked []string, err error)
	// Unlock is Unlock on the given items (prompting when the service asks).
	Unlock(items []string) error
	// Secret is Item.GetSecret over the open session: the raw secret bytes.
	Secret(item string) ([]byte, error)
	// SetSecret is Item.SetSecret.
	SetSecret(item string, secret []byte) error
	// Create is CreateItem in the default collection (unlocked first).
	Create(label string, attrs map[string]string, secret []byte) error
	// Delete is Item.Delete.
	Delete(item string) error
}

// ssErrKind is keyring-core's error variant, for the Error text Node shows.
type ssErr struct {
	kind  string // "platform", "access", "ambiguous", "encoding"
	count int
	err   error
}

func (e *ssErr) Error() string {
	switch e.kind {
	case "access":
		return "Couldn't access platform storage: " + e.err.Error()
	case "ambiguous":
		return fmt.Sprintf("Entry is matched by %d credentials", e.count)
	case "encoding":
		return "Password data is not valid UTF-8"
	}
	return "Platform failure: " + e.err.Error()
}

// ssLocked is what a service answer of "locked", "no result" or a dismissed
// prompt maps to (errors.rs decode_error: NoStorageAccess).
var ssLocked = errors.New("prompt dismissed or the collection is locked")

// ssWrap is errors.rs decode_error.
func ssWrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ssLocked) {
		return &ssErr{kind: "access", err: err}
	}
	return &ssErr{kind: "platform", err: err}
}

// ssProbeAccount is keychain.mjs PROBE_ACCOUNT.
const ssProbeAccount = "__code-forge-probe__"

// ssStore is one Secret Service store (service "code-forge").
type ssStore struct{ api ssAPI }

func ssAttrs(name string) map[string]string {
	return map[string]string{"service": kcService, "username": name}
}

// unique is cred.rs get_unique_item: locked matches are unlocked first;
// none is found false; more than one is Ambiguous.
func (s *ssStore) unique(name string) (string, bool, error) {
	unlocked, locked, err := s.api.Search(ssAttrs(name))
	if err != nil {
		return "", false, ssWrap(err)
	}
	if len(locked) > 0 {
		if err := s.api.Unlock(locked); err != nil {
			return "", false, ssWrap(err)
		}
	}
	paths := append(append([]string{}, unlocked...), locked...)
	switch len(paths) {
	case 0:
		return "", false, nil
	case 1:
		return paths[0], true, nil
	}
	return "", false, &ssErr{kind: "ambiguous", count: len(paths)}
}

// get is Entry.getPassword (null for no entry) then keychain.mjs get: an
// empty password is null too; a secret that is not UTF-8 throws.
func (s *ssStore) get(name string) (string, bool, error) {
	path, found, err := s.unique(name)
	if err != nil || !found {
		return "", false, err
	}
	secret, err := s.api.Secret(path)
	if err != nil {
		return "", false, ssWrap(err)
	}
	if !utf8.Valid(secret) {
		return "", false, &ssErr{kind: "encoding"}
	}
	return string(secret), len(secret) > 0, nil
}

// set is Specifier::set_secret: the one matching item gets the secret, else
// a new item is made in the default collection.
func (s *ssStore) set(name, value string) error {
	path, found, err := s.unique(name)
	if err != nil {
		return err
	}
	if found {
		return ssWrap(s.api.SetSecret(path, []byte(value)))
	}
	return ssWrap(s.api.Create("keyring:"+name+"@"+kcService, ssAttrs(name), []byte(value)))
}

// delete is Entry.deletePassword: true when an item was deleted, false when
// none matched.
func (s *ssStore) delete(name string) (bool, error) {
	path, found, err := s.unique(name)
	if err != nil || !found {
		return false, err
	}
	if err := s.api.Delete(path); err != nil {
		return false, ssWrap(err)
	}
	return true, nil
}

// probe is keychain.mjs available(): the probe entry's getPassword must not
// throw.
func (s *ssStore) probe() bool {
	_, _, err := s.get(ssProbeAccount)
	return err == nil
}

// ssConnect opens the session bus and a Secret Service session (keychain_linux.go);
// tests replace it. An error is Node's failed SecretService::connect.
var ssConnect = ssDial
