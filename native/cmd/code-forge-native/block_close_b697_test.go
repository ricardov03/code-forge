package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B697: the close cases B684 left to Node run in Go: human_checks of any
// shape, the --decide rulings Go's reader did not decide (cap and late
// findings, invalid autopilot waivers, claims from a signed ticket result,
// accepted transcript hits, waive prechecks, grant until forms) and
// --auto-claim --no-require-reviews at a terminal. Every case runs on the
// same temp-HOME fixture as Node did when testdata/b697/<case>.txt was
// recorded (B697_RECORD_NODE=<repo root>, Node's runBlock in process with the
// terminal and the answer injected).

const b697Ticket = "0123456789abcdef01234567"

func b697Entry(f *b684Fixture, set func(entry ledger.Object) ledger.Object) {
	f.t.Helper()
	record, err := state.ReadRun(f.home, f.run)
	f.must(err)
	blocks := record.Get("blocks").(ledger.Object)
	blocks.Set("B1", set(blocks.Get("B1").(ledger.Object)))
	record.Set("blocks", blocks)
	f.must(state.SaveRun(f.home, record))
}

func b697Checks(v any) func(f *b684Fixture) {
	return func(f *b684Fixture) {
		b697Entry(f, func(entry ledger.Object) ledger.Object {
			entry.Set("human_checks", v)
			return entry
		})
	}
}

func b697Until(v any) func(f *b684Fixture) {
	return func(f *b684Fixture) {
		record, err := state.ReadRun(f.home, f.run)
		f.must(err)
		grant := record.Get("autopilot").(ledger.Object)
		grant.Set("until", v)
		record.Set("autopilot", grant)
		f.must(state.SaveRun(f.home, record))
	}
}

func b697Transcript(text string) func(f *b684Fixture) {
	return func(f *b684Fixture) {
		f.must(os.WriteFile(f.transcript, []byte(text), 0600))
	}
}

// b697Cap: an approval, then the file stopped at review_cap with F2 open.
func b697Cap(f *b684Fixture, hash string) {
	b684Approved(f, hash)
	f.row(ledger.Object{{Key: "event", Value: "review.cap"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "open", Value: []any{"F2"}}})
}

func b697Late(file, finding string) func(f *b684Fixture, hash string) {
	return func(f *b684Fixture, hash string) {
		b684Approved(f, hash)
		f.row(ledger.Object{{Key: "event", Value: "review.late_finding"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "finding", Value: finding}, {Key: "severity", Value: "warning"}, {Key: "claim", Value: "a late claim"}})
	}
}

// b697AutopilotWaived: the warning F1 is waived under a grant the run never had.
func b697AutopilotWaived(f *b684Fixture, hash string) {
	b684Finding("warning")(f, hash)
	f.row(ledger.Object{{Key: "severity", Value: "warning"}, {Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "finding", Value: "F1"}, {Key: "reason", Value: "naming only"}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: "ag-none"}})
}

// b697Ticketed: the review left F1 open without a claim; the signed result
// file of its ticket names the claim.
func b697Ticketed(f *b684Fixture, hash string) {
	b684Approved(f, hash)
	open := []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}}}
	f.row(ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}, {Key: "status", Value: "done"}, {Key: "approved", Value: false}, {Key: "ticket", Value: b697Ticket}, {Key: "open", Value: open}})
	key, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", f.run+".key"))
	f.must(err)
	result, err := sign.SignRow(map[string]any{"ticket": b697Ticket, "run": f.run, "findings": []any{map[string]any{"id": "F1", "claim": "the ticket's claim"}}}, key)
	f.must(err)
	data, err := json.Marshal(result)
	f.must(err)
	dir := filepath.Join(f.repo, ".code-forge/reviews", f.run)
	f.must(os.MkdirAll(dir, 0o755))
	f.must(os.WriteFile(filepath.Join(dir, b697Ticket+".json"), data, 0o600))
}

func b697TicketedAgentWaived(f *b684Fixture, hash string) {
	b697Ticketed(f, hash)
	f.row(ledger.Object{{Key: "severity", Value: "warning"}, {Key: "verdict", Value: "agree"}, {Key: "judge_reason", Value: "fine"}, {Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "finding", Value: "F1"}, {Key: "reason", Value: "naming only"}, {Key: "by", Value: "agent"}})
}

