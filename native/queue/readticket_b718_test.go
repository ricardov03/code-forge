package queue

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// B718: Refresh and Identity read tickets as Node's readTicket and
// reviewIdentity do. Messages are Node's (src/worker/queue.mjs readTicket,
// assertRowPath, assertAnswers).

const b718ID = "0123456789abcdef01234567"

func b718Write(t *testing.T, dir, id, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestB718RefreshPrintsReadTicketMessages(t *testing.T) {
	base := `"run":"r","block":"B1","content_hash":"h"`
	cases := []struct{ name, body, code, message string }{
		{"not-json", `{`, "bad-ticket", "ticket " + b718ID + " is not readable JSON"},
		{"bom", "\xef\xbb\xbf{}", "bad-ticket", "ticket " + b718ID + " is not readable JSON"},
		{"number", `5`, "bad-ticket", "ticket " + b718ID + " is not readable JSON"},
		{"null", `null`, "bad-ticket", "ticket " + b718ID + " is not readable JSON"},
		{"array", `[]`, "bad-path", "file must be a non-empty relative path"},
		{"long-file", `{"file":"` + longName() + `"}`, "bad-path", "file must be a non-empty relative path"},
		{"nul", `{"file":"a\u0000b"}`, "bad-path", "file must be a POSIX path"},
		{"backslash", `{"file":"a\\b"}`, "bad-path", "file must be a POSIX path"},
		{"absolute", `{"file":"/a"}`, "bad-path", "file must be relative to the repository root, not absolute"},
		{"dotdot", `{"file":"a/../b"}`, "bad-path", `file must not contain "", "." or ".." segments`},
		{"git-upper", `{"file":".Code-Forge/x"}`, "bad-path", "file may not be under .Code-Forge/"},
		{"tree-number", `{"file":"a","tree":1}`, "bad-ticket", "ticket " + b718ID + " names a tree that is not an absolute path"},
		{"block-missing", `{"file":"a","run":"r","content_hash":"h"}`, "bad-ticket", "ticket " + b718ID + " does not match its id"},
		{"kind-false", `{"file":"a",` + base + `,"kind":false}`, "bad-ticket", "ticket " + b718ID + " has an unknown kind"},
		{"answers-by-file", `{"file":"a",` + base + `,"kind":"answers"}`, "bad-path", "file must be a non-empty relative path"},
		{"answers-empty", `{"file":"a",` + base + `,"kind":"answers","by_file":"b","answers":[]}`, "bad-ticket", "ticket " + b718ID + " names no valid finding ids"},
		{"answers-unsorted", `{"file":"a",` + base + `,"kind":"answers","by_file":"b","by_hash":"x","answers":["F2","F1"]}`, "bad-ticket", "ticket " + b718ID + " does not match its id"},
		{"revision-null", `{"file":"a",` + base + `,"acceptance_revision":null}`, "bad-ticket", "ticket " + b718ID + " has an invalid acceptance revision"},
		{"reopened-text", `{"file":"a",` + base + `,"reopened_at":"2026-13-01T00:00:00.000Z"}`, "bad-ticket", "ticket " + b718ID + " has an invalid reopen timestamp"},
		{"wrong-id", `{"file":"a",` + base + `}`, "bad-ticket", "ticket " + b718ID + " does not match its id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			b718Write(t, dir, b718ID, c.body)
			err := (&Index{Dir: dir}).Refresh()
			var te *TicketError
			if !errors.As(err, &te) || te.Code != c.code || err.Error() != c.message {
				t.Fatalf("Refresh() = %v (%#v), want %s %q", err, te, c.code, c.message)
			}
		})
	}
}

