package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/childreg"
)

// B573: the worker, the daemon (internal serve) and every verb whose Node
// counterpart spawns through src/util/exec.mjs record each child in the
// process's run root (pids/<pid>.json while it runs) and give it exec.mjs's
// owner variables. testdata/b573/git.sh is a fake git on PATH: a child with
// CODE_FORGE_OWNER_PID waits for its own entry under CODE_FORGE_TMP_ROOT and
// logs what it sees, then the real git runs. review-flow.txt is Node's log
// (CODE_FORGE_NATIVE=0 ./bin/code-forge review ..., the B553 twin fixture)
// reduced to its distinct lines. Masks: run ids <RUN>, start times <START>,
// every other number <PID>.

var (
	b573Line   = regexp.MustCompile(`^(\S+) git SEEN (\S+) \{"pid":(\d+),"start_time":"([^"]*)","argv0":"([^"]*)"\} self=(\d+) owner=(\d+) start=(.*) run=(\S+)$`)
	b573Run    = regexp.MustCompile(`r-[0-9]{8}-[0-9]{6}-[0-9a-f]+`)
	b573Start  = regexp.MustCompile(`[A-Z][a-z]{2} [A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2} \d{4}`)
	b573Number = regexp.MustCompile(`\b\d+\b`)
)

// b573Fake puts the fake git first on dir's PATH entry and returns the log.
func b573Fake(t *testing.T, bin string) string {
	t.Helper()
	fake, err := os.ReadFile(filepath.Join("testdata", "b573", "git.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(t.TempDir(), "git.log")
}

// b573Lines checks every logged child: its entry is its own, names git, has a
// ps start time, and the owner variables are set; it returns the distinct
// masked lines.
func b573Lines(t *testing.T, log string) []string {
	t.Helper()
	data, _ := os.ReadFile(log)
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		t.Fatal("no child was spawned with the owner variables (no registry)")
	}
	t.Logf("%d children logged", strings.Count(text, "\n")+1)
	set := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := b573Line.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("child without its pid entry: %q", line)
			continue
		}
		if m[3] != m[6] || m[5] != "git" || !b573Start.MatchString(m[4]) || !b573Start.MatchString(m[8]) {
			t.Errorf("bad entry or owner variables: %q", line)
		}
		masked := b573Start.ReplaceAllString(b573Run.ReplaceAllString(line, "<RUN>"), "<START>")
		set[b573Number.ReplaceAllString(masked, "<PID>")] = true
	}
	lines := []string{}
	for line := range set {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines
}

// b573Install is main()'s childregInstall for an in-process verb, undone at
// the end of the test (the lazy root released as at exit).
func b573Install(t *testing.T, args ...string) {
	t.Helper()
	lrRoot, lrLazyRoots = "", nil
	childregInstall(args)
	t.Cleanup(func() {
		childregRelease()
		childreg.Started, childreg.Env, childregArgs = nil, nil, nil
		lrRoot, lrLazyRoots = "", nil
	})
}

// b573Left lists what is under the temp root.
func b573Left(t *testing.T, root string) []string {
	t.Helper()
	entries, _ := os.ReadDir(root)
	left := []string{}
	for _, e := range entries {
		left = append(left, e.Name())
	}
	return left
}

