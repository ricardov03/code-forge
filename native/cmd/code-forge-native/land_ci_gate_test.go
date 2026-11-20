package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B765 tests: land.require_ci: github. gh is a fake script on PATH that
// answers from canned files; PATH holds only it, /usr/bin and /bin, so the
// real gh (and GitHub) is never reached. HOME is a temp dir (the fixtures).

// landCIFakeGh puts a fake gh first on PATH. It logs each call's argv to
// $dir/calls, answers `run list` with $dir/runs.json and `run view <id>
// --log-failed` with $dir/log-<id>.txt.
func landCIFakeGh(t *testing.T, runs string, logs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + closeCommand([]string{filepath.Join(dir, "calls")}) + "\n" +
		"case \"$1 $2\" in\n" +
		"  'run list') cat " + closeCommand([]string{filepath.Join(dir, "runs.json")}) + " ;;\n" +
		"  'run view') cat " + closeCommand([]string{dir}) + "/log-\"$3\".txt ;;\n" +
		"  *) echo \"fake gh: unexpected $*\" >&2; exit 9 ;;\n" +
		"esac\n"
	landWrite(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(dir, "runs.json"), runs)
	for id, text := range logs {
		landWrite(t, filepath.Join(dir, "log-"+id+".txt"), text)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return dir
}

func landCICalls(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// landCISnapshot records cfg plus the given land section as the run-start
// config snapshot (hash-checked like a real run).
func landCISnapshot(t *testing.T, home, run string, cfg map[string]any, land map[string]any) {
	t.Helper()
	copied := map[string]any{}
	for k, v := range cfg {
		copied[k] = v
	}
	copied["land"] = land
	data, _ := json.Marshal(copied)
	snapshot, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canon.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	record, err := state.ReadRun(home, run)
	if err != nil {
		t.Fatal(err)
	}
	record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}})
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
}

// landCIBatch is the batch fixture with the given land config, an optional
// known-red list (land.ci_red_list) and an optional CODE_FORGE_CI_RED_BLOCKS.
func landCIBatch(t *testing.T, land map[string]any, redList, redBlocks string) *landBatchFixture {
	t.Helper()
	f := newLandBatchFixture(t, 0)
	f.rounds = true
	if redList != "" {
		// An absolute list path: a file in the repo would add a commit on main.
		dir := t.TempDir()
		f.write(dir, "ci-red.map", redList)
		copied := map[string]any{}
		for k, v := range land {
			copied[k] = v
		}
		copied["ci_red_list"] = filepath.Join(dir, "ci-red.map")
		land = copied
	}
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if land != nil {
		landCISnapshot(t, f.home, "r-batch-unit", cfg, land)
	}
	t.Setenv(landCIRedBlocksEnv, redBlocks)
	return f
}

// landCIState is every file under ~/.code-forge except lock files, so a
// refusal can be shown to write nothing.
func landCIState(t *testing.T, home string) map[string]string {
	t.Helper()
	files := map[string]string{}
	root := filepath.Join(home, ".code-forge")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(d.Name(), "lock") {
			return nil
		}
		data, _ := os.ReadFile(path)
		files[path] = string(data)
		return nil
	})
	return files
}

const (
	landCIGreen     = `[{"databaseId":7,"status":"completed","conclusion":"success","name":"CI"}]`
	landCIRed       = `[{"databaseId":7,"status":"completed","conclusion":"failure","name":"CI"}]`
	landCIRedLog    = "test (ubuntu)\tRun go test\t2026-11-15T10:00:00.1234567Z --- FAIL: TestOld (0.20s)\ntest (ubuntu)\tRun go test\t2026-11-15T10:00:00.1234567Z     --- FAIL: TestOld/sub (0.10s)\n"
	landCINewLog    = landCIRedLog + "test (ubuntu)\tRun go test\t2026-11-15T10:00:01.0000000Z --- FAIL: TestNew (0.30s)\n"
	landCIKnownReds = "# known reds\nB9 ^TestOld$\n"
)

// B796: ci_wait off keeps these B765 checks to one CI read (no waiting).
var landCIOn = map[string]any{"require_ci": "github", "ci_wait": "off"}

