//go:build linux

package main

import (
	"os"
	"strconv"
	"testing"
)

// B679: the D-Bus side against a real Secret Service, when the machine has
// one (a desktop session with gnome-keyring or KeePassXC). Skipped without a
// session bus or a service; the item name is unique and removed at the end.
func TestB679SecretServiceRealBus(t *testing.T) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" || os.Getenv("CODE_FORGE_B679_REAL_BUS") != "1" {
		t.Skip("set CODE_FORGE_B679_REAL_BUS=1 in a session with a Secret Service")
	}
	api, err := ssDial()
	if err != nil {
		t.Skipf("no Secret Service: %v", err)
	}
	store := &ssStore{api: api}
	name := "__b679-test-" + strconv.Itoa(os.Getpid()) + "__"
	t.Cleanup(func() { _, _ = store.delete(name) })
	if !store.probe() {
		t.Fatal("probe failed")
	}
	if err := store.set(name, "b679-value"); err != nil {
		t.Fatal(err)
	}
	if err := store.set(name, "b679-value-2"); err != nil {
		t.Fatal(err)
	}
	if value, found, err := store.get(name); value != "b679-value-2" || !found || err != nil {
		t.Fatalf("get = %q %v %v", value, found, err)
	}
	if removed, err := store.delete(name); !removed || err != nil {
		t.Fatalf("delete = %v %v", removed, err)
	}
	if _, found, err := store.get(name); found || err != nil {
		t.Fatalf("after delete = %v %v", found, err)
	}
}
