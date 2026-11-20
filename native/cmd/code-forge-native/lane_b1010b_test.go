package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func b1010bChdir(t *testing.T, dir string) {
	t.Helper()
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(before); err != nil {
			t.Error(err)
		}
	})
}

func b1010bRead(t *testing.T, file string) []byte {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func b1010bWrite(t *testing.T, file string, body []byte) {
	t.Helper()
	if err := os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func b1010bLane(t *testing.T) (startFixture, string, string) {
	t.Helper()
	t.Setenv("CODE_FORGE_RUN_ID", "")
	f, base := b1010StartFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "go.mod"), []byte("module fixture\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte("project:\n  slug: unit\nengine: auto\ngates:\n  test: [go, test, './...']\n  types: null\n  format: null\nland:\n  min_free_gb: 0\n  regenerate: [\"printf 'once\\n' >> regenerated.txt\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b1010Git(t, f.repo, "add", ".")
	b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture module")
	base = b1010Git(t, f.repo, "rev-parse", "HEAD")
	if code, out, errs := b1010Start(t, f, base); code != 0 {
		t.Fatalf("start: %d %s %s", code, out, errs)
	}
	body := "package fixture\nimport \"testing\"\nfunc TestLaneFeature(t *testing.T) {}\nfunc TestUnrelated(t *testing.T) { t.Fatal(\"must not run\") }\n"
	if err := os.WriteFile(filepath.Join(f.repo, "feature_test.go"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	b1010Git(t, f.repo, "add", ".")
	b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "B1: feature")
	head := b1010Git(t, f.repo, "rev-parse", "HEAD")
	b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) {
		r["blocks"] = map[string]any{"B1": map[string]any{"status": "closed", "opened_at": "2000-01-01T00:00:00Z", "owned_files": []string{"feature_test.go"}, "base_sha": base, "acceptance": []any{map[string]any{"tests": []string{"TestLaneFeature"}}}}}
	})
	for _, row := range []ledger.Object{
		{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}},
		// Close precedes the coder's final commit by design. Prove that a
		// signed close predating the commit is accepted for approved bytes.
		{{Key: "event", Value: "block.close"}, {Key: "block", Value: "B1"}, {Key: "ts", Value: "2001-01-01T00:00:00.000Z"}},
	} {
		if _, err := state.WriteSigned(f.home, "r-lane", row); err != nil {
			t.Fatal(err)
		}
	}
	b1010bApprove(t, f, "feature_test.go", body, true)
	b1010bChdir(t, f.repo)
	return f, base, head
}

func b1010bApprove(t *testing.T, f startFixture, file, body string, approved bool) {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	for _, event := range []string{"review.approved", "review.result"} {
		if !approved && event == "review.approved" {
			continue
		}
		_, err := state.WriteSigned(f.home, "r-lane", ledger.Object{{Key: "event", Value: event}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "approved", Value: approved}, {Key: "open", Value: []any{}}})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestB1010bReadyVerifiesCommittedEvidence(t *testing.T) {
	f, _, head := b1010bLane(t)
	code, out, errs := runCaptured(t, "lane", "ready", "--run", "r-lane", "--json")
	if code != 0 || !strings.Contains(out, `"ready":true`) || !strings.Contains(out, head) {
		t.Fatalf("ready %d: %s %s", code, out, errs)
	}
	if b1010Git(t, f.repo, "rev-parse", "HEAD") != head {
		t.Fatal("ready moved HEAD")
	}
}

func TestB1010bReadyRefusesUnsafeLanes(t *testing.T) {
	for _, kind := range []string{"dirty", "open", "unowned", "late", "stale-review", "denied", "unsigned", "oversize", "unknown-commit", "extra-block"} {
		t.Run(kind, func(t *testing.T) {
			f, _, _ := b1010bLane(t)
			want := kind
			switch kind {
			case "dirty":
				b1010bWrite(t, filepath.Join(f.repo, "scratch.txt"), []byte("x"))
				want = "lane worktree is dirty"
			case "open", "unowned", "late", "extra-block":
				b1010EditRecord(t, f.home, "r-lane", func(r map[string]any) {
					blocks := r["blocks"].(map[string]any)
					b := blocks["B1"].(map[string]any)
					switch kind {
					case "open":
						b["status"] = "open"
						want = "closed"
					case "unowned":
						b["owned_files"] = []string{}
						want = "owned"
					case "late":
						b["opened_at"] = "2999-01-01T00:00:00Z"
						want = "before"
					case "extra-block":
						blocks["B2"] = map[string]any{"status": "open"}
						want = "every lane block must have a commit"
					}
				})
			case "unsigned":
				file := filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				data = []byte(strings.ReplaceAll(string(data), `"approved":true`, `"approved":false`))
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				want = "review"
			case "denied":
				body := b1010bRead(t, filepath.Join(f.repo, "feature_test.go"))
				b1010bApprove(t, f, "feature_test.go", string(body), false)
				want = "review"
			default:
				name, body, message := "feature_test.go", "package fixture\n", "B1: changed after review"
				if kind == "oversize" {
					name = "big.bin"
					body = strings.Repeat("x", 5000001)
					want = "size"
				}
				if kind == "unknown-commit" {
					message = "untracked work"
					want = "block id"
				}
				b1010bWrite(t, filepath.Join(f.repo, name), []byte(body))
				b1010Git(t, f.repo, "add", ".")
				b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", message)
				if kind == "stale-review" {
					want = "review"
				}
			}
			code, out, errs := runCaptured(t, "lane", "ready", "--run", "r-lane")
			if code != 1 || !strings.Contains(out+errs, want) {
				t.Fatalf("%s: %d %s %s; want %s", kind, code, out, errs, want)
			}
		})
	}
}

func b1010bCombine(t *testing.T, f startFixture, base string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "combine")
	b1010Git(t, f.repo, "worktree", "add", "-b", "combine-test", dir, base)
	code, out, errs := runVerbCall(t, "start", "--cwd", dir, "--mode", "brief", "--run", "r-combine", "--worker-pid", strconv.Itoa(os.Getpid()))
	if code != 0 {
		t.Fatalf("combine start %d %s %s", code, out, errs)
	}
	// A recorded generator proves that regeneration happens once, after named tests.
	b1010EditRecord(t, f.home, "r-combine", func(r map[string]any) { r["workspace"] = dir })
	b1010bChdir(t, dir)
	// Product git children discard inherited GIT_* variables. Pin identity
	// in the shared repository config so trial worktrees inherit it too.
	b1010Git(t, dir, "config", "--local", "user.useConfigOnly", "true")
	b1010Git(t, dir, "config", "--local", "user.name", "Fixture")
	b1010Git(t, dir, "config", "--local", "user.email", "fixture@example.invalid")
	return dir
}

func TestB1010bCombineMergesAndTests(t *testing.T) {
	f, base, head := b1010bLane(t)
	// Remove the intentional failure for this green two-lane scenario;
	// TestB1010bCombineStopsOnRed retains it to prove the red prefix stops.
	body := b1010bRead(t, filepath.Join(f.repo, "feature_test.go"))
	body = []byte(strings.ReplaceAll(string(body), "func TestUnrelated(t *testing.T) { t.Fatal(\"must not run\") }\n", ""))
	b1010bWrite(t, filepath.Join(f.repo, "feature_test.go"), body)
	b1010Git(t, f.repo, "add", ".")
	b1010Git(t, f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "B1: final test")
	b1010bApprove(t, f, "feature_test.go", string(body), true)
	head = b1010Git(t, f.repo, "rev-parse", "HEAD")
	second := filepath.Join(t.TempDir(), "second-lane")
	b1010Git(t, f.repo, "worktree", "add", "-b", "lane-second", second, base)
	code, out, errs := runCaptured(t, "lane", "start", "--cwd", second, "--base", base, "--run", "r-second", "--worker-pid", strconv.Itoa(os.Getpid()))
	if code != 0 {
		t.Fatalf("second start %d %s %s", code, out, errs)
	}
	secondBody := "package fixture\nimport \"testing\"\nfunc TestSecond(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(second, "second_test.go"), []byte(secondBody), 0600); err != nil {
		t.Fatal(err)
	}
	b1010Git(t, second, "add", ".")
	b1010Git(t, second, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "B2: second lane")
	b1010EditRecord(t, f.home, "r-second", func(r map[string]any) {
		r["blocks"] = map[string]any{"B2": map[string]any{"status": "closed", "opened_at": "2000-01-01T00:00:00Z", "owned_files": []string{"second_test.go"}, "base_sha": base}}
	})
	for _, row := range []ledger.Object{
		{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B2"}},
		{{Key: "event", Value: "block.close"}, {Key: "block", Value: "B2"}},
		{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B2"}, {Key: "file", Value: "second_test.go"}, {Key: "content_hash", Value: fmt.Sprintf("%x", sha256.Sum256([]byte(secondBody)))}},
		{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B2"}, {Key: "file", Value: "second_test.go"}, {Key: "content_hash", Value: fmt.Sprintf("%x", sha256.Sum256([]byte(secondBody)))}, {Key: "approved", Value: true}, {Key: "open", Value: []any{}}},
	} {
		if _, err := state.WriteSigned(f.home, "r-second", row); err != nil {
			t.Fatal(err)
		}
	}
	dir := b1010bCombine(t, f, base)
	code, out, errs = runCaptured(t, "lane", "combine", "--run", "r-combine", "r-lane", "r-second")
	if code != 0 || !strings.Contains(out, "combined") {
		t.Fatalf("combine %d %s %s", code, out, errs)
	}
	if strings.Count(out, "block B1: TestLaneFeature") != 2 || strings.Count(out, "block B2: TestSecond") != 1 {
		t.Fatalf("per-merge named checks: %s", out)
	}
	b1010Git(t, dir, "merge-base", "--is-ancestor", head, "HEAD")
	if b1010Git(t, f.repo, "rev-parse", "HEAD") != head {
		t.Fatal("combine rebased lane")
	}
	if b1010Git(t, dir, "branch", "--show-current") != "combine-test" {
		t.Fatal("combine switched branch")
	}
	generated, err := os.ReadFile(filepath.Join(dir, "regenerated.txt"))
	if err != nil || string(generated) != "once\n" {
		t.Fatalf("regeneration: %q %v", generated, err)
	}
}

func TestB1010bCombineStopsOnRed(t *testing.T) {
	f, base, head := b1010bLane(t)
	dir := b1010bCombine(t, f, base)
	code, out, errs := runCaptured(t, "lane", "combine", "--run", "r-combine", "r-lane")
	if code != 1 || !strings.Contains(out+errs, "TestUnrelated") {
		t.Fatalf("red combine %d %s %s", code, out, errs)
	}
	if b1010Git(t, dir, "rev-parse", "HEAD") != base || b1010Git(t, f.repo, "rev-parse", "HEAD") != head {
		t.Fatal("red combine moved a branch")
	}
}

func TestC28cCombineMissingIdentity(t *testing.T) {
	for _, field := range []string{"name", "email"} {
		t.Run(field, func(t *testing.T) {
			f, base, head := b1010bLane(t)
			dir := b1010bCombine(t, f, base)
			b1010Git(t, dir, "config", "--local", "--unset-all", "user."+field)
			before := b1010Git(t, dir, "worktree", "list", "--porcelain")
			code, out, errs := runCaptured(t, "lane", "combine", "--run", "r-combine", "r-lane")
			if code != 1 || !strings.Contains(errs, "configure repository-local git identity") ||
				!strings.Contains(errs, "git config user.name") || !strings.Contains(errs, "git config user.email") {
				t.Fatalf("missing %s: %d %s %s", field, code, out, errs)
			}
			if b1010Git(t, dir, "rev-parse", "HEAD") != base || b1010Git(t, f.repo, "rev-parse", "HEAD") != head {
				t.Fatal("missing identity moved a branch")
			}
			if after := b1010Git(t, dir, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("missing identity changed worktrees:\nbefore %s\nafter %s", before, after)
			}
			if _, err := os.Stat(filepath.Join(dir, "regenerated.txt")); !os.IsNotExist(err) {
				t.Fatalf("generator ran without identity: %v", err)
			}
		})
	}
}
