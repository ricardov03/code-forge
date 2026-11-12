package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/procguard"
)

// B553: `code-forge review` with a file to review runs the whole flow in Go
// (run start → block open → review-file → review-file --wait → block stop →
// run end) with a fake claude on PATH, a temp HOME, a temp
// CODE_FORGE_TMP_ROOT and CODE_FORGE_NODE naming no program, so a hand-over
// anywhere (this verb or one of its children) fails the test. When node is
// on PATH each case also runs through Node (CODE_FORGE_NATIVE=0) on a twin
// fixture and the two must match.
//
// Masks (the only ones): run ids `r-<yyyymmdd>-<hhmmss>-<hex>` and block ids
// `R-<yyyymmdd>-<hhmmss>-<hex>` (a time and random bytes). Commits carry a
// fixed date, so the base sha is compared as is.

var (
	b553RunRe   = regexp.MustCompile(`r-[0-9]{8}-[0-9]{6}-[0-9a-f]+`)
	b553BlockRe = regexp.MustCompile(`R-[0-9]{8}-[0-9]{6}-[0-9a-f]{4}`)
)

func b553Mask(s string) string {
	return b553BlockRe.ReplaceAllString(b553RunRe.ReplaceAllString(s, "<RUN>"), "<BLOCK>")
}

const b553Claude = `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case $(cat "$(dirname "$0")/mode") in
approve) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
findings) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"src/a.txt","line_start":2,"line_end":2,"severity":"warning","category":"correctness","claim":"a bug","evidence":"the line says world","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
fail) echo boom >&2; exit 3 ;;
esac
`

const b553Config = `version: 1
project:
  slug: rvb553
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
review:
  min_tokens_out: 40
`

