package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// b690aFiles lists every file under root (relative paths), sorted.
func b690aFiles(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// b690aInRepo runs the guarded entry from an empty temp repository folder.
func b690aInRepo(t *testing.T, args ...string) (b508Result, string) {
	t.Helper()
	repo := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	return b508Run(t, nil, args...), repo
}

// A case that used to start Node (block close with secret-like text in argv,
// past Node's usage checks) now gets the Go refusal: one stderr line, exit 1, empty stdout, and the
// only file written is the handovers.jsonl row (with its lock file).
func TestB690aFormerHandOverRefuses(t *testing.T) {
	res, repo := b690aInRepo(t, "block", "close", "B1", "--run", "r1", "--reason=sk-private")
	want := "code-forge: block close is not supported in this case yet (close-unsupported-flag). Please report it: code-forge logs report\n"
	if res.code != 1 || res.err != nil || res.stdout != "" || res.stderr != want {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 1 and %q", res.code, res.err, res.stdout, res.stderr, want)
	}
	if files := b690aFiles(t, repo); len(files) != 0 {
		t.Errorf("repo files %q; want none", files)
	}
	home := b690aFiles(t, res.home)
	if strings.Join(home, ",") != ".code-forge/native/handovers.jsonl,.code-forge/native/handovers.jsonl.lock" {
		t.Errorf("HOME files %q; want only the handovers log and its lock", home)
	}
}

// The refusal still writes the handovers.jsonl row, with its fields kept:
// verb, sub, reason and version.
func TestB690aRefusalWritesHandOverRow(t *testing.T) {
	res, _ := b690aInRepo(t, "block", "close", "B1", "--run", "r1", "--reason=sk-private")
	if res.code != 1 || res.err != nil {
		t.Fatalf("code=%d err=%v; want the refusal, exit 1", res.code, res.err)
	}
	rows := b508Rows(t, res.home)
	if len(rows) != 1 || rows[0].Verb != "block" || rows[0].Sub != "close" || rows[0].Reason != "close-unsupported-flag" || rows[0].Version != version || rows[0].At == "" {
		t.Fatalf("rows %+v; want one block close close-unsupported-flag row", rows)
	}
}

// b690aVerbFile is bin/code-forge.mjs's VERB_FILE_PATTERN.
var b690aVerbFile = regexp.MustCompile(`^[a-z][a-z0-9-]*\.mjs$`)

// An unknown verb prints Node's text (bin/code-forge.mjs: the verb list is
// src/cli's verb file names, sorted), exit 1, and writes nothing at all.
func TestB690aUnknownVerbMatchesNode(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "src", "cli"))
	if err != nil {
		t.Skipf("no src/cli to compare with: %v", err)
	}
	verbs := []string{}
	for _, e := range entries {
		if !e.IsDir() && b690aVerbFile.MatchString(e.Name()) {
			verbs = append(verbs, strings.TrimSuffix(e.Name(), ".mjs"))
		}
	}
	sort.Strings(verbs)
	want := "code-forge: unknown verb \"private-verb\"\nVerbs:\n  " + strings.Join(verbs, "\n  ") + "\n"
	res, repo := b690aInRepo(t, "private-verb", "private-value")
	if res.code != 1 || res.err != nil || res.stdout != "" || res.stderr != want {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 1 and %q", res.code, res.err, res.stdout, res.stderr, want)
	}
	if files := append(b690aFiles(t, repo), b690aFiles(t, res.home)...); len(files) != 0 {
		t.Errorf("files written %q; want none", files)
	}
}

// b690aRefusalLine is the whole stderr of a Go refusal (B690a).
var b690aRefusalLine = regexp.MustCompile(`^code-forge: [^\n]+ is not supported in this case yet \([a-z0-9:-]+\)\. Please report it: code-forge logs report\n$`)

// b690aIsRefusal reports whether stderr is exactly one Go refusal line.
func b690aIsRefusal(stderr string) bool { return b690aRefusalLine.MatchString(stderr) }

// b690aWithSecrets registers values as Node's registerSecret would and
// restores the registry when the test ends.
func b690aWithSecrets(t *testing.T, values ...string) {
	t.Helper()
	saved := append([]string(nil), autopilotSecrets...)
	t.Cleanup(func() { autopilotSecrets = saved })
	for _, v := range values {
		autopilotRegisterSecret(v)
	}
}

// b690aFakeToken is shaped like an API key and built here; it is not a key.
func b690aFakeToken() string { return "sk-ant-api03-" + strings.Repeat("x", 40) }

// Node's entry (bin/code-forge.mjs) passes the unknown-verb text through
// redact(). Recorded from Node: with no secret registered (the entry
// registers none before this line) the verb prints as typed; once the value
// is registered, redact() prints [REDACTED] in its place. Go does the same.
func TestB690aUnknownVerbRedactsLikeNode(t *testing.T) {
	token := b690aFakeToken()
	t.Run("nothing registered prints the verb as Node does", func(t *testing.T) {
		res, _ := b690aInRepo(t, token)
		// Recorded: node bin/code-forge.mjs <token> (HOME empty), first two lines.
		want := "code-forge: unknown verb \"sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\nVerbs:\n"
		if res.code != 1 || !strings.HasPrefix(res.stderr, want) {
			t.Fatalf("code=%d stderr=%q; want exit 1 and prefix %q", res.code, res.stderr, want)
		}
	})
	t.Run("a registered secret is masked", func(t *testing.T) {
		b690aWithSecrets(t, token)
		res, _ := b690aInRepo(t, token)
		// Recorded: redact() of the same text after registerSecret(token).
		want := "code-forge: unknown verb \"[REDACTED]\"\nVerbs:\n"
		if res.code != 1 || !strings.HasPrefix(res.stderr, want) || strings.Contains(res.stderr, token) {
			t.Fatalf("code=%d stderr=%q; want exit 1, prefix %q and no raw token", res.code, res.stderr, want)
		}
	})
}

// The refusal line goes through the same redaction (recorded from Node's
// redact() after registerSecret("close")).
func TestB690aRefusalLineRedacts(t *testing.T) {
	b690aWithSecrets(t, "close")
	res, _ := b690aInRepo(t, "block", "close", "B1", "--run", "r1", "--reason=sk-private")
	want := "code-forge: block [REDACTED] is not supported in this case yet ([REDACTED]-unsupported-flag). Please report it: code-forge logs report\n"
	if res.code != 1 || res.stderr != want {
		t.Fatalf("code=%d stderr=%q; want exit 1 and %q", res.code, res.stderr, want)
	}
}
