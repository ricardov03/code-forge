package gatecheck

import (
	"encoding/json"
	"slices"
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

func TestTranscriptRunsAccessHandsOverOtherCodeForgeRules(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	bash := func(cmd string) string { return b416Claude("Bash", map[string]any{"command": cmd}) }
	cases := []struct{ name, text string }{
		{"reviews dir", "cat x > .code-forge/reviews/a.json"},
		{"queue done", "touch .code-forge/queue/a.done"},
		{"forge outside .code-forge", "npx code-forge worker"},
		{"bare forge word", "forge block waive B1 --run r"},
		{"codex log", `{"type":"thread.started","thread_id":"t"}` + "\n" + `{"type":"item.completed","item":{"type":"command_execution","command":"ls ` + b416Dir + `"}}`},
		{"claude sh -c wrapper", bash("sh -c 'cat ~/" + b416Dir + "/k'")},
		{"claude interpreter heredoc", bash("python3 - <<'EOF'\nprint(1)\nEOF\nls " + b416Dir)},
		{"claude non-ASCII path", b416Claude("Read", map[string]any{"file_path": "/home/tester/" + b416Dir + "/é"})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, ok := Transcript(c.text)
			if ok {
				t.Fatalf("Go decided %q; want a Node handover", b416Details(hits))
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
