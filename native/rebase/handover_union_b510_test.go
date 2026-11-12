package rebase

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B510: the hand-over reason list (native/cmd/code-forge-native/handover_reasons.go since B513)
// is append-only in practice: every block adds a constant at the end of the
// iota block and an entry at the end of the handoverReasons array. Two blocks
// doing that at once used to stop the land on a conflict. The repository's
// .gitattributes marks the file merge=union, so both appends are kept. These
// tests run real git and real go on a small file of the same shape, with the
// repository's own .gitattributes copied in.

const handoverPath = "native/cmd/code-forge-native/handover_reasons.go"
const handoverMod = "native/cmd/code-forge-native/go.mod"

// handoverBase has the shape of the real file: an iota block and a keyed
// array; main exits 1 when an entry is empty or repeated.
const handoverBase = `package main

import (
	"fmt"
	"os"
)

type handoverReason int

const (
	hUnrouted handoverReason = iota
	hBlockUnportedSubcommand
	hWaitResult
	hGatesHome
)

var handoverReasons = [...]string{
	hUnrouted:                "unrouted",
	hBlockUnportedSubcommand: "block-unported-subcommand",
	hWaitResult:              "wait-result",
	hGatesHome:               "gates-home",
}

func pick(verb string) handoverReason {
	if verb == "status" {
		return hWaitResult
	}
	return hUnrouted
}

func main() {
	seen := map[string]bool{}
	for _, reason := range handoverReasons {
		if reason == "" || seen[reason] {
			fmt.Println("bad list:", handoverReasons)
			os.Exit(1)
		}
		seen[reason] = true
		fmt.Println(reason)
	}
	fmt.Println("pick", handoverReasons[pick("status")])
}
`

// appendReason adds one constant and its array entry at the end, as blocks
// do, and gofmts the result.
func appendReason(t *testing.T, body, name, code string) string {
	t.Helper()
	i := strings.Index(body, "\n)\n\nvar handoverReasons")
	body = body[:i] + "\n\t" + name + body[i:]
	i = strings.Index(body, "\n}\n\nfunc pick")
	return gofmtSource(t, body[:i]+"\n\t"+name+": \""+code+"\","+body[i:])
}

func repoAttributes(t *testing.T) string {
	t.Helper()
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(strings.TrimSpace(top), ".gitattributes"))
	if err != nil {
		t.Fatalf("read the repository .gitattributes: %v", err)
	}
	return string(data)
}

// goCheck runs gofmt -l, go vet and the program on the merged file; it returns
// the combined failure text, or "" and the program output when all pass.
func goCheck(t *testing.T, dir string) (failure, out string) {
	t.Helper()
	pkg := filepath.Join(dir, filepath.Dir(handoverPath))
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = pkg
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	if text, err := run("gofmt", "-l", "."); err != nil || text != "" {
		return "gofmt: " + text, ""
	}
	if text, err := run("go", "vet", "."); err != nil {
		return "go vet: " + text, ""
	}
	text, err := run("go", "run", ".")
	if err != nil {
		return "go run: " + text, ""
	}
	return "", text
}

// unionFixture: main and the block both start from base without the
// attribute (as the queued branches did); main then lands block A's append
// and, when withAttributes, the repository's .gitattributes.
func unionFixture(t *testing.T, withAttributes bool, mainBody, blockBody string) *openFixture {
	t.Helper()
	f := newOpenFixture(t, []string{handoverPath}, map[string]string{handoverPath: handoverBase, handoverMod: "module unionfixture\n\ngo 1.21\n"})
	f.write(f.repo, handoverPath, mainBody)
	f.commit(f.repo, "B1 lands")
	if withAttributes {
		f.write(f.repo, ".gitattributes", repoAttributes(t))
		f.commit(f.repo, "B510 lands")
	}
	f.write(f.tree, handoverPath, blockBody)
	f.commit(f.tree, "B2")
	return f
}

func TestB510RepoMarksHandoverListUnion(t *testing.T) {
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	out, err := git(strings.TrimSpace(top), "check-attr", "merge", "--", handoverPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), handoverPath+": merge: union"; got != want {
		t.Fatalf("check-attr = %q, want %q", got, want)
	}
}

