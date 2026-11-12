package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

// The fixture and the expected texts under testdata/autopilot_binnacle are
// what Node printed for the same run records and ledger (CODE_FORGE_NATIVE=0,
// a temp HOME), with the home and workspace paths as $HOME$ and $WS$ and the
// byline date set to the fixed test clock's day.

func bnTestdata(t *testing.T, f runFixture, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "autopilot_binnacle", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer("$HOME$", f.home, "$WS$", f.workspace).Replace(string(data))
}

// newBinnacleFixture copies the fixture runs and ledger into the temp HOME;
// the byline owner comes from a fixed seam instead of git.
func newBinnacleFixture(t *testing.T) runFixture {
	t.Helper()
	f := newAutopilotFixture(t)
	ownerBefore := bnGitOwner
	bnGitOwner = func(workspace any) string {
		if workspace != f.workspace {
			t.Fatalf("git owner asked for %v", workspace)
		}
		return "Test Owner"
	}
	t.Cleanup(func() { bnGitOwner = ownerBefore })
	runs := filepath.Join(f.home, ".code-forge", "runs")
	for _, id := range []string{"r-a", "r-b", "r-c"} {
		if err := os.WriteFile(filepath.Join(runs, id+".json"), []byte(bnTestdata(t, f, id+".json")), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runs, id+".key"), f.key, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLedger(t, f, strings.TrimSuffix(bnTestdata(t, f, "unit.jsonl"), "\n"))
	return f
}

// bnSignedRows returns the rows a command signed, each verified with the run
// key; the fixture ledger's own rows carry no mac.
func bnSignedRows(t *testing.T, f runFixture) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if _, signed := row["mac"]; !signed {
			continue
		}
		if verified, err := sign.VerifyRow(row, f.key); err != nil || !verified.OK {
			t.Fatalf("row signature: %s %#v %v", line, verified, err)
		}
		out = append(out, row)
	}
	return out
}

// B478: binnacle and log run in Go, byte-equal to Node's stdout and files.
func TestAutopilotBinnacleInGo(t *testing.T) {
	f := newBinnacleFixture(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"binnacle", "--run", "r-a"}, "r-a.binnacle.md"},
		{[]string{"binnacle", "--run", "r-a", "--json"}, "r-a.binnacle.json"},
		{[]string{"log", "--run", "r-a"}, "r-a.log.md"},
		{[]string{"log", "--run", "r-a", "--json"}, "r-a.log.json"},
		{[]string{"binnacle", "--run", "r-b"}, "r-b.binnacle.md"},
	}
	for _, tc := range cases {
		code, out, errText := autopilotCall(t, tc.args...)
		if want := bnTestdata(t, f, tc.want); code != 0 || out != want || errText != "" {
			t.Fatalf("%v = %d %q\n%s\nwant\n%s", tc.args, code, errText, out, want)
		}
	}

	// --markdown writes both files (0600) and prints the one asked for
	dir := filepath.Join(f.home, ".code-forge", "runs", "r-a")
	code, out, errText := autopilotCall(t, "log", "--run", "r-a", "--markdown")
	if code != 0 || out != filepath.Join(dir, "autopilot-log.md")+"\n" || errText != "" {
		t.Fatalf("log --markdown = %d %q %q", code, out, errText)
	}
	code, out, _ = autopilotCall(t, "binnacle", "--run", "r-a", "--markdown")
	if code != 0 || out != filepath.Join(dir, "autopilot-binnacle.md")+"\n" {
		t.Fatalf("binnacle --markdown = %d %q", code, out)
	}
	for file, want := range map[string]string{"autopilot-binnacle.md": "r-a.binnacle.md", "autopilot-log.md": "r-a.log.md"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || string(data) != bnTestdata(t, f, want) {
			t.Fatalf("%s = %v\n%s", file, err, data)
		}
		if info, _ := os.Stat(filepath.Join(dir, file)); info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode %v", file, info.Mode())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("run dir holds %d entries (a temp file left?)", len(entries))
	}

	refusals := []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"binnacle", "--run", "r-a", "--json", "--markdown"}, 2, "autopilot binnacle: give --json or --markdown, not both\n"},
		{[]string{"log", "--run", "r-zz", "--json", "--markdown"}, 2, "autopilot log: give --json or --markdown, not both\n"},
		{[]string{"log", "--run", "r-zz"}, 1, "autopilot log: no run record for r-zz\n"},
	}
	for _, tc := range refusals {
		code, out, errText := autopilotCall(t, tc.args...)
		if code != tc.code || out != "" || errText != tc.err {
			t.Fatalf("%v = %d %q %q", tc.args, code, out, errText)
		}
	}

	// a file where the run dir goes: mkdirSync's EEXIST
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-b"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errText = autopilotCall(t, "binnacle", "--run", "r-b", "--markdown")
	if want := "autopilot binnacle: EEXIST: file already exists, mkdir '" + filepath.Join(f.home, ".code-forge", "runs", "r-b") + "'\n"; code != 1 || out != "" || errText != want {
		t.Fatalf("file in the way = %d %q %q", code, out, errText)
	}
	if rows := bnSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a read wrote rows: %#v", rows)
	}
}

