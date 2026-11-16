package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B759: B744 leftovers. Goldens in testdata/b759 were recorded from the Node
// CLI (CODE_FORGE_NATIVE=0, cwd = HOME, CODE_FORGE_KEY_BACKEND=file);
// <H> is HOME and <REPO> the checkout in Node's stack frames.

type b759Golden struct {
	code           int
	stdout, stderr string
}

// b759Read reads a golden; Node's stack frames ("    at ..." lines) are
// dropped, since Go prints the first line of a thrown error only (as every Go
// "verb failed" line does).
func b759Read(t *testing.T, name, home string) b759Golden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b759", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "<H>", home)
	head, rest, ok := strings.Cut(text, "\n-- stdout\n")
	if !ok {
		t.Fatalf("%s: no stdout marker", name)
	}
	stdout, stderr, ok := strings.Cut(rest, "-- stderr\n")
	if !ok {
		t.Fatalf("%s: no stderr marker", name)
	}
	var kept []string
	for _, line := range strings.SplitAfter(stderr, "\n") {
		if !strings.HasPrefix(line, "    at ") {
			kept = append(kept, line)
		}
	}
	g := b759Golden{stdout: stdout, stderr: strings.Join(kept, "")}
	switch head {
	case "exit 0":
	case "exit 1":
		g.code = 1
	default:
		t.Fatalf("%s: head %q", name, head)
	}
	return g
}

// b759Check runs args through runGuarded in a fresh HOME (cwd = HOME, Node
// unavailable, hand-over log on): output is Node's golden and no hand-over
// row is written.
func b759Check(t *testing.T, name string, setup func(home string), args ...string) string {
	t.Helper()
	var seam func([]string)
	if setup != nil {
		seam = func([]string) { setup(os.Getenv("HOME")) }
	}
	res := b744InHome(t, seam, args...)
	want := b759Read(t, name, res.home)
	if res.err != nil || res.code != want.code || res.stdout != want.stdout || res.stderr != want.stderr {
		t.Fatalf("%s: exit %d err %v\nstdout %q\nstderr %q\nwant exit %d stdout %q stderr %q", name, res.code, res.err, res.stdout, res.stderr, want.code, want.stdout, want.stderr)
	}
	if rows := b508Rows(t, res.home); len(rows) > 0 {
		t.Fatalf("%s: hand-over rows %+v; want none", name, rows)
	}
	return res.home
}

func b759Write(path, text string, mode os.FileMode) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		panic(err)
	}
}

func b759Mkdir(path string) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		panic(err)
	}
}

func b759Symlink(target, link string) {
	if err := os.Symlink(target, link); err != nil {
		panic(err)
	}
}

// b759Index makes store/index.json a folder ("dir") or a 0000 file ("000").
func b759Index(t *testing.T, home, mode string) {
	index := filepath.Join(home, ".code-forge", "store", "index.json")
	if mode == "dir" {
		b759Mkdir(index)
		return
	}
	b759Write(index, "{}\n", 0o644)
	b744Chmod(t, index, 0, 0o644)
}

func b759Key(home, exp string) {
	b759Write(filepath.Join(home, ".code-forge", "store", "foo.key"), `{"v":"abc","exp":`+exp+`}`, 0o600)
}

// Case 2: keys list, remove and test when store/index.json is a folder or
// unreadable print Node's text (list and remove: the thrown read error; test:
// the stored key, or the failed index update as a reason) instead of handing
// over.
func TestB759KeysIndexUnreadableRunsInGo(t *testing.T) {
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	for _, mode := range []string{"dir", "000"} {
		t.Run("list-"+mode, func(t *testing.T) {
			b759Check(t, "keys-list-index-"+mode, func(h string) { b759Index(t, h, mode) }, "keys", "list")
		})
		t.Run("remove-"+mode, func(t *testing.T) {
			home := b759Check(t, "keys-remove-index-"+mode, func(h string) { b759Key(h, "null"); b759Index(t, h, mode) }, "keys", "remove", "foo")
			// Node deletes the key file before the index update throws
			if _, err := os.Lstat(filepath.Join(home, ".code-forge", "store", "foo.key")); !os.IsNotExist(err) {
				t.Fatalf("foo.key still there: %v", err)
			}
		})
		t.Run("test-"+mode, func(t *testing.T) {
			b759Check(t, "keys-test-index-"+mode, func(h string) { b759Key(h, "null"); b759Index(t, h, mode) }, "keys", "test", "foo")
		})
		t.Run("test-expired-"+mode, func(t *testing.T) {
			home := b759Check(t, "keys-test-expired-index-"+mode, func(h string) { b759Key(h, "1"); b759Index(t, h, mode) }, "keys", "test", "foo")
			if _, err := os.Lstat(filepath.Join(home, ".code-forge", "store", "foo.key")); !os.IsNotExist(err) {
				t.Fatalf("expired foo.key still there: %v", err)
			}
		})
	}
}