var b697Mac = regexp.MustCompile(`"mac":"[0-9a-f]{64}"`)

const b697Hit = "Completed implementation.\n$ git stash\nall green\n"

var b697Cases = []struct {
	name, ask, args string
	setup           b684Setup
	after           func(f *b684Fixture)
}{
	{"hc-string", "notty", "", b684Setup{review: b684Approved}, b697Checks("ab")},
	{"hc-empty-string", "notty", "", b684Setup{review: b684Approved}, b697Checks("")},
	{"hc-number", "notty", "", b684Setup{review: b684Approved}, b697Checks(float64(2))},
	{"hc-object", "notty", "", b684Setup{review: b684Approved}, b697Checks(ledger.Object{{Key: "k", Value: "v"}})},
	{"hc-mixed", "notty", "", b684Setup{review: b684Approved}, b697Checks([]any{"look", float64(7), nil})},
	{"hc-mixed-json", "notty", "|--json", b684Setup{review: b684Approved}, b697Checks([]any{"look", float64(7), nil})},
	{"decide-cap", "yes", "|--decide|--reason|cap reached", b684Setup{review: b697Cap}, nil},
	{"decide-late", "yes", "|--decide|--reason|late is fine", b684Setup{review: b697Late("sample.mjs", "L1")}, nil},
	{"decide-not-owned", "yes", "|--decide|--reason|late is fine", b684Setup{review: b697Late("other.mjs", "L1")}, nil},
	{"decide-late-bad-id", "yes", "|--decide|--reason|late is fine", b684Setup{review: b697Late("sample.mjs", "L 1")}, nil},
	{"decide-waiver-invalid", "yes", "|--decide|--reason|naming only", b684Setup{review: b697AutopilotWaived}, nil},
	{"decide-ticket-claim", "yes", "|--decide|--reason|naming only", b684Setup{review: b697Ticketed}, nil},
	{"decide-ack-ticket", "yes", "|--decide|--reason|the owner read the waiver", b684Setup{config: b684Config(map[string]any{"review": map[string]any{"agent_waivers": map[string]any{"require_ack": true}}}), review: b697TicketedAgentWaived}, nil},
	{"decide-hit", "yes", "|--decide|--reason|the stash was harmless", b684Setup{review: b684Approved}, b697Transcript(b697Hit)},
	{"decide-hit-no", "no", "|--decide|--reason|the stash was harmless", b684Setup{review: b684Approved}, b697Transcript(b697Hit)},
	{"decide-hit-and-finding", "yes", "|--decide|--reason|both are fine", b684Setup{review: b684Finding("warning")}, b697Transcript(b697Hit)},
	{"decide-grant-null-until", "yes", "|--decide|--reason|naming only", b684Setup{grant: "active", review: b684Finding("warning")}, b697Until(nil)},
}

