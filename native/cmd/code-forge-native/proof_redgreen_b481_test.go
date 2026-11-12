package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B481: `proof red-green` runs in Go. Before B481 every red-green call that
// passed the usage checks handed over to Node (proof-run), so runCaptured
// fails each of these cases on the old code.

// b481FakeNode is a stand-in for `node --test <file>`: the test file is never
// run. The mode in B481_FAKE says what the "test" checks; a test file holding
// the assertion-deletion marker always fails with an assertion error.
const b481FakeNode = `#!/bin/sh
for a; do f=$a; done
printf '%s\n' "$*" >> "$B481_ARGS"
if grep -q 'code-forge: assertion deleted' "$f"; then
  echo "AssertionError [ERR_ASSERTION]: code-forge: assertion deleted"; echo "# fail 1"; exit 1
fi
case "$B481_FAKE" in
  src) if grep -q fixed src/x.js; then echo "# pass 1"; exit 0; fi
       echo "AssertionError [ERR_ASSERTION]: expected fixed"; echo "# fail 1"; exit 1 ;;
  pass) echo "# pass 1"; exit 0 ;;
  kill) kill -9 $$ ;;
  syntax) echo "SyntaxError: Unexpected token"; exit 1 ;;
esac
exit 0
`

// b481Repo commits a base with a source and its test, then fixes the source
// in the tree (uncommitted): the block's change is src/x.js.
func b481Repo(config string) func(t *testing.T, repo string) {
	return func(t *testing.T, repo string) {
		w := func(rel, body string) { b462Write(t, filepath.Join(repo, rel), []byte(body), 0o644) }
		w(".code-forge.yml", config)
		w(".gitignore", ".code-forge/\n")
		w("src/x.js", "export const x = 'broken';\n")
		w("t/x.test.mjs", "import assert from 'node:assert/strict';\nimport test from 'node:test';\nimport { x } from '../src/x.js';\ntest('a.b(c)', () => {\n  assert.equal(x, 'fixed');\n});\n")
		w("t/x.py", "pass\n")
		b462Git(t, repo, "add", "-A")
		b462Git(t, repo, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "red-green base")
		w("src/x.js", "export const x = 'fixed';\n")
	}
}

func b481Block() ledger.Object {
	return ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "main"},
		{Key: "owned_files", Value: []any{"src/**", "t/**"}}, {Key: "owned_kinds", Value: ledger.Object{}}}
}

// b481Snapshot is a run config field whose snapshot holds proof.runners.
func b481Snapshot(t *testing.T, body string) ledger.Object {
	t.Helper()
	snapshot, err := ledger.DecodeObject([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canon.Marshal(snapshot)
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	return ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}}
}

// b481Call is b462GoCall with the fake node first on PATH and its argument log.
func b481Call(t *testing.T, home, repo, mode string, args []string) (int, string, string, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "node")
	b462Write(t, bin, []byte(b481FakeNode), 0o755)
	argLog := filepath.Join(t.TempDir(), "args")
	t.Setenv("B481_FAKE", mode)
	t.Setenv("B481_ARGS", argLog)
	t.Setenv("HOME", home)
	// a hand-over must fail the test (never exec): the fake node is found on
	// PATH by the red-green runner, while CODE_FORGE_NODE does not resolve
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b481-node")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	saved := proofNow
	proofNow = func() time.Time { return time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC) }
	defer func() { proofNow = saved }()
	here, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	code, stdout, stderr := runCaptured(t, args...)
	logged, _ := os.ReadFile(argLog)
	return code, strings.ReplaceAll(stdout, repo, "<REPO>"), strings.ReplaceAll(stderr, repo, "<REPO>"), string(logged)
}

type b481Case struct {
	name, mode, config string
	args               []string
	block              ledger.Object
	extra              ledger.Object
	prepare            func(t *testing.T, repo string)
	code               int
	stdout, stderr     string
	argv               string // the fake node's arguments after argv[0]
	row                string // the red-green row, "" for none
}

var b481Args = []string{"proof", "red-green", "B1", "--run", b462Run, "--test", "t/x.test.mjs"}

func b481With(extra ...string) []string { return append(append([]string{}, b481Args...), extra...) }

const b481Export = `"export":{"dir":"<REPO>/.code-forge/export/B1","restored":[],"untracked":[]}`

