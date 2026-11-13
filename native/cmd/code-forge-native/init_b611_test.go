package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// B611: the interactive init's Jev auto step (no Jev flag, no keys.jev) and
// its doctor step run in Go. Each golden case is a Node run recorded like
// B528's (CODE_FORGE_NATIVE=0 ./bin/code-forge init in an 80x24 pty, temp
// HOME and project "p", CODE_FORGE_KEY_BACKEND=file so Node never reads the
// real keychain), replayed in Go with the same keystrokes.

// b611Op is the fake 1Password CLI of the recordings: one item, any other
// id is not found.
const b611Op = `#!/bin/sh
if [ "$1 $2" = "item get" ] && [ "$3" = "abcdefghijklmnopqrstuvwxyz" ]; then
  printf '%s\n' '{"id":"abcdefghijklmnopqrstuvwxyz","title":"Jev API","vault":{"id":"vault123","name":"Dev"},"fields":[{"id":"username","type":"STRING","label":"username","value":"me"},{"id":"credential","type":"CONCEALED","label":"credential","value":"not-a-real-key"}]}'
  exit 0
fi
echo "[ERROR] 2026/11/13 10:00:00 \"$3\" isn't an item. Specify the item with its UUID, name, or domain." >&2
exit 1
`

const b611Pasted = "sk-test-b611"

// the keystrokes up to the settings choice ("Use these")
var b611Defaults = []string{"\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r", "\r"}

const (
	b611Up   = "\x1b[A"
	b611Down = "\x1b[B"
)

var b611Cases = []struct {
	name string
	keys []string
	args []string
	env  map[string]string
	op   bool
}{
	// the Jev menu: skip (its default)
	{"jev-skip", []string{"\r"}, []string{"--skip-doctor"}, nil, false},
	// an environment variable name
	{"jev-env", []string{b611Up, b611Up, "\r", "MY_JEV_KEY", "\r"}, []string{"--skip-doctor"}, nil, false},
	// a name that is not one: Node's usage error, exit 2
	{"jev-bad-env", []string{b611Up, b611Up, "\r", "1bad", "\r"}, []string{"--skip-doctor"}, nil, false},
	// a pasted key: masked, stored in the file store with its index row
	{"jev-paste", []string{b611Up, "\r", b611Pasted, "\r"}, []string{"--skip-doctor"}, nil, false},
	// nothing pasted: no key
	{"jev-paste-empty", []string{b611Up, "\r", "\r"}, []string{"--skip-doctor"}, nil, false},
	// the key is in the environment: no question
	{"jev-found", nil, []string{"--skip-doctor"}, map[string]string{"CODE_FORGE_KEY_JEV": "jev-env-value"}, false},
	// a 1Password item ID resolved through op
	{"jev-op", []string{b611Up, b611Up, b611Up, "\r", "abcdefghijklmnopqrstuvwxyz", "\r"}, []string{"--skip-doctor"}, nil, true},
	// an item op does not know, then "Skip for now"
	{"jev-op-fail", []string{b611Up, b611Up, b611Up, "\r", "zzzzzzzzzzzzzzzzzzzzzzzzzz", "\r", b611Down, b611Down, b611Down, "\r"}, []string{"--skip-doctor"}, nil, true},
	// not a reference, then "Choose another key source", then skip
	{"jev-op-other", []string{b611Up, b611Up, b611Up, "\r", "nope", "\r", b611Down, b611Down, "\r", "\r"}, []string{"--skip-doctor"}, nil, true},
	// the doctor step after the write
	{"doctor", nil, []string{"--no-jev"}, nil, false},
}

func b611GoldenFile(name string) string {
	return filepath.Join("testdata", "b611", name+".json")
}

