package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b690aUsageGolden is one Node-recorded case in testdata/b690a/node_usage.json
// (bin/code-forge.mjs with an empty HOME and the error log on). <TOKEN> stands
// for a fake key built in the test and <FF> for the byte 0xff in argv. The
// error row drops ts, node, version, platform and arch.
type b690aUsageGolden struct {
	Args        []string       `json:"args"`
	Exit        int            `json:"exit"`
	Stdout      string         `json:"stdout"`
	Stderr      string         `json:"stderr"`
	Breadcrumbs *string        `json:"breadcrumbs"`
	ErrorRow    map[string]any `json:"error_row"`
	HomeFiles   []string       `json:"home_files"`
}

// b690aUsageRun runs the guarded entry in process from an empty folder with
// an empty HOME and the error log on, as the goldens were recorded.
func b690aUsageRun(t *testing.T, args []string) (code int, stdout, stderr, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saveOut, saveErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { data, _ := io.ReadAll(outR); outC <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errC <- string(data) }()
	code, runErr := runGuarded(args)
	os.Stdout, os.Stderr = saveOut, saveErr
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outC, <-errC
	if runErr != nil {
		t.Fatalf("runGuarded: %v", runErr)
	}
	return code, stdout, stderr, home
}

// Where Node answers with a usage error (exit 2), Go prints the same text and
// writes the same breadcrumbs and errors.jsonl row; it no longer refuses
// (B690a fix1: unknown block subcommand, review-file unknown flag with a
// value, logs and init argv that is not UTF-8, block close and the human
// block verbs with secret-like text in a usage error).
func TestB690aUsageErrorsMatchNode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "b690a", "node_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens []b690aUsageGolden
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens")
	}
	token := b690aFakeToken()
	for _, g := range goldens {
		name := strings.Join(g.Args, " ")
		t.Run(name, func(t *testing.T) {
			args := make([]string, len(g.Args))
			for i, a := range g.Args {
				args[i] = strings.ReplaceAll(strings.ReplaceAll(a, "<TOKEN>", token), "<FF>", "\xff")
			}
			code, stdout, stderr, home := b690aUsageRun(t, args)
			stdout = strings.ReplaceAll(stdout, token, "<TOKEN>")
			stderr = strings.ReplaceAll(stderr, token, "<TOKEN>")
			want := g.Stdout
			if want == "" && g.Exit != 0 && jsonRefusalWanted(args) {
				// B872: a --json refusal also prints one {"ok":false,...} line.
				want = jsonRefusalLine(args, g.Exit, g.Stderr)
			}
			if code != g.Exit || stdout != want || stderr != g.Stderr {
				t.Fatalf("exit %d stdout %q stderr %q\nwant exit %d stdout %q stderr %q", code, stdout, stderr, g.Exit, want, g.Stderr)
			}
			if files := b690aFiles(t, home); strings.Join(files, ",") != strings.Join(g.HomeFiles, ",") {
				t.Fatalf("HOME files %q; want %q", files, g.HomeFiles)
			}
			if g.Breadcrumbs != nil {
				crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
				if string(crumbs) != *g.Breadcrumbs {
					t.Fatalf("breadcrumbs %q; want %q", crumbs, *g.Breadcrumbs)
				}
			}
			if g.ErrorRow != nil {
				raw, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
				var row map[string]any
				if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), token, "<TOKEN>")), &row); err != nil {
					t.Fatalf("errors.jsonl %q: %v", raw, err)
				}
				for _, key := range []string{"ts", "node", "version", "platform", "arch"} {
					delete(row, key)
				}
				got, _ := json.Marshal(row)
				want, _ := json.Marshal(g.ErrorRow)
				if string(got) != string(want) {
					t.Fatalf("error row %s\nwant %s", got, want)
				}
			}
		})
	}
}