var b481Cases = []b481Case{
	{name: "revert-proven", mode: "src", args: b481Args, code: 0,
		stderr: "RED t/x.test.mjs\nGREEN t/x.test.mjs\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,` + b481Export + "}\n",
		argv:   "--test t/x.test.mjs\n--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "case-pattern", mode: "src", args: append(append([]string{}, b481Args[:6]...), "t/x.test.mjs::a.b(c)"), code: 0,
		stderr: "RED t/x.test.mjs::a.b(c)\nGREEN t/x.test.mjs::a.b(c)\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs::a.b(c)","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,` + b481Export + "}\n",
		argv:   "--test --test-name-pattern ^a\\.b\\(c\\)$ t/x.test.mjs\n--test --test-name-pattern ^a\\.b\\(c\\)$ t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs::a.b(c)","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "not-red", mode: "pass", args: b481Args, code: 1,
		stderr: "NOT_RED t/x.test.mjs\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"export","red":"NOT_RED","red_kind":"none","green":null,"proven":false,` + b481Export + "}\n",
		argv:   "--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":"none","red":"NOT_RED","green":null,"proven":false,"failed":null,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "red-invalid-fatal", mode: "kill", args: b481Args, code: 1,
		stderr: "RED_INVALID t/x.test.mjs (fatal)\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"export","red":"RED_INVALID","red_kind":"fatal","green":null,"proven":false,` + b481Export + "}\n",
		argv:   "--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":"fatal","red":"RED_INVALID","green":null,"proven":false,"failed":null,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "red-invalid-compile", mode: "syntax", args: b481Args, code: 1,
		stderr: "RED_INVALID t/x.test.mjs (compile)\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"export","red":"RED_INVALID","red_kind":"compile","green":null,"proven":false,` + b481Export + "}\n",
		argv:   "--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":"compile","red":"RED_INVALID","green":null,"proven":false,"failed":null,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "assertion-deletion", mode: "pass", args: b481With("--mechanism", "assertion-deletion"), code: 0,
		stderr: "RED t/x.test.mjs\nGREEN t/x.test.mjs\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"assertion-deletion","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,` + b481Export + "}\n",
		argv:   "--test t/x.test.mjs\n--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"assertion-deletion","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["t/x.test.mjs"],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "lock-isolation", mode: "src", config: b462Levels + "proof:\n  isolation: lock\n", args: b481Args, code: 0,
		extra:  ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B1"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		stderr: "RED t/x.test.mjs\nGREEN t/x.test.mjs\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"lock","red":"RED","red_kind":"assertion","green":"GREEN","proven":true}` + "\n",
		argv:   "--test t/x.test.mjs\n--test t/x.test.mjs\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["src/x.js"],"event":"proof","block":"B1","isolation":"lock","tokens_source":null,"cost_source":null}`},
	{name: "lock-not-held", mode: "src", config: b462Levels + "proof:\n  isolation: lock\n", args: b481Args, code: 1,
		stderr: "proof red-green: proof.isolation is lock — run code-forge proof lock B1 --run r-b462-proof first\n"},
	{name: "journal-present", mode: "src", config: b462Levels + "proof:\n  isolation: lock\n", args: b481Args, code: 1,
		extra:   ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B1"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		prepare: func(t *testing.T, repo string) { b462Journal(t, repo, "") },
		stderr:  "proof red-green: a red→green journal exists in <REPO> — an earlier run was interrupted; run code-forge proof restore first\n"},
	{name: "setup-failed", mode: "src", config: b462Levels + "proof:\n  worktree:\n    setup:\n      - [sh, -c, \"echo prepared; echo broke >&2; exit 3\"]\n", args: b481Args, code: 1,
		stderr: "SETUP_FAILED t/x.test.mjs\nprepared\n\nbroke\n",
		stdout: `{"block":"B1","test":"t/x.test.mjs","mechanism":"revert","isolation":"export","red":null,"red_kind":null,"green":null,"proven":false,"reason":"setup","output_tail":"prepared\n\nbroke",` + b481Export + "}\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"t/x.test.mjs","mechanism":"revert","red_kind":null,"red":null,"green":null,"proven":false,"failed":null,"covers":["src/x.js"],"reason":"setup","output_tail":"prepared\n\nbroke","event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	{name: "unsupported-test", mode: "src", args: append(append([]string{}, b481Args[:6]...), "t/x.py"), code: 1,
		stderr: "proof red-green: only node:test files (.mjs, .cjs, .js) have a filtered test command in this version — add a proof.runners entry for it (see proof.md §2)\n"},
	{name: "test-not-a-file", mode: "src", args: append(append([]string{}, b481Args[:6]...), "t/none.mjs"), code: 2,
		stderr: "proof red-green: --test t/none.mjs is not a file in <REPO>\n"},
	{name: "revert-no-source", mode: "src", args: b481With("--mechanism", "revert"), code: 2,
		block:  ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "main"}, {Key: "owned_files", Value: []any{"t/**"}}, {Key: "owned_kinds", Value: ledger.Object{}}},
		stderr: "proof red-green: block B1 changed no non-test source file to revert — use --mechanism assertion-deletion\n"},
	{name: "not-open", mode: "src", args: b481Args, code: 1,
		block:  ledger.Object{{Key: "status", Value: "landed"}},
		stderr: "proof red-green: block B1 is not open in run r-b462-proof\n"},
	{name: "runners-empty", mode: "src", args: b481Args, code: 1,
		stderr: "proof red-green: run r-b462-proof: proof.runners in the run snapshot is an empty list\n"},
}

