package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
)

func b1010Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = envWithoutGit()
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func b1010StartFixture(t *testing.T) (startFixture, string) {
	t.Helper()
	f := newStartFixture(t, "project:\n  slug: unit\nengine: auto\nland:\n  min_free_gb: 0\ntmp:\n  root: TMPROOT\n")
	b1010Git(t, f.repo, "symbolic-ref", "HEAD", "refs/heads/lane-test")
	b1010Git(t, f.repo, "add", ".")
	b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := b1010Git(t, f.repo, "rev-parse", "HEAD")
	return f, base
}

func b1010Start(t *testing.T, f startFixture, base string) (int, string, string) {
	t.Helper()
	// Pin this process: no child worker, network or real HOME.
	return runCaptured(t, "lane", "start", "--cwd", f.repo, "--base", base, "--run", "r-lane", "--worker-pid", strconv.Itoa(os.Getpid()))
}

func b1010EditRecord(t *testing.T, home, id string, edit func(map[string]any)) {
	t.Helper()
	file := filepath.Join(home, ".code-forge", "runs", id+".json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	edit(record)
	data, _ = json.Marshal(record)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestB1010LaneStartPinsBase(t *testing.T) {
	f, base := b1010StartFixture(t)
	// Main's tip is a valid supplied base when no combine is open. The
	// checkout stays on lane-test; only checkout of main itself is refused.
	b1010Git(t, f.repo, "branch", "main", base)
	code, out, errs := b1010Start(t, f, "main")
	if code != 0 || !strings.Contains(out, "lane "+base[:12]) {
		t.Fatalf("start %d: %s %s", code, out, errs)
	}
	record := f.read(t, "r-lane")
	lane, ok := record["lane"].(map[string]any)
	if !ok || lane["base"] != base || lane["branch"] != "lane-test" || record["mode"] != "brief" {
		t.Fatalf("record: %#v", record)
	}
	rows := f.startRows(t, "r-lane")
	found := false
	for _, row := range rows {
		if row["event"] == "lane.start" && row["base"] == base {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing signed lane.start: %#v", rows)
	}
	code, out, errs = runCaptured(t, "run", "status", "--run", "r-lane")
	var status map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &status) != nil || status["lane"] == nil {
		t.Fatalf("lane status: %d %s %s", code, out, errs)
	}
	if len(f.launched(t)) != 0 {
		t.Fatal("launched a worker despite pin")
	}
}

func TestB1010LaneStartRefusesUnsafeBase(t *testing.T) {
	for _, scenario := range []string{"dirty", "moved", "main", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			f, base := b1010StartFixture(t)
			want := "clean worktree"
			switch scenario {
			case "dirty":
				if err := os.WriteFile(filepath.Join(f.repo, "untracked"), []byte("dirty"), 0600); err != nil {
					t.Fatal(err)
				}
			case "moved":
				b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "moved")
				want = "HEAD must equal"
			case "main":
				b1010Git(t, f.repo, "branch", "-m", "main")
				want = "lane branch"
			case "missing":
				base = "-bad"
				want = "git ref"
			}
			code, out, errs := b1010Start(t, f, base)
			if code != 1 || !strings.Contains(out+errs, want) {
				t.Fatalf("%s: exit %d: %s %s", scenario, code, out, errs)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-lane.json")); !os.IsNotExist(err) {
				t.Fatalf("refusal created run: %v", err)
			}
		})
	}
}

func b1010OpenFixture(t *testing.T) (startFixture, string) {
	t.Helper()
	f, base := b1010StartFixture(t)
	// Use the existing run command so the regression tests reach the old block
	// implementation as well; lane.start itself is tested separately.
	code, out, errs := runVerbCall(t, "start", "--cwd", f.repo, "--mode", "brief", "--run", "r-lane", "--worker-pid", strconv.Itoa(os.Getpid()))
	if code != 0 {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) { r["lane"] = map[string]any{"base": base, "branch": "lane-test"} })
	if err := os.Chdir(f.repo); err != nil {
		t.Fatal(err)
	}
	return f, base
}

func b1010Open(t *testing.T, f startFixture, id string, extra ...string) (int, string) {
	t.Helper()
	acc := filepath.Join(f.home, "B1010-acceptance.yml")
	if err := os.WriteFile(acc, []byte(`[{"clause":"works","tests":["TestLane"]}]`), 0600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"block", "open", id, "--run", "r-lane", "--owned", id + ".go", "--acceptance", acc}, extra...)
	code, out, errs := runCaptured(t, args...)
	return code, out + errs
}

