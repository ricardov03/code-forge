package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B827: `proof red-green` runs a proof.runners entry, and the default
// Laravel/Pest runner for a .php test, in Go. Before B827 each of these
// cases returned the proof-runner hand-over (exit 1, "not supported in this
// case yet"), so every test below fails on the old code.
//
// The customer's case: a Laravel 13 + Pest 4 app on macOS. The fake `php`
// below replays what `php artisan test` printed and wrote (console and JUnit
// report) on a real Laravel 13.43.0 + Pest 4.7.8 app for the same tests
// (testdata/b827/laravel13, captured 2026-11-19 with PHP 8.4). It reads the
// tree it runs in, so it fails while the route is reverted and passes once
// the block's change is back, and it applies --filter as a regular expression
// to the test names, as Pest does.

const b827FakePHP = `#!/bin/sh
printf '%s\n' "$*" >> "$B827_ARGS"
if [ "$1" != artisan ] || [ "$2" != test ]; then echo "fake php: unexpected $*" >&2; exit 90; fi
file=$3; shift 3; report=; filter=; filtered=
while [ $# -gt 0 ]; do
  case "$1" in
    --log-junit) report=$2; shift 2 ;;
    --filter) filter=$2; filtered=1; shift 2 ;;
    *) echo "fake php: unknown option $1" >&2; exit 91 ;;
  esac
done
case "$file" in
  tests/Feature/StatusTest.php) names='it reports (v1.0) [ok] status + a/b?
it reports a json body' ;;
  tests/Feature/ClampTest.php) names='test_caps_high
test_keeps_inside' ;;
  tests/Feature/GreetingTest.php) names='it greets from the owned class' ;;
  *) echo "fake php: no test file $file" >&2; exit 92 ;;
esac
if [ -n "$filtered" ]; then
  selected=$(printf '%s\n' "$names" | perl -ne 'BEGIN { $f = shift @ARGV } chomp; print "$_\n" if /$f/' "$filter")
else
  selected=$names
fi
count=$(printf '%s' "$selected" | grep -c .)
broken=; grep -q "'broken'" routes/web.php && broken=1
case "$count:$file" in
  0:*) capture=pest-nomatch code=1 ;;
  2:tests/Feature/StatusTest.php) if [ -n "$broken" ]; then capture=pest-file-red code=1; else capture=pest-file-green code=0; fi ;;
  1:tests/Feature/StatusTest.php) if [ -n "$broken" ]; then capture=pest-red2 code=1; else capture=pest-green code=0; fi ;;
  1:tests/Feature/ClampTest.php) if [ -n "$broken" ]; then capture=phpunit-red code=1; else capture=phpunit-green code=0; fi ;;
  1:tests/Feature/GreetingTest.php) if [ -f app/Support/Greeting.php ]; then capture=pest-newclass-green code=0; else capture=pest-newclass code=2; fi ;;
  *) echo "fake php: the filter selected $count tests: $selected" >&2; exit 93 ;;
esac
form=text; [ -n "$CLAUDECODE" ] && form=json
cp "$B827_CAPTURED/$capture.xml" "$report"
cat "$B827_CAPTURED/$capture.$form.txt"
exit $code
`

const b827StatusTest = "<?php\n\nit('reports (v1.0) [ok] status + a/b?', function () {\n    $this->get('/status')->assertOk()->assertJson(['status' => 'ok']);\n});\n\nit('reports a json body', function () {\n    $this->get('/status')->assertOk()->assertHeader('content-type', 'application/json');\n});\n"

const b827Route = "<?php\n\nuse Illuminate\\Support\\Facades\\Route;\n\nRoute::get('/', function () {\n    return view('welcome');\n});\n\nRoute::get('/status', function () {\n    return response()->json(['status' => '%s']);\n});\n"

// b827Laravel writes the Laravel 13 + Pest 4 app at its base into dir.
func b827Laravel(t *testing.T, dir string) {
	t.Helper()
	w := func(rel, body string) { b462Write(t, filepath.Join(dir, rel), []byte(body), 0o644) }
	w(".gitignore", ".code-forge/\n/vendor\n")
	w("artisan", "#!/usr/bin/env php\n<?php\n\nuse Illuminate\\Foundation\\Application;\nuse Symfony\\Component\\Console\\Input\\ArgvInput;\n\ndefine('LARAVEL_START', microtime(true));\n\nrequire __DIR__.'/vendor/autoload.php';\n\n/** @var Application $app */\n$app = require_once __DIR__.'/bootstrap/app.php';\n\n$status = $app->handleCommand(new ArgvInput);\n\nexit($status);\n")
	w("composer.json", `{"name":"laravel/laravel","type":"project","require":{"php":"^8.3","laravel/framework":"^13.0"},"require-dev":{"pestphp/pest":"^4.0","pestphp/pest-plugin-laravel":"^4.0"},"autoload":{"psr-4":{"App\\":"app/"}},"autoload-dev":{"psr-4":{"Tests\\":"tests/"}}}`+"\n")
	w("routes/web.php", strings.Replace(b827Route, "%s", "broken", 1))
	w("tests/Pest.php", "<?php\n\npest()->extend(Tests\\TestCase::class)->in('Feature');\n")
	w("tests/TestCase.php", "<?php\n\nnamespace Tests;\n\nuse Illuminate\\Foundation\\Testing\\TestCase as BaseTestCase;\n\nabstract class TestCase extends BaseTestCase\n{\n}\n")
	w("tests/Feature/StatusTest.php", b827StatusTest)
	w("tests/Feature/ClampTest.php", "<?php\n\nnamespace Tests\\Feature;\n\nuse Tests\\TestCase;\n\nclass ClampTest extends TestCase\n{\n    public function test_caps_high(): void\n    {\n        $this->get('/status')->assertJson(['status' => 'ok']);\n    }\n\n    public function test_keeps_inside(): void\n    {\n        $this->get('/status')->assertOk();\n    }\n}\n")
	w("tests/Feature/GreetingTest.php", "<?php\n\nuse App\\Support\\Greeting;\n\nit('greets from the owned class', function () {\n    expect(Greeting::text())->toBe('hello');\n});\n")
}

