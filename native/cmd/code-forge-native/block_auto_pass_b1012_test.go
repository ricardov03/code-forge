package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func b1012Edit(t *testing.T, f *closeReviewFixture, edit func(ledger.Object, ledger.Object)) {
	t.Helper()
	r, e := state.ReadRun(f.home, b632Run)
	if e != nil {
		t.Fatal(e)
	}
	b := blockObject(blockObject(r.Get("blocks")).Get("B1"))
	b.Set("transcript", f.transcript)
	edit(r, b)
	blocks := blockObject(r.Get("blocks"))
	blocks.Set("B1", b)
	r.Set("blocks", blocks)
	if e := state.SaveRun(f.home, r); e != nil {
		t.Fatal(e)
	}
}
func b1012Fixture(t *testing.T, status string) (*closeReviewFixture, string) {
	t.Helper()
	t.Setenv("CODE_FORGE_RUN_ID", "")
	_ = os.Unsetenv("CODE_FORGE_RUN_ID")
	f := b632Fixture(t, []any{"human-pass:evidence"}, []any{}, b632Screen)
	dir := t.TempDir()
	file := filepath.Join(dir, "proof.json")
	closeFixtureWrite(t, filepath.Join(dir, "shot.png"), []byte("evidence"))
	closeFixtureWrite(t, file, []byte(`{"block":"B1","run":"r-close-fixture","prover":"qa-session","created_at":"2026-11-20T00:00:00Z","all_green":true,"checks":[{"clause":"C2","result":"pass","evidence":["shot.png"]}]}`))
	r, e := state.ReadRun(f.home, b632Run)
	if e != nil {
		t.Fatal(e)
	}
	cfg, _ := blockSnapshot(r)
	cfg["human_check"] = map[string]any{"autopilot": map[string]any{"enabled": true, "require_proof": true, "proof_dirs": []any{dir}, "require_separate_prover": true}}
	data, _ := json.Marshal(cfg)
	obj, _ := ledger.DecodeObject(data)
	b493SetConfig(t, f, obj)
	b1012Edit(t, f, func(r, b ledger.Object) { b.Set("status", status); b.Set("transcript", f.transcript) })
	return f, file
}
func b1012Call(t *testing.T, file string, extra ...string) (int, string, string) {
	t.Helper()
	return runCaptured(t, append([]string{"block", "auto-pass", "B1", "--run", b632Run, "--proof", file, "--note", "checked"}, extra...)...)
}
func TestB1012AutoPass(t *testing.T) {
	for _, status := range []string{"awaiting_human_check", "closed_deferred_human"} {
		t.Run(status, func(t *testing.T) {
			f, file := b1012Fixture(t, status)
			code, out, err := b1012Call(t, file, "--json")
			if code != 0 {
				t.Fatalf("%d %s %s", code, out, err)
			}
			if b632Status(t, f) != "closed" {
				t.Fatal("not closed")
			}
			rows := b632Rows(t, f, "human_check.cleared")
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			r := rows[0]
			if r["by"] != "autopilot" || r["passed_by"] != "autopilot" || r["grant_id"] != "g-1" || r["decision_id"] == nil || r["ts"] == nil {
				t.Fatal(r)
			}
			data, _ := os.ReadFile(file)
			if r["proof_sha256"] != blockAutoPassTestHash(data) {
				t.Fatal(r)
			}
			key, e := blockclose.ReadKey(f.home, b632Run)
			if e != nil {
				t.Fatal(e)
			}
			record, e := state.ReadRun(f.home, b632Run)
			if e != nil {
				t.Fatal(e)
			}
			all, e := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
			if e != nil || len(all) == 0 {
				t.Fatalf("ledger: %v rows=%d", e, len(all))
			}
			if len(humancheck.Open(b632Run, record, all, key)) != 0 {
				t.Fatal("checks remain open")
			}
			if _, e := os.Stat(filepath.Join(f.home, ".code-forge/runs", b632Run, "blocks/B1.done.json")); e != nil {
				t.Fatal(e)
			}
			decisions := b632Rows(t, f, "autopilot.decision")
			if len(decisions) != 1 || decisions[0]["scope"] != "human-pass:evidence" || decisions[0]["to_owner"] != true {
				t.Fatal(decisions)
			}
		})
	}
}

