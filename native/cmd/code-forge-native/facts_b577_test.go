package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B577 goldens (testdata/b577) were printed by Node v24.22.1 (ICU 78.3,
// Unicode 17.0) from its bundled minimatch with path.matchesGlob's options
// (platform posix, nocase true and false): brace.jsonl is braceExpand,
// sets.jsonl is Minimatch#globParts and #set (a RegExp as its _src in
// UTF-16 hex, with its u flag), match.jsonl is Minimatch#match.

func fcB577Lines(t *testing.T, name string, each func(line []byte)) {
	t.Helper()
	file, err := os.Open("testdata/b577/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		each(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func fcB577Hex(units []uint16) string {
	var b strings.Builder
	for _, u := range units {
		fmt.Fprintf(&b, "%04x", u)
	}
	return b.String()
}

func TestB577BraceExpandMatchesNode(t *testing.T) {
	n := 0
	fcB577Lines(t, "brace.jsonl", func(line []byte) {
		var row struct {
			P          string
			Out        []string
			N          int
			Head, Tail []string
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		n++
		got := fcBraceExpand(row.P)
		if row.N > 0 {
			if len(got) != row.N || len(got) < 5 || !reflect.DeepEqual(got[:5], row.Head) || !reflect.DeepEqual(got[len(got)-5:], row.Tail) {
				t.Errorf("braceExpand(%q): %d items, Node %d %q..%q", row.P, len(got), row.N, row.Head, row.Tail)
			}
			return
		}
		if !reflect.DeepEqual(got, row.Out) && !(len(got) == 0 && len(row.Out) == 0) {
			t.Errorf("braceExpand(%q) = %q, Node %q", row.P, got, row.Out)
		}
	})
	if n < 1500 {
		t.Fatalf("only %d brace rows", n)
	}
}

type fcB577SetRow struct {
	P     string
	Nc    bool
	Err   string
	Parts [][]string
	Set   [][]struct {
		K, L, Src string
		U         bool
	}
}

func TestB577MinimatchSetMatchesNode(t *testing.T) {
	checked, unmodelled := 0, 0
	fcB577Lines(t, "sets.jsonl", func(line []byte) {
		var row fcB577SetRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if row.Err != "" {
			t.Fatalf("golden error row %q", row.P)
		}
		mm := fcNewMM(row.P, row.Nc)
		needU := false
		for _, s := range row.Set {
			for _, p := range s {
				needU = needU || p.U
			}
		}
		if needU && !mm.unmodelled {
			t.Errorf("%q nocase=%v: Node needs the u flag, Go claims it", row.P, row.Nc)
		}
		if mm.unmodelled {
			unmodelled++
			return
		}
		checked++
		parts := mm.globParts
		if parts == nil {
			parts = [][]string{}
		}
		if row.Parts == nil {
			row.Parts = [][]string{}
		}
		if !reflect.DeepEqual(parts, row.Parts) {
			t.Errorf("%q globParts = %q, Node %q", row.P, parts, row.Parts)
			return
		}
		if len(mm.set) != len(row.Set) {
			t.Errorf("%q set has %d rows, Node %d", row.P, len(mm.set), len(row.Set))
			return
		}
		for i, s := range row.Set {
			if len(mm.set[i]) != len(s) {
				t.Errorf("%q set[%d] has %d parts, Node %d", row.P, i, len(mm.set[i]), len(s))
				continue
			}
			for j, want := range s {
				got := mm.set[i][j]
				var gotHex string
				switch got.kind {
				case 's':
					gotHex = fcB577Hex(got.lit)
				case 'r':
					gotHex = fcB577Hex(got.re.src)
				}
				wantHex := want.L
				if want.K == "r" {
					wantHex = want.Src
				}
				if string(got.kind) != want.K || gotHex != wantHex {
					t.Errorf("%q nocase=%v set[%d][%d] = %c %s, Node %s %s", row.P, row.Nc, i, j, got.kind, gotHex, want.K, wantHex)
				}
			}
		}
	})
	t.Logf("sets: %d checked, %d unmodelled", checked, unmodelled)
	if checked < 3000 {
		t.Fatalf("only %d modelled patterns", checked)
	}
}

func TestB577MatchesGlobMatchesNode(t *testing.T) {
	checked, hits, skipped := 0, 0, 0
	cache := map[string]*fcMM{}
	fcB577Lines(t, "match.jsonl", func(line []byte) {
		var row struct {
			P, F  string
			Nc, M bool
		}
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprint(row.Nc, row.P)
		mm := cache[key]
		if mm == nil {
			mm = fcNewMM(row.P, row.Nc)
			cache[key] = mm
		}
		if mm.unmodelled {
			skipped++
			return
		}
		checked++
		got := mm.match(row.F)
		if got {
			hits++
		}
		if got != row.M {
			t.Errorf("match(%q, %q, nocase=%v) = %v, Node %v", row.F, row.P, row.Nc, got, row.M)
		}
	})
	t.Logf("match: %d checked (%d hits), %d unmodelled", checked, hits, skipped)
	if checked < 50000 || hits < 1500 {
		t.Fatalf("only %d modelled cases, %d hits", checked, hits)
	}
}

// TestB577FactsGlobServedLikeNode: a brief with brace, backslash, POSIX-class,
// extglob and non-ASCII globs handed over (facts-glob-claim) before B577; the
// brace and non-ASCII ones are now checked in Go. testdata/b577/glob.txt is
// Node's report (CODE_FORGE_NATIVE=0) with the same workspace and answer.
func TestB577FactsGlobServedLikeNode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	brief, err := os.ReadFile(filepath.Join("testdata", "b577", "glob.brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile(filepath.Join("testdata", "b577", "glob.answer.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "b577", "glob.txt"))
	if err != nil {
		t.Fatal(err)
	}
	base := fcWorkspace(t, "anthropic", string(brief))
	t.Setenv("FAKE_ANSWER", string(answer))
	code, stdout, stderr := runCaptured(t, "facts", "--brief", "brief.md")
	if data, _ := os.ReadFile(filepath.Join(base, "home", ".code-forge", "native", "handovers.jsonl")); len(data) != 0 {
		t.Fatalf("handed over: %s", data)
	}
	if got, node := fcB568Mask(fcReport(base, code, stdout, stderr)), fcB568Mask(string(want)); got != node {
		t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, node)
	}
}