func TestB1010LaneBlockLimit(t *testing.T) {
	f, _ := b1010OpenFixture(t)
	for _, id := range []string{"B1", "B2"} {
		if code, out := b1010Open(t, f, id); code != 0 {
			t.Fatalf("open %s: %d %s", id, code, out)
		}
	}
	b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) {
		blocks := r["blocks"].(map[string]any)
		blocks["B1"].(map[string]any)["status"] = "closed"
		blocks["B2"].(map[string]any)["status"] = "stopped"
	})
	code, out := b1010Open(t, f, "B3")
	if code != 1 || !strings.Contains(out, "at most two blocks") {
		t.Fatalf("third block: %d %s", code, out)
	}
	if len(f.read(t, "r-lane")["blocks"].(map[string]any)) != 2 {
		t.Fatal("third block recorded")
	}
}

func TestB1010LaneBlockBase(t *testing.T) {
	f, base := b1010OpenFixture(t)
	cfg, err := config.Load(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	landCISnapshot(t, f.home, "r-lane", cfg, map[string]any{"require_ci": "github", "min_free_gb": 0})
	gh := landCIFakeGh(t, "[]", nil)
	// An unrelated root commit exists in the object store but is not an
	// ancestor of this lane's HEAD. No branch or checkout needs to move.
	tree := b1010Git(t, f.repo, "rev-parse", "HEAD^{tree}")
	unrelated := b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-m", "unrelated root")
	b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) { r["lane"].(map[string]any)["base"] = unrelated })
	code, out := b1010Open(t, f, "B1")
	if code != 1 || !strings.Contains(out, "must descend from the pinned combine tip") {
		t.Fatalf("unrelated base: %d %s", code, out)
	}
	if f.read(t, "r-lane")["blocks"].(map[string]any)["B1"] != nil {
		t.Fatal("unrelated base recorded a block")
	}
	b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) { r["lane"].(map[string]any)["base"] = base })
	b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "B0: prior lane block")
	head := b1010Git(t, f.repo, "rev-parse", "HEAD")
	code, out = b1010Open(t, f, "B1", "--base", base)
	if code != 1 || !strings.Contains(out, "current lane HEAD") {
		t.Fatalf("stale base: %d %s", code, out)
	}
	code, out = b1010Open(t, f, "B1")
	if code != 0 {
		t.Fatalf("open at HEAD: %d %s", code, out)
	}
	b := f.read(t, "r-lane")["blocks"].(map[string]any)["B1"].(map[string]any)
	if b["base_sha"] != head {
		t.Fatalf("block base %#v, want %s", b["base_sha"], head)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Fatalf("lane open tried CI: %v", calls)
	}
}

func TestB1010LaneGatesStayNamed(t *testing.T) {
	for _, flag := range []string{"--full", "--packages", "unsupported", ""} {
		t.Run(flag, func(t *testing.T) {
			f := newB667GateFixtureOn(t, b1009Base, nil, map[string]*string{"a/a.go": b667Body("package a\nvar X = 1\n")})
			b1010EditRecord(t, f.home, "fixture", func(r map[string]any) { r["lane"] = map[string]any{"base": "unused", "branch": "lane-test"} })
			flags := []string{}
			if flag == "unsupported" {
				body, _ := json.Marshal(map[string]any{"gates": map[string]any{"test": []string{"sh", "-c", "echo broad >> \"$B667_GO_LOG\""}, "lint": nil, "types": nil, "format": nil}})
				b667WriteFile(t, filepath.Join(f.repo, ".code-forge.yml"), string(body))
			} else if flag != "" {
				flags = append(flags, flag)
			}
			code, out := b1010Gate(t, f, flags...)
			if flag != "" {
				if code != 1 || !strings.Contains(out, "lane gates require changed tests by name") {
					t.Fatalf("broad gate: %d %s", code, out)
				}
				if data, _ := os.ReadFile(f.log); len(data) != 0 {
					t.Fatalf("gate ran: %s", data)
				}
			} else {
				if code != 0 {
					t.Fatalf("named gate: %d %s", code, out)
				}
				calls := strings.Join(b667Calls(t, f.log), "\n")
				if !strings.Contains(calls, "-run ^(TestB1Existing)$") || strings.Contains(calls, "./...") {
					t.Fatalf("calls: %s", calls)
				}
			}
		})
	}
}

func b1010Gate(t *testing.T, f b667GateFixture, flags ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"gates", "run", "--run", "fixture", "--block", "B1", "--json"}, flags...)
	if slices.Contains(flags, "--packages") {
		args = append(args, "--changed")
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "CODE_FORGE_RUN_ID=") }), "HOME="+f.home, "B667_GO_LOG="+f.log, "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NO_ERROR_LOG=1", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode(), string(out)
		}
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestB1010LaneCannotLand(t *testing.T) {
	f, _ := b1010OpenFixture(t)
	code, out, errs := runCaptured(t, "block", "land", "B1", "--run", "r-lane", "--report", "absent", "--transcript", "absent", "--push", "--watch-ci")
	if code != 1 || !strings.Contains(out+errs, "lane blocks are closed locally and combined") {
		t.Fatalf("land: %d %s %s", code, out, errs)
	}
	if b1010Git(t, f.repo, "branch", "--show-current") != "lane-test" {
		t.Fatal("land switched branches")
	}
}