func blockAutoPassTestHash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func b1012Config(t *testing.T, f *closeReviewFixture, edit func(map[string]any)) {
	t.Helper()
	r, _ := state.ReadRun(f.home, b632Run)
	cfg, _ := blockSnapshot(r)
	edit(cfg)
	data, _ := json.Marshal(cfg)
	obj, _ := ledger.DecodeObject(data)
	b493SetConfig(t, f, obj)
}
func b1012Record(t *testing.T, f *closeReviewFixture) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(f.home, ".code-forge/runs", b632Run+".json"))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestB1012Refusals(t *testing.T) {
	cases := []struct {
		name, want string
		edit       func(*testing.T, *closeReviewFixture, *string)
		args       []string
	}{
		{"off", "human-pass-evidence-off", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Config(t, f, func(c map[string]any) { delete(c, "human_check") })
		}, nil},
		{"ended", "auto-pass-run", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) { r.Set("status", "ended") })
		}, nil},
		{"no grant", "auto-pass-grant", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) { r.Set("autopilot", nil) })
		}, nil},
		{"expired", "auto-pass-grant", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) {
				g := blockObject(r.Get("autopilot"))
				g.Set("until", "2000-01-01T00:00:00Z")
				r.Set("autopilot", g)
			})
		}, nil},
		{"denied", "auto-pass-grant", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) {
				g := blockObject(r.Get("autopilot"))
				g.Set("deny", []any{"human-pass:evidence"})
				r.Set("autopilot", g)
			})
		}, nil},
		{"no scope", "auto-pass-grant", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) {
				g := blockObject(r.Get("autopilot"))
				g.Set("scopes", []any{"close:routine"})
				r.Set("autopilot", g)
			})
		}, nil},
		{"unsigned grant", "auto-pass-signed-grant", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) {
				g := blockObject(r.Get("autopilot"))
				g.Set("grant_id", "no-signed-row")
				r.Set("autopilot", g)
			})
		}, nil},
		{"wrong status", "auto-pass-status", func(t *testing.T, f *closeReviewFixture, _ *string) {
			b1012Edit(t, f, func(r, b ledger.Object) { b.Set("status", "open") })
		}, nil},
		{"missing", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) {
			*p = filepath.Join(filepath.Dir(*p), "missing.json")
		}, nil},
		{"directory", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) { *p = filepath.Dir(*p) }, nil},
		{"empty", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) { closeFixtureWrite(t, *p, nil) }, nil},
		{"fifo", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) {
			*p = filepath.Join(filepath.Dir(*p), "fifo.json")
			if e := syscall.Mkfifo(*p, 0600); e != nil {
				t.Fatal(e)
			}
		}, nil},
		{"symlink escape", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) {
			outside := filepath.Join(t.TempDir(), "outside.json")
			data, _ := os.ReadFile(*p)
			closeFixtureWrite(t, outside, data)
			*p = filepath.Join(filepath.Dir(*p), "link.json")
			if e := os.Symlink(outside, *p); e != nil {
				t.Fatal(e)
			}
		}, nil},
		{"worktree", "proof is inside the project worktree", func(t *testing.T, f *closeReviewFixture, p *string) {
			data, _ := os.ReadFile(*p)
			*p = filepath.Join(f.top, "proof.json")
			closeFixtureWrite(t, *p, data)
			closeFixtureWrite(t, filepath.Join(f.top, "shot.png"), []byte("evidence"))
			b1012Config(t, f, func(c map[string]any) { blockMap(blockMap(c["human_check"])["autopilot"])["proof_dirs"] = []any{f.top} })
		}, nil},
		{"export", "auto-pass-proof", func(t *testing.T, f *closeReviewFixture, p *string) {
			data, _ := os.ReadFile(*p)
			root := filepath.Dir(*p)
			*p = filepath.Join(root, ".code-forge/export/proof.json")
			closeFixtureWrite(t, *p, data)
		}, nil},
		{"self proof", "auto-pass-self-proof", func(t *testing.T, f *closeReviewFixture, p *string) {
			b493Sign(t, f, ledger.Object{{Key: "event", Value: "session"}, {Key: "block", Value: "B1"}, {Key: "role", Value: "coder"}, {Key: "session_id", Value: "qa-session"}})
		}, nil},
		{"waiver", "auto-pass-waived", func(t *testing.T, f *closeReviewFixture, p *string) {
			b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "grant_id", Value: "g-1"}})
		}, nil},
		{"accepted hit", "auto-pass-transcript", func(t *testing.T, f *closeReviewFixture, p *string) {
			b493Sign(t, f, ledger.Object{{Key: "event", Value: "rule_break.accepted"}, {Key: "block", Value: "B1"}})
		}, nil},
		{"finding", "auto-pass-machine", func(t *testing.T, f *closeReviewFixture, p *string) {
			if e := f.result(f.parts[0], "findings"); e != nil {
				t.Fatal(e)
			}
		}, nil},
		{"no green", "no signed green gate row", func(t *testing.T, f *closeReviewFixture, p *string) {
			file := filepath.Join(f.home, ".code-forge/ledger/fixture.jsonl")
			data, _ := os.ReadFile(file)
			lines := strings.Split(string(data), "\n")
			kept := []string{}
			for _, l := range lines {
				var r map[string]any
				_ = json.Unmarshal([]byte(l), &r)
				if r["event"] != "gate.done" {
					kept = append(kept, l)
				}
			}
			closeFixtureWrite(t, file, []byte(strings.Join(kept, "\n")))
		}, nil},
		{"blank note", "auto-pass-note", nil, []string{"--note", "   "}},
		{"control note", "auto-pass-note", nil, []string{"--note", "checked\npass"}},
		{"unknown clause", "auto-pass-design", nil, []string{"--clause", "C99"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, file := b1012Fixture(t, "awaiting_human_check")
			if tc.edit != nil {
				tc.edit(t, f, &file)
			}
			before := b1012Record(t, f)
			lines := b493LedgerLines(t, f)
			args := []string{"block", "auto-pass", "B1", "--run", b632Run, "--proof", file, "--note", "checked"}
			if len(tc.args) > 0 && tc.args[0] == "--note" {
				args = args[:len(args)-2]
			}
			args = append(args, tc.args...)
			code, out, err := runCaptured(t, args...)
			if code != 1 || !strings.Contains(err, tc.want) {
				t.Fatalf("%d %s %s; want %s", code, out, err, tc.want)
			}
			if b1012Record(t, f) != before || b493LedgerLines(t, f) != lines {
				t.Fatal("refusal wrote run or ledger")
			}
		})
	}
}
func TestB1012DryRun(t *testing.T) {
	f, file := b1012Fixture(t, "awaiting_human_check")
	before := b1012Record(t, f)
	lines := b493LedgerLines(t, f)
	for _, path := range []string{file, file + "missing"} {
		code, out, err := b1012Call(t, path, "--dry-run", "--json")
		if (path == file) != (code == 0) {
			t.Fatalf("%d %s %s", code, out, err)
		}
		var obj map[string]any
		if e := json.Unmarshal([]byte(out), &obj); e != nil {
			t.Fatal(e)
		}
		if len(obj["checks"].([]any)) != 12 {
			t.Fatal(out)
		}
	}
	if b1012Record(t, f) != before || b493LedgerLines(t, f) != lines {
		t.Fatal("dry run wrote")
	}
}
func TestB1012DesignPartial(t *testing.T) {
	for _, approval := range []string{"design", "heuristic", "excluded"} {
		t.Run(approval, func(t *testing.T) {
			f, file := b1012Fixture(t, "awaiting_human_check")
			b1012Edit(t, f, func(r, b ledger.Object) {
				a := b.Get("acceptance").([]any)
				c := ledger.Object{{Key: "clause", Value: "owner approves board design"}, {Key: "kind", Value: "human_check"}}
				if approval == "design" {
					c.Set("approval", "design")
				}
				if approval == "excluded" {
					c.Set("clause", "external check")
					c.Set("approval", "device")
					c.Set("waits_for", "owner check")
				}
				b.Set("acceptance", append(a, c))
			})
			if approval == "excluded" {
				b1012Config(t, f, func(c map[string]any) {
					blockMap(blockMap(c["human_check"])["autopilot"])["exclude_waits_for"] = []any{"owner check"}
				})
			}
			before := b493LedgerLines(t, f)
			code, _, err := b1012Call(t, file, "--clause", "C3")
			if code != 1 || !strings.Contains(err, "auto-pass-design: clause C3") {
				t.Fatal(code, err)
			}
			if before != b493LedgerLines(t, f) {
				t.Fatal("design wrote")
			}
			code, out, err := b1012Call(t, file)
			if code != 0 || b632Status(t, f) != "awaiting_human_check" {
				t.Fatal(code, out, err)
			}
			rows := b632Rows(t, f, "human_check.cleared")
			if len(rows) != 1 || rows[0]["clause"] != "C2" {
				t.Fatal(rows)
			}
			if len(b632Rows(t, f, "block.human_pass")) != 0 {
				t.Fatal("partial wrote whole-block pass")
			}
		})
	}
}