// b611Setup is b528Setup with the file key store (never the keychain) and,
// for op cases, the fake op on PATH.
func b611Setup(t *testing.T, env map[string]string, op bool) (home, cwd, repo string) {
	t.Helper()
	b552SealApps(t)
	if err := os.MkdirAll(filepath.Join(toolSystemApps, "Solo.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	home, cwd, repo = b528Setup(t)
	b528Unset(t, "CODE_FORGE_KEY_JEV", "OP_SERVICE_ACCOUNT_TOKEN", "CODE_FORGE_OP_TIMEOUT_MS")
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	if op {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "op"), []byte(b611Op), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return home, cwd, repo
}

func b611Served(t *testing.T, home string, code int) {
	t.Helper()
	if code == 127 {
		handed, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
		t.Fatalf("handed over to Node: %s", handed)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil {
		t.Fatal("a hand-over row was written")
	}
}

// TestB611InitWizardJevAndDoctorMatchNode: each recorded Node run, replayed
// in Go: same terminal, exit code, init log and files.
func TestB611InitWizardJevAndDoctorMatchNode(t *testing.T) {
	for _, c := range b611Cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := os.ReadFile(b611GoldenFile(c.name))
			if err != nil {
				t.Fatal(err)
			}
			var want b528Golden
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			home, cwd, repo := b611Setup(t, c.env, c.op)
			keys := append(append([]string{}, b611Defaults...), c.keys...)
			code, transcript := b528Run(t, cwd, keys, c.args...)
			b611Served(t, home, code)
			log, files := b528Collect(t, home, cwd, repo)
			got := b528Golden{Code: code, Transcript: b528Mask(transcript, home, cwd, repo), Log: log, Files: files}
			if got.Code != want.Code {
				t.Errorf("exit %d, Node %d", got.Code, want.Code)
			}
			if got.Transcript != want.Transcript {
				t.Errorf("transcript differs from Node:\n%s", b528Diff(want.Transcript, got.Transcript))
			}
			if got.Log != want.Log {
				t.Errorf("init log differs from Node:\n%s", b528Diff(want.Log, got.Log))
			}
			want.Files = b552SkillRows(t, want.Files, "")
			want.Files = b607LinkVersion(t, want.Files)
			if got.Files != want.Files {
				t.Errorf("files differ from Node:\n%s", b528Diff(want.Files, got.Files))
			}
			// a pasted key reaches the key store only
			if strings.Contains(got.Transcript, b611Pasted) || strings.Contains(got.Log, b611Pasted) {
				t.Error("the pasted key was shown or logged")
			}
		})
	}
}

// The pasted key goes to the file store with Node's index row; the config
// keeps only the reference.
func TestB611InitWizardPasteStoresKey(t *testing.T) {
	home, cwd, _ := b611Setup(t, nil, false)
	keys := append(append([]string{}, b611Defaults...), b611Up, "\r", b611Pasted, "\r")
	code, transcript := b528Run(t, cwd, keys, "--skip-doctor")
	b611Served(t, home, code)
	if code != 0 || !strings.Contains(transcript, "keys: jev from keychain\n") || strings.Contains(transcript, b611Pasted) {
		t.Fatalf("code=%d transcript tail %q", code, transcript[max(0, len(transcript)-400):])
	}
	stored, _ := os.ReadFile(filepath.Join(home, ".code-forge", "store", "jev.key"))
	if string(stored) != `{"v":"`+b611Pasted+`","exp":null}` {
		t.Fatalf("store file %q", stored)
	}
	index, _ := os.ReadFile(filepath.Join(home, ".code-forge", "store", "index.json"))
	if !strings.Contains(string(index), `"jev": {
    "source": "user",
    "backend": "file",
    "exp": null
  }`) {
		t.Fatalf("index %s", index)
	}
	cfg, _ := os.ReadFile(filepath.Join(cwd, ".code-forge.yml"))
	if !strings.Contains(string(cfg), "jev: user") || strings.Contains(string(cfg), b611Pasted) {
		t.Fatalf("config:\n%s", cfg)
	}
}

