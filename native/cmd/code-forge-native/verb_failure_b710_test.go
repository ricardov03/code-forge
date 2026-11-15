package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// b710LogEntry reads the one error log line and keeps the fields buildEntry
// fills from the failure (not ts, version, platform or before).
func b710LogEntry(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'}); len(lines) != 1 || json.Unmarshal(lines[0], &row) != nil {
		t.Fatalf("log %q", data)
	}
	got, _ := json.Marshal(map[string]any{"verb": row["verb"], "sub": row["sub"], "flags": row["flags"], "exit": row["exit"], "kind": row["kind"], "message": row["message"], "cleaned": row["cleaned"], "fp": row["fp"]})
	return string(got)
}

// B710: Node's loadKey registers the run's signer key as a secret in every
// verb that loads it, so a failure after the load logs the key masked. Here
// block open loads the key to read the block's signed claim rows and then
// refuses a claim whose path is not a string; the path holds the key's hex.
// The golden entry is src/util/error-log.mjs buildEntry's for the same
// stderr text with the key registered through loadKey (src/state/signer.mjs).
func TestB710BlockOpenFailureLogsSignerKeyMasked(t *testing.T) {
	f, acceptance := b581Fixture(t)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	key, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs/r-close-fixture.key"))
	if err != nil || len(key) != 32 {
		t.Fatalf("key %d bytes, %v", len(key), err)
	}
	secret := hex.EncodeToString(key)
	row := ledger.Object{{Key: "event", Value: "block.claim"}, {Key: "run", Value: "r-close-fixture"}, {Key: "block", Value: "B2"}, {Key: "path", Value: []any{secret}}}
	if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
		t.Fatal(err)
	}
	autopilotSecrets = nil // the fixture's own signing registered it: start clean
	_ = os.Remove(filepath.Join(f.home, ".code-forge", "logs", "errors.jsonl"))
	res := b451Run(t, f.home, append([]string{"open"}, b581Open(acceptance, "--attempt", "2")...)...)
	if res.code != 1 || res.stdout != "" || !strings.HasPrefix(res.stderr, "block open: owned files: invalid path [") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.code, res.stdout, res.stderr)
	}
	got := b710LogEntry(t, f.home)
	if strings.Contains(got, secret) {
		t.Fatalf("the error log holds the signer key: %s", got)
	}
	want := `{"cleaned":[],"exit":1,"flags":["--run","--level","--owned","--acceptance","--attempt"],"fp":"8845c84c1450","kind":"error","message":"block open: owned files: invalid path [\"[REDACTED]\"]: must be a non-empty string","sub":"open","verb":"block"}`
	if got != want {
		t.Fatalf("entry\n got %s\nwant %s", got, want)
	}
}

// B711: gatesErrf keeps the last stderr write as Node's captureStderr does
// (a write whose JavaScript trim is empty is not kept) and the logged message
// is JavaScript-trimmed: U+0085 is not a JS space, U+FEFF is. Goldens are
// buildEntry's for the same writes through captureStderr.
func TestB711GatesErrfTrimsLikeJS(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	for _, tc := range []struct {
		name   string
		writes []string
		want   string
	}{
		{"trailing U+0085 is kept", []string{"gates run: tail \u0085\n"}, "{\"cleaned\":[],\"exit\":1,\"flags\":[],\"fp\":\"de9c67465b8f\",\"kind\":\"error\",\"message\":\"gates run: tail \u0085\",\"sub\":\"run\",\"verb\":\"gates\"}"},
		{"a U+FEFF-only write is not kept", []string{"gates run: first\n", "\ufeff \u00a0\n"}, `{"cleaned":[],"exit":1,"flags":[],"fp":"9389fce1ff47","kind":"error","message":"gates run: first","sub":"run","verb":"gates"}`},
		{"leading U+FEFF is trimmed", []string{"\ufeffgates run: lead\u2028\n"}, `{"cleaned":[],"exit":1,"flags":[],"fp":"10d253b19c15","kind":"error","message":"gates run: lead","sub":"run","verb":"gates"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
			saved := os.Stderr
			os.Stderr = devnull
			gatesLastMessage = nil
			for _, w := range tc.writes {
				gatesErrf("%s", w)
			}
			gatesFinish([]string{"run"}, 1)
			os.Stderr = saved
			if got := b710LogEntry(t, home); got != tc.want {
				t.Fatalf("entry\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}
