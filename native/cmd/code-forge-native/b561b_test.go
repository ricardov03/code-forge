package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// B561b: `code-forge review` of several files and of a test-like file runs in
// Go (B553 handed these over as review-worker-handoff because of the B561
// race; B537 ported the moved-code and test-quality hints). Each case runs the
// test-built binary with CODE_FORGE_NODE naming no program, so any hand-over
// fails, and must match the Node golden in testdata/b561b, recorded with
// `CODE_FORGE_NATIVE=0 ./bin/code-forge review …` through b553Node
// (B561B_WRITE_GOLDEN=1 rewrites them).
//
// Masks (the only ones): run ids `<RUN>` and block ids `<BLOCK>` (b553Mask),
// and the order of stderr lines: a per-file progress line comes as each wait
// ends, so with several files the lines are compared sorted. Commits carry a
// fixed date, so the base sha is compared as is.

const b561bTestFile = `import { readFileSync } from 'node:fs';
const text = readFileSync('src/a.mjs', 'utf8');
if (!text.includes('hello')) throw new Error('no hello');
`

type b561bGolden struct {
	Code int      `json:"code"`
	Out  string   `json:"out"`
	Err  string   `json:"err"`
	Tmp  []string `json:"tmp"`
	Home []string `json:"home"`
}

func b561bLines(s string) string {
	l := strings.Split(s, "\n")
	sort.Strings(l)
	return strings.Join(l, "\n")
}

func TestB561bReviewSeveralAndTestLikeFilesGo(t *testing.T) {
	cases := []struct {
		name, mode string
		testFile   bool
		args       []string
	}{
		{"several-files", "approve", false, []string{"--max", "60", "--keep-run"}},
		{"several-files-findings", "findings", false, []string{"--files", "src/a.txt", "src/b.txt", "--max", "60", "--keep-run"}},
		{"several-files-json", "approve", false, []string{"--files", "src/a.txt", "src/b.txt", "--json", "--max", "60", "--keep-run"}},
		{"test-like-file", "approve", true, []string{"--files", "src/a.test.mjs", "--max", "60", "--keep-run"}},
		{"test-like-and-other", "approve", true, []string{"--files", "src/a.txt", "src/a.test.mjs", "--max", "60", "--keep-run"}},
	}
	write := os.Getenv("B561B_WRITE_GOLDEN") == "1"
	var bin string
	if !write {
		bin = b553Binary(t)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden := filepath.Join("testdata", "b561b", c.name+".json")
			addTestFile := func(f b553Fix) {
				if c.testFile {
					if err := os.WriteFile(filepath.Join(f.repo, "src", "a.test.mjs"), []byte(b561bTestFile), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if write {
				f := b553Fixture(t, c.mode)
				addTestFile(f)
				// b553Node, run on this fixture
				n := b561bNode(t, f, c.args)
				data, err := json.MarshalIndent(b561bGolden{n.code, n.out, n.err, n.tmp, n.home}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, append(data, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			var want b561bGolden
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			f := b553Fixture(t, c.mode)
			addTestFile(f)
			env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
			g := b553Run(t, f, append([]string{bin, "review"}, c.args...), env)
			if len(g.handover) != 0 {
				t.Fatalf("hand-overs: %v; stderr %q", g.handover, g.err)
			}
			if g.code != want.Code || g.out != want.Out || b561bLines(g.err) != b561bLines(want.Err) {
				t.Fatalf("go = %d\n%q\n%q\nnode golden %d\n%q\n%q", g.code, g.out, g.err, want.Code, want.Out, want.Err)
			}
			if strings.Join(g.tmp, "\n") != strings.Join(want.Tmp, "\n") || strings.Join(g.home, "\n") != strings.Join(want.Home, "\n") {
				t.Fatalf("files differ\ngo   %v %v\nnode %v %v", g.tmp, g.home, want.Tmp, want.Home)
			}
		})
	}
}

// b561bNode runs Node's review (CODE_FORGE_NATIVE=0) on fixture f.
func b561bNode(t *testing.T, f b553Fix, args []string) b553Side {
	t.Helper()
	node := filepath.Join(b458Repo(t), "bin", "code-forge.mjs")
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is not on PATH: goldens need Node")
	}
	env := b553Env(f, "CODE_FORGE_NATIVE=0")
	env[0] += ":" + filepath.Dir(nodeBin)
	return b553Run(t, f, append([]string{nodeBin, node, "review"}, args...), env)
}
