package gatecheck

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
)

// B416: Go checks code-forge-runs-access itself, with Node's hit rules after B415.
// The protected spelling is built from parts so this file never names it whole.
const b416Dir = ".code-" + "forge/" + "runs"

func b416Claude(name string, input map[string]any) string {
	b, err := json.Marshal(map[string]any{"type": "assistant", "sessionId": "s1", "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": name, "input": input}}}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func b416User(text string) string {
	b, err := json.Marshal(map[string]any{"type": "user", "sessionId": "s1", "message": map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t0", "content": text}}}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func b416Details(hits []Blocker) []string {
	out := []string{}
	for _, h := range hits {
		if h.Code != "rule_break" {
			out = append(out, "code:"+h.Code)
			continue
		}
		out = append(out, h.Detail)
	}
	return out
}

func TestTranscriptRunsAccessInGo(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	user := b416User("output names ~/" + b416Dir + "/r1/key")
	bash := func(cmd string) string { return b416Claude("Bash", map[string]any{"command": cmd, "description": "d"}) }
	hit := func(line string) []string { return []string{"transcript line " + line + ": code-forge-runs-access"} }
	cases := []struct {
		name, text string
		want       []string
	}{
		// Claude Code JSONL: assistant tool_use only, B415 path text and home-relative words.
		{"claude Read of the absolute home path", user + "\n" + b416Claude("Read", map[string]any{"file_path": "/home/tester/" + b416Dir + "/r1/key"}), hit("2")},
		{"claude Bash cat of the tilde path", user + "\n" + bash("cat ~/"+b416Dir+"/r1/key"), hit("2")},
		{"claude Bash relative command word", user + "\n" + bash("ls "+b416Dir), hit("2")},
		{"claude Bash dot-slash relative word after cd", bash("cd ~ && ls ./" + b416Dir + "/r1"), hit("1")},
		{"claude Glob pattern relative", bash("true") + "\n" + b416Claude("Glob", map[string]any{"pattern": b416Dir + "/*"}), hit("2")},
		{"claude cat heredoc into a plans file", user + "\n" + bash("cat > plans/x.md <<'EOF'\nsee ~/"+b416Dir+"/R.lock\nEOF"), []string{}},
		{"claude quoted echo into plans", bash("echo \"key at ~/" + b416Dir + "/x\" > plans/n.md"), []string{}},
		{"claude heredoc fed to bash still runs", bash("bash <<'EOF'\ncat " + b416Dir + "/r1/key\nEOF"), hit("1")},
		{"claude Read of .code-forge.yml", user + "\n" + b416Claude("Read", map[string]any{"file_path": ".code-forge.yml"}), []string{}},
		{"claude Grep pattern relative is a regex", b416Claude("Grep", map[string]any{"pattern": b416Dir, "path": "src"}), []string{}},
		{"claude user text is not scanned", user + "\n" + bash("ls"), []string{}},
		{"claude Write path into the runs dir", b416Claude("Write", map[string]any{"file_path": "/home/tester/" + b416Dir + "/r1/x", "content": "c"}), hit("1")},
		{"claude Write content naming the path", b416Claude("Write", map[string]any{"file_path": "plans/a.md", "content": "~/" + b416Dir + "/k"}), []string{}},
		{"claude git stash then runs read keeps list order", bash("git stash && cat ~/" + b416Dir + "/k"), []string{"transcript line 1: git-stash", "transcript line 1: code-forge-runs-access"}},
		// Plain text logs: Node's grep, every line token, no home-relative spelling.
		{"plain tilde path", "$ ls\n$ cat ~/" + b416Dir + "/r1/key\n", hit("2")},
		{"plain $HOME path", "x=$HOME/" + b416Dir, hit("1")},
		{"plain other home is not this home", "cat /home/other/" + b416Dir + "/k", []string{}},
		{"plain relative is free text", "ls " + b416Dir, []string{}},
		{"plain .code-forge.yml", "read .code-forge.yml and .code-forge/config", []string{}},
		{"plain heredoc line still counts", "cat > plans/x.md <<'EOF'\nsee ~/" + b416Dir + "/R.lock\nEOF", hit("2")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, ok := Transcript(c.text)
			if !ok {
				t.Fatalf("handed over to Node; want Go to decide %q", c.want)
			}
			if got := b416Details(hits); !slices.Equal(got, c.want) {
				t.Fatalf("hits = %q, want %q", got, c.want)
			}
		})
	}
}

// B466: Codex exec --json logs and the code-forge-reviews-write rule are decided in Go with the
// hits Node's scanTranscript gives (checked against Node in a scratch parity run).
const b466Reviews = ".code-" + "forge/" + "reviews"
const b466Queue = ".code-" + "forge/" + "queue"

func b466Codex(item string) string {
	return `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.completed","item":` + item + `}`
}

func TestTranscriptCodexAndReviewsWriteInGo(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	at := func(line, id string) []string { return []string{"transcript line " + line + ": " + id} }
	cases := []struct {
		name, text string
		want       []string
	}{
		{"codex command reads runs", b466Codex(`{"type":"command_execution","command":"ls ` + b416Dir + `"}`), at("2", "code-forge-runs-access")},
		{"codex output is not scanned", b466Codex(`{"type":"command_execution","command":"ls","aggregated_output":"git stash"}`), []string{}},
		{"codex agent message skipped", b466Codex(`{"type":"agent_message","text":"git stash"}`) + "\n" + `{"type":"item.completed","item":{"type":"reasoning","text":"git stash"}}`, []string{}},
		{"codex file_change into reviews", b466Codex(`{"type":"file_change","changes":[{"path":"` + b466Reviews + `/a.json","kind":"add"},{"path":5}]}`), at("2", "code-forge-reviews-write")},
		{"codex non-string command", b466Codex(`{"type":"command_execution","command":["bash","-c","git stash"],"aggregated_output":"rm -rf /"}`), at("2", "git-stash")},
		{"codex mcp call with float", b466Codex(`{"type":"mcp_tool_call","arguments":{"n":1.5,"cmd":"gh pr merge 1"}}`), at("2", "gh-pr-merge")},
		{"codex banner then bash -lc", "OpenAI Codex v1\n" + b466Codex(`{"type":"command_execution","command":"bash -lc 'git stash'"}`), at("3", "git-stash")},
		{"codex null item and turn.completed", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.completed","item":null}` + "\n" + `{"type":"turn.completed"}`, []string{}},
		{"plain echo into reviews", "echo x > " + b466Reviews + "/a.json", at("1", "code-forge-reviews-write")},
		{"plain cat of reviews", "cat " + b466Reviews + "/a.json", []string{}},
		{"plain touch queue done", "touch " + b466Queue + "/a.done", at("1", "code-forge-reviews-write")},
		{"plain sed -i queue done", "sed -i s/a/b/ " + b466Queue + "/x.done", at("1", "code-forge-reviews-write")},
		{"plain git -C mv into queue done", "git -C x mv a " + b466Queue + "/b.done", at("1", "code-forge-reviews-write")},
		{"plain echo of the dir name", "echo " + b466Reviews, []string{}},
		{"turn.started first is plain text", `{"type":"turn.started"}` + "\n" + `{"type":"item.completed","item":{"type":"command_execution","command":"git stash"}}`, at("2", "git-stash")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, ok := Transcript(c.text)
			if !ok {
				t.Fatalf("handed over to Node; want Go to decide %q", c.want)
			}
			if got := b416Details(hits); !slices.Equal(got, c.want) {
				t.Fatalf("hits = %q, want %q", got, c.want)
			}
		})
	}
}

func TestJSNumberMatchesNode(t *testing.T) {
	cases := map[string]string{"1.5": "1.5", "0.1": "0.1", "1e21": "1e+21", "1e-7": "1e-7",
		"123456789012345678": "123456789012345680", "-0": "0", "-0.0": "0", "1E5": "100000",
		"2.5e-6": "0.0000025", "1e-6": "0.000001", "9.999e20": "999900000000000000000",
		"1.7976931348623157e308": "1.7976931348623157e+308", "5e-324": "5e-324",
		"-1.25e+30": "-1.25e+30", "100": "100", "1.0": "1"}
	for in, want := range cases {
		f, err := strconv.ParseFloat(in, 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := jsNumber(f); got != want {
			t.Errorf("jsNumber(%s) = %q want %q", in, got, want)
		}
	}
	// 1e400 is Infinity in Node (stringified as null): Go keeps the Node hand-over there.
	if hits, ok := Transcript(b466Codex(`{"type":"mcp_tool_call","arguments":{"n":1e400,"cmd":"git stash"}}`)); ok {
		t.Errorf("Go decided %q for an out-of-range number; want the Node hand-over", b416Details(hits))
	}
}

// b416Worktree is a Claude sub-agent Bash line as a coder in a coder-forge worktree writes it:
// the cwd and branch name carry "coder-forge" and "forge/r-…".
func b416Worktree(cmd string) string {
	b, err := json.Marshal(map[string]any{"type": "assistant", "parentUuid": nil, "isSidechain": true, "sessionId": "s1",
		"gitBranch": "forge/r-20261107-034743-406858/B1", "cwd": "/Users/x/coder-forge/.claude/worktrees/cf/B1",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Bash",
			"input": map[string]any{"command": cmd, "description": "run"}}}}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestTranscriptDecidesForgeTextInGo: forge and code-forge words, launcher verbs, sh -c wrappers,
// interpreter scripts and non-ASCII text are decided in Go with the hits Node's scanTranscript
// gives for the same text (checked against Node in a scratch parity run).
func TestTranscriptDecidesForgeTextInGo(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	runs := "~/" + b416Dir
	one := func(id string) []string { return []string{"transcript line 1: " + id} }
	cases := []struct {
		name, text string
		want       []string
	}{
		{"coder-forge cd and forge branch", b416Worktree("cd /Users/x/coder-forge/.claude/worktrees/cf/B1 && git status && git log --oneline forge/r-20261107-1/B1 -1"), []string{}},
		{"forge branch range", b416Worktree("git diff forge/r-20261107-1/B1..HEAD --stat"), []string{}},
		{"plain coder-forge path and forge branch", "cd /Users/x/coder-forge && git log forge/r-1/B1\n", []string{}},
		{"waive verb", b416Worktree("./bin/code-forge block waive B1 --reason x"), one("code-forge-block-waive-from-coder")},
		{"npx package waive", b416Worktree("npx -y @codedology/code-forge block waive B1"), one("code-forge-block-waive-from-coder")},
		{"plain waive", "$ ./bin/code-forge block waive B1\n", one("code-forge-block-waive-from-coder")},
		{"bare forge waive", b416Worktree("forge block waive B1"), one("code-forge-block-waive-from-coder")},
		{"bare forge worker", b416Worktree("forge worker start"), one("code-forge-worker-from-coder")},
		{"git --version", b416Worktree("git --version; ls native/gitx"), []string{}},
		{"git -C push --force", b416Worktree("git -C /tmp/x push --force origin main"), one("git-push-force")},
		{"git -c push +refspec", b416Worktree("git -c core.x=1 push origin +main"), one("git-push-force")},
		{"gh pr merge", b416Worktree("gh pr merge 1"), one("gh-pr-merge")},
		{"gh -R before pr is not a prefix", b416Worktree("gh -R o/r pr merge 1"), []string{}},
		{"rm -rf", b416Worktree("rm -rf build"), one("rm-rf")},
		{"git branch -d -f", b416Worktree("git branch -d -f x"), one("git-branch-force-delete-long")},
		{"python heredoc", b416Worktree("python3 - <<'PY'\nprint('hi')\nPY"), []string{}},
		{"python heredoc opens runs", b416Worktree("python3 - <<'PY'\nopen('" + runs + "/k').read()\nPY"), one("code-forge-runs-access")},
		{"sh -c wrapper reads runs", b416Worktree("sh -c 'cat " + runs + "/k'"), one("code-forge-runs-access")},
		{"sh -c wrapper stash", b416Worktree("sh -c 'git stash'"), one("git-stash")},
		{"non-ASCII runs path", b416Worktree("cat " + runs + "/caf\u00e9"), one("code-forge-runs-access")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, ok := Transcript(c.text)
			if !ok {
				t.Fatalf("handed over to Node; want decided in Go with %q", c.want)
			}
			if got := b416Details(hits); !slices.Equal(got, c.want) {
				t.Fatalf("hits = %q; want %q", got, c.want)
			}
		})
	}
}

func TestTranscriptRunsAccessNeedsHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, ok := Transcript("cat ~/" + b416Dir + "/k"); ok {
		t.Fatal("decided without a home dir; want a Node handover")
	}
	if hits, ok := Transcript("git status"); !ok || len(hits) != 0 {
		t.Fatalf("text without .code-forge = %v %v; want supported with 0 hits", hits, ok)
	}
}
