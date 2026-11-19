package main

import (
	"io"
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

// b690aRunDirSeam writes ~/.code-forge/runs/r1.json in the fresh HOME
// b508Run sets, before dispatch, with an open block whose owned_files is not
// a list: a registry shape block attempt still leaves to Node. (It was a
// run-file folder until B759 ported that read error.)
func b690aRunDirSeam([]string) {
	runs := filepath.Join(os.Getenv("HOME"), ".code-forge", "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		panic(err)
	}
	record := `{"run_id":"r1","blocks":{"B1":{"status":"open","owned_files":"x"}}}`
	if err := os.WriteFile(filepath.Join(runs, "r1.json"), []byte(record), 0o600); err != nil {
		panic(err)
	}
}

func b690aInRepoSeam(t *testing.T, seam func([]string), args ...string) (b508Result, string) {
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
	return b508Run(t, seam, args...), repo
}

// A case that used to start Node (block attempt on a registry shape Go does
// not serve; B744 and B759 ported the cases it used before) now gets the Go refusal: one stderr line, exit 1, empty stdout, and the
// only file written is the handovers.jsonl row (with its lock file).
func TestB690aFormerHandOverRefuses(t *testing.T) {
	res, repo := b690aInRepoSeam(t, b690aRunDirSeam, "block", "attempt", "B1", "--run", "r1")
	want := "code-forge: block attempt is not supported in this case yet (block-unported-subcommand). Please report it: code-forge logs report\n"
	if res.code != 1 || res.err != nil || res.stdout != "" || res.stderr != want {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 1 and %q", res.code, res.err, res.stdout, res.stderr, want)
	}
	if files := b690aFiles(t, repo); len(files) != 0 {
		t.Errorf("repo files %q; want none", files)
	}
	home := b690aFiles(t, res.home)
	if strings.Join(home, ",") != ".code-forge/native/handovers.jsonl,.code-forge/native/handovers.jsonl.lock,.code-forge/runs/r1.json" {
		t.Errorf("HOME files %q; want only the run record, the handovers log and its lock", home)
	}
}

// The refusal still writes the handovers.jsonl row, with its fields kept:
// verb, sub, reason and version.
func TestB690aRefusalWritesHandOverRow(t *testing.T) {
	res, _ := b690aInRepoSeam(t, b690aRunDirSeam, "block", "attempt", "B1", "--run", "r1")
	if res.code != 1 || res.err != nil {
		t.Fatalf("code=%d err=%v; want the refusal, exit 1", res.code, res.err)
	}
	rows := b508Rows(t, res.home)
	if len(rows) != 1 || rows[0].Verb != "block" || rows[0].Sub != "attempt" || rows[0].Reason != "block-unported-subcommand" || rows[0].Version != version || rows[0].At == "" {
		t.Fatalf("rows %+v; want one block attempt block-unported-subcommand row", rows)
	}
}

// b690bNodeVerbs is the verb list of the removed Node router (src/cli's verb
// file names at B690b), which the unknown-verb text keeps.
var b690bNodeVerbs = []string{"author", "autopilot", "block", "ci", "cloud-coder", "context", "decisions", "docs", "doctor", "facts", "gates", "heal", "help", "init", "jev", "keys", "land", "ledger", "list", "logs", "models", "native-status", "outcome", "pipeline", "plan", "proof", "queue", "remove", "report", "resolve", "review", "review-file", "run", "s2", "setup", "spawn", "status", "sweep", "tools", "upgrade", "validate", "version", "worker"}

// An unknown verb prints the unknown-verb text (since B878: the usage line
// and the visible verbs of the help table, sorted), exits 2 (a usage error)
// and writes nothing at all.
func TestB690aUnknownVerbMatchesNode(t *testing.T) {
	// The visible verbs: the Node router's list (B690b), config (B791),
	// release (B799) and audit (B802), without the hidden land and native-status.
	verbs := []string{"config", "release", "audit"}
	for _, verb := range b690bNodeVerbs {
		if verb != "land" && verb != "native-status" {
			verbs = append(verbs, verb)
		}
	}
	sort.Strings(verbs)
	want := "code-forge: unknown verb \"private-verb\"\nusage: code-forge <command> [options] [arguments]\nVerbs:\n  " + strings.Join(verbs, "\n  ") + "\nRun code-forge help for what each one does.\n"
	res, repo := b690aInRepo(t, "private-verb", "private-value")
	if res.code != 2 || res.err != nil || res.stdout != "" || res.stderr != want {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 2 and %q", res.code, res.err, res.stdout, res.stderr, want)
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
		want := "code-forge: unknown verb \"sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"\nusage: code-forge <command> [options] [arguments]\nVerbs:\n"
		if res.code != 2 || !strings.HasPrefix(res.stderr, want) {
			t.Fatalf("code=%d stderr=%q; want exit 2 and prefix %q", res.code, res.stderr, want)
		}
	})
	t.Run("a registered secret is masked", func(t *testing.T) {
		// B756: each verb starts with no secret registered (B715), so the
		// secret is registered inside the command, before the line prints.
		code, stderr := b756WithStderr(t, func() int {
			b690aWithSecrets(t, token)
			return unknownVerb(token)
		})
		// Recorded: redact() of the same text after registerSecret(token).
		want := "code-forge: unknown verb \"[REDACTED]\"\nusage: code-forge <command> [options] [arguments]\nVerbs:\n"
		if code != 2 || !strings.HasPrefix(stderr, want) || strings.Contains(stderr, token) {
			t.Fatalf("code=%d stderr=%q; want exit 2, prefix %q and no raw token", code, stderr, want)
		}
	})
}

// b756WithStderr runs fn from an empty temp folder with a temp HOME (as
// b508Run sets up) and returns its exit code and what it wrote to stderr.
func b756WithStderr(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { data, _ := io.ReadAll(r); done <- string(data) }()
	code := fn()
	os.Stderr = saved
	_ = w.Close()
	return code, <-done
}

// The refusal line goes through the same redaction (recorded from Node's
// redact() after registerSecret("close")).
func TestB690aRefusalLineRedacts(t *testing.T) {
	// B756: the secret is registered inside the command (B715: each verb
	// starts with none), then the block close refusal prints.
	code, stderr := b756WithStderr(t, func() int {
		b690aWithSecrets(t, "close")
		return refuseUnported([]string{"block", "close", "B1", "--run", "r1", "--reason=sk-private"}, hCloseGatesFormat)
	})
	want := "code-forge: block [REDACTED] is not supported in this case yet ([REDACTED]-gates-format). Please report it: code-forge logs report\n"
	if code != 1 || stderr != want {
		t.Fatalf("code=%d stderr=%q; want exit 1 and %q", code, stderr, want)
	}
}