func b481Fixture(t *testing.T, c b481Case) (home, repo string) {
	t.Helper()
	block := c.block
	if block == nil {
		block = b481Block()
	}
	config := c.config
	if config == "" {
		config = b462Levels
	}
	extra := c.extra
	if c.name == "runners-empty" {
		extra = append(append(ledger.Object{}, extra...), ledger.Field{Key: "config", Value: b481Snapshot(t, `{"version":1,"proof":{"runners":[]}}`)})
	}
	home, repo = b462Fixture(t, b462Case{blocks: ledger.Object{{Key: "B1", Value: block}}, extra: extra, setup: func(t *testing.T, repo string) {
		b481Repo(config)(t, repo)
		if c.prepare != nil {
			c.prepare(t, repo)
		}
	}})
	// the block's base is the commit the fixture made (a commit id, as the gate needs)
	record, err := state.ReadRun(home, b462Run)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := record.Get("blocks").(ledger.Object).Get("B1").(ledger.Object); b.Get("base_sha") == "main" {
		b.Set("base_sha", strings.TrimSpace(b462Git(t, repo, "rev-parse", "HEAD")))
		blocks, _ := record.Get("blocks").(ledger.Object)
		blocks.Set("B1", b)
		record.Set("blocks", blocks)
		if err := state.SaveRun(home, record); err != nil {
			t.Fatal(err)
		}
	}
	return home, repo
}

func TestB481ProofRedGreenGo(t *testing.T) {
	for _, c := range b481Cases {
		t.Run(c.name, func(t *testing.T) {
			home, repo := b481Fixture(t, c)
			code, stdout, stderr, argv := b481Call(t, home, repo, c.mode, c.args)
			if code != c.code || stdout != c.stdout || stderr != c.stderr || argv != c.argv {
				t.Fatalf("got code %d stdout %q stderr %q argv %q\nwant code %d stdout %q stderr %q argv %q", code, stdout, stderr, argv, c.code, c.stdout, c.stderr, c.argv)
			}
			after := b462Collect(t, home, repo)
			var rows []string
			for _, row := range after.rows {
				if strings.Contains(row, `"step":"red-green"`) {
					rows = append(rows, row)
				}
			}
			if c.row == "" && rows != nil || c.row != "" && !reflect.DeepEqual(rows, []string{c.row}) {
				t.Fatalf("red-green rows %q, want %q", rows, c.row)
			}
			// the block's tree is never changed, and no journal is left behind
			if got := after.files["src/x.js"]; got != "644 export const x = 'fixed';\n" {
				t.Fatalf("src/x.js is %q", got)
			}
			if c.name != "journal-present" {
				for rel := range after.files {
					if strings.HasSuffix(rel, "proof-journal.json") {
						t.Fatalf("journal left at %s", rel)
					}
				}
			}
			if strings.HasPrefix(c.name, "assertion") {
				if got := after.files[".code-forge/export/B1/t/x.test.mjs"]; !strings.Contains(got, "assert.equal(x, 'fixed');") {
					t.Fatalf("test file not restored in the export: %q", got)
				}
			}
		})
	}
}

