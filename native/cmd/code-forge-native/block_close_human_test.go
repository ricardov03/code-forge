package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B684: the owner's close paths at a terminal run in Go: --no-require-reviews
// (the question, the grant refusal, the signed gate.reviews_waived row) and
// --decide (agent-waiver ack, scope warnings, a grant Go's reader refuses).
// Every case runs on the same temp-HOME fixture as Node did when
// testdata/b684/<case>.txt was recorded; the report holds the exit code,
// stdout, stderr, the question asked, the new ledger rows (ts and mac
// dropped), the block status and the files under the workspace's .code-forge.
//
// Record Node's goldens with B684_RECORD_NODE=<repo root>: the test then
// runs Node's runBlock in process with the terminal and the answer injected
// (the B80 test seam, the B680 driver; no real terminal) and writes the files.

type b684Fixture struct {
	t                           *testing.T
	home, repo, run, transcript string
	tmp                         string
}

type b684Setup struct {
	grant  string         // "", "active", "malformed"
	config map[string]any // the run's config snapshot; nil for none
	scope  bool           // tree_at_open is missing
	shared bool           // no worktree: B1 works in the main tree beside open B2
	review func(f *b684Fixture, hash string)
}

func newB684Fixture(t *testing.T, s b684Setup) *b684Fixture {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &b684Fixture{t: t, home: t.TempDir(), repo: repo, run: "r-b684", tmp: t.TempDir()}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	f.must(os.WriteFile(filepath.Join(repo, "sample.mjs"), []byte("old\n"), 0600))
	f.must(os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0600))
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	tree := filepath.Join(repo, ".code-forge/worktrees/B1")
	if s.shared {
		// B1 and B2 both change the main tree; B2 is still open
		tree = repo
		f.must(os.WriteFile(filepath.Join(repo, "sample.mjs"), []byte("new\n"), 0600))
		f.must(os.WriteFile(filepath.Join(repo, "other.mjs"), []byte("b2\n"), 0600))
	} else {
		git(repo, "worktree", "add", "-qb", "block-B1", tree, base)
		f.must(os.WriteFile(filepath.Join(tree, "sample.mjs"), []byte("new\n"), 0600))
		git(tree, "add", ".")
		git(tree, "commit", "-qm", "change")
	}
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}}
	if !s.scope {
		entry = append(entry, ledger.Field{Key: "tree_at_open", Value: ledger.Object{}})
	}
	entry = append(entry, ledger.Object{{Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}}...)
	if !s.shared {
		entry = append(entry, ledger.Field{Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: base}}})
	}
	blocks := ledger.Object{{Key: "B1", Value: entry}}
	if s.shared {
		blocks = append(blocks, ledger.Field{Key: "B2", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"other.mjs"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}, {Key: "tree_at_open", Value: ledger.Object{}}, {Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}}})
	}
	record := ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "worker", Value: ledger.Object{{Key: "pid", Value: float64(os.Getpid())}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}}}, {Key: "blocks", Value: blocks}, {Key: "orphans", Value: []any{}}}
	if s.config != nil {
		canonical, err := canon.Marshal(s.config)
		f.must(err)
		sum := sha256.Sum256(canonical)
		hash := hex.EncodeToString(sum[:])
		record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: s.config}}}})
	}
	stamp := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
	now := time.Now()
	if s.grant != "" {
		grant := ledger.Object{{Key: "grant_id", Value: "ag-unit"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"waive:warning"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: stamp(now.Add(time.Hour))}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.9}, {Key: "started_at", Value: stamp(now.Add(-time.Minute))}}
		if s.grant == "malformed" {
			// a field Go's grant reader refuses; Node's grantState still says active
			grant = append(grant, ledger.Field{Key: "extra", Value: "x"})
		}
		record.Set("autopilot", grant)
	}
	f.must(state.SaveRun(f.home, record))
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge/runs", f.run+".key"), make([]byte, 32), 0600))
	f.row(ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	hash, err := gitx.ContentHash(tree, "sample.mjs")
	f.must(err)
	if s.review != nil {
		s.review(f, hash)
	}
	f.transcript = filepath.Join(f.home, "transcript")
	f.must(os.WriteFile(f.transcript, []byte("Completed implementation.\n"), 0600))
	return f
}

func (f *b684Fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *b684Fixture) row(row ledger.Object) {
	f.t.Helper()
	_, err := state.WriteSigned(f.home, f.run, row)
	f.must(err)
}

func b684Approved(f *b684Fixture, hash string) {
	f.row(ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}})
}

