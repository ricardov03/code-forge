package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// The expected bytes and hash come from Node: JSON.stringify(loadConfigFile(...).config, null, 2)
// and configHash(config) on the same file.
const snapshotYAML = `review:
  workers: 2
  zeta: true
  alpha: [3, 1, .nan]
project:
  slug: demo
  "10": ten
  "2": two
  name: Demo
base: &b
  x: 1
  y: [a, b]
copy: *b
when: 2026-01-02
big: .inf
version: 1
engine: claude
tail:
  "k.z": null
  "-1": neg
`

const snapshotNodeJSON = `{
  "review": {
    "workers": 2,
    "zeta": true,
    "alpha": [
      3,
      1,
      null
    ]
  },
  "project": {
    "2": "two",
    "10": "ten",
    "slug": "demo",
    "name": "Demo"
  },
  "base": {
    "x": 1,
    "y": [
      "a",
      "b"
    ]
  },
  "copy": {
    "x": 1,
    "y": [
      "a",
      "b"
    ]
  },
  "when": "2026-01-02",
  "big": null,
  "version": 1,
  "engine": "claude",
  "tail": {
    "k.z": null,
    "-1": "neg"
  }
}`

const snapshotNodeHash = "78bd5d1d5e6c122d9d501a86c2ea2c946420d48bf438062460095582ba264290"

func TestLoadSnapshotKeepsNodeKeyOrderAndHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte(snapshotYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := LoadSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ledger.Stringify(snap.Ordered, true)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != snapshotNodeJSON {
		t.Fatalf("snapshot bytes differ from Node:\n%s", data)
	}
	if got := snap.Hash(); got != snapshotNodeHash {
		t.Fatalf("hash %s, Node %s", got, snapshotNodeHash)
	}
}

func TestLoadSnapshotAppendsMissingVersionLast(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte("engine: claude\nreview: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := LoadSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := ledger.Stringify(snap.Ordered, false)
	if string(data) != `{"engine":"claude","review":{},"version":1}` {
		t.Fatalf("got %s", data)
	}
}

func TestLoadSnapshotRefusesLikeNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	cases := map[string]string{
		"":             "parse-error",
		"- a\n":        "parse-error",
		"version: 2\n": "migrate-error",
		"a: [\n":       "parse-error",
	}
	for body, kind := range cases {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadSnapshotFile(path)
		e, ok := err.(*Error)
		if !ok || e.Kind != kind {
			t.Fatalf("%q: got %v, want %s", body, err, kind)
		}
	}
	if _, err := LoadSnapshotFile(filepath.Join(dir, "missing.yml")); err == nil || err.(*Error).Kind != "not-found" {
		t.Fatalf("missing file: %v", err)
	}
}