func TestB1012ProofFormats(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(string) string
	}{
		{"html", "", func(s string) string {
			return `<html><script id="code-forge-proof" type="application/json">` + s + `</script></html>`
		}},
		{"wrong run", "header block/run must match", func(s string) string { return strings.Replace(s, `"run":"r-close-fixture"`, `"run":"other"`, 1) }},
		{"wrong block", "header block/run must match", func(s string) string { return strings.Replace(s, `"block":"B1"`, `"block":"B2"`, 1) }},
		{"red", "header block/run must match and all_green must be true", func(s string) string { return strings.Replace(s, `"all_green":true`, `"all_green":false`, 1) }},
		{"failed check", "check C2 must pass and name evidence", func(s string) string { return strings.Replace(s, `"result":"pass"`, `"result":"fail"`, 1) }},
		{"missing check", "missing passing check C2", func(s string) string { return strings.Replace(s, `"clause":"C2"`, `"clause":"C3"`, 1) }},
		{"missing evidence", "check C2 evidence is missing or escapes the proof directory", func(s string) string { return strings.Replace(s, `shot.png`, `missing.png`, 1) }},
		{"escape evidence", "check C2 evidence is missing or escapes the proof directory", func(s string) string { return strings.Replace(s, `shot.png`, `../outside.png`, 1) }},
		{"bad created_at", "header created_at must be ISO-8601", func(s string) string { return strings.Replace(s, `2026-11-20T00:00:00Z`, `yesterday`, 1) }},
		{"blank prover", "header prover must name a session", func(s string) string { return strings.Replace(s, `qa-session`, ``, 1) }},
		{"trailing JSON", "invalid proof JSON header", func(s string) string { return s + `{}` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, file := b1012Fixture(t, "awaiting_human_check")
			data, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			if tc.name == "escape evidence" {
				closeFixtureWrite(t, filepath.Join(filepath.Dir(filepath.Dir(file)), "outside.png"), []byte("outside evidence exists"))
			}
			if tc.name == "html" {
				file = filepath.Join(filepath.Dir(file), "proof.html")
			}
			closeFixtureWrite(t, file, []byte(tc.change(string(data))))
			before := b493LedgerLines(t, f)
			record := b1012Record(t, f)
			code, out, err := b1012Call(t, file)
			if tc.want == "" {
				if code != 0 {
					t.Fatal(code, out, err)
				}
			} else {
				if code != 1 || !strings.Contains(err, "auto-pass-proof: "+tc.want) {
					t.Fatal(code, out, err)
				}
				if before != b493LedgerLines(t, f) || record != b1012Record(t, f) {
					t.Fatal("bad proof wrote")
				}
			}
		})
	}
}
func TestB1012Grant(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f, _ := b1012Fixture(t, "awaiting_human_check")
			b1012Edit(t, f, func(r, b ledger.Object) {
				g := blockObject(r.Get("autopilot"))
				g.Set("status", "stopped")
				r.Set("autopilot", g)
			})
			if !enabled {
				b1012Config(t, f, func(c map[string]any) { delete(c, "human_check") })
			}
			before := b493LedgerLines(t, f)
			code, out, err := autopilotCall(t, "start", "--run", b632Run, "--until", autopilotStamp(autopilotNow().Add(time.Hour)), "--delegate", "L2", "--allow", "human-pass:evidence", "--yes")
			if enabled {
				if code != 0 {
					t.Fatal(code, out, err)
				}
			} else {
				if code != 1 || !strings.Contains(err, "human-pass-evidence-off") || b493LedgerLines(t, f) != before {
					t.Fatal(code, out, err)
				}
			}
		})
	}
	_, e := autopilotValidate(autopilotFlags{values: map[string]string{"until": autopilotStamp(autopilotNow().Add(time.Hour)), "delegate": "L2", "allow": "design:approve"}}, autopilotNow())
	if e == nil || !strings.Contains(e.Error(), "fixed deny") {
		t.Fatal(e)
	}
}
func TestB1012LockRecheck(t *testing.T) {
	for _, kind := range []string{"grant", "proof", "status", "config", "finding", "note-independent"} {
		t.Run(kind, func(t *testing.T) {
			f, file := b1012Fixture(t, "awaiting_human_check")
			old := autoPassBeforeUpdate
			t.Cleanup(func() { autoPassBeforeUpdate = old })
			var afterRecord, afterLedger string
			autoPassBeforeUpdate = func() {
				switch kind {
				case "grant":
					b1012Edit(t, f, func(r, b ledger.Object) {
						g := blockObject(r.Get("autopilot"))
						g.Set("status", "stopped")
						r.Set("autopilot", g)
					})
				case "proof":
					closeFixtureWrite(t, file, []byte(`{}`))
				case "status":
					b1012Edit(t, f, func(r, b ledger.Object) { b.Set("status", "open") })
				case "config":
					b1012Config(t, f, func(c map[string]any) { delete(c, "human_check") })
				case "finding":
					if e := f.result(f.parts[0], "findings"); e != nil {
						t.Fatal(e)
					}
				case "note-independent":
					b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "grant_id", Value: "g-1"}})
				}
				afterRecord = b1012Record(t, f)
				afterLedger = b493LedgerLines(t, f)
			}
			code, out, err := b1012Call(t, file)
			want := map[string]string{"grant": "auto-pass-grant", "proof": "auto-pass-proof", "status": "auto-pass-status", "config": "human-pass-evidence-off", "finding": "auto-pass-machine", "note-independent": "auto-pass-waived"}[kind]
			if code != 1 || !strings.Contains(err, want) {
				t.Fatal(code, out, err)
			}
			if afterRecord != b1012Record(t, f) || afterLedger != b493LedgerLines(t, f) {
				t.Fatal("authoritative refusal wrote")
			}
		})
	}
}
func TestB1012HumanGuards(t *testing.T) {
	f, _ := b1012Fixture(t, "awaiting_human_check")
	old := humanTTY
	humanTTY = func() bool { return false }
	t.Cleanup(func() { humanTTY = old })
	before := b493LedgerLines(t, f)
	code, _, err := runCaptured(t, "block", "human-pass", "B1", "--run", b632Run, "--note", "checked")
	if code != 2 || !strings.Contains(err, "no --yes") || b493LedgerLines(t, f) != before {
		t.Fatal(code, err)
	}
}
func TestB1012HookAndEvidence(t *testing.T) {
	f, file := b1012Fixture(t, "awaiting_human_check")
	hook := filepath.Join(t.TempDir(), "B1012-hook")
	b1012Config(t, f, func(c map[string]any) {
		c["hooks"] = map[string]any{"on_block_end": map[string]any{"argv": []any{"sh", "-c", `printf 'called\n' >> "$1"`, "hook", hook}}}
	})
	code, out, err := b1012Call(t, file)
	if code != 0 {
		t.Fatal(code, out, err)
	}
	b, e := os.ReadFile(hook)
	if e != nil || string(b) != "called\n" {
		t.Fatal(string(b), e)
	}
	if code, _, _ := b1012Call(t, file); code != 1 {
		t.Fatal("second pass accepted")
	}
	b, _ = os.ReadFile(hook)
	if string(b) != "called\n" {
		t.Fatal("hook ran twice")
	}
	rows := b632Rows(t, f, "human_check.cleared")
	evidence := rows[0]["evidence"].([]any)
	if len(evidence) != 1 {
		t.Fatal(evidence)
	}
	ev := evidence[0].(map[string]any)
	if ev["sha256"] != blockAutoPassTestHash([]byte("evidence")) {
		t.Fatal(ev)
	}
}