// b827Change is the block's change: the route is fixed and the block adds
// the App\Support\Greeting class.
func b827Change(t *testing.T, dir string) {
	t.Helper()
	b462Write(t, filepath.Join(dir, "routes/web.php"), []byte(strings.Replace(b827Route, "%s", "ok", 1)), 0o644)
	b462Write(t, filepath.Join(dir, "app/Support/Greeting.php"), []byte("<?php\n\nnamespace App\\Support;\n\nclass Greeting\n{\n    public static function text(): string\n    {\n        return 'hello';\n    }\n}\n"), 0o644)
}

func b827Commit(t *testing.T, dir, message string) {
	t.Helper()
	b462Git(t, dir, "add", "-A")
	b462Git(t, dir, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", message)
}

// b827Fixture is a b462 run whose block B1 owns the app; setup builds the
// repo (its base is committed and becomes the block's base_sha).
func b827Fixture(t *testing.T, snapshot string, setup func(t *testing.T, repo string)) (home, repo string) {
	t.Helper()
	block := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "main"},
		{Key: "owned_files", Value: []any{"app/**", "routes/**", "tests/**", "Sources/**", "Tests/**", "lib/**"}}, {Key: "owned_kinds", Value: ledger.Object{}}}
	var extra ledger.Object
	if snapshot != "" {
		extra = ledger.Object{{Key: "config", Value: b481Snapshot(t, snapshot)}}
	}
	base := ""
	home, repo = b462Fixture(t, b462Case{blocks: ledger.Object{{Key: "B1", Value: block}}, extra: extra, setup: func(t *testing.T, repo string) {
		b462Write(t, filepath.Join(repo, ".code-forge.yml"), []byte(b462Levels), 0o644)
		b828Exclude(t, repo) // B828: proof red-green refuses while git would see .code-forge/
		setup(t, repo)
		base = strings.TrimSpace(b462Git(t, repo, "rev-parse", "HEAD"))
	}})
	record, err := state.ReadRun(home, b462Run)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := record.Get("blocks").(ledger.Object)
	b, _ := blocks.Get("B1").(ledger.Object)
	b.Set("base_sha", base)
	blocks.Set("B1", b)
	record.Set("blocks", blocks)
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	return home, repo
}

// b827LaravelRepo: the app at base, committed, then the block's change in the tree.
func b827LaravelRepo(t *testing.T, repo string) {
	b827Laravel(t, repo)
	b827Commit(t, repo, "laravel base")
	b827Change(t, repo)
}

// b827Tools puts the fake tools (name → script) first on PATH, with the
// captures dir and an argument log, and sets the console form (CLAUDECODE).
func b827Tools(t *testing.T, tools map[string]string, claudecode string) string {
	t.Helper()
	bin := t.TempDir()
	for name, script := range tools {
		b462Write(t, filepath.Join(bin, name), []byte(script), 0o755)
	}
	captured, err := filepath.Abs(filepath.Join("testdata", "b827"))
	if err != nil {
		t.Fatal(err)
	}
	argLog := filepath.Join(t.TempDir(), "b827-args")
	t.Setenv("B827_CAPTURED", filepath.Join(captured, "laravel13"))
	t.Setenv("B827_GOLDENS", captured)
	t.Setenv("B827_ARGS", argLog)
	t.Setenv("CLAUDECODE", claudecode)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argLog
}

// b827Call runs the verb in Go (b481Call's settings); a hand-over cannot
// pass: CODE_FORGE_NODE does not resolve and no node is faked.
func b827Call(t *testing.T, home, repo string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b827-node")
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
	return code, strings.ReplaceAll(stdout, repo, "<REPO>"), strings.ReplaceAll(stderr, repo, "<REPO>")
}

func b827Rows(t *testing.T, home, repo string) []string {
	t.Helper()
	var rows []string
	for _, row := range b462Collect(t, home, repo).rows {
		if strings.Contains(row, `"step":"red-green"`) {
			rows = append(rows, row)
		}
	}
	return rows
}

func b827Args(test string, extra ...string) []string {
	return append([]string{"proof", "red-green", "B1", "--run", b462Run, "--test", test}, extra...)
}

const b827Status = "tests/Feature/StatusTest.php"
const b827Case = "reports (v1.0) [ok] status + a/b?"
const b827Export = `"export":{"dir":"<REPO>/.code-forge/export/B1","restored":[],"untracked":[]}`