// landCIRefused checks a refusal at step ci that merged and wrote nothing.
func landCIRefused(t *testing.T, f *landBatchFixture, want string) string {
	t.Helper()
	main := f.git(f.repo, "rev-parse", "main")
	before := landCIState(t, f.home)
	code, output := f.land("B1", "B2")
	if code != 1 {
		t.Fatalf("exit=%d, want 1: %s", code, output)
	}
	if !strings.Contains(output, "block B1 land stopped at ci: ") || !strings.Contains(output, want) {
		t.Errorf("output=%q, want a refusal at ci with %q", output, want)
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != main {
		t.Errorf("main moved to %s, want %s (nothing merged)", got, main)
	}
	after := landCIState(t, f.home)
	for path, data := range after {
		if before[path] != data {
			t.Errorf("refusal wrote %s", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("refusal changed the state file set: %d -> %d files", len(before), len(after))
	}
	return output
}

func TestB765LandCIGreenLands(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	gh := landCIFakeGh(t, landCIGreen, nil)
	heads := f.heads()
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	// B796: after the land, `pr list` looks for CI PRs to close; count run lists.
	calls := []string{}
	for _, call := range landCICalls(t, gh) {
		if strings.HasPrefix(call, "run list ") {
			calls = append(calls, call)
		}
	}
	// B910: B2 is rebased onto B1's merge (a new sha), so its rebased tip
	// gets a CI read of its own.
	rebased := f.git(f.repo, "rev-parse", "block-B2")
	if len(calls) != 3 {
		t.Fatalf("gh run list calls=%q, want one per block tip plus B2's rebased tip", calls)
	}
	for i, sha := range []string{heads["B1"], heads["B2"], rebased} {
		want := "run list --commit " + sha + " --limit 100 --json databaseId,status,conclusion,name"
		if calls[i] != want {
			t.Errorf("gh call %d=%q, want %q", i, calls[i], want)
		}
	}
	for _, id := range []string{"B1", "B2"} {
		if !strings.Contains(output, "ci: tip "+heads[id][:8]+" green (1 runs)\n") {
			t.Errorf("output must say %s's tip is green: %s", id, output)
		}
	}
}

func TestB765LandCIRedUntrackedRefused(t *testing.T) {
	f := landCIBatch(t, landCIOn, landCIKnownReds, "B9")
	landCIFakeGh(t, landCIRed, map[string]string{"7": landCINewLog})
	output := landCIRefused(t, f, "CI is red (CI: failure, run 7)")
	for _, want := range []string{"  TestNew -> NO BLOCK\n", "  TestOld -> B9\n", "a failing test has no block in CODE_FORGE_CI_RED_BLOCKS=B9"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q: %s", want, output)
		}
	}
	if strings.Contains(output, "TestOld/sub") {
		t.Errorf("an indented subtest is not a top-level failing test: %s", output)
	}
}

func TestB765LandCIRedTrackedLands(t *testing.T) {
	f := landCIBatch(t, landCIOn, landCIKnownReds, "B9")
	landCIFakeGh(t, landCIRed, map[string]string{"7": landCIRedLog})
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (red only on a known red): %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	// B910: B1's tip, B2's tip and B2's rebased tip.
	if strings.Count(output, "  TestOld -> B9\n") != 3 || strings.Count(output, "red only on known reds (B9), landing allowed\n") != 3 {
		t.Errorf("output must show each block's known red: %s", output)
	}
}

func TestB765LandCISelfCoverRefused(t *testing.T) {
	f := landCIBatch(t, landCIOn, "B1 ^TestOld$\n", "B1")
	landCIFakeGh(t, landCIRed, map[string]string{"7": landCIRedLog})
	landCIRefused(t, f, "a block never covers its own red (B1 dropped from CODE_FORGE_CI_RED_BLOCKS)")
}

func TestB765LandCINoRunRefused(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	landCIFakeGh(t, `[]`, nil)
	// B796: a tip without a CI run is pushed to a CI branch; this fake gh
	// cannot list pull requests, so the land refuses before any push.
	landCIRefused(t, f, "cannot list the CI pull requests of block B1: gh pr list: fake gh: unexpected pr list")
}

func TestB765LandCIRunningRefused(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	landCIFakeGh(t, `[{"databaseId":7,"status":"in_progress","conclusion":"","name":"CI"}]`, nil)
	landCIRefused(t, f, "has no green CI run yet (running: CI)")
}

func TestB765LandCIGhMissingRefused(t *testing.T) {
	f := landCIBatch(t, landCIOn, "", "")
	t.Setenv("PATH", landCIToolsPath(t, landCITools...))
	if _, err := exec.LookPath("gh"); err == nil {
		t.Fatal("gh is on the tools-only PATH; this test needs a PATH without gh")
	}
	landCIRefused(t, f, "gh is not on PATH; install gh and run gh auth login")
}

// landCITools is the measured set of programs block land runs before the CI
// gate: with an empty tools PATH the land fails on git rev-parse; with git
// alone it reaches the gate.
var landCITools = []string{"git"}

// landCIToolsPath builds a PATH of one temp dir holding only symlinks to the
// named tools (found on the current PATH), so gh is never found on any host,
// even where gh ships in /usr/bin.
func landCIToolsPath(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s not found on PATH: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Default off: without land.require_ci the land never asks gh.
func TestB765LandCIOffNeverCallsGh(t *testing.T) {
	f := landCIBatch(t, nil, "", "")
	gh := landCIFakeGh(t, `[]`, nil)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if calls := landCICalls(t, gh); len(calls) != 0 {
		t.Errorf("gh calls=%q, want none with the gate off", calls)
	}
}

// The single-block path (a closed-block retry with --merge) runs the same gate.
func TestB765LandCISingleBlock(t *testing.T) {
	for _, tc := range []struct {
		name, runs string
		code       int
	}{{"red", landCIRed, 1}, {"green", landCIGreen, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := androidLand(t)
			const run = "r-ci-single"
			entry := ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
			record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
			if err := state.SaveRun(f.home, record); err != nil {
				t.Fatal(err)
			}
			landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
			tip := landGit(t, f.tree, "rev-parse", "HEAD")
			landDone(t, f.home, run, tip)
			landCISnapshot(t, f.home, run, f.cfg, landCIOn)
			t.Setenv(landCIRedBlocksEnv, "")
			gh := landCIFakeGh(t, tc.runs, map[string]string{"7": landCIRedLog})
			saved, err := state.ReadRun(f.home, run)
			if err != nil {
				t.Fatal(err)
			}
			main := landGit(t, f.repo, "rev-parse", "main")
			capture, err := os.CreateTemp(t.TempDir(), "output-")
			if err != nil {
				t.Fatal(err)
			}
			out, stderr := os.Stdout, os.Stderr
			os.Stdout, os.Stderr = capture, capture
			flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "no-merge-gate": true}
			code := landExecuteWithReason(nil, flags, f.home, run, "B1", saved, blockObject(blockObject(saved.Get("blocks")).Get("B1")), func(reason handoverReason) int {
				t.Errorf("handed over to Node: %v", reason)
				return 127
			})
			os.Stdout, os.Stderr = out, stderr
			capture.Close()
			data, _ := os.ReadFile(capture.Name())
			if code != tc.code {
				t.Fatalf("exit=%d, want %d: %s", code, tc.code, data)
			}
			if calls := landCICalls(t, gh); len(calls) == 0 || !strings.HasPrefix(calls[0], "run list --commit "+tip+" ") {
				t.Errorf("gh calls=%q, want run list for the block tip %s", calls, tip)
			}
			moved := landGit(t, f.repo, "rev-parse", "main") != main
			if tc.code == 1 {
				if moved || !strings.Contains(string(data), "block B1 land stopped at ci: tip "+tip[:8]+" CI is red") || !strings.Contains(string(data), "next: gh run view 7 --log-failed") {
					t.Errorf("red: moved=%v output=%q, want a ci refusal with nothing merged", moved, data)
				}
			} else if !moved || !strings.Contains(string(data), "block B1 landed") {
				t.Errorf("green: moved=%v output=%q, want B1 landed", moved, data)
			}
		})
	}
}
