package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B828: the customer case. A Laravel 13 + Pest repository with untracked
// .env and .env.testing: .code-forge/export/ held a copy of .env and git
// did not ignore .code-forge/. init and upgrade now add the .gitignore line,
// doctor reports the folder, and run start, proof export and proof
// red-green refuse while git would see it.

// b828LaravelIgnore is the .gitignore a Laravel 13 skeleton ships (no
// .code-forge/ line; .env.testing is not in it).
const b828LaravelIgnore = "/.phpunit.cache\n/node_modules\n/public/build\n/public/hot\n/public/storage\n/storage/*.key\n/vendor\n.env\n.env.backup\n.env.production\n.phpactor.json\n.phpunit.result.cache\nHomestead.json\nHomestead.yaml\nauth.json\nnpm-debug.log\nyarn-error.log\n/.fleet\n/.idea\n/.vscode\n"

func b828Git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func b828MustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := b828Git(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// b828Laravel makes dir a Laravel 13 + Pest git repository with one commit
// and the untracked .env and .env.testing of the customer.
func b828Laravel(t *testing.T, dir, gitignore string) {
	t.Helper()
	files := map[string]string{
		"composer.json":              `{"name":"acme/app","require":{"php":"^8.3","laravel/framework":"^13.0"},"require-dev":{"pestphp/pest":"^4.0","pestphp/pest-plugin-laravel":"^4.0"}}` + "\n",
		"artisan":                    "#!/usr/bin/env php\n<?php\n",
		"app/Models/User.php":        "<?php\nnamespace App\\Models;\nclass User {}\n",
		"tests/Pest.php":             "<?php\n",
		"tests/Feature/UserTest.php": "<?php\nit('works', fn () => expect(true)->toBeTrue());\n",
		"phpunit.xml":                "<phpunit/>\n",
	}
	if gitignore != "" {
		files[".gitignore"] = gitignore
	}
	for rel, body := range files {
		b462Write(t, filepath.Join(dir, rel), []byte(body), 0o644)
	}
	b828MustGit(t, dir, "init", "-qb", "main")
	b828MustGit(t, dir, "add", "-A")
	b828MustGit(t, dir, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "laravel base")
	b462Write(t, filepath.Join(dir, ".env"), []byte("APP_KEY=base64:secret\nDB_PASSWORD=hunter2\n"), 0o600)
	b462Write(t, filepath.Join(dir, ".env.testing"), []byte("DB_PASSWORD=testing-secret\n"), 0o600)
}

// b828Ignored runs `git check-ignore` on a path as the customer would.
func b828Ignored(t *testing.T, dir, rel string) bool {
	t.Helper()
	_, err := b828Git(t, dir, "check-ignore", "-q", "--", rel)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return err == nil
}

// b828Exclude makes git ignore .code-forge/ in repo through
// .git/info/exclude, which no tree scan, diff or commit sees: the fixture
// fix for tests that start runs in a fresh repository (B828 refuses a run
// while git would see the state folder).
func b828Exclude(t *testing.T, repo string) {
	t.Helper()
	file := filepath.Join(repo, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("\n.code-forge/\n"); err != nil {
		t.Fatal(err)
	}
}

func b828Count(text, line string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

// b828InitAgain runs a second `init --no-interaction` in cwd with the same
// sealed seams b548Go uses.
func b828InitAgain(t *testing.T, cwd string) (int, string, string) {
	t.Helper()
	savedTTY, savedNow := initStdinTTY, initNow
	initStdinTTY = func() bool { return false }
	initNow = func() time.Time { return time.Now().UTC() }
	repo := b458Repo(t)
	savedRoot, savedUpgrade := metaPackageRoot, upgradePackageRoot
	metaPackageRoot = func() (string, error) { return repo, nil }
	upgradePackageRoot = func() (string, bool) { return repo, true }
	defer func() {
		initStdinTTY, initNow = savedTTY, savedNow
		metaPackageRoot, upgradePackageRoot = savedRoot, savedUpgrade
	}()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	return runCaptured(t, "init", "--no-interaction", "--skip-doctor")
}

func TestB828InitIgnoresTheStateFolder(t *testing.T) {
	var cwd string
	side := b548Go(t, b548Case{name: "b828 laravel", args: []string{"--no-interaction", "--skip-doctor", "--no-jev"}, env: b548File, setup: func(dir string) {
		cwd = dir
		b828Laravel(t, dir, b828LaravelIgnore)
	}})
	if side.code != 0 || !strings.Contains(side.stdout, `".gitignore: added .code-forge/"`) {
		t.Fatalf("init: code %d stdout %q stderr %q", side.code, side.stdout, side.stderr)
	}
	for _, rel := range []string{".code-forge/export/B1/.env", ".code-forge/export/B1/.env.testing", ".code-forge/runs/r1.json"} {
		if !b828Ignored(t, cwd, rel) {
			t.Fatalf("git does not ignore %s after init", rel)
		}
	}
	first := readText(t, filepath.Join(cwd, ".gitignore"))
	if first != b828LaravelIgnore+".code-forge/\n" {
		t.Fatalf("init did not keep the user's lines in order:\n%s", first)
	}
	// a second init adds no duplicate line and says nothing about it
	code, stdout, stderr := b828InitAgain(t, cwd)
	if code != 0 || strings.Contains(stdout, ".gitignore") {
		t.Fatalf("second init: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if again := readText(t, filepath.Join(cwd, ".gitignore")); again != first || b828Count(again, ".code-forge/") != 1 {
		t.Fatalf("second init changed .gitignore:\n%s", again)
	}
}

// init creates a missing .gitignore; a file without a final newline keeps
// its last line whole.
func TestB828InitCreatesOrCompletesTheGitignore(t *testing.T) {
	for _, c := range []struct{ name, before, after string }{
		{"missing", "", ".code-forge/\n"},
		{"no final newline", "/vendor\n.env", "/vendor\n.env\n.code-forge/\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var cwd string
			side := b548Go(t, b548Case{name: "b828 " + c.name, args: []string{"--no-interaction", "--skip-doctor", "--no-jev"}, env: b548File, setup: func(dir string) {
				cwd = dir
				b828Laravel(t, dir, c.before)
			}})
			if side.code != 0 || !strings.Contains(side.stdout, `".gitignore: added .code-forge/"`) {
				t.Fatalf("init: code %d stdout %q stderr %q", side.code, side.stdout, side.stderr)
			}
			if got := readText(t, filepath.Join(cwd, ".gitignore")); got != c.after {
				t.Fatalf(".gitignore = %q, want %q", got, c.after)
			}
		})
	}
}

// upgrade on a 0.25.1 project (config, no .gitignore line) adds the line
// once; the second upgrade leaves the file alone.
func TestB828UpgradeAddsTheLine(t *testing.T) {
	_, repo, _ := upgradeFixture(t)
	b462Write(t, filepath.Join(repo, ".code-forge.yml"), []byte("version: 1\nprovider: anthropic\n"), 0o600)
	b462Write(t, filepath.Join(repo, ".gitignore"), []byte(b828LaravelIgnore), 0o644)
	code, stdout, stderr := runCaptured(t, "upgrade")
	if code != 0 || b828Count(stdout, ".gitignore: added .code-forge/") != 1 {
		t.Fatalf("upgrade: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := readText(t, filepath.Join(repo, ".gitignore")); got != b828LaravelIgnore+".code-forge/\n" {
		t.Fatalf(".gitignore after upgrade:\n%s", got)
	}
	if !b828Ignored(t, repo, ".code-forge/export/B1/.env.testing") {
		t.Fatal("git does not ignore the export after upgrade")
	}
	code, stdout, _ = runCaptured(t, "upgrade")
	if code != 0 || strings.Contains(stdout, ".gitignore") {
		t.Fatalf("second upgrade: code %d stdout %q", code, stdout)
	}
	if b828Count(readText(t, filepath.Join(repo, ".gitignore")), ".code-forge/") != 1 {
		t.Fatal("second upgrade added a duplicate line")
	}
}

// A repository that committed .code-forge/runs/<run>.json: the doctor row
// fails with the exact fix, init and upgrade say it too, and nothing runs
// the fix (the file stays tracked).
func TestB828TrackedStateIsReported(t *testing.T) {
	_, repo, _ := upgradeFixture(t)
	b462Write(t, filepath.Join(repo, ".code-forge.yml"), []byte("version: 1\nprovider: anthropic\n"), 0o600)
	b462Write(t, filepath.Join(repo, ".code-forge", "runs", "r-acme-1.json"), []byte(`{"run_id":"r-acme-1"}`+"\n"), 0o600)
	b828MustGit(t, repo, "add", "-A")
	b828MustGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "oops")
	b462Write(t, filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0o644)
	const want = ".code-forge/: git tracks 1 file under it (.code-forge/runs/r-acme-1.json); remove them from git with: git rm -r --cached .code-forge && git commit"
	row, shown := doctorStateIgnoreRow(repo)
	if !shown || row.status != "FAIL" || row.label != ".code-forge/ ignored by git" || row.detail != want {
		t.Fatalf("doctor row = %+v shown=%v", row, shown)
	}
	code, stdout, _ := runCaptured(t, "upgrade")
	if code != 0 || b828Count(stdout, want) != 1 {
		t.Fatalf("upgrade did not say the fix: code %d stdout %q", code, stdout)
	}
	if tracked := b828MustGit(t, repo, "ls-files", ".code-forge"); tracked != ".code-forge/runs/r-acme-1.json\n" {
		t.Fatalf("something ran the fix: ls-files = %q", tracked)
	}
}

func TestB828DoctorRow(t *testing.T) {
	plain := t.TempDir()
	if _, shown := doctorStateIgnoreRow(plain); shown {
		t.Fatal("the row shows outside a git work tree")
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	b828Laravel(t, repo, b828LaravelIgnore)
	row, shown := doctorStateIgnoreRow(repo)
	wantFail := "no; git would see the proof export (copies of .env) and the run files; fix: add the line .code-forge/ to " + filepath.Join(repo, ".gitignore") + " (code-forge upgrade or init adds it)"
	if !shown || row.status != "FAIL" || row.detail != wantFail {
		t.Fatalf("visible row = %+v", row)
	}
	b462Write(t, filepath.Join(repo, ".gitignore"), []byte(b828LaravelIgnore+".code-forge/\n"), 0o644)
	if row, _ = doctorStateIgnoreRow(repo); row.status != "OK" || row.detail != "yes" {
		t.Fatalf("ignored row = %+v", row)
	}
}

// b828ExportRepo is b462's export repo with the customer's .gitignore: the
// .code-forge/ line only when ignore is true, plus an untracked .env.testing.
func b828ExportRepo(ignore bool) func(t *testing.T, repo string) {
	return func(t *testing.T, repo string) {
		b462ExportRepo(b462Levels+"proof:\n  export:\n    copy_untracked: [.env, .env.testing]\n")(t, repo)
		body := b828LaravelIgnore
		if ignore {
			body += ".code-forge/\n"
		}
		b462Write(t, filepath.Join(repo, ".gitignore"), []byte(body), 0o644)
		b462Write(t, filepath.Join(repo, ".env.testing"), []byte("DB_PASSWORD=testing-secret\n"), 0o600)
	}
}

func TestB828ProofExportRefusesWhileVisible(t *testing.T) {
	args := []string{"proof", "export", "B1", "--run", b462Run}
	home, repo := b462Fixture(t, b462Case{blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}}, setup: b828ExportRepo(false)})
	code, stdout, stderr := b462GoCall(t, home, repo, args)
	want := "proof export: .code-forge/ is not ignored by git, so the export (copies of .env) would be visible to git; nothing was exported. Fix: add the line .code-forge/ to " + filepath.Join(repo, ".gitignore") + " (code-forge upgrade adds it), then re-run\n"
	if code != 1 || stdout != "" || stderr != want {
		t.Fatalf("got %d %q %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, ".code-forge", "export")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused export left files: %v", err)
	}

	home, repo = b462Fixture(t, b462Case{blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}}, setup: b828ExportRepo(true)})
	if code, _, stderr := b462GoCall(t, home, repo, args); code != 0 {
		t.Fatalf("export with the line failed: %s", stderr)
	}
	for _, rel := range []string{".env", ".env.testing"} {
		if _, err := os.Stat(filepath.Join(repo, ".code-forge", "export", "B1", rel)); err != nil {
			t.Fatalf("the export has no %s: %v", rel, err)
		}
	}
	for _, line := range strings.Split(b828MustGit(t, repo, "status", "--porcelain", "--untracked-files=all"), "\n") {
		if strings.Contains(line, ".code-forge/") {
			t.Fatalf("git status shows the state folder: %q", line)
		}
	}
}

