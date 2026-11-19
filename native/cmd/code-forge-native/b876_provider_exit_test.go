package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B876 (B801 audit P09): when the provider CLI fails, facts and s2 print what
// it wrote on stderr (or that it wrote nothing) and the next step. The
// samples are the audit's exact commands with a provider CLI that exits 1.

const b876Next = "next: code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)\n"

// b876Stub replaces the fake claude with one that reads its prompt, prints
// stderrText on stderr and exits 1.
func b876Stub(t *testing.T, bin, stderrText string) {
	t.Helper()
	body := "#!/bin/sh\ncat > /dev/null\n"
	if stderrText != "" {
		body += "printf '%s\\n' '" + stderrText + "' >&2\n"
	}
	body += "exit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestB876FactsShowsTheProviderExit(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, want string
	}{
		{"silent", "", "facts: the provider CLI printed nothing on stderr\n"},
		{"stderr", "Error: bad request for key sk-ant-api03-abcdefghijklmnop", "facts: provider stderr: Error: bad request for key [REDACTED]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := fcWorkspace(t, "anthropic", fcBrief)
			t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
			b876Stub(t, filepath.Join(base, "bin"), tc.stderr)
			if err := os.MkdirAll(filepath.Join(base, "repo", "plans"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, "repo", "plans", "brief.md"), []byte(fcBrief), 0o644); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := runCaptured(t, "facts", "--brief", "plans/brief.md", "--timeout", "5")
			want := "level=L0 provider=anthropic model=m-x effort=none fallback_step=0\n" + tc.want + "facts: failed (exit 1)\n" + b876Next
			if code != 1 || stderr != want {
				t.Fatalf("exit %d\nstderr %q\nwant   %q", code, stderr, want)
			}
			// the error log keeps the verdict line as its message
			message, _, _ := b699LastEntry(t, filepath.Join(base, "home"))
			if message != "facts: failed (exit 1)" {
				t.Fatalf("logged message %q", message)
			}
		})
	}
}

func TestB876S2ShowsTheProviderExit(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, want string
	}{
		{"silent", "", "s2: the provider CLI printed nothing on stderr\n"},
		{"stderr", "Invalid API key", "s2: provider stderr: Invalid API key\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo := newB550Fixture(t, "anthropic")
			b876Stub(t, filepath.Join(filepath.Dir(repo), "bin"), tc.stderr)
			if err := os.MkdirAll(filepath.Join(repo, "plans"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "plans", "p.json"), []byte(`{"question":"q"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := b460Call(t, "s2", "--packet", "plans/p.json", "--timeout", "5")
			want := b550A + tc.want + "s2: failed (exit 1)\n" + b876Next
			if code != 1 || out != "" || stderr != want {
				t.Fatalf("exit %d out %q\nstderr %q\nwant   %q", code, out, stderr, want)
			}
		})
	}
}

// A provider that echoes its prompt (the packet) on stderr: the packet lines
// are not printed again.
func TestB876S2DropsPacketLines(t *testing.T) {
	_, repo := newB550Fixture(t, "anthropic")
	body := "#!/bin/sh\ncat >&2\necho 'quota check failed' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(repo), "bin", "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "p.json"), []byte(`{"question": "the secret plan of the quarter"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := b460Call(t, "s2", "--packet", "p.json")
	if code != 1 || strings.Contains(stderr, "secret plan") || !strings.HasSuffix(stderr, "s2: provider stderr: quota check failed\ns2: failed (exit 1)\n"+b876Next) {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

// The audit's review sample: the provider CLI exits 1 and prints nothing.
// stdout (--json) is unchanged; stderr names the session's exit and the
// next step; the error log keeps the "unavailable" line.
func TestB876ReviewShowsTheProviderExit(t *testing.T) {
	bin := b553Binary(t)
	_, g := b553Go(t, bin, "silent", []string{"--base", "HEAD", "--intent", "nothing", "--json", "--max", "5"})
	if g.code != 1 {
		t.Fatalf("exit %d stderr %q", g.code, g.err)
	}
	// each file: the verdict, then why (files run in parallel, in any
	// order); one next line after all files
	next := strings.Index(g.err, "printed nothing on stderr\n"+b876Next)
	for _, file := range []string{"src/a.txt", "src/b.txt"} {
		pair := "review: " + file + "  unavailable: exit\n" +
			"review: " + file + "  session failed (exit 1); the provider CLI printed nothing on stderr\n"
		if i := strings.Index(g.err, pair); i < 0 || next < 0 || i > next {
			t.Fatalf("stderr %q\nwant %q before the next line", g.err, pair)
		}
	}
	if strings.Count(g.err, "next: ") != 1 {
		t.Fatalf("stderr %q: want one next line after the files", g.err)
	}
	if !strings.Contains(g.out, `"result":"unavailable","reason":"exit"`) || strings.Contains(g.out, "provider CLI") {
		t.Fatalf("stdout %q", g.out)
	}
}