func TestB1012OpenScope(t *testing.T) {
	t.Setenv("CODE_FORGE_RUN_ID", "")
	_ = os.Unsetenv("CODE_FORGE_RUN_ID")
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f, _ := b581Fixture(t)
			b470SetRun(t, f, func(record, _ *ledger.Object) {
				record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"human-pass:evidence"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}, {Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
				if enabled {
					cfg, _ := blockSnapshot(*record)
					if cfg == nil {
						cfg = map[string]any{}
					}
					cfg["human_check"] = map[string]any{"autopilot": map[string]any{"enabled": true, "proof_dirs": []any{"/proof"}}}
					data, _ := json.Marshal(cfg)
					obj, _ := ledger.DecodeObject(data)
					canonical, _ := canon.Marshal(obj)
					hash := blockAutoPassTestHash(canonical)
					record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: obj}}}})
				}
			})
			b581Level(t, f)
			acc := filepath.Join(f.home, "B1012-human.yml")
			closeFixtureWrite(t, acc, []byte("- clause: c\n  tests: [t]\n- clause: phone check\n  kind: human_check\n"))
			res := b451Run(t, f.home, append([]string{"open"}, b581Open(acc)...)...)
			if enabled {
				if res.code != 0 {
					t.Fatal(res.code, res.stderr)
				}
			} else {
				if res.code != 1 || !strings.Contains(res.stderr, "autopilot-human-check") {
					t.Fatal(res.code, res.stderr)
				}
			}
		})
	}
}
func TestB1012ExplicitDevice(t *testing.T) {
	f, file := b1012Fixture(t, "awaiting_human_check")
	b1012Edit(t, f, func(r, b ledger.Object) {
		a := b.Get("acceptance").([]any)
		c := blockObject(a[1])
		c.Set("clause", "design on board looks right")
		c.Set("approval", "device")
		a[1] = c
		b.Set("acceptance", a)
	})
	code, out, err := b1012Call(t, file, "--clause", "C2")
	if code != 0 {
		t.Fatal(code, out, err)
	}
}

