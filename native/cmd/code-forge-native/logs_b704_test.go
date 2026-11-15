package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestB704LogsLoneSurrogateInGo: logs, logs --json, logs summary and logs
// summary --json read an error log whose entries hold lone surrogate escapes
// as JSON.parse does and print Node's bytes: JSON keeps the \udXXX escape,
// text has U+FFFD in its place (captured from CODE_FORGE_NATIVE=0 code-forge
// logs on the same file), instead of handing over.
func TestB704LogsLoneSurrogateInGo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b704-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	first := `{"ts":"` + ts + `","verb":"block","sub":"close","exit":1,"kind":"usage","fp":"0123456789ab","message":"\\n  bad \ud83d end\\nmore"}`
	second := `{"ts":"` + ts + `","verb":"gates","kind":"warning","warning":"w\udc00","fp":"ba9876543210","message":"m"}`
	dir := filepath.Join(home, ".code-forge", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "errors.jsonl"), []byte(first+"\n"+second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"logs"}, ts + "  [warning] gates  w�  ba9876543210  m\n" + ts + "  block close  1  usage  0123456789ab  \\n  bad � end\\nmore\n"},
		{[]string{"logs", "--json"}, `{"errors":[` + second + "," + first + "]}\n"},
		{[]string{"logs", "summary"}, "errors in the last 30 days:\n0123456789ab  block close  usage  seen 1 time\nwarnings in the last 30 days:\nba9876543210  gates  w�  seen 1 time\n"},
		{[]string{"logs", "summary", "--json"}, `{"days":30,"counts":[{"fp":"0123456789ab","verb":"block","sub":"close","kind":"usage","count":1}],"warnings":[{"fp":"ba9876543210","verb":"gates","sub":null,"warning":"w\udc00","count":1}]}` + "\n"},
	}
	for _, c := range cases {
		code, out, errText := runCaptured(t, c.args...)
		if code != 0 || out != c.want || errText != "" {
			t.Errorf("%v: code=%d stdout=%q stderr=%q, want %q", c.args, code, out, errText, c.want)
		}
	}
}