// Case 2, keys set at the prompt: Node writes foo.key, then its index update
// throws; the verb fails with the thrown error's first line (recorded from
// Node on a pty with script(1), testdata/b759/keys-set-prompt-index-*.txt).
func TestB759KeysSetPromptIndexUnreadable(t *testing.T) {
	for _, mode := range []string{"dir", "000"} {
		t.Run(mode, func(t *testing.T) {
			dir, project, _ := b678PromptHome(t)
			home := filepath.Dir(filepath.Dir(dir))
			b759Index(t, home, mode)
			data, err := os.ReadFile(filepath.Join("testdata", "b759", "keys-set-prompt-index-"+mode+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := "\x1b[?25h" + strings.ReplaceAll(string(data), "<H>", home)
			code, got := b678Prompt(t, project, []string{"x", "y", "\r"}, "set", "foo")
			if code != 1 || !strings.HasSuffix(got, want) || strings.Contains(got, "stored foo") {
				t.Fatalf("exit %d: %q\nwant suffix %q", code, got, want)
			}
			if b678Read(t, filepath.Join(dir, "foo.key")) != `{"v":"xy","exp":null}` {
				t.Fatalf("foo.key = %q", b678Read(t, filepath.Join(dir, "foo.key")))
			}
		})
	}
}

// Case 3: block attempt when runs/r1.json is a folder or unreadable prints
// Node's "block attempt: <fs error>" text, exit 1, instead of handing over.
func TestB759AttemptRunRecordUnreadable(t *testing.T) {
	t.Run("dir", func(t *testing.T) {
		b759Check(t, "attempt-run-dir", func(h string) { b759Mkdir(filepath.Join(h, ".code-forge", "runs", "r1.json")) }, "block", "attempt", "B1", "--run", "r1")
	})
	t.Run("000", func(t *testing.T) {
		b759Check(t, "attempt-run-000", func(h string) {
			record := filepath.Join(h, ".code-forge", "runs", "r1.json")
			b759Write(record, "{}\n", 0o600)
			b744Chmod(t, record, 0, 0o600)
		}, "block", "attempt", "B1", "--run", "r1")
	})
}

// Case 1: report --export onto dangling symlinks (the export folder or a
// section file) prints what Node prints; B764 already served these in Go,
// these pin the file-level and nested forms.
func TestB759ReportExportDanglingSymlinks(t *testing.T) {
	for _, c := range []struct {
		name, dir string
		setup     func(h string)
	}{
		{"report-export-file-dangling", "d", func(h string) {
			b759Mkdir(filepath.Join(h, "d"))
			b759Symlink("../missing/x", filepath.Join(h, "d", "agent_waivers.json"))
		}},
		{"report-export-file-dangling-made", "d", func(h string) {
			b759Mkdir(filepath.Join(h, "d"))
			b759Mkdir(filepath.Join(h, "tgt"))
			b759Symlink("../tgt/x", filepath.Join(h, "d", "agent_waivers.json"))
		}},
		{"report-export-file-loop", "d", func(h string) {
			b759Mkdir(filepath.Join(h, "d"))
			b759Symlink("agent_waivers.json", filepath.Join(h, "d", "agent_waivers.json"))
		}},
		{"report-export-dangling-sub", "dl/sub", func(h string) { b759Symlink("nowhere", filepath.Join(h, "dl")) }},
		{"report-export-dangling-abs", "dl", func(h string) { b759Symlink("/nonexistent-b759/x", filepath.Join(h, "dl")) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := b759Check(t, c.name, func(h string) { b744Ledger(h); c.setup(h) }, "report", "--slug", "t", "--export", c.dir)
			if c.name == "report-export-file-dangling-made" {
				if _, err := os.Stat(filepath.Join(home, "tgt", "x")); err != nil {
					t.Fatalf("the symlink target was not written: %v", err)
				}
			}
		})
	}
}