type b827PHPCase struct {
	name, test, claudecode string
	code                   int
	stderr, stdout, row    string
	argv                   string
}

var b827PHPCases = []b827PHPCase{
	// the customer's case: a Pest description with ( ) [ ] . + ? / selects one test
	{name: "pest-case-proven", test: b827Status + "::" + b827Case, claudecode: "1", code: 0,
		stderr: "RED " + b827Status + "::" + b827Case + "\nGREEN " + b827Status + "::" + b827Case + "\n",
		stdout: `{"block":"B1","test":"` + b827Status + "::" + b827Case + `","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,"runner":"junit","failed_tests":["Tests.Feature.StatusTest.it reports (v1.0) [ok] status + a/b?"],"env_keys":[],` + b827Export + "}\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"` + b827Status + "::" + b827Case + `","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["app/Support/Greeting.php","routes/web.php"],"runner":"junit","failed_tests":["Tests.Feature.StatusTest.it reports (v1.0) [ok] status + a/b?"],"env_keys":[],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`,
		argv:   "artisan test " + b827Status + " --log-junit <REPORT> --filter reports \\(v1\\.0\\) \\[ok\\] status \\+ a/b\\?\n"},
	// the same in a plain terminal (the text console)
	{name: "pest-case-proven-text", test: b827Status + "::" + b827Case, code: 0,
		stderr: "RED " + b827Status + "::" + b827Case + "\nGREEN " + b827Status + "::" + b827Case + "\n"},
	// the whole file: two tests run, one fails at RED
	{name: "pest-file-proven", test: b827Status, claudecode: "1", code: 0,
		stderr: "RED " + b827Status + "\nGREEN " + b827Status + "\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"` + b827Status + `","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["app/Support/Greeting.php","routes/web.php"],"runner":"junit","failed_tests":["Tests.Feature.StatusTest.it reports (v1.0) [ok] status + a/b?"],"env_keys":[],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`,
		argv:   "artisan test " + b827Status + " --log-junit <REPORT>\n"},
	// a filter that selects no test proves nothing
	{name: "filter-selects-none", test: b827Status + "::no such test", claudecode: "1", code: 1,
		stderr: "RED_INVALID " + b827Status + "::no such test (error)\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"` + b827Status + `::no such test","mechanism":"revert","red_kind":"error","red":"RED_INVALID","green":null,"proven":false,"failed":0,"covers":["app/Support/Greeting.php","routes/web.php"],"runner":"junit","failed_tests":[],"env_keys":[],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	// a test of a class the block added: the class is gone at RED (plain console)
	{name: "new-class-text", test: "tests/Feature/GreetingTest.php::greets from the owned class", code: 1,
		stderr: "RED_INVALID tests/Feature/GreetingTest.php::greets from the owned class (import)\n"},
	// the same under CLAUDECODE: Laravel 13's one-line JSON console
	{name: "new-class-json", test: "tests/Feature/GreetingTest.php::greets from the owned class", claudecode: "1", code: 1,
		stderr: "RED_INVALID tests/Feature/GreetingTest.php::greets from the owned class (import)\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"tests/Feature/GreetingTest.php::greets from the owned class","mechanism":"revert","red_kind":"import","red":"RED_INVALID","green":null,"proven":false,"failed":1,"covers":["app/Support/Greeting.php","routes/web.php"],"runner":"junit","failed_tests":["Tests.Feature.GreetingTest.it greets from the owned class"],"env_keys":[],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`},
	// a PHPUnit method case
	{name: "phpunit-method", test: "tests/Feature/ClampTest.php::test_caps_high", code: 0,
		stderr: "RED tests/Feature/ClampTest.php::test_caps_high\nGREEN tests/Feature/ClampTest.php::test_caps_high\n",
		row:    `{"run":"r-b462-proof","step":"red-green","test":"tests/Feature/ClampTest.php::test_caps_high","mechanism":"revert","red_kind":"assertion","red":"RED","green":"GREEN","proven":true,"failed":1,"covers":["app/Support/Greeting.php","routes/web.php"],"runner":"junit","failed_tests":["Tests.Feature.ClampTest.Caps high"],"env_keys":[],"event":"proof","block":"B1","isolation":"export","tokens_source":null,"cost_source":null}`,
		argv:   "artisan test tests/Feature/ClampTest.php --log-junit <REPORT> --filter test_caps_high\n"},
}

// b827Report is the report path in the fake php's argument log.
var b827Report = regexp.MustCompile(`/\S*/proof-report-[^/ ]+/report\.xml`)

func TestB827PestCustomerCase(t *testing.T) {
	for _, c := range b827PHPCases {
		t.Run(c.name, func(t *testing.T) {
			home, repo := b827Fixture(t, "", b827LaravelRepo)
			argLog := b827Tools(t, map[string]string{"php": b827FakePHP}, c.claudecode)
			code, stdout, stderr := b827Call(t, home, repo, b827Args(c.test)...)
			if code != c.code || stderr != c.stderr || c.stdout != "" && stdout != c.stdout {
				t.Fatalf("got code %d stdout %q stderr %q\nwant code %d stdout %q stderr %q", code, stdout, stderr, c.code, c.stdout, c.stderr)
			}
			rows := b827Rows(t, home, repo)
			if len(rows) != 1 || c.row != "" && rows[0] != c.row {
				t.Fatalf("red-green rows %q, want %q", rows, c.row)
			}
			if c.argv != "" {
				logged, _ := os.ReadFile(argLog)
				lines := strings.Split(strings.TrimSuffix(b827Report.ReplaceAllString(string(logged), "<REPORT>"), "\n"), "\n")
				for _, line := range lines {
					if line+"\n" != c.argv {
						t.Fatalf("php argv %q, want %q", logged, c.argv)
					}
				}
			}
			// the block's tree is never changed
			after := b462Collect(t, home, repo)
			if !strings.Contains(after.files["routes/web.php"], "'ok'") || after.files["app/Support/Greeting.php"] == "" {
				t.Fatalf("tree changed: %q %q", after.files["routes/web.php"], after.files["app/Support/Greeting.php"])
			}
		})
	}
}

// TestB827CloseNeedsTheProvenRow: worktree isolation, and the row `block
// close` reads. A high-tier change (an approved review at risk 2) is blocked
// as unproven until red-green proves it; the proven row clears the rule and
// the real close passes.
func TestB827CloseNeedsTheProvenRow(t *testing.T) {
	root := t.TempDir()
	f := &preflightFixture{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "tree"), argvLog: filepath.Join(root, "argv.log"), run: "r-b827"}
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	b827Laravel(t, f.repo)
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "add", ".")
	f.commit(f.repo, "laravel base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.repo, "worktree", "add", "-qb", "forge-b1", f.tree, f.base)
	b827Change(t, f.tree)
	f.git(f.tree, "add", ".")
	f.commit(f.tree, "fix the status route")
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "kind", Value: "code"}, {Key: "owned_files", Value: []any{"app/**", "routes/**", "tests/**"}},
		{Key: "test_files", Value: []any{}}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge-b1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "laravel"}, {Key: "workspace", Value: f.repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = 43
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", f.run+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sign(ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	f.setSnapshot(readyBaseConfig+"}", nil)
	err := state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
		record.Set("worker", ledger.Object{{Key: "pid", Value: os.Getpid()}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"app/Support/Greeting.php", "routes/web.php"} {
		hash, err := gitx.ContentHash(f.tree, file)
		if err != nil {
			t.Fatal(err)
		}
		id := ticket.ReviewID(ticket.Parts{Run: f.run, Block: "B1", File: file, ContentHash: hash})
		row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "ticket", Value: id},
			{Key: "content_hash", Value: hash}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: true}, {Key: "risk", Value: 2}, {Key: "open", Value: []any{}}}
		f.sign(row)
		row.Set("event", "review.approved")
		f.sign(row)
	}
	b827Tools(t, map[string]string{"php": b827FakePHP}, "1")
	unproven := func() []string {
		code, stdout, stderr := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
		r := b622Parse(t, code, stdout, stderr)
		files := []string{}
		for _, b := range r.Blockers {
			if b.Code == "unproven" && b.File != nil {
				files = append(files, *b.File)
			}
		}
		return files
	}
	if got := unproven(); !reflect.DeepEqual(got, []string{"app/Support/Greeting.php", "routes/web.php"}) {
		t.Fatalf("before the proof: unproven %q", got)
	}
	code, stdout, stderr := f.call("proof", "red-green", "B1", "--run", f.run, "--test", b827Status+"::"+b827Case)
	label := b827Status + "::" + b827Case
	stdout = strings.ReplaceAll(stdout, f.repo, "<REPO>")
	if code != 0 || stderr != "RED "+label+"\nGREEN "+label+"\n" ||
		stdout != `{"block":"B1","test":"`+label+`","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,"runner":"junit","failed_tests":["Tests.Feature.StatusTest.it reports (v1.0) [ok] status + a/b?"],"env_keys":[],"export":{"dir":"<REPO>/.code-forge/export/B1","restored":[],"untracked":[]}}`+"\n" {
		t.Fatalf("proof in the worktree: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := unproven(); len(got) != 0 {
		t.Fatalf("after the proof: unproven %q", got)
	}
	code, stdout, stderr = f.call("block", "close", "B1", "--run", f.run, "--json")
	if code != 0 || !strings.Contains(stdout, `"status":"closed"`) {
		t.Fatalf("close after the proof: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	// the worktree is back as the block left it
	if data, _ := os.ReadFile(filepath.Join(f.tree, "routes/web.php")); !strings.Contains(string(data), "'ok'") {
		t.Fatalf("routes/web.php in the worktree: %q", data)
	}
}

// b827FakePytest is a junit proof.runners entry's command: it needs the
// entry's env, and fails until lib/clamp.py has the fix.
const b827FakePytest = `#!/bin/sh
printf '%s\n' "$*" >> "$B827_ARGS"
[ "$B827_TOKEN" = "a-long-secret-value" ] || { echo "fake pytest: the entry env is missing" >&2; exit 94; }
env | grep -q '^GIT_DIR=' && { echo "fake pytest: GIT_DIR leaked" >&2; exit 95; }
report=$3
if grep -q fixed lib/clamp.py; then
  printf '<testsuites><testsuite name="pytest" tests="2" failures="0"><testcase classname="tests.test_clamp" name="test_clamps_low"/><testcase classname="tests.test_clamp" name="test_clamps_high"/></testsuite></testsuites>\n' > "$report"
  echo "2 passed"; exit 0
fi
cp "$B827_GOLDENS/junit/pytest.xml" "$report"
echo "1 failed, 1 passed"; exit 1
`

// TestB827JUnitRunnerEntry: a configured junit entry (pytest). A file the
// entry matches is a test, never a reverted source.
func TestB827JUnitRunnerEntry(t *testing.T) {
	snapshot := `{"version":1,"proof":{"runners":[{"adapter":"junit","match":["checks/**/*.py"],"argv":["pytest","{test_file}","--junitxml","{report}"],"env":{"PYTHONDONTWRITEBYTECODE":"1","B827_TOKEN":"a-long-secret-value"}}]}}`
	home, repo := b827Fixture(t, snapshot, func(t *testing.T, repo string) {
		b462Write(t, filepath.Join(repo, "lib/clamp.py"), []byte("def clamp(x, lo, hi):\n    return x  # broken\n"), 0o644)
		b462Write(t, filepath.Join(repo, "checks/test_clamp.py"), []byte("from lib.clamp import clamp\n\ndef test_clamps_high():\n    assert clamp(12, 0, 10) == 10\n"), 0o644)
		b827Commit(t, repo, "python base")
		b462Write(t, filepath.Join(repo, "lib/clamp.py"), []byte("def clamp(x, lo, hi):\n    return max(lo, min(x, hi))  # fixed\n"), 0o644)
		b462Write(t, filepath.Join(repo, "checks/test_clamp.py"), []byte("from lib.clamp import clamp\n\ndef test_clamps_high():\n    assert clamp(12, 0, 10) == 10\n\ndef test_clamps_low():\n    assert clamp(-1, 0, 10) == 0\n"), 0o644)
	})
	argLog := b827Tools(t, map[string]string{"pytest": b827FakePytest}, "")
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	code, stdout, stderr := b827Call(t, home, repo, b827Args("checks/test_clamp.py")...)
	want := `{"block":"B1","test":"checks/test_clamp.py","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,"runner":"junit","failed_tests":["tests.test_clamp.test_clamps_high"],"env_keys":["B827_TOKEN","PYTHONDONTWRITEBYTECODE"],"export":{"dir":"<REPO>/.code-forge/export/B1","restored":[],"untracked":[]}}` + "\n"
	if code != 0 || stdout != want || stderr != "RED checks/test_clamp.py\nGREEN checks/test_clamp.py\n" {
		t.Fatalf("got %d %q %q\nwant %q", code, stdout, stderr, want)
	}
	rows := b827Rows(t, home, repo)
	if len(rows) != 1 || !strings.Contains(rows[0], `"covers":["lib/clamp.py"],"runner":"junit","failed_tests":["tests.test_clamp.test_clamps_high"],"env_keys":["B827_TOKEN","PYTHONDONTWRITEBYTECODE"]`) || strings.Contains(rows[0], "a-long-secret-value") {
		t.Fatalf("row %q", rows)
	}
	if logged, _ := os.ReadFile(argLog); strings.Count(string(logged), "checks/test_clamp.py --junitxml ") != 2 {
		t.Fatalf("pytest argv %q", logged)
	}
}

// b827XCTestLine is the console line XCTest prints for the failing case.
const b827XCTestLine = `/Users/dev/Kit/Tests/KitTests/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : XCTAssertEqual failed: ("12") is not equal to ("10")`

// b827FakeSwift is `swift test --filter <f> --xunit-output <report>`: it
// writes the XCTest report and the Swift Testing one beside it (the af02c208^
// goldens), failing until Sources/Kit/Clamp.swift has the fix.
const b827FakeSwift = `#!/bin/sh
printf '%s\n' "$*" >> "$B827_ARGS"
report=$5
second=${report%.xml}-swift-testing.xml
cp "$B827_GOLDENS/junit/swift-testing-none.xml" "$second"
if grep -q fixed Sources/Kit/Clamp.swift; then cp "$B827_GOLDENS/junit/xctest-pass.xml" "$report"; exit 0; fi
cp "$B827_GOLDENS/junit/xctest.xml" "$report"
echo '` + b827XCTestLine + `'
exit 1
`

// b827FakeXcodebuild serves the af02c208^ xcresult goldens: xcodebuild
// leaves the golden's name in the result bundle and xcrun's xcresulttool
// prints it; simctl lists one available iPhone 17; xcode-select points at Xcode.
const b827FakeXcodebuild = `#!/bin/sh
printf '%s\n' "$*" >> "$B827_ARGS"
while [ $# -gt 0 ]; do [ "$1" = -resultBundlePath ] && bundle=$2; shift; done
mkdir -p "$bundle"
if grep -q fixed Sources/Kit/Clamp.swift; then echo xctest-pass.json > "$bundle/b827-golden"; echo "** TEST SUCCEEDED **"; exit 0; fi
echo xctest.json > "$bundle/b827-golden"
cat "$B827_GOLDENS/xcresult/xctest.console"
exit 65
`

const b827FakeXcrun = `#!/bin/sh
case "$1" in
  simctl) printf '{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-26-0":[{"name":"iPhone 17","udid":"0B9E3D1A-0000-4000-8000-000000000017","isAvailable":true,"state":"Shutdown"}]}}\n' ;;
  xcresulttool) while [ $# -gt 0 ]; do [ "$1" = --path ] && bundle=$2; shift; done; cat "$B827_GOLDENS/xcresult/$(cat "$bundle/b827-golden")" ;;
  *) exit 97 ;;
esac
`

const b827FakeXcodeSelect = "#!/bin/sh\necho /Applications/Xcode.app/Contents/Developer\n"

func b827SwiftRepo(t *testing.T, repo string) {
	b462Write(t, filepath.Join(repo, "Sources/Kit/Clamp.swift"), []byte("public func clamp(_ x: Int) -> Int { x } // broken\n"), 0o644)
	b462Write(t, filepath.Join(repo, "Tests/KitTests/ClampXCTests.swift"), []byte("import XCTest\n@testable import Kit\n\nfinal class ClampXCTests: XCTestCase {\n    func testKeepsInside() { XCTAssertEqual(clamp(5), 5) }\n    func testCapsHigh() { XCTAssertEqual(clamp(12), 10) }\n}\n"), 0o644)
	b827Commit(t, repo, "swift base")
	b462Write(t, filepath.Join(repo, "Sources/Kit/Clamp.swift"), []byte("public func clamp(_ x: Int) -> Int { min(x, 10) } // fixed\n"), 0o644)
}

// TestB827SwiftAndXcodebuildAdapters: both adapters prove the XCTest case
// from the af02c208^ goldens; the toolchain is faked, so no Swift or Xcode
// is needed.
func TestB827SwiftAndXcodebuildAdapters(t *testing.T) {
	cases := []struct {
		name, runner string
		tools        map[string]string
		argv         string
	}{
		{"swift", `{"adapter":"swift","match":["Tests/**/*.swift"],"argv":["swift","test","--filter","{test_filter}","--xunit-output","{report}"]}`,
			map[string]string{"swift": b827FakeSwift}, `test --filter \.ClampXCTests/ --xunit-output `},
		{"xcodebuild", `{"adapter":"xcodebuild","match":["Tests/**/*.swift"],"argv":["xcodebuild","test","-scheme","{scheme}","-destination","{destination}","{only_testing}","-resultBundlePath","{result_bundle}"],"xcode":{"scheme":"Kit","destination":"platform=iOS Simulator,name=iPhone 17","test_target":"KitTests"}}`,
			map[string]string{"xcodebuild": b827FakeXcodebuild, "xcrun": b827FakeXcrun, "xcode-select": b827FakeXcodeSelect}, "test -scheme Kit -destination platform=iOS Simulator,name=iPhone 17 -only-testing:KitTests/ClampXCTests -resultBundlePath "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, repo := b827Fixture(t, `{"version":1,"proof":{"runners":[`+c.runner+`]}}`, b827SwiftRepo)
			argLog := b827Tools(t, c.tools, "")
			code, stdout, stderr := b827Call(t, home, repo, b827Args("Tests/KitTests/ClampXCTests.swift")...)
			failed := "KitTests.ClampXCTests.testCapsHigh"
			if c.name == "xcodebuild" {
				failed = "KitTests/ClampXCTests/testCapsHigh()"
			}
			want := `{"block":"B1","test":"Tests/KitTests/ClampXCTests.swift","mechanism":"revert","isolation":"export","red":"RED","red_kind":"assertion","green":"GREEN","proven":true,"runner":"` + c.name + `","failed_tests":["` + failed + `"],"env_keys":[],"export":{"dir":"<REPO>/.code-forge/export/B1","restored":[],"untracked":[]}}` + "\n"
			if code != 0 || stdout != want || stderr != "RED Tests/KitTests/ClampXCTests.swift\nGREEN Tests/KitTests/ClampXCTests.swift\n" {
				t.Fatalf("got %d %q %q\nwant %q", code, stdout, stderr, want)
			}
			rows := b827Rows(t, home, repo)
			if len(rows) != 1 || !strings.Contains(rows[0], `"proven":true,"failed":1,"covers":["Sources/Kit/Clamp.swift"],"runner":"`+c.name+`"`) {
				t.Fatalf("row %q", rows)
			}
			if logged, _ := os.ReadFile(argLog); strings.Count(string(logged), c.argv) != 2 {
				t.Fatalf("argv %q, want twice %q", logged, c.argv)
			}
		})
	}
}

func b827Golden(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b827", rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func b827Run(code int, stdout string) *proofExecResult {
	return &proofExecResult{code: &code, stdout: stdout}
}

// TestB827ReportGoldens: the JUnit and xcresult readers and classifiers over
// the af02c208^ goldens give what junit.test.mjs and xcodebuild-adapter.test.mjs pinned.
func TestB827ReportGoldens(t *testing.T) {
	newClassJSON := `{"tool":"pest","result":"failed","tests":1,"passed":0,"assertions":0,"duration_ms":10,"errors":1,"error_details":[{"test":"P\\Tests\\Unit\\GreetingTest::__pest_evaluable_greeting_comes_from_the_owned_class","file":"/x/tests/Unit/GreetingTest.php","line":6,"message":"Class \"App\\Support\\Greeting\" not found"}]}` + "\n"
	newClassText := "  \x1b[41;1m FAILED \x1b[49;22m \x1b[1mTests\\Unit\\GreetingTest\x1b[22m \x1b[90m>\x1b[39m greeting comes from the owned cl… \x1b[41;1m Error \x1b[49;22m  \n\x1b[39;1m  Class \"App\\Support\\Greeting\" not found\x1b[39;22m\n"
	xctestLine := "/Users/dev/Kit/Tests/KitTests/ClampXCTests.swift:6: error: -[KitTests.ClampXCTests testCapsHigh] : XCTAssertEqual failed: (\"12\") is not equal to (\"10\")\n"
	red := []struct {
		file, console string
		code          int
		verdict, kind string
		failedTests   []string
	}{
		{"junit/pest.xml", "", 1, "RED", "assertion", []string{"Tests.Unit.ClampTest.caps high"}},
		{"junit/phpunit.xml", "", 1, "RED", "assertion", []string{"Tests.Unit.ClampTest.test_caps_high"}},
		{"junit/pytest.xml", "", 1, "RED", "assertion", []string{"tests.test_clamp.test_clamps_high"}},
		{"junit/xctest.xml", xctestLine, 1, "RED", "assertion", []string{"KitTests.ClampXCTests.testCapsHigh"}},
		{"junit/xctest.xml", "", 1, "RED_INVALID", "error", []string{"KitTests.ClampXCTests.testCapsHigh"}},
		{"junit/swift-testing.xml", "", 1, "RED", "assertion", []string{"KitTests.ClampSwiftTests.capsHigh()"}},
		{"junit/pest-new-class.xml", newClassJSON, 2, "RED_INVALID", "import", nil},
		{"junit/pest-new-class.xml", newClassText, 2, "RED_INVALID", "import", nil},
		{"junit/pest-new-class.xml", "", 2, "RED_INVALID", "error", nil},
		{"junit/pest-pass.xml", "", 0, "NOT_RED", "none", nil},
		{"laravel13/pest-nomatch.xml", "", 1, "RED_INVALID", "error", nil},
	}
	for _, c := range red {
		got := prClassifyReport(b827Run(c.code, c.console), []prReport{prReadJUnit(b827Golden(t, c.file))}, nil)
		if got.verdict != c.verdict || got.kind != c.kind || c.failedTests != nil && !reflect.DeepEqual(got.failedTests, c.failedTests) {
			t.Errorf("%s (console %q): got %s %s %q, want %s %s %q", c.file, c.console, got.verdict, got.kind, got.failedTests, c.verdict, c.kind, c.failedTests)
		}
	}
	for _, pass := range []struct{ red, green string }{{"junit/pest.xml", "junit/pest-pass.xml"}, {"junit/phpunit.xml", "junit/phpunit-pass.xml"}, {"junit/swift-testing.xml", "junit/swift-testing-pass.xml"}} {
		r := prClassifyReport(b827Run(1, ""), []prReport{prReadJUnit(b827Golden(t, pass.red))}, nil)
		if got := prClassifyGreen(b827Run(0, ""), []prReport{prReadJUnit(b827Golden(t, pass.green))}, r.failedIDs); got != "GREEN" {
			t.Errorf("%s: %s", pass.green, got)
		}
		if got := prClassifyGreen(b827Run(0, ""), []prReport{prReadJUnit(b827Golden(t, pass.red))}, r.failedIDs); got != "NOT_GREEN" {
			t.Errorf("%s as GREEN: %s", pass.red, got)
		}
	}
	if got := prClassifyReport(b827Run(1, ""), []prReport{prReadJUnit([]byte("<testsuites><testcase"))}, nil); got.verdict != "RED_INVALID" || got.kind != "error" {
		t.Errorf("unreadable report: %s %s", got.verdict, got.kind)
	}

	xc := func(file string) prReport { return prReadXcresult(string(b827Golden(t, "xcresult/"+file))) }
	type xt struct{ id, status, kind string }
	tests := func(r prReport) []xt {
		out := []xt{}
		for _, x := range r.tests {
			out = append(out, xt{x.id, x.status, x.kind})
		}
		return out
	}
	for _, c := range []struct {
		file string
		want []xt
	}{
		{"xctest.json", []xt{{"KitTests/ClampXCTests/testCapsHigh()", "failed", "assertion"}, {"KitTests/ClampXCTests/testKeepsInside()", "passed", ""}}},
		{"swift-testing.json", []xt{{"KitTests/ClampSwiftTests/capsHigh()", "failed", "assertion"}, {"KitTests/ClampSwiftTests/keepsInside()", "passed", ""}}},
		{"swift-testing-noparens.json", []xt{}},
		{"build-failed.json", []xt{}},
		{"bare-fail.json", []xt{{"KitTests/BareFailXCTests/testBareFail()", "failed", "assertion"}, {"KitTests/BareIssueTests/recorded()", "failed", "error"}}},
	} {
		if got := tests(xc(c.file)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.file, got, c.want)
		}
	}
	console := string(b827Golden(t, "xcresult/xctest.console"))
	if got := prClassifyXcodeRed(b827Run(65, console), xc("xctest.json"), "KitTests/ClampXCTests/"); got.verdict != "RED" || got.kind != "assertion" || !reflect.DeepEqual(got.failedTests, []string{"KitTests/ClampXCTests/testCapsHigh()"}) {
		t.Errorf("xctest RED: %+v", got)
	}
	if got := prClassifyXcodeRed(b827Run(65, ""), xc("near-names.json"), "KitTests/ClampXCTests/"); got.verdict != "RED_INVALID" || got.kind != "error" || got.failed != nil || len(got.failedTests) != 0 {
		t.Errorf("near-names: %+v", got)
	}
	if got := prClassifyXcodeRed(b827Run(65, string(b827Golden(t, "xcresult/build-failed.console"))), xc("build-failed.json"), "KitTests/ClampXCTests/"); got.verdict != "RED_INVALID" || got.kind != "compile" {
		t.Errorf("build failed: %+v", got)
	}
	red0 := prClassifyXcodeRed(b827Run(65, console), xc("xctest.json"), "KitTests/ClampXCTests/")
	if got := prClassifyXcodeGreen(b827Run(0, ""), xc("xctest-pass.json"), red0.failedIDs, "KitTests/ClampXCTests/"); got != "GREEN" {
		t.Errorf("xctest GREEN: %s", got)
	}
	for text, kind := range map[string]string{
		`ClampXCTests.swift:7: XCTAssertEqual failed: ("12") is not equal to ("10")`: "assertion",
		"XCTAssertTrue failed":                     "assertion",
		"BareFailTests.swift:7: failed":            "assertion",
		"XCTAssertEqual failed: threw error \"x\"": "error",
		"Caught error: x":                          "error",
		"Issue recorded":                           "error",
		"Crash: Kit at clamp":                      "error",
		"failed!":                                  "error",
		"":                                         "error",
	} {
		if got := prFailureMessageKind(text); got != kind {
			t.Errorf("failureMessageKind(%q) = %s, want %s", text, got, kind)
		}
	}
}

