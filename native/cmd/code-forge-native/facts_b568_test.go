package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestB568GlobMatchesNode: fcMatchesGlob against path.matchesGlob results
// Node 24 printed on macOS (nocase) for each [path, pattern] pair, both
// normalized as globRule does (testdata/b568/globs.txt). Patterns Go does not
// model hand over before the run and are skipped here.
func TestB568GlobMatchesNode(t *testing.T) {
	file, err := os.Open("testdata/b568/globs.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	checked := 0
	for scanner.Scan() {
		var row []any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		p, g, want := row[0].(string), row[1].(string), row[2].(bool)
		pattern := fcRelPosix(g)
		if fcGlobUnmodelled(pattern) {
			continue
		}
		checked++
		if got := fcMatchesGlob(fcRelPosix(p), pattern, true); got != want {
			t.Errorf("matchesGlob(%q, %q) = %v, Node %v", p, g, got, want)
		}
	}
	if checked < 150 {
		t.Fatalf("only %d modelled cases", checked)
	}
}

// TestB568BriefDecodeMatchesNode: fcDecodeUTF8 against Buffer#toString('utf8')
// for the byte strings in testdata/b568/utf8.txt (hex in, hex out, by Node 24).
func TestB568BriefDecodeMatchesNode(t *testing.T) {
	data, err := os.ReadFile("testdata/b568/utf8.txt")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.Split(line, " ")
		in, _ := hex.DecodeString(parts[0])
		if got := hex.EncodeToString([]byte(fcDecodeUTF8(in))); got != parts[1] {
			t.Errorf("decode %s = %s, Node %s", parts[0], got, parts[1])
		}
		n++
	}
	if n < 300 {
		t.Fatalf("only %d cases", n)
	}
}