// TestB481ProofRedGreenHandsOver: what Go has no port for keeps a named
// hand-over: a configured runner and the Pest default runner.
func TestB481ProofRedGreenHandsOver(t *testing.T) {
	c := b481Case{name: "runners", extra: ledger.Object{{Key: "config", Value: b481Snapshot(t, `{"version":1,"proof":{"runners":[{"adapter":"phpunit","match":"**/*.php","command":["vendor/bin/phpunit"]}]}}`)}}}
	home, repo := b481Fixture(t, c)
	t.Setenv("HOME", home)
	if rg, out, reason := proofRedGreenPlan(b462Run, "B1", "t/x.test.mjs", "", false, ""); rg != nil || out != nil || reason != hProofRunner {
		t.Fatalf("runners: got %v %v %v, want hand-over proof-runner", rg, out, reason)
	}
	home, repo = b481Fixture(t, b481Case{prepare: func(t *testing.T, repo string) {
		b462Write(t, filepath.Join(repo, "artisan"), []byte("<?php\n"), 0o644)
	}})
	t.Setenv("HOME", home)
	if rg, out, reason := proofRedGreenPlan(b462Run, "B1", "tests/XTest.php", "", false, ""); rg != nil || out != nil || reason != hProofRunner {
		t.Fatalf("pest: got %v %v %v, want hand-over proof-runner", rg, out, reason)
	}
	_ = repo
}

// TestB481ProofRedGreenMatchesNode runs real node:test files through both
// engines on twin fixtures (skipped without node on PATH).
func TestB481ProofRedGreenMatchesNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the Go side is checked by TestB481ProofRedGreenGo")
	}
	for _, c := range b481Cases {
		switch c.name {
		case "revert-proven", "case-pattern", "assertion-deletion", "lock-isolation", "lock-not-held", "journal-present", "setup-failed", "unsupported-test", "test-not-a-file", "revert-no-source", "not-open", "runners-empty":
		default:
			continue // the fake test modes only exist for the Go side
		}
		t.Run(c.name, func(t *testing.T) {
			goHome, goRepo := b481Fixture(t, c)
			// CODE_FORGE_NODE does not resolve (a hand-over fails the test); the
			// red-green runner finds the real node on PATH
			goCode, goOut, goErr := b462GoCall(t, goHome, goRepo, c.args)
			nodeHome, nodeRepo := b481Fixture(t, c)
			cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs")}, c.args...)...)
			cmd.Dir = nodeRepo
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + nodeHome, "TMPDIR=" + os.TempDir(), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NO_ERROR_LOG=1", "NO_COLOR=1"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			nodeCode := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				nodeCode = exit.ExitCode()
			}
			norm := func(s, repo string) string {
				return b462Stamp.ReplaceAllString(strings.ReplaceAll(s, repo, "<REPO>"), "<T>")
			}
			if goCode != nodeCode || norm(goOut, goRepo) != norm(stdout.String(), nodeRepo) || norm(goErr, goRepo) != norm(stderr.String(), nodeRepo) {
				t.Fatalf("go %d %q %q\nnode %d %q %q", goCode, goOut, goErr, nodeCode, stdout.String(), stderr.String())
			}
			goState, nodeState := b462Collect(t, goHome, goRepo), b462Collect(t, nodeHome, nodeRepo)
			goState.record, nodeState.record = b462Stamp.ReplaceAllString(goState.record, "<T>"), b462Stamp.ReplaceAllString(nodeState.record, "<T>")
			if !reflect.DeepEqual(goState, nodeState) {
				t.Fatalf("state differs\ngo   %+v\nnode %+v", goState, nodeState)
			}
		})
	}
}

// b481Lex are assertion-deletion inputs: statements over lines, chains,
// strings and comments, template literals, and the three refusals.
var b481Lex = []string{
	"import assert from 'node:assert';\nassert.equal(1, 1);\n",
	"test('x', () => {\n  assert.deepEqual(\n    a,\n    b,\n  );\n  expect(x).toBe(2);\n});\n",
	"const s = 'assert(1)'; // assert.ok(x)\n/* assert(2) */ t.assert.ok(y);\n",
	"const t = `${assert.ok(1)}`;\nassert.ok(2)\n",
	"const a = 1;\n",
	"assert.ok((1);\n",
	"assert.ok(1); assert.ok(2);\n",
	"x = assert\n",
	"\u00a0assert.strictEqual(é, '\U0001F600');\r\n",
}