// With the default chain on macOS the keychain is read through `security`
// (a fake here): a stored key is used without a question; a pasted key is
// written through `security` and read back.
func TestB611InitWizardKeychain(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		home, cwd, _ := b611Setup(t, nil, false)
		b528Unset(t, "CODE_FORGE_KEY_BACKEND")
		kc := b610Fake(t, false)
		kc.put(t, "jev", `{"v":"kc-value-b611","exp":null}`)
		code, transcript := b528Run(t, cwd, b611Defaults, "--skip-doctor")
		b611Served(t, home, code)
		if code != 0 || strings.Contains(transcript, "Where is the Jev key?") || !strings.Contains(transcript, "keys: jev from security-cli\n") || strings.Contains(transcript, "kc-value-b611") {
			t.Fatalf("code=%d transcript tail %q", code, transcript[max(0, len(transcript)-400):])
		}
		user, _ := os.ReadFile(filepath.Join(home, ".code-forge", "config.yml"))
		if !strings.Contains(string(user), "source: security-cli") {
			t.Fatalf("user config:\n%s", user)
		}
	})
	t.Run("paste", func(t *testing.T) {
		home, cwd, _ := b611Setup(t, nil, false)
		b528Unset(t, "CODE_FORGE_KEY_BACKEND")
		kc := b610Fake(t, false)
		keys := append(append([]string{}, b611Defaults...), b611Up, "\r", b611Pasted, "\r")
		code, transcript := b528Run(t, cwd, keys, "--skip-doctor")
		b611Served(t, home, code)
		if code != 0 || !strings.Contains(transcript, "keys: jev from keychain\n") || strings.Contains(transcript, b611Pasted) {
			t.Fatalf("code=%d transcript tail %q", code, transcript[max(0, len(transcript)-400):])
		}
		if got := kc.item("jev"); got != `{"v":"`+b611Pasted+`","exp":null}` {
			t.Fatalf("keychain item %q", got)
		}
		if strings.Contains(kc.calls(), b611Pasted) {
			t.Fatal("the key reached security's argv")
		}
		index, _ := os.ReadFile(filepath.Join(home, ".code-forge", "store", "index.json"))
		if !strings.Contains(string(index), `"backend": "security-cli"`) {
			t.Fatalf("index %s", index)
		}
	})
	t.Run("paste write fails", func(t *testing.T) {
		home, cwd, _ := b611Setup(t, nil, false)
		b528Unset(t, "CODE_FORGE_KEY_BACKEND")
		kc := b610Fake(t, false)
		// items can be read (none there) but not written: add exits 1
		if err := os.Chmod(filepath.Join(kc.dir, "items"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(kc.dir, "items"), 0o700) })
		keys := append(append([]string{}, b611Defaults...), b611Up, "\r", b611Pasted, "\r")
		code, transcript := b528Run(t, cwd, keys, "--skip-doctor")
		b611Served(t, home, code)
		if code != 1 || !strings.Contains(transcript, "init: unexpected error: security-cli: add-generic-password failed (exit 1)\n") || strings.Contains(transcript, b611Pasted) {
			t.Fatalf("code=%d transcript tail %q", code, transcript[max(0, len(transcript)-400):])
		}
		if _, err := os.Stat(filepath.Join(cwd, ".code-forge.yml")); err == nil {
			t.Fatal("config written after the failed store write")
		}
	})
}

// TestB611WriteNodeGoldens turns recorded Node pty runs into the goldens
// above. It runs only with B611_NODE_RUNS=<dir> holding n-<case>/{h,p} and
// B611_NODE_OUT=<dir> holding n-<case>.out (raw pty bytes, then "\nEXIT <code>\n").
func TestB611WriteNodeGoldens(t *testing.T) {
	dir := os.Getenv("B611_NODE_RUNS")
	if dir == "" {
		t.Skip("B611_NODE_RUNS is not set")
	}
	repo := b458Repo(t)
	exitLine := regexp.MustCompile(`\nEXIT (\d+)\n$`)
	_ = os.MkdirAll(filepath.Join("testdata", "b611"), 0o755)
	for _, c := range b611Cases {
		home, cwd := filepath.Join(dir, "n-"+c.name, "h"), filepath.Join(dir, "n-"+c.name, "p")
		raw, err := os.ReadFile(filepath.Join(os.Getenv("B611_NODE_OUT"), "n-"+c.name+".out"))
		if err != nil {
			t.Fatal(err)
		}
		m := exitLine.FindSubmatchIndex(raw)
		if m == nil {
			t.Fatalf("%s: no exit line", c.name)
		}
		code, _ := strconv.Atoi(string(raw[m[2]:m[3]]))
		log, files := b528Collect(t, home, cwd, repo)
		g := b528Golden{Code: code, Transcript: b528Mask(string(raw[:m[0]]), home, cwd, repo), Log: log, Files: files}
		data, _ := json.MarshalIndent(g, "", "  ")
		if err := os.WriteFile(b611GoldenFile(c.name), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A key store file Go cannot read back like Node still hands over, before
// any prompt or write (init-jev-key).
func TestB611InitWizardJevKeyHandOver(t *testing.T) {
	home, cwd, _ := b611Setup(t, nil, false)
	store := filepath.Join(home, ".code-forge", "store")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "jev.key"), []byte("\xff\xfe"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := b528Tree(t, cwd, home, cwd)
	code, transcript := b528Run(t, cwd, []string{"\r"}, "--skip-doctor")
	if code != 127 || !strings.HasPrefix(transcript, "code-forge-native: node executable") || strings.Count(transcript, "\n") != 1 {
		t.Fatalf("code=%d transcript=%q; want the Node hand-over before any output", code, transcript)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	var row handoverRow
	if json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row) != nil || row.Reason != "init-jev-key" || row.Verb != "init" {
		t.Fatalf("handover row %q", data)
	}
	if after := b528Tree(t, cwd, home, cwd); after != before {
		t.Fatalf("Go wrote into the project before the hand-over:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs")); err == nil {
		t.Fatal("Go wrote logs before the hand-over")
	}
}