func TestB1012WaiverWithoutGrantID(t *testing.T) {
	f, file := b1012Fixture(t, "awaiting_human_check")
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: "B1"}, {Key: "by", Value: "human"}})
	before := b493LedgerLines(t, f)
	code, out, err := b1012Call(t, file)
	if code != 1 || !strings.Contains(err, "auto-pass-waived") || b493LedgerLines(t, f) != before {
		t.Fatal(code, out, err)
	}
}

func TestB1012ProofSafety(t *testing.T) {
	for _, kind := range []string{"invalid-dir", "mixed-case-export", "require-proof"} {
		t.Run(kind, func(t *testing.T) {
			f, file := b1012Fixture(t, "awaiting_human_check")
			want := ""
			switch kind {
			case "invalid-dir":
				b1012Config(t, f, func(c map[string]any) {
					ap := blockMap(blockMap(c["human_check"])["autopilot"])
					ap["proof_dirs"] = append(ap["proof_dirs"].([]any), nil)
				})
				want = "proof_dirs must contain non-blank"
			case "require-proof":
				b1012Config(t, f, func(c map[string]any) { blockMap(blockMap(c["human_check"])["autopilot"])["require_proof"] = false })
				want = "require_proof must be true"
			case "mixed-case-export":
				if runtime.GOOS != "darwin" {
					t.Skip("case-insensitive export spelling on darwin")
				}
				data, e := os.ReadFile(file)
				if e != nil {
					t.Fatal(e)
				}
				file = filepath.Join(filepath.Dir(file), ".CODE-FORGE/EXPORT/proof.json")
				closeFixtureWrite(t, file, data)
				closeFixtureWrite(t, filepath.Join(filepath.Dir(file), "shot.png"), []byte("evidence"))
				want = "proof is inside .code-forge/export"
			}
			before := b493LedgerLines(t, f)
			record := b1012Record(t, f)
			code, out, err := b1012Call(t, file)
			if code != 1 || !strings.Contains(err, want) || before != b493LedgerLines(t, f) || record != b1012Record(t, f) {
				t.Fatal(code, out, err)
			}
		})
	}
}

func TestB1012WorktreeCaseAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case alias on darwin")
	}
	f, file := b1012Fixture(t, "awaiting_human_check")
	data, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	file = filepath.Join(f.top, "proof.json")
	closeFixtureWrite(t, file, data)
	closeFixtureWrite(t, filepath.Join(f.top, "shot.png"), []byte("evidence"))
	alias := strings.ToUpper(f.top)
	if _, e := os.Stat(alias); e != nil {
		t.Skip("filesystem is case sensitive")
	}
	b1012Config(t, f, func(c map[string]any) { blockMap(blockMap(c["human_check"])["autopilot"])["proof_dirs"] = []any{alias} })
	before := b493LedgerLines(t, f)
	code, out, err := b1012Call(t, filepath.Join(alias, "proof.json"))
	if code != 1 || !strings.Contains(err, "proof is inside the project worktree") || before != b493LedgerLines(t, f) {
		t.Fatal(code, out, err)
	}
}