// B478: status and stop of a grant with no link rewrite the two files in Go
// and print their paths (Node's recordFiles / recordText).
func TestAutopilotStatusStopFilesInGo(t *testing.T) {
	f := newBinnacleFixture(t)
	dir := filepath.Join(f.home, ".code-forge", "runs", "r-c")
	binnacle, log := filepath.Join(dir, "autopilot-binnacle.md"), filepath.Join(dir, "autopilot-log.md")
	record := "  link      none\n  binnacle  " + binnacle + "\n  full log  " + log + "\n"

	code, out, errText := autopilotCall(t, "status", "--run", "r-c")
	if code != 0 || !strings.HasPrefix(out, "autopilot run r-c: active · 50h 30m left (until 2026-11-13T12:30:00.000Z)\n") || !strings.HasSuffix(out, "paused none\n"+record) || errText != "" {
		t.Fatalf("status = %d %q %q", code, out, errText)
	}
	data, _ := os.ReadFile(binnacle)
	if !strings.HasPrefix(string(data), "# Autopilot run r-c\n\n2026-11-11 · Test Owner\n") || !strings.Contains(string(data), "| Log link | none |\n") {
		t.Fatalf("binnacle file:\n%s", data)
	}
	if data, _ := os.ReadFile(log); string(data) != "# Autopilot full log r-c\n\nNewest first.\n\nnone yet\n" {
		t.Fatalf("log file:\n%s", data)
	}
	code, out, _ = autopilotCall(t, "status", "--run", "r-c", "--json")
	var status map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &status) != nil {
		t.Fatalf("status --json = %d %q", code, out)
	}
	if files, _ := status["files"].(map[string]any); files["binnacle"] != binnacle || files["log"] != log || !strings.Contains(out, "\"link\": null,\n  \"files\": {\n    \"binnacle\": ") {
		t.Fatalf("status files: %s", out)
	}

	code, out, errText = autopilotCall(t, "stop", "--run", "r-c")
	if code != 0 || out != "autopilot run r-c: grant ap-0000abcd stopped\n"+record || errText != "" {
		t.Fatalf("stop = %d %q %q", code, out, errText)
	}
	rows := bnSignedRows(t, f)
	if last := rows[len(rows)-1]; last["event"] != "autopilot.stop" || last["run"] != "r-c" || last["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("stop row: %#v", last)
	}
	// the files were rewritten after the stop row: the timeline shows it
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "| 2026-11-11T10:00:00.000Z | Grant stopped |  |  | grant ap-0000abcd |  |\n") {
		t.Fatalf("log after stop:\n%s", data)
	}

	// a write that fails is told on stderr and the status still prints
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errText = autopilotCall(t, "status", "--run", "r-c")
	if code != 0 || !strings.HasSuffix(out, "  link      none\n") || errText != "autopilot: the binnacle files could not be written (EEXIST: file already exists, mkdir '"+dir+"')\n" {
		t.Fatalf("failed write = %d %q %q", code, out, errText)
	}
}