// fcB568Masks: on top of fcMasks, Node's JS stack frames (Go prints the
// stack's first line only) and factsTmpRoot's hash of the run root's path.
var fcB568Masks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?m)^    at .*\n`), ""},
	{regexp.MustCompile(`(\\n    at [^"\\]*)+`), ""},
	{regexp.MustCompile(`facts-[0-9a-f]{16}`), "facts-H"},
}

func fcB568Mask(text string) string {
	for _, m := range fcB568Masks {
		text = m.re.ReplaceAllString(text, m.with)
	}
	return fcMask(text)
}

func fcWrite(t *testing.T, file, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func fcAppend(t *testing.T, file, text string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func fcGit(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", argv, err, out)
	}
}

// B568: each case handed over before (facts-glob-claim, facts-brief-encoding,
// facts-root-in-git, facts-outside-source, facts-session for Node's crashes)
// and now runs in Go. testdata/b568/<case>.txt is Node's report from the
// scratchpad golden.sh (as testdata/b556) with the same setup.
func TestB568FactsServedLikeNode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	globAnswer, err := os.ReadFile(filepath.Join("testdata", "b568", "glob.answer.json"))
	if err != nil {
		t.Fatal(err)
	}
	const globBrief = "# Brief\n\nSee `src/*.mjs` and docs/*.md, src/*/deep.mjs and lib/*.js.\nAlso src/[ab]*.mjs and src/a?p.mjs, plus src/it's*.mjs.\n"
	const encBrief = "# Brief\n\nThe file `src/\xffapp.mjs` and src/app.mjs; also docs/r\xc3\xa9adme.md and docs/\xe2\x82readme.md.\n"
	cases := []struct {
		name, brief, answer string
		setup               func(t *testing.T, base, repo string) []string
	}{
		{"glob", globBrief, string(globAnswer), nil},
		{"enc", encBrief, "", nil},
		{"noL0", fcBrief, "", func(t *testing.T, base, repo string) []string {
			fcWrite(t, filepath.Join(repo, ".code-forge.yml"), "version: 1\nproject:\n  name: unit\n  slug: unit\nlevels:\n  L1:\n    provider: anthropic\n    model: m-x\n", 0o644)
			fcGit(t, repo, "-c", "user.email=a@b", "-c", "user.name=n", "commit", "-qam", "noL0")
			return nil
		}},
		{"gitroot_run", fcBrief, "", func(t *testing.T, base, repo string) []string {
			fcAppend(t, filepath.Join(repo, ".code-forge.yml"), "tmp:\n  root: "+filepath.Join(repo, "tmpcf")+"\n")
			return []string{"--run", "r9"}
		}},
		{"gitroot_lazy", fcBrief, "", func(t *testing.T, base, repo string) []string {
			t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(repo, "tmpcf"))
			return nil
		}},
		{"outside", fcBrief, "", func(t *testing.T, base, repo string) []string {
			fcWrite(t, filepath.Join(base, "ext", "lib", "util.mjs"), "export const u = 1;\n", 0o644)
			fcWrite(t, filepath.Join(base, "docsout", "notes.md"), "outside notes\n", 0o644)
			fcWrite(t, filepath.Join(repo, "brief.md"), "# Brief\n\nThe file `src/app.mjs` and lib/util.mjs; also "+filepath.Join(base, "docsout", "notes.md")+" and "+
				filepath.Join(base, "ext", "lib", "util.mjs")+" and "+filepath.Join(base, "home", "none.md")+".\n", 0o644)
			return []string{"--sources", "../ext", "--sources", "../docsout/notes.md"}
		}},
		{"outsidegit", fcBrief, "", func(t *testing.T, base, repo string) []string {
			other := filepath.Join(base, "other")
			fcWrite(t, filepath.Join(other, "lib", "a.mjs"), "export function fooBar() {}\n", 0o644)
			fcWrite(t, filepath.Join(base, "x", "lib", "b.mjs"), "const b = \"--alpha\";\n", 0o644)
			fcGit(t, other, "init", "-q")
			fcGit(t, other, "add", "-A")
			fcGit(t, other, "-c", "user.email=a@b", "-c", "user.name=n", "commit", "-qm", "init")
			fcWrite(t, filepath.Join(other, "lib", "u.mjs"), "untracked\n", 0o644)
			return []string{"--sources", "../other/lib", "--sources", "../x/lib"}
		}},
		{"tmprel", fcBrief, "", func(t *testing.T, base, repo string) []string {
			fcAppend(t, filepath.Join(repo, ".code-forge.yml"), "tmp:\n  root: rel/dir\n")
			return []string{"--run", "r9"}
		}},
		{"untrusted", fcBrief, "", func(t *testing.T, base, repo string) []string {
			open := filepath.Join(base, "tmp", "open")
			if err := os.MkdirAll(open, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(open, 0o777); err != nil {
				t.Fatal(err)
			}
			fcAppend(t, filepath.Join(repo, ".code-forge.yml"), "tmp:\n  root: "+open+"\n")
			return []string{"--run", "r9"}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden, err := filepath.Abs(filepath.Join("testdata", "b568", c.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			base := fcWorkspace(t, "anthropic", c.brief)
			if c.answer != "" {
				t.Setenv("FAKE_ANSWER", c.answer)
			}
			args := []string{"facts", "--brief", "brief.md"}
			if c.setup != nil {
				args = append(args, c.setup(t, base, filepath.Join(base, "repo"))...)
			}
			code, stdout, stderr := runCaptured(t, args...)
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(filepath.Join(base, "home", ".code-forge", "native", "handovers.jsonl")); len(data) != 0 {
				t.Fatalf("handed over: %s", data)
			}
			got, node := fcB568Mask(fcReport(base, code, stdout, stderr)), fcB568Mask(string(want))
			if c.name == "outside" {
				// the brief names absolute paths in the workspace: its hash differs per workspace
				sha := regexp.MustCompile("brief_sha256: `[0-9a-f]{64}`")
				got, node = sha.ReplaceAllString(got, "brief_sha256: `S`"), sha.ReplaceAllString(node, "brief_sha256: `S`")
			}
			if got != node {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, node)
			}
		})
	}
}

// B568: the run-pinned gate's Date.parse cases Go may read differently from
// Node (an autopilot grant's until, a counted ledger row's ts) hand over
// before any write; before B568 they handed over in the middle of the run.
func TestB568FactsGateDateParseHandsOverFirst(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	grant := func(until string) string {
		return `{"run_id":"r9","status":"active","project":"unit","autopilot":{"grant_id":"g1","status":"active","until":"` + until +
			`","started_at":"2026-01-01T00:00:00.000Z","caps":{"review":5}}}`
	}
	cases := []struct {
		name, record, ledger string
	}{
		{"an autopilot grant whose until Date.parse may read differently", grant("Nov 12 2099"), ""},
		{"a counted ledger row whose ts Date.parse may read differently", grant("2099-01-01T00:00:00.000Z"),
			`{"event":"session","role":"facts","run":"r9","usd":0.1,"ts":"Nov 12 2026"}` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := fcWorkspace(t, "anthropic", fcBrief)
			home := filepath.Join(base, "home")
			fcWrite(t, filepath.Join(home, ".code-forge", "runs", "r9.json"), c.record+"\n", 0o600)
			ledgerFile := filepath.Join(home, ".code-forge", "ledger", "unit.jsonl")
			if c.ledger != "" {
				fcWrite(t, ledgerFile, c.ledger, 0o600)
			}
			if code, _ := run([]string{"facts", "--brief", "brief.md", "--run", "r9"}); code != 127 {
				t.Fatalf("code=%d; want the Node hand-over", code)
			}
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row) != nil || row.Verb != "facts" || row.Reason != "facts-session" {
				t.Fatalf("handover row %q; want reason facts-session", data)
			}
			for _, p := range []string{".code-forge/logs/breadcrumbs.json", ".code-forge/logs/errors.jsonl"} {
				if _, err := os.Stat(filepath.Join(home, p)); err == nil {
					t.Fatalf("Go wrote %s before the hand-over", p)
				}
			}
			if got, _ := os.ReadFile(ledgerFile); string(got) != c.ledger {
				t.Fatalf("ledger changed before the hand-over: %q", got)
			}
			for _, p := range []string{"tmp", "log", "repo/plans"} {
				if left := fcTree(t, filepath.Join(base, p)); len(left) != 0 {
					t.Fatalf("Go wrote under %s before the hand-over: %v", p, left)
				}
			}
		})
	}
}
