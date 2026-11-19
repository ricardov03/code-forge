package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// b781StartOn writes cfg as the workspace config and makes it the run's
// start snapshot, as a run started on that config has it.
func b781StartOn(t *testing.T, f b536Fixture, cfg string) {
	t.Helper()
	file := filepath.Join(f.ws, ".code-forge.yml")
	if err := os.WriteFile(file, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, order, err := config.ParseTextOrdered(cfg, file)
	if err != nil {
		t.Fatal(err)
	}
	recordFile := filepath.Join(f.home, ".code-forge", "runs", "r-fix.json")
	data, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.ParseObject(data)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b7", 32)
	record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: order.Object(parsed)}}}, {Key: "pins", Value: ledger.Object{}}})
	out, err := ledger.Stringify(record, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordFile, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// B781: approve on the customer's config (comment lines, nested review:
// maps, review.routes as a block list of mappings) handed over
// (autopilot-approve-yaml-shape), a dead end without Node. It now writes
// the change in place, keeps every other line, and the restore puts the
// old value back byte for byte.
func TestB781ApproveProofTiersOnCustomerConfig(t *testing.T) {
	f := b536Setup(t)
	cfg := strings.Replace(b781CustomerConfig, "      paths: []\n", "      paths:\n        - Sources/Payments/**\n", 1)
	b781StartOn(t, f, cfg)
	until := "2026-11-11T11:00:00.000Z"
	code, out, errText := b536Approve(t, "proof.tiers", "{}", until)
	want := "autopilot approve run r-fix: proof.tiers changed until " + until + " (approval apr-0000aaa1); the old value comes back then\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("approve = %d %q %q, want 0 %q", code, out, errText, want)
	}
	approved := strings.Replace(cfg, "  tiers:\n    high:\n      paths:\n        - Sources/Payments/**\n", "  tiers: {}\n", 1)
	if got := f.config(t); got != approved {
		t.Fatalf(".code-forge.yml after approve:\n%s\nwant:\n%s", got, approved)
	}
	f.at(until)
	if code, _, errText := autopilotCall(t, "status", "--run", "r-fix"); code != 0 || errText != "autopilot: approval apr-0000aaa1 expired; the old value is back\n" {
		t.Fatalf("restore = %d %q", code, errText)
	}
	if got := f.config(t); got != cfg {
		t.Fatalf(".code-forge.yml after restore:\n%s\nwant the start:\n%s", got, cfg)
	}
	f.noHandover(t)
}

// A shape no edit can serve safely (an anchor) is refused with what to
// change by hand, not handed over.
func TestB781ApproveRefusesUnsafeShape(t *testing.T) {
	f := b536Setup(t)
	cfg := strings.Replace(b781CustomerConfig, "  close_wait_s: 1\n", "  close_wait_s: &wait 1\n  max_rounds_per_file: *wait\n", 1)
	b781StartOn(t, f, cfg)
	code, out, errText := b536Approve(t, "proof.tiers", "{}", "2026-11-11T11:00:00.000Z")
	if code == 0 || out != "" || !strings.Contains(errText, "cannot be changed safely") || !strings.Contains(errText, "proof.tiers") || !strings.Contains(errText, "by hand") || !strings.Contains(errText, "an anchor, alias or tag") {
		t.Fatalf("approve = %d %q %q; want a refusal saying what to change by hand", code, out, errText)
	}
	if got := f.config(t); got != cfg {
		t.Fatalf("a refusal must not change the config:\n%s", got)
	}
	f.noHandover(t)
}

// The B783 audit configs: approve of a scalar (review.close_wait_s) and of
// a map (proof.tiers) on each config. 0.25.0 handed over
// (autopilot-approve-yaml-shape or autopilot-approve-reload-shape) on flow
// lists (proof.runners argv), a fallback list of maps, gates.custom,
// review.routes, on_unavailable, flow maps, anchors, a comment holding
// "!!" and coder.sandbox.add_dirs. Now each config the run can use is
// changed; the others get a refusal before the prompt that says why.
func TestB781ApproveAuditConfigs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "gatecheck", "testdata", "b781", "*.yml"))
	if err != nil || len(files) != 49 {
		t.Fatalf("%d configs (%v)", len(files), err)
	}
	refused := map[string]string{
		"07-review-judge-undoc.yml":     "fails the config schema (/review must NOT have additional property \"judge\"",
		"09-mode-consensus-undoc.yml":   "fails the config schema (/review must NOT have additional property \"mode\"",
		"14-level-no-tools-undoc.yml":   "fails the config schema (/levels/L3 must NOT have additional property \"no_tools\"",
		"08-multimodel.yml":             "is invalid (2 errors); nothing was changed; run code-forge validate\n  [multimodel-missing-second-provider]",
		"10-proof-runners.yml":          "is invalid (2 errors); nothing was changed; run code-forge validate\n  [proof-runners-invalid] proof.runners[0].argv needs a {report} item",
		"27-on-unavailable.yml":         "is invalid (1 error); nothing was changed; run code-forge validate\n  [review-on-unavailable-invalid] review.on_unavailable[0].model is not a known \"openai\" id",
		"46-l2-l3-same-model.yml":       "is invalid (1 error); nothing was changed; run code-forge validate\n  [duplicate-level-tuple]",
		"99-real-team.yml":              "is invalid (3 errors)",
		"23-yaml-anchors.yml":           "cannot be changed safely in .code-forge.yml (it holds an anchor, alias or tag)",
		"24-yaml-anchors-schema-ok.yml": "cannot be changed safely in .code-forge.yml (it holds an anchor, alias or tag)",
	}
	for _, file := range files {
		name := filepath.Base(file)
		if name == "45-duplicate-key.yml" {
			continue // no run starts on a config that does not load (DUPLICATE_KEY)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []struct{ key, value string }{{"review.close_wait_s", "5"}, {"proof.tiers", "{}"}} {
			t.Run(name+"/"+step.key, func(t *testing.T) {
				f := b536Setup(t)
				b781StartOn(t, f, string(data))
				code, out, errText := b536Approve(t, step.key, step.value, "2026-11-11T11:00:00.000Z")
				if hs := f.handovers(t); len(hs) > 0 {
					t.Fatalf("handed over %v (code=%d err=%q)", hs, code, errText)
				}
				if want, ok := refused[name]; ok {
					if code != 1 || out != "" || !strings.Contains(errText, want) || f.config(t) != string(data) {
						t.Fatalf("code=%d out=%q err=%q; want a refusal holding %q and the file unchanged", code, out, errText, want)
					}
					return
				}
				if code != 0 || !strings.HasPrefix(out, "autopilot approve run r-fix: "+step.key+" changed until ") || errText != "" {
					t.Fatalf("code=%d out=%q err=%q; want the change", code, out, errText)
				}
				if f.config(t) == string(data) {
					t.Fatal("the config did not change")
				}
			})
		}
	}
}