// b684Finding: an approval, then a later review of the same content that
// leaves F1 open at the severity.
func b684Finding(severity string) func(f *b684Fixture, hash string) {
	return func(f *b684Fixture, hash string) {
		b684Approved(f, hash)
		open := []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: severity}, {Key: "claim", Value: "the name is unclear"}}}
		f.row(ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}, {Key: "status", Value: "done"}, {Key: "approved", Value: false}, {Key: "open", Value: open}})
	}
}

// b684AgentWaived: the warning F1 is waived by the agent with an agreeing
// judge, so require_ack asks the owner to acknowledge it.
func b684AgentWaived(f *b684Fixture, hash string) {
	b684Finding("warning")(f, hash)
	f.row(ledger.Object{{Key: "severity", Value: "warning"}, {Key: "verdict", Value: "agree"}, {Key: "judge_reason", Value: "fine"}, {Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "finding", Value: "F1"}, {Key: "reason", Value: "naming only"}, {Key: "by", Value: "agent"}})
}

func b684Config(extra map[string]any) map[string]any {
	cfg := landTestConfig(nil)
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

var b684Cases = []struct {
	name, ask, args string
	setup           b684Setup
}{
	{"nrr-yes", "yes", "|--no-require-reviews", b684Setup{review: b684Finding("major")}},
	{"nrr-json", "yes", "|--no-require-reviews|--json", b684Setup{review: b684Finding("major")}},
	{"nrr-approved", "yes", "|--no-require-reviews", b684Setup{review: b684Approved}},
	{"nrr-cancel", "no", "|--no-require-reviews", b684Setup{review: b684Finding("major")}},
	{"nrr-notty", "notty", "|--no-require-reviews", b684Setup{review: b684Finding("major")}},
	{"nrr-grant", "yes", "|--no-require-reviews", b684Setup{grant: "active", review: b684Finding("major")}},
	{"nrr-require-human", "yes", "|--no-require-reviews", b684Setup{config: b684Config(map[string]any{"close": map[string]any{"require_human": true}}), review: b684Finding("major")}},
	{"nrr-test-gate", "yes", "|--no-require-reviews", b684Setup{config: landTestConfig("true"), review: b684Finding("major")}},
	{"decide-ack", "yes", "|--decide|--reason|the owner read the waiver", b684Setup{config: b684Config(map[string]any{"review": map[string]any{"agent_waivers": map[string]any{"require_ack": true}}}), review: b684AgentWaived}},
	{"decide-ack-no", "no", "|--decide|--reason|the owner read the waiver", b684Setup{config: b684Config(map[string]any{"review": map[string]any{"agent_waivers": map[string]any{"require_ack": true}}}), review: b684AgentWaived}},
	{"decide-no-tree-at-open", "yes", "|--decide|--reason|naming only", b684Setup{scope: true, review: b684Finding("warning")}},
	{"decide-scope-warn", "yes", "|--decide|--reason|naming only", b684Setup{shared: true, review: b684Finding("warning")}},
	{"decide-scope-warn-no", "no", "|--decide|--reason|naming only", b684Setup{shared: true, review: b684Finding("warning")}},
	{"decide-scope-unchecked", "yes", "|--decide|--reason|naming only", b684Setup{shared: true, scope: true, review: b684Finding("warning")}},
	{"decide-grant-malformed", "yes", "|--decide|--reason|naming only", b684Setup{grant: "malformed", review: b684Finding("warning")}},
	{"decide-grant-malformed-notty", "notty", "|--decide|--reason|naming only", b684Setup{grant: "malformed", review: b684Finding("warning")}},
}

func (f *b684Fixture) args(extra string) string {
	return "block|close|B1|--run|" + f.run + "|--transcript|" + f.transcript + extra
}

// b684Child is the Go side: the B453 child with the block verbs' terminal
// seam (humanTTY/humanAsk) set the same way.
func b684Child() {
	ask := os.Getenv("BLOCK_HV_ASK")
	humanTTY = func() bool { return ask == "yes" || ask == "no" }
	humanAsk = func(message string) (humanask.Result, error) {
		switch ask {
		case "yes", "no":
			_ = os.WriteFile(os.Getenv("BLOCK_HV_ASKED"), []byte(message), 0600)
			if ask == "yes" {
				return humanask.Confirmed, nil
			}
			return humanask.Cancelled, nil
		}
		return humanask.NoTTY, nil
	}
	blockHVChild()
}

func (f *b684Fixture) goRun(test, ask, args string) hvResult {
	f.t.Helper()
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK="+ask, "BLOCK_HV_ASKED="+asked, "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NATIVE=1",
		"TMPDIR="+f.tmp, "CODE_FORGE_TMP_ROOT=")
	return b680Exec(f.t, cmd, asked)
}