func TestB697HumanCloseMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	b760NoNodeRecorder(t, "B697_RECORD_NODE")
	record := os.Getenv("B697_RECORD_NODE")
	for _, c := range b697Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, c.setup)
			if c.after != nil {
				c.after(f)
			}
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b697", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB697HumanCloseMatchesNode", c.ask, args)
			}
			// the ticket result file is signed over its own ts
			got := b697Mac.ReplaceAllString(f.report(r, before), `"mac":"<MAC>"`)
			if record != "" {
				f.must(os.MkdirAll(filepath.Dir(golden), 0o755))
				f.must(os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			f.must(err)
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// --auto-claim on the B493 fixture (extra.txt is claimed and reviewed):
// with --no-require-reviews at a terminal the owner is asked; a failed or
// unchecked claim scope is Node's answer too.
var b697ClaimCases = []struct {
	name, until, ask, args, setup string
}{
	{"nrr-claim-yes", "", "yes", "|--no-require-reviews", ""},
	{"nrr-claim-no", "", "no", "|--no-require-reviews", ""},
	{"nrr-claim-notty", "", "notty", "|--no-require-reviews", ""},
	{"nrr-claim-expired", "2020-01-01T00:00:00.000Z", "yes", "|--no-require-reviews", ""},
	{"nrr-claim-active", "2099-01-01T00:00:00.000Z", "yes", "|--no-require-reviews", ""},
	{"claim-unchecked", "2099-01-01T00:00:00.000Z", "notty", "", "unchecked"},
}

func b697ClaimFixture(t *testing.T, until, setup string) *closeReviewFixture {
	t.Helper()
	f := b493AutoClaimFixture(t, "2099-01-01T00:00:00.000Z")
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if until == "" {
		record.Delete("autopilot")
	} else {
		grant := record.Get("autopilot").(ledger.Object)
		grant.Set("until", until)
		record.Set("autopilot", grant)
	}
	blocks := record.Get("blocks").(ledger.Object)
	entry := blocks.Get("B1").(ledger.Object)
	switch setup {
	case "unchecked":
		entry.Delete("tree_at_open")
	}
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	return f
}

func b697ClaimReport(t *testing.T, f *closeReviewFixture, r hvResult, before int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("asked=" + r.asked + "\nexit=" + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr + "--- new rows\n")
	lines := strings.Split(strings.TrimSpace(b493LedgerLines(t, f)), "\n")
	for _, line := range lines[before:] {
		if strings.Contains(line, `"event":"review.`) {
			continue
		}
		b.WriteString(b612Drop.ReplaceAllString(line, "") + "\n")
	}
	saved, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("--- status " + blockString(blockObject(blockObject(saved.Get("blocks")).Get("B1")).Get("status")) + "\n")
	if grant, ok := saved.Get("autopilot").(ledger.Object); ok {
		b.WriteString("--- grant " + blockString(grant.Get("status")) + "\n")
	}
	text := b.String()
	for _, dir := range []string{f.top, f.home} {
		real, _ := filepath.EvalSymlinks(dir)
		name := map[string]string{f.top: "<WS>", f.home: "<HOME>"}[dir]
		text = strings.ReplaceAll(strings.ReplaceAll(text, real, name), dir, name)
	}
	text = b684Sha.ReplaceAllString(text, "<SHA>")
	return b612ID.ReplaceAllString(b612TS.ReplaceAllString(text, "<TS>"), "<ID>")
}

func TestB697AutoClaimCloseMatchesNode(t *testing.T) {
	b760NoNodeRecorder(t, "B697_RECORD_NODE")
	record := os.Getenv("B697_RECORD_NODE")
	for _, c := range b697ClaimCases {
		t.Run(c.name, func(t *testing.T) {
			f := b697ClaimFixture(t, c.until, c.setup)
			before := len(strings.Split(strings.TrimSpace(b493LedgerLines(t, f)), "\n"))
			args := "close|B1|--run|r-close-fixture|--report|" + f.report + "|--transcript|" + f.transcript + "|--auto-claim" + c.args
			golden := filepath.Join("testdata", "b697", c.name+".txt")
			var r hvResult
			if record != "" {
				nodeBin, err := exec.LookPath("node")
				if err != nil {
					t.Fatal(err)
				}
				driver := filepath.Join(t.TempDir(), "b697-driver.mjs")
				if err := os.WriteFile(driver, []byte(b680NodeDriver), 0o600); err != nil {
					t.Fatal(err)
				}
				asked := filepath.Join(t.TempDir(), "asked")
				cmd := exec.Command(nodeBin, append([]string{driver, record, c.ask, asked}, strings.Split(args, "|")...)...)
				cmd.Dir = f.top
				cmd.Env = []string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "PATH=" + filepath.Dir(nodeBin) + ":/usr/bin:/bin"}
				r = b680Exec(t, cmd, asked)
			} else {
				answer := humanask.Cancelled
				if c.ask == "yes" {
					answer = humanask.Confirmed
				}
				asked := b493HumanSeam(t, c.ask != "notty", answer)
				var out, errs bytes.Buffer
				var reason handoverReason
				r.code = closeFlagsStep(strings.Split(args, "|")[1:], &landStep{stdout: &out, stderr: &errs}, &reason)
				if reason != 0 {
					t.Fatalf("handed over (%v): exit %d stdout %q stderr %q", reason, r.code, out.String(), errs.String())
				}
				r.stdout, r.stderr = out.String(), errs.String()
				if c.ask != "notty" && len(*asked) > 0 {
					r.asked = (*asked)[len(*asked)-1]
				}
			}
			got := b697ClaimReport(t, f, r, before)
			if record != "" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}