func longName() string {
	b := make([]byte, 4097)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestB718RefreshScandirError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "queue")
	if err := os.WriteFile(dir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := TicketNames(dir)
	var pe *os.PathError
	if !errors.As(err, &pe) || pe.Op != "scandir" || pe.Path != dir || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("TicketNames() = %v, want scandir ENOTDIR on %s", err, dir)
	}
	// B735: every Index.Refresh caller (block rebase, review-file, the
	// worker) keeps Node's WorkerError queue-unreadable.
	if err := (&Index{Dir: dir}).Refresh(); err == nil || err.Error() != "queue-unreadable" {
		t.Fatalf("Refresh() = %v, want queue-unreadable", err)
	}
}

func TestB718RefreshDanglingTicket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, b718ID+".json")); err != nil {
		t.Fatal(err)
	}
	err := (&Index{Dir: dir}).Refresh()
	if err == nil || err.Error() != "ticket "+b718ID+" is not readable JSON" {
		t.Fatalf("Refresh() = %v", err)
	}
}

func b718Parts() ticket.Parts {
	return ticket.Parts{Run: "r", Block: "B1", File: "a.go", ContentHash: "h"}
}

// A reopened attempt whose bytes hold invalid UTF-8 or a lone surrogate
// escape outside the hashed fields is still Node's latest attempt.
func TestB718IdentityReadsRawTicketsLikeNode(t *testing.T) {
	for name, extra := range map[string]string{
		"invalid-utf8":   `,"note":"` + "\xff\xfe" + `"`,
		"lone-surrogate": `,"note":"\ud800x"`,
		"low-surrogate":  `,"note":"\uDC00"`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := b718Parts()
			at := "2026-11-01T00:00:00.000Z"
			p.ReopenedAt = &at
			id := ticket.ReviewID(p)
			b718Write(t, dir, id, `{"ticket":"`+id+`","run":"r","block":"B1","file":"a.go","content_hash":"h","enqueued_at":"2026-11-01T00:00:01.000Z","reopened_at":"`+at+`"`+extra+`}`)
			got, err := Identity(dir, b718Parts())
			if err != nil || got != id {
				t.Fatalf("Identity = %q, %v; want %q", got, err, id)
			}
			if err := (&Index{Dir: dir}).Refresh(); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
		})
	}
}

// reviewIdentity skips a truthy kind; a falsy one reaches readTicket.
func TestB718IdentityKind(t *testing.T) {
	dir := t.TempDir()
	b718Write(t, dir, b718ID, `{"run":"r","block":"B1","file":"a.go","content_hash":"h","kind":"x"}`)
	if got, err := Identity(dir, b718Parts()); err != nil || got != ticket.ReviewID(b718Parts()) {
		t.Fatalf("truthy kind: %q, %v", got, err)
	}
	b718Write(t, dir, b718ID, `{"run":"r","block":"B1","file":"a.go","content_hash":"h","kind":""}`)
	_, err := Identity(dir, b718Parts())
	var te *TicketError
	if !errors.As(err, &te) || err.Error() != "ticket "+b718ID+" has an unknown kind" {
		t.Fatalf("falsy kind: %v", err)
	}
}

// One matching ticket is never compared, so its enqueued_at is not read.
func TestB718IdentitySingleTicketWithoutTime(t *testing.T) {
	dir := t.TempDir()
	id := ticket.ReviewID(b718Parts())
	b718Write(t, dir, id, `{"ticket":"`+id+`","run":"r","block":"B1","file":"a.go","content_hash":"h"}`)
	if got, err := Identity(dir, b718Parts()); err != nil || got != id {
		t.Fatalf("Identity = %q, %v", got, err)
	}
	// Two matching tickets are sorted: a missing enqueued_at stays unsupported.
	p := b718Parts()
	at := "2026-11-01T00:00:00.000Z"
	p.ReopenedAt = &at
	other := ticket.ReviewID(p)
	b718Write(t, dir, other, `{"ticket":"`+other+`","run":"r","block":"B1","file":"a.go","content_hash":"h","reopened_at":"`+at+`"}`)
	if _, err := Identity(dir, b718Parts()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("two tickets without time: %v", err)
	}
}