// TestB827LiveLaravel runs the customer's case on a real Laravel 13 + Pest 4
// app. It needs php and composer, network for composer, and
// CODE_FORGE_LIVE_PHP=1 (or CODE_FORGE_LIVE_LARAVEL_APP, a ready app to copy).
func TestB827LiveLaravel(t *testing.T) {
	if os.Getenv("CODE_FORGE_LIVE_PHP") != "1" {
		t.Skip("live Laravel run: set CODE_FORGE_LIVE_PHP=1 (needs php, composer and network); the fake-php tests above replay its captures")
	}
	for _, tool := range []string{"php", "composer"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("live Laravel run: " + tool + " is not on PATH")
		}
	}
	app := os.Getenv("CODE_FORGE_LIVE_LARAVEL_APP")
	home, repo := b827Fixture(t, "", func(t *testing.T, repo string) {
		var cmd *exec.Cmd
		if app != "" {
			cmd = exec.Command("cp", "-R", app+"/.", repo)
		} else {
			cmd = exec.Command("sh", "-c", `composer create-project laravel/laravel:^13.0 app --no-interaction --prefer-dist --quiet && cd app && composer remove phpunit/phpunit --dev --no-interaction --quiet && composer require pestphp/pest:^4.0 pestphp/pest-plugin-laravel:^4.0 --dev -W --no-interaction --quiet && ./vendor/bin/pest --init --no-interaction >/dev/null && cp -R . "$0"`, repo)
			cmd.Dir = t.TempDir()
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("laravel app: %v\n%s", err, out)
		}
		_ = os.Remove(filepath.Join(repo, "app/Support/Greeting.php"))
		_ = os.RemoveAll(filepath.Join(repo, ".git"))
		b462Git(t, repo, "init", "-qb", "main")
		b462Write(t, filepath.Join(repo, ".code-forge.yml"), []byte(b462Levels), 0o644)
		b462Write(t, filepath.Join(repo, "routes/web.php"), []byte(strings.Replace(b827Route, "%s", "broken", 1)), 0o644)
		b462Write(t, filepath.Join(repo, "tests/Feature/StatusTest.php"), []byte(b827StatusTest), 0o644)
		b462Write(t, filepath.Join(repo, "tests/Feature/GreetingTest.php"), []byte("<?php\n\nuse App\\Support\\Greeting;\n\nit('greets from the owned class', function () {\n    expect(Greeting::text())->toBe('hello');\n});\n"), 0o644)
		b462Write(t, filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n/vendor\n/node_modules\n.env\n"), 0o644)
		b827Commit(t, repo, "laravel base")
		b827Change(t, repo)
	})
	for _, form := range []string{"", "1"} {
		t.Setenv("CLAUDECODE", form)
		code, stdout, stderr := b827Call(t, home, repo, b827Args(b827Status+"::"+b827Case)...)
		if code != 0 || !strings.Contains(stdout, `"red":"RED","red_kind":"assertion","green":"GREEN","proven":true,"runner":"junit"`) {
			t.Fatalf("live pest (CLAUDECODE=%q): exit %d stdout %q stderr %q", form, code, stdout, stderr)
		}
		code, _, stderr = b827Call(t, home, repo, b827Args("tests/Feature/GreetingTest.php::greets from the owned class")...)
		if code != 1 || !strings.HasSuffix(stderr, "(import)\n") {
			t.Fatalf("live new class (CLAUDECODE=%q): exit %d stderr %q", form, code, stderr)
		}
	}
	proven := 0
	for _, line := range b827Rows(t, home, repo) {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["proven"] == true && row["red_kind"] == "assertion" && row["test"] == b827Status+"::"+b827Case {
			proven++
		}
	}
	if proven != 2 {
		t.Fatalf("live rows: %d proven Pest rows, want 2 (one per console form)", proven)
	}
}
