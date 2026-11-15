package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// B706: the Go error log's clean step is Node buildEntry's
// scrub(redact(s)) over a JavaScript-trimmed message: registered secrets are
// masked in the message and the flags (overlapping secrets merge per
// occurrence), U+FEFF is trimmed and U+0085 is kept. The golden entry was
// recorded from src/util/error-log.mjs buildEntry with the same secrets
// registered through src/util/redact.mjs registerSecret.
func TestB706VerbFailureRedactsAndTrimsLikeNode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	autopilotRegisterSecret("abcdef0123456789")
	autopilotRegisterSecret("0123456789zz")
	nativeVerbFailure("logs", "", []string{"--abcdef0123456789", "x"}, 1, "\ufeff fail abcdef0123456789 and cdef0123456789zz, then tail \u0085\n", []any{})
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'}); len(lines) != 1 || json.Unmarshal(lines[0], &row) != nil {
		t.Fatalf("log %q", data)
	}
	got, _ := json.Marshal(map[string]any{"sub": row["sub"], "flags": row["flags"], "kind": row["kind"], "message": row["message"], "cleaned": row["cleaned"], "fp": row["fp"]})
	want := "{\"cleaned\":[],\"flags\":[\"--[REDACTED]\"],\"fp\":\"7f6dca358ff7\",\"kind\":\"error\",\"message\":\"fail [REDACTED] and cdef[REDACTED], then tail \u0085\",\"sub\":null}"
	if string(got) != want {
		t.Fatalf("entry\n got %s\nwant %s", got, want)
	}
}

// B706: Node's captureStderr keeps the last chunk whose JavaScript trim is
// non-empty, so a chunk holding only U+0085 (not a JS space) becomes the
// last error text, while a chunk of U+FEFF and spaces (JS spaces) does not.
func TestB706StderrCaptureUsesJSTrim(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	saved := os.Stderr
	os.Stderr = devnull
	defer func() { os.Stderr = saved }()
	var run runOut
	var land landErrorStream
	var close closeErrorBuffer
	for _, chunk := range []string{"first failure\n", "\u0085\n", "\ufeff \u00a0\n"} {
		run.err(chunk)
		_, _ = land.Write([]byte(chunk))
		_, _ = close.Write([]byte(chunk))
	}
	if run.last != "\u0085\n" {
		t.Fatalf("run last %q; want the U+0085 chunk", run.last)
	}
	if land.last != "\u0085" || close.last != "\u0085" {
		t.Fatalf("land last %q, close last %q; want the trimmed U+0085 chunk", land.last, close.last)
	}
}