// The whole review flow through the built binary: `review` (the CLI), the
// worker it starts and the daemon the worker starts (internal serve) record
// each git child like Node's exec: the same distinct lines as Node's flow,
// plus the daemon's, which are the lazy root's lines of a process run with
// --run (Node runs no daemon with CODE_FORGE_NATIVE=0).
func TestB573ReviewFlowWorkerAndDaemonRegisterChildren(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	log := b573Fake(t, f.bin)
	env := b553Env(f, "FAKELOG="+log, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	s := b553Run(t, f, []string{bin, "review", "--files", "src/a.txt", "--max", "60", "--keep-run"}, env)
	if s.code != 0 || len(s.handover) != 0 {
		t.Fatalf("review = %d %q %q, hand-overs %v", s.code, s.out, s.err, s.handover)
	}
	got := b573Lines(t, log)
	want := strings.Split(strings.TrimSuffix(b500Read(t, filepath.Join("testdata", "b573", "review-flow.txt")), "\n"), "\n")
	byWho := map[string][]string{}
	for _, line := range got {
		who := strings.SplitN(line, " ", 2)[0]
		byWho[who] = append(byWho[who], line)
	}
	if len(byWho["serve"]) == 0 {
		t.Errorf("the daemon logged no child:\n%s", strings.Join(got, "\n"))
	}
	for _, line := range byWho["serve"] {
		if want := `serve git SEEN proc-<PID>-<PID> {"pid":<PID>,"start_time":"<START>","argv0":"git"} self=<PID> owner=<PID> start=<START> run=<RUN>`; line != want {
			t.Errorf("daemon child %q, want %q", line, want)
		}
	}
	rest := append(byWho["cli"], byWho["worker"]...)
	sort.Strings(rest)
	if strings.Join(rest, "\n") != strings.Join(want, "\n") {
		t.Errorf("CLI and worker children:\n%s\nwant (Node):\n%s", strings.Join(rest, "\n"), strings.Join(want, "\n"))
	}
	// the lazy roots are released: only the kept run's root stays, as with Node
	if strings.Join(s.tmp, ",") != "<RUN>" {
		t.Errorf("temp root entries = %v", s.tmp)
	}
}

// block close: every git child of the close path is recorded in the lazy
// root while it runs, with the run id from --run, and the root goes at exit.
func TestB573CloseRegistersEveryGitChild(t *testing.T) {
	f := newCloseReviewFixture(t, 2)
	bin, tmp := t.TempDir(), t.TempDir()
	log := b573Fake(t, bin)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAKELOG", log)
	t.Setenv("CODE_FORGE_TMP_ROOT", tmp)
	t.Setenv("CODE_FORGE_OWNER_PID", "")
	b573Install(t, "block", "close", "B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript)
	if err := os.MkdirAll(filepath.Join(f.top, ".code-forge/queue"), 0o700); err != nil {
		t.Fatal(err)
	}
	done := f.reviewer("approved")
	code, out, errs := f.close()
	if err := <-done; err != nil {
		t.Error(err)
	}
	if code != 0 || !strings.Contains(out, "block B1 closed") {
		t.Fatalf("close = %d %s %s", code, out, errs)
	}
	want := `cli git SEEN proc-<PID>-<PID> {"pid":<PID>,"start_time":"<START>","argv0":"git"} self=<PID> owner=<PID> start=<START> run=r-close-fixture`
	if got := b573Lines(t, log); strings.Join(got, "\n") != want {
		t.Errorf("close children:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	b573OwnerIsThis(t, log)
	childregRelease()
	if left := b573Left(t, tmp); len(left) != 0 {
		t.Errorf("lazy root left: %v", left)
	}
}

// block land (merge gate, merge, worktree removal): every git child of the
// land path, the gate's included, is recorded like Node's exec.
func TestB573LandRegistersEveryGitChild(t *testing.T) {
	bin, tmp := t.TempDir(), t.TempDir()
	log := b573Fake(t, bin)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAKELOG", log)
	t.Setenv("CODE_FORGE_TMP_ROOT", tmp)
	t.Setenv("CODE_FORGE_OWNER_PID", "")
	b573Install(t, "block", "land", "B1", "--run", "r-land-unit")
	blockLandChain(t, "same-tree")
	got := b573Lines(t, log)
	want := `cli git SEEN proc-<PID>-<PID> {"pid":<PID>,"start_time":"<START>","argv0":"git"} self=<PID> owner=<PID> start=<START> run=r-land-unit`
	if strings.Join(got, "\n") != want {
		t.Errorf("land children:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	b573OwnerIsThis(t, log)
}

// b573OwnerIsThis: an in-process verb's children name this process.
func b573OwnerIsThis(t *testing.T, log string) {
	t.Helper()
	data, _ := os.ReadFile(log)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if !strings.Contains(line, " owner="+strconv.Itoa(os.Getpid())+" ") {
			t.Errorf("owner is not this process: %q", line)
		}
	}
}

// The run id rule of exec.mjs: the environment's, else --run X / --run=X on
// the command line, else an r- root's name; owner start time from ps.
func TestB573OwnerEnvRunID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CODE_FORGE_TMP_ROOT", tmp)
	t.Setenv("CODE_FORGE_RUN_ID", "")
	_ = os.Unsetenv("CODE_FORGE_RUN_ID")
	cases := []struct {
		name string
		args []string
		env  []string
		root string
		want string
	}{
		{"argv", []string{"status", "--run", "r-1"}, nil, "", "r-1"},
		{"argv inline", []string{"status", "--run=r-2"}, nil, "", "r-2"},
		{"own env wins", []string{"status", "--run", "r-1"}, []string{"CODE_FORGE_RUN_ID=r-own"}, "", "r-own"},
		{"run root", []string{"status"}, nil, "r-root", "r-root"},
		{"none", []string{"status"}, nil, "", ""},
		{"--run last", []string{"status", "--run"}, nil, "", ""},
	}
	for _, c := range cases {
		lrRoot, lrLazyRoots = "", nil
		if c.root != "" {
			lrRoot = filepath.Join(tmp, c.root)
		}
		childregArgs = c.args
		env := childregOwnerEnv(append([]string{"CODE_FORGE_OWNER_PID=1", "CODE_FORGE_OWNER_START_TIME=x", "A=b"}, c.env...))
		got, owner, start := "", "", ""
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "CODE_FORGE_RUN_ID":
				got = v
			case "CODE_FORGE_OWNER_PID":
				owner = v
			case "CODE_FORGE_OWNER_START_TIME":
				start = v
			}
		}
		if got != c.want || owner != strconv.Itoa(os.Getpid()) || !b573Start.MatchString(start) || env[0] != "A=b" {
			t.Errorf("%s: run %q owner %q start %q env %v", c.name, got, owner, start, env)
		}
		childregRelease()
	}
	childregArgs, lrRoot, lrLazyRoots = nil, "", nil
	if left := b573Left(t, tmp); len(left) != 0 {
		t.Errorf("lazy roots left: %v", left)
	}
}

// Verbs Node runs as a whole Go binary and the internal accelerators other
// than the daemon keep plain spawns.
func TestB573InstallSkipsGoOnlyVerbs(t *testing.T) {
	for _, args := range [][]string{{"ci", "watch"}, {"heal"}, {"docs", "sync"}, {"decisions"}, {"cloud-coder"}, {"internal", "version"}, {}} {
		childregInstall(args)
		if childreg.Started != nil || childreg.Env != nil {
			t.Errorf("%v installed the registry", args)
		}
	}
	t.Setenv("CODE_FORGE_TMP_ROOT", t.TempDir())
	for _, args := range [][]string{{"internal", "serve", "--home", "h", "--run", "r1"}, {"worker", "--run", "r1"}, {"block", "land", "B1"}} {
		childregInstall(args)
		if childreg.Started == nil || childreg.Env == nil {
			t.Errorf("%v did not install the registry", args)
		}
		childreg.Started, childreg.Env, childregArgs = nil, nil, nil
	}
}