// B478: `binnacle --link` stores the link in Go: Node's checks, its signed
// autopilot.link row and the grant's link.
func TestAutopilotBinnacleLinkInGo(t *testing.T) {
	f := newBinnacleFixture(t)
	f.record(t, "r-stop", "active", grantJSON("stopped", "2026-11-11T12:30:00.000Z", `"2026-11-11T09:10:00.000Z"`, "null"))
	f.record(t, "r-old", "active", grantJSON("active", "2026-11-11T09:30:00.000Z", "null", "null"))
	f.record(t, "r-end", "ended", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))

	code, out, errText := autopilotCall(t, "binnacle", "--run", "r-c", "--link", "https://docs.example.com?x=(1)&y#top")
	if code != 0 || !strings.Contains(out, "| Log link | https://docs.example.com/?x=(1)&y#top |\n") || !strings.Contains(out, "- 2026-11-11T10:00:00.000Z — Log link stored — grant ap-0000abcd\n") || errText != "" {
		t.Fatalf("--link = %d %q\n%s", code, errText, out)
	}
	rows := bnSignedRows(t, f)
	if len(rows) != 1 || rows[0]["event"] != "autopilot.link" || rows[0]["link"] != "https://docs.example.com/?x=(1)&y#top" || rows[0]["grant_id"] != "ap-0000abcd" || rows[0]["ts"] != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("link rows: %#v", rows)
	}
	if grant := f.read(t, "r-c")["autopilot"].(map[string]any); grant["link"] != "https://docs.example.com/?x=(1)&y#top" {
		t.Fatalf("grant link: %#v", grant)
	}
	// with a link the status names it and writes no files
	code, out, _ = autopilotCall(t, "status", "--run", "r-c")
	if code != 0 || !strings.HasSuffix(out, "  link      https://docs.example.com/?x=(1)&y#top\n") {
		t.Fatalf("status with link = %d %q", code, out)
	}

	refusals := []struct {
		run, link string
		code      int
		err       string
	}{
		{"r-c", "https://example.com/sk-abcdefghijklmnop", 2, "autopilot binnacle: --link looks like it carries a secret; give a plain URL\n"},
		{"r-c", strings.Repeat("a", 2049), 2, "autopilot binnacle: --link must be an https URL of at most 2048 characters\n"},
		{"r-b", "https://example.com", 1, "autopilot binnacle: run r-b has no autopilot grant\n"},
		{"r-stop", "https://example.com", 1, "autopilot binnacle: run r-stop's autopilot grant is stopped; the link goes into an active grant\n"},
		{"r-end", "https://example.com", 1, "autopilot binnacle: run r-end has ended\n"},
		{"r-old", "https://example.com", 1, "autopilot binnacle: run r-old's autopilot grant already expired at 2026-11-11T09:30:00.000Z\n"},
	}
	for _, tc := range refusals {
		code, out, errText := autopilotCall(t, "binnacle", "--run", tc.run, "--link", tc.link)
		if code != tc.code || out != "" || errText != tc.err {
			t.Fatalf("--link %s %.40s = %d %q %q", tc.run, tc.link, code, out, errText)
		}
	}
	events := []string{}
	for _, row := range bnSignedRows(t, f) {
		events = append(events, row["run"].(string)+" "+row["event"].(string))
	}
	if !slices.Equal(events, []string{"r-c autopilot.link", "r-old autopilot.expire"}) {
		t.Fatalf("rows after refusals: %v", events)
	}
}

// B478: what Go reads differently from Node keeps the hand-over, before any
// write: a timestamp Date.parse may read, an id outside printable ASCII, a
// link outside the plain https subset.
func TestAutopilotBinnacleHandsOver(t *testing.T) {
	f := newBinnacleFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	f.record(t, "r-ts", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", "null"))
	f.record(t, "r-id", "active", `,"blocks":{"Bé":{},"B1":{}}`)
	ledgerFile := filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")
	data, _ := os.ReadFile(ledgerFile)
	extra := `{"run":"r-ts","event":"autopilot.custom","ts":"Nov 11 2026"}` + "\n"
	if err := os.WriteFile(ledgerFile, append(data, extra...), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args   []string
		reason string
	}{
		{[]string{"binnacle", "--run", "r-ts"}, "autopilot-binnacle"},
		{[]string{"log", "--run", "r-ts", "--json"}, "autopilot-binnacle"},
		{[]string{"status", "--run", "r-ts"}, "autopilot-files"},
		{[]string{"stop", "--run", "r-ts"}, "autopilot-files"},
		{[]string{"binnacle", "--run", "r-id"}, "autopilot-binnacle"},
		{[]string{"binnacle", "--run", "r-c", "--link", "https://Example.com/"}, "autopilot-binnacle"},
		{[]string{"binnacle", "--run", "r-c", "--link", "http://example.com/"}, "autopilot-binnacle"},
		{[]string{"binnacle", "--run", "r-c", "--link", "https://example.com/a/../b"}, "autopilot-binnacle"},
	}
	for _, tc := range cases {
		if code, out, _ := autopilotCall(t, tc.args...); code != 127 || out != "" {
			t.Fatalf("%v: hand-over exit = %d stdout %q, want 127", tc.args, code, out)
		}
	}
	logData, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal([]byte(line), &row); err != nil || row["reason"] != cases[i].reason {
			t.Fatalf("row %d = %s, want %s", i, line, cases[i].reason)
		}
	}
	if rows := bnSignedRows(t, f); len(rows) != 0 {
		t.Fatalf("a hand-over wrote rows: %#v", rows)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-ts")); !os.IsNotExist(err) {
		t.Fatalf("a hand-over wrote the files: %v", err)
	}
}

// B478: the id order is localeCompare's (ICU root) for printable ASCII; the
// expected order is what Node printed for the same list.
func TestAutopilotBinnacleCollate(t *testing.T) {
	ids := []string{"a", "A", "aB", "ab", "Ab", "a-b", "a_b", "ab1", "ab10", "ab2", "B1", "b-x", "B10", "B2", "", "a b", "a.b"}
	slices.SortStableFunc(ids, func(a, b string) int { c, _ := bnCollate(a, b); return c })
	want := []string{"", "a", "A", "a b", "a_b", "a-b", "a.b", "ab", "aB", "Ab", "ab1", "ab10", "ab2", "b-x", "B1", "B10", "B2"}
	if !slices.Equal(ids, want) {
		t.Fatalf("order = %q", ids)
	}
	if _, ok := bnCollate("é", "e"); ok {
		t.Fatal("a non-ASCII id must not be ordered in Go")
	}
}
