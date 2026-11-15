package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B732 fixtures: testdata/b732/<case> holds what Node's runAutopilot wrote
// for `autopilot approve --value <JSON with a \udXXX escape>` on the B536
// run (approve.*) and then for `autopilot status` at the approval's until,
// the restore (restore.*): .code-forge.yml, the run record (@WORKSPACE@ for
// the workspace), the ledger and {code, stdout, stderr, prompt}. Recorded
// with testdata/b732/record.mjs.txt (Node 24, yaml 2.9.1); the random
// approval id is replaced by apr-0000aaa1.

var b732Dir, _ = filepath.Abs(filepath.Join("testdata", "b732"))

func b732Data(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b732Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type b732Out struct {
	Code   int     `json:"code"`
	Stdout string  `json:"stdout"`
	Stderr string  `json:"stderr"`
	Prompt *string `json:"prompt"`
}

func b732Result(t *testing.T, name string) b732Out {
	t.Helper()
	var out b732Out
	if err := json.Unmarshal([]byte(b732Data(t, name)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// b732Check compares the config, the record and the ledger (ts and mac
// left out, as B536) with Node's after step (approve or restore).
func b732Check(t *testing.T, f b536Fixture, dir, step string) {
	t.Helper()
	if got, want := f.config(t), b732Data(t, dir+"/"+step+".yml"); got != want {
		t.Fatalf("%s .code-forge.yml:\n%s\nwant (Node):\n%s", step, got, want)
	}
	if got, want := f.record(t), strings.ReplaceAll(b732Data(t, dir+"/"+step+".json"), "@WORKSPACE@", f.ws); got != want {
		t.Fatalf("%s run record:\n%s\nwant (Node):\n%s", step, got, want)
	}
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "ws.jsonl"))
	if got, want := b536Ledger(string(data)), b536Ledger(b732Data(t, dir+"/"+step+".ledger.txt")); got != want {
		t.Fatalf("%s ledger:\n%s\nwant (Node):\n%s", step, got, want)
	}
}

// B732: approve --value with a lone surrogate (high, low, alone, next to
// text, in a list, replacing a present value) writes .code-forge.yml,
// reloads, signs the autopilot.approve row and stores the approval in Go,
// every byte as Node; the restore at until puts the old value back in Go.
// Nothing hands over.
func TestB732ApproveLoneSurrogateMatchesNode(t *testing.T) {
	cases := []struct{ dir, key, value, start string }{
		{"high", "project.name", `"x\ud800y"`, ""},
		{"low", "project.name", `"\udc00"`, ""},
		{"alone", "project.name", `"\ud800"`, ""},
		{"present", "project.name", `"a\uDFFFb"`, "start-present.yml"},
		{"list", "review.exclude", `["\udbff","ok"]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			f := b536Setup(t)
			if tc.start != "" {
				if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(b732Data(t, tc.start)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			prompt := ""
			autopilotConfirm = func(message string) (humanask.Result, error) { prompt = message; return humanask.Confirmed, nil }
			apdNewApprovalID = func() (string, error) { return "apr-0000aaa1", nil }
			want := b732Result(t, tc.dir+"/approve.out.txt")
			code, out, errText := b536Approve(t, tc.key, tc.value, "2026-11-11T11:00:00.000Z")
			if code != want.Code || out != want.Stdout || errText != want.Stderr || want.Prompt == nil || prompt != *want.Prompt {
				t.Fatalf("approve = %d %q %q prompt %q\nwant (Node) %d %q %q prompt %q", code, out, errText, prompt, want.Code, want.Stdout, want.Stderr, *want.Prompt)
			}
			b732Check(t, f, tc.dir, "approve")
			f.at("2026-11-11T11:00:00.000Z")
			want = b732Result(t, tc.dir+"/restore.out.txt")
			code, _, errText = autopilotCall(t, "status", "--run", "r-fix")
			if code != want.Code || errText != want.Stderr {
				t.Fatalf("restore = %d %q, want (Node) %d %q", code, errText, want.Code, want.Stderr)
			}
			b732Check(t, f, tc.dir, "restore")
			f.noHandover(t)
		})
	}
}

// B732: a surrogate pair escape is one character (😀) in JavaScript; Node
// writes it plain (testdata/b732/pair). The confirm message then holds a
// character outside printable ASCII, whose clack wrapping stays with Node
// (B490): the hand-over autopilot-approve-value before the prompt, nothing
// written.
func TestB732ApprovePairEscapeKeepsHandOver(t *testing.T) {
	f := b536Setup(t)
	before := f.state(t)
	asked := false
	autopilotConfirm = func(string) (humanask.Result, error) { asked = true; return humanask.Confirmed, nil }
	code, out, _ := b536Approve(t, "project.name", `"😀"`, "2026-11-11T11:00:00.000Z")
	if code != 127 || out != "" || asked {
		t.Fatalf("= %d %q asked %v, want a hand-over before the prompt", code, out, asked)
	}
	if f.state(t) != before {
		t.Fatal("wrote before handing over")
	}
	if got := f.handovers(t); len(got) != 1 || got[0] != "autopilot-approve-value" {
		t.Fatalf("reasons = %v", got)
	}
}