func (f *b684Fixture) nodeRun(root, ask, args string) hvResult {
	f.t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal(err)
	}
	driver := filepath.Join(f.t.TempDir(), "b684-driver.mjs")
	f.must(os.WriteFile(driver, []byte(b680NodeDriver), 0o600))
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(nodeBin, append([]string{driver, root, ask, asked}, strings.Split(args, "|")[1:]...)...)
	cmd.Dir = f.repo
	cmd.Env = []string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "PATH=" + filepath.Dir(nodeBin) + ":/usr/bin:/bin", "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT="}
	return b680Exec(f.t, cmd, asked)
}

func (f *b684Fixture) ledgerLines() []string {
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "fixture.jsonl"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

var (
	b684Sha = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
	// the ack digest hashes the waiver rows' macs, which sign their ts
	b684Digest = regexp.MustCompile(`"digest":"[0-9a-f]{64}"`)
)

func (f *b684Fixture) report(r hvResult, before int) string {
	var b strings.Builder
	b.WriteString("asked=" + r.asked + "\nexit=" + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr + "--- new rows\n")
	for _, line := range f.ledgerLines()[before:] {
		b.WriteString(b612Drop.ReplaceAllString(line, "") + "\n")
	}
	record, err := state.ReadRun(f.home, f.run)
	f.must(err)
	b.WriteString("--- status " + blockString(blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status")) + "\n")
	root := filepath.Join(f.repo, ".code-forge")
	var names []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "worktrees" {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	sort.Strings(names)
	for _, name := range names {
		rel, _ := filepath.Rel(root, name)
		data, _ := os.ReadFile(name)
		b.WriteString("--- ws/" + b612ID.ReplaceAllString(rel, "<ID>") + "\n" + string(data) + "\n")
	}
	text := b.String()
	for _, p := range []struct{ from, to string }{{f.repo, "<WS>"}, {f.tmp, "<TMP>"}, {f.home, "<HOME>"}} {
		text = strings.ReplaceAll(text, p.from, p.to)
	}
	text = b612Pid.ReplaceAllString(text, `"pid":<PID>,"start_time":"<START>"`)
	text = b684Sha.ReplaceAllString(text, "<SHA>")
	text = b684Digest.ReplaceAllString(text, `"digest":"<DIGEST>"`)
	return b612ID.ReplaceAllString(b612TS.ReplaceAllString(text, "<TS>"), "<ID>")
}

func TestB684HumanCloseMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	record := os.Getenv("B684_RECORD_NODE")
	for _, c := range b684Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, c.setup)
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b684", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB684HumanCloseMatchesNode", c.ask, args)
			}
			got := f.report(r, before)
			if record != "" {
				f.must(os.MkdirAll(filepath.Dir(golden), 0o755))
				f.must(os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			f.must(err)
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// The signed rows verify with the run key and the ack names the counted
// waivers' digest: the goldens drop ts and mac and mask the digest.
func TestB684HumanCloseSignsRows(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	signed := func(t *testing.T, f *b684Fixture, event string) map[string]any {
		t.Helper()
		all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
		f.must(err)
		key, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", f.run+".key"))
		f.must(err)
		for _, v := range all {
			if row := v.(map[string]any); row["event"] == event {
				if !gatecheck.Verified(row, key) {
					t.Fatalf("%s row does not verify: %v", event, row)
				}
				return row
			}
		}
		t.Fatalf("no %s row: %v", event, all)
		return nil
	}
	t.Run("reviews waived", func(t *testing.T) {
		f := newB684Fixture(t, b684Setup{review: b684Finding("major")})
		r := f.goRun("TestB684HumanCloseSignsRows", "yes", f.args("|--no-require-reviews"))
		if r.code != 0 {
			t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
		}
		if row := signed(t, f, "gate.reviews_waived"); row["by"] != "human" || row["block"] != "B1" {
			t.Fatalf("row %v", row)
		}
	})
	t.Run("agent waivers acked", func(t *testing.T) {
		f := newB684Fixture(t, b684Setup{config: b684Config(map[string]any{"review": map[string]any{"agent_waivers": map[string]any{"require_ack": true}}}), review: b684AgentWaived})
		r := f.goRun("TestB684HumanCloseSignsRows", "yes", f.args("|--decide|--reason|the owner read the waiver"))
		if r.code != 0 {
			t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
		}
		row := signed(t, f, "agent_waivers.acked")
		all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
		f.must(err)
		rows := []gatecheck.Row{}
		for _, v := range all {
			rows = append(rows, v.(map[string]any))
		}
		key, _ := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", f.run+".key"))
		acked, known := gatecheck.AgentWaiversAcked(true, f.run, "B1", rows, key)
		if !acked || !known || row["by"] != "human" || row["count"] != float64(1) {
			t.Fatalf("ack row %v does not ack the counted waivers (acked %v known %v)", row, acked, known)
		}
	})
}