func TestB510TwoAppendsRebaseClean(t *testing.T) {
	mainBody := appendReason(t, handoverBase, "hAlphaReason", "alpha-reason")
	blockBody := appendReason(t, handoverBase, "hBetaReason", "beta-reason")
	want := appendReason(t, mainBody, "hBetaReason", "beta-reason")

	t.Run("plain git rebase, as land.sh runs it", func(t *testing.T) {
		f := unionFixture(t, true, mainBody, blockBody)
		if out, err := git(f.tree, "-c", "user.name=F", "-c", "user.email=f@example.invalid", "rebase", "main"); err != nil {
			t.Fatalf("git rebase main: %v\n%s", err, out)
		}
		if got := f.read(f.tree, handoverPath); got != want {
			t.Fatalf("merged file =\n%s\nwant:\n%s", got, want)
		}
		failure, out := goCheck(t, f.tree)
		if failure != "" {
			t.Fatal(failure)
		}
		if !strings.Contains(out, "gates-home\nalpha-reason\nbeta-reason\npick wait-result\n") {
			t.Fatalf("program output = %q, want both new reasons in order", out)
		}
	})

	t.Run("block rebase in Go", func(t *testing.T) {
		f := unionFixture(t, true, mainBody, blockBody)
		f.save()
		_, err := Run(f.home, "unit", "A")
		f.requireRebased(err, []string{handoverPath})
		if got := f.read(f.tree, handoverPath); got != want {
			t.Fatalf("merged file =\n%s\nwant:\n%s", got, want)
		}
		if failure, _ := goCheck(t, f.tree); failure != "" {
			t.Fatal(failure)
		}
	})

	t.Run("without the attribute the same rebase conflicts", func(t *testing.T) {
		f := unionFixture(t, false, mainBody, blockBody)
		out, err := git(f.tree, "-c", "user.name=F", "-c", "user.email=f@example.invalid", "rebase", "main")
		if err == nil || !strings.Contains(out, "CONFLICT (content): Merge conflict in "+handoverPath) {
			t.Fatalf("git rebase main err=%v, want a conflict:\n%s", err, out)
		}
		if _, err := git(f.tree, "rebase", "--abort"); err != nil {
			t.Fatal(err)
		}
	})
}

// Union is only safe for appends. When both sides change the same existing
// line, union keeps both lines with no conflict; the land gate (land.sh:
// gofmt -l, go vet ./cmd/..., go test ./...; block land: the merge gate's
// gates) must then fail. Each case shows which check catches it.
func TestB510UnionNonAppendEditsFailTheGate(t *testing.T) {
	cases := []struct {
		name, main, block, caught string
	}{
		{
			name:   "both rename the same reason",
			main:   strings.Replace(handoverBase, `"wait-result",`, `"wait-result-a",`, 1),
			block:  strings.Replace(handoverBase, `"wait-result",`, `"wait-result-b",`, 1),
			caught: "go vet: ", // duplicate index in the array literal does not compile
		},
		{
			name:   "both change the same return",
			main:   strings.Replace(handoverBase, "return hWaitResult", "return hGatesHome", 1),
			block:  strings.Replace(handoverBase, "return hWaitResult", "return hBlockUnportedSubcommand", 1),
			caught: "go vet: ", // two returns: unreachable code
		},
		{
			name:   "one side adds a key longer than the alignment",
			main:   appendReason(t, handoverBase, "hAlphaReason", "alpha-reason"),
			block:  appendReason(t, handoverBase, "hAVeryLongReasonNameThatRealigns", "long"),
			caught: "", // set below from the run
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := unionFixture(t, true, c.main, c.block)
			out, err := git(f.tree, "-c", "user.name=F", "-c", "user.email=f@example.invalid", "rebase", "main")
			if err != nil {
				t.Fatalf("union rebase stopped (%v); this case expects a silent union merge:\n%s", err, out)
			}
			failure, _ := goCheck(t, f.tree)
			if failure == "" {
				t.Fatalf("merged file passed gofmt, vet and run; the gate would not catch it:\n%s", f.read(f.tree, handoverPath))
			}
			if c.caught != "" && !strings.HasPrefix(failure, c.caught) {
				t.Fatalf("caught by %q, want %q", failure, c.caught)
			}
			t.Logf("caught: %.300s", failure)
		})
	}
}

func gofmtSource(t *testing.T, src string) string {
	t.Helper()
	cmd := exec.Command("gofmt")
	cmd.Stdin = strings.NewReader(src)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gofmt: %v\n%s", err, src)
	}
	return string(out)
}