// b481Runs are test outputs for parseRed.
var b481Runs = []struct {
	stdout, stderr, signal string
	code                   *int
	timedOut               bool
}{
	{stdout: "\x1b[31mℹ fail 2\x1b[0m\nAssertionError [ERR_ASSERTION]: x\n", code: b481Int(1)},
	{stdout: "# fail 1\n", stderr: "Error [ERR_MODULE_NOT_FOUND]: Cannot find module 'x'\n", code: b481Int(1)},
	{stderr: "  SyntaxError: bad\n", code: b481Int(1)},
	{stdout: "Tests:  3 failed, 1 passed\n", stderr: "Failed asserting that false is true.\n", code: b481Int(2)},
	{stdout: "boom\n", code: b481Int(1)},
	{stdout: "# pass 1\n", code: b481Int(0)},
	{signal: "SIGKILL"},
	{code: b481Int(0), timedOut: true},
}

func b481Int(n int) *int { return &n }

// TestB481LexAndParseMatchNode: Go's deleteAssertions and parseRed give
// Node's bytes, verdicts and error messages (skipped without node).
func TestB481LexAndParseMatchNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	repo := b458Repo(t)
	script := `const { deleteAssertions } = await import(process.argv[1] + '/src/proof/red-green.mjs');
const { parseRed } = await import(process.argv[1] + '/src/proof/red-parse.mjs');
const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const lex = input.lex.map((src) => { try { return { out: deleteAssertions('t/x.test.mjs', Buffer.from(src)).bytes.toString('base64') }; } catch (e) { return { code: e.code, message: e.message }; } });
const red = input.runs.map((r) => { const p = parseRed({ result: 'failed', stdout: r.stdout, stderr: r.stderr, code: r.code, signal: r.signal, timedOut: r.timedOut }); return { verdict: p.verdict, kind: p.red_kind, failed: p.failed ?? null }; });
console.log(JSON.stringify({ lex, red }));`
	type run struct {
		Stdout   string  `json:"stdout"`
		Stderr   string  `json:"stderr"`
		Code     *int    `json:"code"`
		Signal   *string `json:"signal"`
		TimedOut bool    `json:"timedOut"`
	}
	runs := []run{}
	for _, r := range b481Runs {
		var signal *string
		if r.signal != "" {
			signal = &r.signal
		}
		runs = append(runs, run{r.stdout, r.stderr, r.code, signal, r.timedOut})
	}
	input, _ := json.Marshal(map[string]any{"lex": b481Lex, "runs": runs})
	cmd := exec.Command(node, "--input-type=commonjs", "-e", "(async () => {"+script+"})()", repo)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v: %s", err, out)
	}
	var want struct {
		Lex []struct {
			Out     *string `json:"out"`
			Code    string  `json:"code"`
			Message string  `json:"message"`
		} `json:"lex"`
		Red []struct {
			Verdict string `json:"verdict"`
			Kind    string `json:"kind"`
			Failed  any    `json:"failed"`
		} `json:"red"`
	}
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("node output %s: %v", out, err)
	}
	for i, src := range b481Lex {
		got, err := proofDeleteAssertions("t/x.test.mjs", []byte(src))
		w := want.Lex[i]
		if w.Out != nil {
			if err != nil || base64.StdEncoding.EncodeToString(got) != *w.Out {
				gotNode, _ := base64.StdEncoding.DecodeString(*w.Out)
				t.Fatalf("lex %d: go %q %v, node %q", i, got, err, gotNode)
			}
			continue
		}
		var pe *proofError
		if !errors.As(err, &pe) || pe.code != w.Code || pe.message != w.Message {
			t.Fatalf("lex %d: go %v, node %s %s", i, err, w.Code, w.Message)
		}
	}
	for i, r := range b481Runs {
		got := proofParseRed(&proofExecResult{stdout: r.stdout, stderr: r.stderr, code: r.code, signal: r.signal, timedOut: r.timedOut})
		w := want.Red[i]
		if got.verdict != w.Verdict || got.kind != w.Kind || !reflect.DeepEqual(got.failed, w.Failed) {
			t.Fatalf("parse %d: go %+v, node %+v", i, got, w)
		}
	}
}

// TestB481NoRunRecord: the calls B458 handed over (proof-run) now refuse in
// Go as Node does (checked with CODE_FORGE_NATIVE=0: same text, exit 1).
func TestB481NoRunRecord(t *testing.T) {
	for _, test := range []string{"./", "t.test.mjs::case"} {
		home := t.TempDir()
		code, stdout, stderr := b462GoCall(t, home, home, []string{"proof", "red-green", "B1", "--run", "r", "--test", test})
		if code != 1 || stdout != "" || stderr != "proof red-green: no run record for r\n" {
			t.Fatalf("%s: got %d %q %q", test, code, stdout, stderr)
		}
	}
}