// b553Binary is the test-built binary placed as a package would hold it
// (<pkg>/native/bin/<platform>/code-forge-native), the package's src, bin and
// package.json linked from this repository: the Go worker reads its lenses
// and schema there, and a hand-over runs <pkg>/bin/code-forge.mjs.
func b553Binary(t *testing.T) string {
	t.Helper()
	built := b511BuildNative(t)
	pkg, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pkg, "native", "bin", "test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "code-forge-native")
	if err := os.Rename(built, bin); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"src", "bin", "package.json"} {
		if err := os.Symlink(filepath.Join(b458Repo(t), name), filepath.Join(pkg, name)); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

type b553Fix struct{ dir, home, tmp, bin, repo string }

// b553Fixture: a repo with src/a.txt and src/b.txt committed (fixed date)
// then changed, an untracked .env, a fake claude in mode.
func b553Fixture(t *testing.T, mode string) b553Fix {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := b553Fix{dir, filepath.Join(dir, "home"), filepath.Join(dir, "tmp"), filepath.Join(dir, "bin"), filepath.Join(dir, "repo")}
	for _, d := range []string{f.home, f.tmp, f.bin, filepath.Join(f.repo, "src")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(f.bin, "claude"), b553Claude, 0o755)
	write(filepath.Join(f.bin, "mode"), mode+"\n", 0o644)
	write(filepath.Join(f.repo, ".code-forge.yml"), b553Config, 0o644)
	write(filepath.Join(f.repo, "src", "a.txt"), "hello\nworld\nthree\n", 0o644)
	write(filepath.Join(f.repo, "src", "b.txt"), "one\n", 0o644)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.repo
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + f.home, "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("-c", "init.defaultBranch=main", "init", "-q")
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@e", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	write(filepath.Join(f.repo, "src", "a.txt"), "hello\nWORLD\nthree\n", 0o644)
	write(filepath.Join(f.repo, "src", "b.txt"), "one\ntwo\n", 0o644)
	write(filepath.Join(f.repo, ".env"), "API_KEY=x\n", 0o644)
	return f
}

type b553Side struct {
	code     int
	out, err string
	tmp      []string // the temp root's entries, masked
	home     []string // files under HOME, masked (no hand-over log, no native.lock)
	handover []string
}

func b553Env(f b553Fix, extra ...string) []string {
	env := []string{"PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + f.home, "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=" + f.tmp,
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_KEY_JEV=", procguard.Env + "=" + os.Getenv(procguard.Env)}
	return append(env, extra...)
}

func b553Run(t *testing.T, f b553Fix, argv []string, env []string) b553Side {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = f.repo
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	s := b553Side{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		s.code = exit.ExitCode()
	}
	b553Reap(t, f)
	s.out, s.err = b553Mask(stdout.String()), b553Mask(stderr.String())
	entries, _ := os.ReadDir(f.tmp)
	for _, e := range entries {
		s.tmp = append(s.tmp, b553Mask(e.Name()))
	}
	_ = filepath.WalkDir(f.home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(f.home, path)
		if strings.Contains(rel, "native/handovers") || filepath.Base(rel) == "native.lock" {
			return nil
		}
		s.home = append(s.home, b553Mask(rel))
		return nil
	})
	sort.Strings(s.home)
	s.handover = b460Handovers(t, f.home)
	return s
}

// b553Reap stops the worker a kept run left. The flow cases pass
// --keep-run: a binary built for a test (B529) may signal only processes
// that descend from the test, and the run's worker is a session leader
// whose parent is init, so `run end` could not stop it here.
func b553Reap(t *testing.T, f b553Fix) {
	t.Helper()
	runs, _ := filepath.Glob(filepath.Join(f.repo, ".code-forge", "workers", "*.json"))
	for _, file := range runs {
		pid, err := strconv.Atoi(strings.SplitN(filepath.Base(file), "-", 2)[0])
		if err != nil || pid <= 1 {
			continue
		}
		if b511Command(pid) == "" || !strings.Contains(b511Command(pid), "worker") {
			continue
		}
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		deadline := time.Now().Add(5 * time.Second)
		for b511Command(pid) != "" && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if b511Command(pid) != "" {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
}

func b553Go(t *testing.T, bin string, mode string, args []string, extra ...string) (b553Fix, b553Side) {
	t.Helper()
	f := b553Fixture(t, mode)
	env := b553Env(f, append([]string{"CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE=" + filepath.Join(f.dir, "missing-node")}, extra...)...)
	return f, b553Run(t, f, append([]string{bin, "review"}, args...), env)
}

func b553Node(t *testing.T, mode string, args []string) (b553Fix, b553Side, bool) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		return b553Fix{}, b553Side{}, false
	}
	f := b553Fixture(t, mode)
	env := b553Env(f, "CODE_FORGE_NATIVE=0")
	env[0] += ":" + filepath.Dir(node)
	return f, b553Run(t, f, append([]string{node, filepath.Join(b458Repo(t), "bin", "code-forge.mjs"), "review"}, args...), env), true
}

func b553Base(t *testing.T, f b553Fix) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = f.repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestB553ReviewFlowGo(t *testing.T) {
	bin := b553Binary(t)
	findingsText := "src/a.txt  1 finding\n\nfix list: src/a.txt\n  F1 · warning · lines 2-2\n    claim: a bug\n    fix:   remove it\n\ntotals: 1 file · 0 approved · 1 with findings (1 finding) · 0 stopped · 0 unavailable\n"
	cases := []struct {
		name, mode string
		args       []string
		code       int
		out        func(base string) string
		err        string
	}{
		{"one file passes", "approve", []string{"--files", "src/a.txt", "--max", "60", "--keep-run"}, 0,
			func(string) string {
				return "src/a.txt  approved\n\ntotals: 1 file · 1 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n"
			},
			"review: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  approved\n"},
		{"findings", "findings", []string{"--files", "src/a.txt", "--max", "60", "--keep-run"}, 1,
			func(string) string { return findingsText },
			"review: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  1 finding\n"},
		{"provider fails", "fail", []string{"--files", "src/a.txt", "--max", "60", "--keep-run"}, 1,
			func(string) string {
				return "src/a.txt  unavailable: exit\n\ntotals: 1 file · 0 approved · 0 with findings (0 findings) · 0 stopped · 1 unavailable\n"
			},
			"review: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  unavailable: exit\n"},
		{"json", "findings", []string{"--files", "src/a.txt", "--json", "--max", "60", "--keep-run"}, 1,
			func(base string) string {
				return `{"ok":false,"base":"` + base + `","base_from":"HEAD (uncommitted changes)","run":"<RUN>","block":"<BLOCK>","files":[{"file":"src/a.txt","result":"findings","reason":null,"findings":[{"id":"F1","severity":"warning","lines":"2-2","claim":"a bug","fix":"remove it"}]}],"skipped":[],"totals":{"files":1,"approved":0,"unchanged":0,"with_findings":1,"findings":1,"stopped":0,"unavailable":0}}` + "\n"
			},
			"review: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  1 finding\n"},
		{"skipped plus reviewed", "approve", []string{"--files", "src/a.txt", ".env", "--max", "60", "--keep-run"}, 0,
			func(string) string {
				return "src/a.txt  approved\n\ntotals: 1 file · 1 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n"
			},
			"review: skipped .env (secret-like name, never sent to a reviewer)\nreview: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  approved\n"},
		{"intent", "approve", []string{"--files", "src/a.txt", "--intent", "say hi", "--max", "60", "--keep-run"}, 0,
			func(string) string {
				return "src/a.txt  approved\n\ntotals: 1 file · 1 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n"
			},
			"review: base %s (HEAD (uncommitted changes)) · 1 file · run <RUN>\nreview: src/a.txt  approved\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, g := b553Go(t, bin, c.mode, c.args)
			base := b553Base(t, f)
			wantErr := strings.Replace(c.err, "%s", base[:12], 1) + "review: run <RUN> kept (--keep-run): reuse it with --run <RUN>; end it with `code-forge run end --run <RUN>`\n"
			if g.code != c.code || g.out != c.out(base) || g.err != wantErr {
				t.Fatalf("go = %d\n%q\n%q\nwant %d\n%q\n%q", g.code, g.out, g.err, c.code, c.out(base), wantErr)
			}
			if len(g.handover) != 0 {
				t.Fatalf("hand-overs: %v", g.handover)
			}
			// the review's temp root is gone; the run root of the ended run stays, as with Node
			if strings.Join(g.tmp, ",") != "<RUN>" {
				t.Fatalf("temp root entries = %v", g.tmp)
			}
			nf, n, ok := b553Node(t, c.mode, c.args)
			if !ok {
				t.Log("node is not on PATH: the Node twin is skipped")
				return
			}
			if b553Base(t, nf) != base {
				t.Fatalf("twin bases differ")
			}
			if n.code != g.code || n.out != g.out || n.err != g.err {
				t.Fatalf("go %d %q %q\nnode %d %q %q", g.code, g.out, g.err, n.code, n.out, n.err)
			}
			if strings.Join(n.tmp, ",") != strings.Join(g.tmp, ",") || strings.Join(n.home, "\n") != strings.Join(g.home, "\n") {
				t.Fatalf("files differ\ngo   %v %v\nnode %v %v", g.tmp, g.home, n.tmp, n.home)
			}
		})
	}
}

