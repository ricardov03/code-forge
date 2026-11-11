package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B430 tests: the remote is a local bare repo; no network, no real gh.

func (f *landBatchFixture) bareRemote() string {
	f.t.Helper()
	bare := f.t.TempDir()
	f.git(bare, "init", "-q", "--bare")
	f.git(f.repo, "remote", "add", "origin", bare)
	f.git(f.repo, "push", "-q", "origin", "main")
	return bare
}

func healItems(t *testing.T, home, run string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "heal", run+".jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		item := map[string]any{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("heal line %q: %v", line, err)
		}
		items = append(items, item)
	}
	return items
}

func TestLandPushBatchPutsMergeOnRemote(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	code, output := f.landWith([]string{"--push"}, "B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	head := f.git(f.repo, "rev-parse", "main")
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("remote main=%s, want local main %s", got, head)
	}
	if got := strings.Count(output, "pushed "+head+" to origin/main\n"); got != 1 {
		t.Errorf("push line count=%d, want 1: %s", got, output)
	}
	if items := healItems(t, f.home, "r-batch-unit"); len(items) != 0 {
		t.Errorf("heal items=%d, want 0 after a clean push: %v", len(items), items)
	}
}

func TestLandPushConfigKeyPushes(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	f.snapshotWithLandPush()
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if got, want := f.git(bare, "rev-parse", "refs/heads/main"), f.git(f.repo, "rev-parse", "main"); got != want {
		t.Errorf("remote main=%s, want %s (land.push: true without the flag)", got, want)
	}
}

// snapshotWithLandPush records the fixture's config plus land.push: true as
// the run-start config snapshot (hash-checked like a real run).
func (f *landBatchFixture) snapshotWithLandPush() {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		f.t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		f.t.Fatal(err)
	}
	cfg["land"] = map[string]any{"push": true}
	data, _ = json.Marshal(cfg)
	snapshot, err := ledger.DecodeObject(data)
	if err != nil {
		f.t.Fatal(err)
	}
	canonical, err := canon.Marshal(snapshot)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		f.t.Fatal(err)
	}
	record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}})
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
}

func TestLandPushRejectedQueuesOneHealItem(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	// The remote moves: a commit local main does not have.
	other := f.git(f.repo, "commit-tree", "-p", f.base, "-m", "remote moved", f.base+"^{tree}")
	f.git(f.repo, "push", "-q", "origin", other+":refs/heads/main")
	code, output := f.landWith([]string{"--push"}, "B1", "B2")
	if code == 0 {
		t.Fatalf("exit=0, want non-zero on a rejected push: %s", output)
	}
	f.checkMain([]string{"B1", "B2"})
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != other {
		t.Errorf("remote main=%s, want it unchanged at %s", got, other)
	}
	items := healItems(t, f.home, "r-batch-unit")
	if len(items) != 1 {
		t.Fatalf("heal items=%d, want 1: %v", len(items), items)
	}
	item := items[0]
	argv, _ := item["argv"].([]any)
	if item["kind"] != "land" || item["status"] != "open" || item["tries"] != float64(0) || item["block"] != "B1,B2" || item["run"] != "r-batch-unit" || len(argv) != 6 || argv[3] != "push" || argv[5] != "main" {
		t.Errorf("heal item=%v, want kind land, status open, tries 0, block B1,B2 and the push argv", item)
	}
	if id, _ := item["id"].(string); id == "" || strings.Count(output, "heal: queued "+id+"\n") != 1 {
		t.Errorf("output must name the queued item %v once: %s", item["id"], output)
	}
	if !strings.Contains(output, "land stopped at push:") {
		t.Errorf("output must say the land stopped at push: %s", output)
	}
}

func TestLandWatchCIStartsDetachedWatch(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	f.bareRemote()
	marker := filepath.Join(f.home, "watch-args")
	script := filepath.Join(f.home, "fake-self")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+marker+".tmp && mv "+marker+".tmp "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := landSelf
	landSelf = func() (string, error) { return script, nil }
	t.Cleanup(func() { landSelf = old })
	started := time.Now()
	code, output := f.landWith([]string{"--push", "--watch-ci"}, "B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	head := f.git(f.repo, "rev-parse", "main")
	var args []byte
	for time.Since(started) < 10*time.Second {
		if data, err := os.ReadFile(marker); err == nil {
			args = data
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	top := f.git(f.repo, "rev-parse", "--show-toplevel")
	want := "ci\nwatch\n--sha\n" + head + "\n--cwd\n" + top + "\n--run\nr-batch-unit\n"
	if string(args) != want {
		t.Errorf("watch argv=%q, want %q", args, want)
	}
	log := filepath.Join(f.home, ".code-forge", "heal", "ci-"+head+".log")
	if _, err := os.Stat(log); err != nil {
		t.Errorf("watch log %s: %v", log, err)
	}
	if strings.Count(output, "ci watch: started for "+head+"; log "+log+"\n") != 1 {
		t.Errorf("output must name the watch log once: %s", output)
	}
}

func TestLandWatchCIRefusals(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"B1", "--run", "r-batch-unit", "--report", "r", "--transcript", "t", "--push"}, "block land: --push and --watch-ci need --merge\n"},
		{[]string{"B1", "B2", "--run", "r-batch-unit", "--watch-ci"}, "block land: --watch-ci needs --push or land.push: true\n"},
		{[]string{"B1", "--run", "r-batch-unit", "--push=yes"}, "block land: --push takes no value\n"},
	} {
		capture, err := os.CreateTemp(f.home, "refusal-")
		if err != nil {
			t.Fatal(err)
		}
		before := os.Stderr
		os.Stderr = capture
		code := runBlockLand(tc.args)
		os.Stderr = before
		_ = capture.Close()
		data, _ := os.ReadFile(capture.Name())
		if code != 2 || string(data) != tc.want {
			t.Errorf("%v: exit=%d stderr=%q, want 2 and %q", tc.args, code, data, tc.want)
		}
	}
}