func TestB828ProofRedGreenRefusesWhileVisible(t *testing.T) {
	c := b481Case{name: "b828-visible", mode: "src", args: b481Args, prepare: func(t *testing.T, repo string) {
		b462Write(t, filepath.Join(repo, ".gitignore"), []byte("/vendor\n"), 0o644)
	}}
	home, repo := b481Fixture(t, c)
	code, stdout, stderr, logged := b481Call(t, home, repo, c.mode, c.args)
	want := "proof red-green: .code-forge/ is not ignored by git, so the export (copies of .env) would be visible to git; nothing was exported. Fix: add the line .code-forge/ to <REPO>/.gitignore (code-forge upgrade adds it), then re-run\n"
	if code != 1 || stdout != "" || stderr != want || logged != "" {
		t.Fatalf("got %d %q %q (runner %q)", code, stdout, stderr, logged)
	}
	if _, err := os.Stat(filepath.Join(repo, ".code-forge", "export")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused red-green left an export: %v", err)
	}
}

func TestB828RunStartRefusesWhileVisible(t *testing.T) {
	f := newStartFixture(t, "project:\n  slug: unit\ntmp:\n  root: TMPROOT\n")
	// the fixture's exclude line off: the customer's repository
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	b462Write(t, filepath.Join(f.repo, ".env"), []byte("APP_KEY=secret\n"), 0o600)
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo)
	ws, _ := filepath.EvalSymlinks(f.repo)
	want := "run start: .code-forge/ is not ignored by git in " + ws + ", so the proof export (copies of .env) and the run files would be visible to git; no run was started. Fix: add the line .code-forge/ to " + filepath.Join(ws, ".gitignore") + " (code-forge upgrade adds it), then re-run\n"
	if code != 1 || out != "" || errs != want {
		t.Fatalf("got %d %q %q\nwant %q", code, out, errs, want)
	}
	entries, _ := os.ReadDir(filepath.Join(f.home, ".code-forge", "runs"))
	if len(entries) != 0 || len(f.launched(t)) != 0 {
		t.Fatalf("a refusal wrote a run or launched a worker: %v", entries)
	}
}