// Node's refusals before the run: Go answers them the same, with nothing
// written and nothing handed over.
func TestB553ReviewRefusalsGo(t *testing.T) {
	bin := b553Binary(t)
	t.Run("acceptance not a list", func(t *testing.T) {
		f := b553Fixture(t, "approve")
		file := filepath.Join(f.dir, "acc.json")
		if err := os.WriteFile(file, []byte(`{"clause":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
		s := b553Run(t, f, []string{bin, "review", "--files", "src/a.txt", "--acceptance", file}, env)
		if s.code != 2 || s.out != "" || s.err != "review: --acceptance "+file+": must be a non-empty list of {clause, tests: [test ids…]}\n" || len(s.handover) != 0 || len(s.tmp) != 0 {
			t.Fatalf("got %+v", s)
		}
	})
	t.Run("acceptance unreadable", func(t *testing.T) {
		f := b553Fixture(t, "approve")
		file := filepath.Join(f.dir, "none.json")
		env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
		s := b553Run(t, f, []string{bin, "review", "--files", "src/a.txt", ".env", "--acceptance", file}, env)
		if s.code != 2 || s.out != "" || s.err != "review: --acceptance "+file+": not a readable YAML or JSON file\n" || len(s.handover) != 0 || len(s.tmp) != 0 {
			t.Fatalf("got %+v", s)
		}
	})
	t.Run("config does not load", func(t *testing.T) {
		f := b553Fixture(t, "approve")
		if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte("version: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
		s := b553Run(t, f, []string{bin, "review", "--files", "src/a.txt"}, env)
		if s.code != 2 || s.out != "" || s.err != "review: .code-forge.yml does not load (parse-error) — run `code-forge validate`\n" || len(s.handover) != 0 || len(s.tmp) != 0 {
			t.Fatalf("got %+v", s)
		}
	})
}

// Several files: the Go worker gives such tickets to a Node worker (the
// moved-code hints are not ported) and a wait already polling reads the gap
// as worker_down, so the run stays Node's: a named hand-over before anything
// is written or printed. With node on PATH the handed-over run matches Node.
func TestB553ReviewSeveralFilesHandOver(t *testing.T) {
	bin := b553Binary(t)
	f, g := b553Go(t, bin, "approve", []string{"--max", "60"})
	if g.code != 127 || g.out != "" || strings.Contains(g.err, "review:") || strings.Join(g.handover, ",") != "review review-worker-handoff" || len(g.tmp) != 0 {
		t.Fatalf("several files = %+v", g)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs")); err == nil {
		t.Fatal("a run was started before the hand-over")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH: the Node twin is skipped")
	}
	f2 := b553Fixture(t, "approve")
	env := b553Env(f2, "CODE_FORGE_NODE="+node)
	env[0] += ":" + filepath.Dir(node)
	g2 := b553Run(t, f2, []string{bin, "review", "--max", "60"}, env)
	_, n, _ := b553Node(t, "approve", []string{"--max", "60"})
	want := "src/a.txt  approved\nsrc/b.txt  approved\n\ntotals: 2 files · 2 approved · 0 with findings (0 findings) · 0 stopped · 0 unavailable\n"
	// the per-file progress lines come as each wait ends, in either order
	lines := func(s string) string { l := strings.Split(s, "\n"); sort.Strings(l); return strings.Join(l, "\n") }
	if g2.code != 0 || g2.out != want || n.code != g2.code || n.out != g2.out || lines(n.err) != lines(g2.err) {
		t.Fatalf("go via node %d %q %q\nnode %d %q %q", g2.code, g2.out, g2.err, n.code, n.out, n.err)
	}
}
