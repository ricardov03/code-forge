package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B981 tests: gh pr create and pr close change GitHub, so a network error
// there is retried only after land looks whether the change was made. The
// fake gh's first create/close fails with a network error; with made=1 the
// failed call still made the change on GitHub (the reply was lost).

const landB981NetErr = `echo 'Post "https://api.github.com/graphql": read tcp 10.0.0.2:5555->140.82.112.6:443: read: connection reset by peer' >&2; exit 1`

func landB981FakeGh(t *testing.T, made bool) string {
	t.Helper()
	dir := t.TempDir()
	m := "0"
	if made {
		m = "1"
	}
	script := `#!/bin/sh
d=` + closeCommand([]string{dir}) + `
printf '%s\n' "$*" >> "$d/calls"
first() { if [ -f "$d/tried-$1" ]; then return 1; fi; : > "$d/tried-$1"; return 0; }
case "$1 $2" in
  'pr create')
    if first create; then [ ` + m + ` = 1 ] && : > "$d/created"; ` + landB981NetErr + `; fi
    : > "$d/created"; echo "https://github.com/o/r/pull/301" ;;
  'pr list')
    if [ -f "$d/created" ]; then echo '[{"number":300,"headRefName":"ci-b1","headRefOid":"x","state":"OPEN"}]'; else echo '[]'; fi ;;
  'pr close')
    if first close; then [ ` + m + ` = 1 ] && : > "$d/closed"; ` + landB981NetErr + `; fi
    : > "$d/closed" ;;
  'pr view')
    if [ -f "$d/closed" ]; then echo '{"state":"CLOSED"}'; else echo '{"state":"OPEN"}'; fi ;;
  *) echo "fake gh: unexpected $*" >&2; exit 9 ;;
esac
`
	landWrite(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return dir
}

func landB981Count(calls []string, prefix string) int {
	n := 0
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func landB981Ensure(t *testing.T, made bool) (string, []string) {
	t.Helper()
	f := landCIBatch(t, map[string]any{"require_ci": "github"}, "", "")
	f.bareRemote()
	gh := landB981FakeGh(t, made)
	landCIFakeClock(t)
	var out bytes.Buffer
	if refusal := landCIEnsureBranch(f.repo, "B1", f.heads()["B1"], "main", landCISettings{prefix: "ci-", remote: "origin"}, &out); refusal != "" {
		t.Fatalf("refusal %q:\n%s", refusal, out.String())
	}
	return out.String(), landCICalls(t, gh)
}

// The create's reply was lost but GitHub opened the PR: land finds #300 on
// ci-b1 and does not create a second PR.
func TestB981PRCreateRetryFindsExistingPR(t *testing.T) {
	output, calls := landB981Ensure(t, true)
	if n := landB981Count(calls, "pr create "); n != 1 {
		t.Errorf("pr create sent %d times, want 1 (the PR exists):\n%s", n, strings.Join(calls, "\n"))
	}
	if !strings.Contains(output, "ci: opened draft PR #300 for ci-b1\n") {
		t.Errorf("output must name the found PR #300:\n%s", output)
	}
}

// The create failed and no PR exists: land creates it again.
func TestB981PRCreateRetryCreatesWhenNone(t *testing.T) {
	output, calls := landB981Ensure(t, false)
	if n := landB981Count(calls, "pr create "); n != 2 {
		t.Errorf("pr create sent %d times, want 2 (none found after the error):\n%s", n, strings.Join(calls, "\n"))
	}
	if !strings.Contains(output, "ci: opened draft PR #301 for ci-b1\n") {
		t.Errorf("output must name the created PR #301:\n%s", output)
	}
}

// A close whose reply was lost (GitHub closed it) is not sent again; a close
// that failed is.
func TestB981PRCloseRetrySkipsClosed(t *testing.T) {
	for _, made := range []bool{true, false} {
		gh := landB981FakeGh(t, made)
		landCIFakeClock(t)
		if err := landCIPRClose(t.TempDir(), 300, "done"); err != nil {
			t.Fatalf("made=%v: %v", made, err)
		}
		calls := landCICalls(t, gh)
		want := 2
		if made {
			want = 1
		}
		if n := landB981Count(calls, "pr close 300 "); n != want {
			t.Errorf("made=%v: pr close sent %d times, want %d:\n%s", made, n, want, strings.Join(calls, "\n"))
		}
		if n := landB981Count(calls, "pr view 300 --json state"); n != 1 {
			t.Errorf("made=%v: the state must be read once before the retry:\n%s", made, strings.Join(calls, "\n"))
		}
	}
}

// B805's head search comes back full (200 rows of ci-b1… branches of other
// blocks, as ci-b10…): land lists each exact branch ci-b1, ci-b1-2, … and
// finds the block's PRs, stopping after 5 names in a row with none.
func TestB981BlockPRsPastFullSearchPage(t *testing.T) {
	decoys := []string{}
	for i := 0; i < 200; i++ {
		decoys = append(decoys, `{"number":`+strings.Repeat("9", 1+i%3)+`,"headRefName":"ci-b1`+string(rune('0'+i%10))+`x","headRefOid":"y","state":"OPEN"}`)
	}
	gh := landCIFakeGhSeq(t, map[string]string{
		"prs-1.json":                 landB805PRs(t, 1, 200, 999),
		"prs-search-head:ci-b1.json": "[" + strings.Join(decoys, ",") + "]",
		"prs-2.json":                 `[{"number":12,"headRefName":"ci-b1","headRefOid":"a","state":"CLOSED"}]`,
		"prs-4.json":                 `[{"number":40,"headRefName":"ci-b1-3","headRefOid":"b","state":"OPEN"}]`,
	})
	prs, err := landCIBlockPRs(t.TempDir(), "B1", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 || prs[0].Number != 12 || prs[1].Number != 40 || prs[1].n != 3 {
		t.Fatalf("block PRs=%+v, want #12 on ci-b1 and #40 on ci-b1-3", prs)
	}
	calls := landCICalls(t, gh)
	heads := []string{}
	for _, call := range calls {
		if i := strings.Index(call, "--head "); i >= 0 {
			heads = append(heads, strings.Fields(call[i:])[1])
		}
	}
	want := "ci-b1 ci-b1-2 ci-b1-3 ci-b1-4 ci-b1-5 ci-b1-6 ci-b1-7 ci-b1-8"
	if strings.Join(heads, " ") != want {
		t.Errorf("exact head lists=%q, want %q", strings.Join(heads, " "), want)
	}
}
