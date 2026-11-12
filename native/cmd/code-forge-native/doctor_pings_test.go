package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B523 goldens: testdata/b523/<name>.rows and <name>.argv are what Node's
// src/doctor/probes.mjs (pingRoles, probeIsolation, probePathDeny,
// probeCodexRules) returned and ran on testdata/b523/fake.sh (copied as
// claude and grok), with the paths replaced by <WORK>, <PROJECT>, <RUNROOT>,
// <RUNS>, random hex by <H> and timestamps by <T>.

type b523Env struct {
	dir, work, project, runRoot, runs, codexHome, log, bin string
}

func b523Setup(t *testing.T, doc string) b523Env {
	t.Helper()
	dir := t.TempDir()
	e := b523Env{dir: dir, work: filepath.Join(dir, "work"), project: filepath.Join(dir, "project"), runRoot: filepath.Join(dir, "runroot"),
		runs: filepath.Join(dir, "home", ".code-forge", "runs"), codexHome: filepath.Join(dir, "codexhome"), log: filepath.Join(dir, "argv.log"), bin: filepath.Join(dir, "bin")}
	for _, d := range []string{e.work, e.project, e.runRoot, e.runs, e.codexHome, e.bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fake, err := os.ReadFile("testdata/b523/fake.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "grok"} {
		if err := os.WriteFile(filepath.Join(e.bin, name), fake, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(e.log, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.codexHome, "auth.json"), []byte(`{"fake":"not-a-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	switch doc {
	case "doc":
		err = os.WriteFile(filepath.Join(e.project, "CLAUDE.md"), []byte("\n  # Secret project rules  \nmore\n"), 0o600)
	case "doc2":
		err = os.WriteFile(filepath.Join(e.project, "CLAUDE.md"), []byte("# Project rules for B523\n"), 0o600)
	case "agents":
		err = os.WriteFile(filepath.Join(e.project, "AGENTS.md"), []byte("agents first line\n"), 0o600)
	case "badutf8":
		err = os.WriteFile(filepath.Join(e.project, "CLAUDE.md"), []byte("rules \xff\xfe\n"), 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e b523Env) ctx(cfg map[string]any, mode string) doctorProbeCtx {
	environ := append(os.Environ(), "FAKE_MODE="+mode, "FAKE_LOG="+e.log, "CODEX_HOME="+e.codexHome, "HOME="+filepath.Join(e.dir, "home"))
	return doctorProbeCtx{cfg: cfg, cwd: e.project, workDir: e.work, runRootDir: e.runRoot, environ: environ,
		bins: map[string]string{"claude": filepath.Join(e.bin, "claude"), "grok": filepath.Join(e.bin, "grok")}, canary: "cf-canary-fixed0123"}
}

var (
	b523Stamp = regexp.MustCompile(`-\d{10,}-[0-9a-f]{8}`)
	b523Named = regexp.MustCompile(`(repo|facts|coder|reviewer|s2|delegate|isolation)-[0-9a-f]{8}`)
	b523Can   = regexp.MustCompile(`doctor-canary-[0-9a-f]{12}`)
)

func (e b523Env) argv(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.log)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, p := range [][2]string{{e.work, "<WORK>"}, {e.project, "<PROJECT>"}, {e.runRoot, "<RUNROOT>"}, {e.runs, "<RUNS>"}} {
		if real, err := filepath.EvalSymlinks(p[0]); err == nil {
			text = strings.ReplaceAll(text, real, p[1])
		}
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	text = b523Stamp.ReplaceAllString(text, "-<T>-<H>")
	text = b523Named.ReplaceAllString(text, "$1-<H>")
	return b523Can.ReplaceAllString(text, "doctor-canary-<H>")
}

func b523Rows(rows []doctorRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.status + " " + r.id + " | " + r.label + " | " + r.detail + "\n")
	}
	return b.String()
}

func b523Golden(t *testing.T, name, ext string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b523", name+"."+ext))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func b523Cfgs() map[string]map[string]any {
	lv := func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	return map[string]map[string]any{
		"anth": {"provider": "anthropic", "autopilot": map[string]any{}, "levels": map[string]any{
			"L0": lv("model", "claude-haiku-9"), "L1": lv("model", "claude-sonnet-9", "effort", "high"), "L2": lv("model", "claude-opus-9"), "L3": lv("model", "claude-opus-9", "effort", "max")}},
		"mixed": {"provider": "xai", "autopilot": map[string]any{}, "levels": map[string]any{
			"L0": lv("model", "grok-9-fast"), "L1": lv("model", "grok-9-code", "effort", "high"), "L2": lv("provider", "anthropic", "model", "claude-opus-9"), "L3": lv("provider", "openai", "model", "gpt-9")}},
		"grokAll": {"provider": "xai", "autopilot": map[string]any{"grants": []any{}}, "levels": map[string]any{
			"L0": lv("model", "grok-9-fast"), "L1": lv("model", "grok-9-code"), "L2": lv("model", "grok-9"), "L3": lv("model", "grok-9", "effort", "high")}},
		"odd": {"provider": "anthropic", "levels": map[string]any{
			"L0": lv("model", "claude-haiku-9", "effort", "ultra"), "L1": lv("effort", "low"), "L2": lv("model", "claude-opus-9", "provider", nil), "L3": lv("model", "m3", "provider", "OpenAI ")}},
		"codexL2": {"provider": "anthropic", "levels": map[string]any{
			"L0": lv("model", "a"), "L1": lv("model", "b"), "L2": lv("provider", "openai", "model", "gpt-9"), "L3": lv("model", "c")}},
	}
}

// B523: each probe's rows and the argv it ran, on the same fake CLIs, are
// byte-equal to Node's (pings: ok, 402, schema mismatch, failed, cli-missing,
// Codex refusal, TypeError/Error; isolation: no doc, clean, saw CLAUDE.md /
// AGENTS.md, 402, rejected, Codex refusal; path deny: OK, not honoured,
// inconclusive, 402; Codex rules: OK with 83 rules).
func TestB523DoctorProbesMatchNode(t *testing.T) {
	cfgs := b523Cfgs()
	cases := []struct {
		name, kind, cfg, mode, doc string
		missing                    []string
	}{
		{"ping-anth-ok", "ping", "anth", "ok", "", nil},
		{"ping-anth-402", "ping", "anth", "402", "", nil},
		{"ping-anth-mismatch", "ping", "anth", "mismatch", "", nil},
		{"ping-anth-fail", "ping", "anth", "fail", "", nil},
		{"ping-mixed-ok", "ping", "mixed", "ok", "", []string{"anthropic"}},
		{"ping-grok-ok", "ping", "grokAll", "ok", "", nil},
		{"ping-grok-mismatch", "ping", "grokAll", "mismatch", "", nil},
		{"ping-odd-ok", "ping", "odd", "ok", "", nil},
		{"iso-nodoc", "iso", "anth", "ok", "", nil},
		{"iso-ok", "iso", "anth", "ok", "doc", nil},
		{"iso-leak", "iso", "anth", "leak", "doc", nil},
		{"iso-claude-leak", "iso", "anth", "leak", "doc2", nil},
		{"iso-agents-leak", "iso", "grokAll", "leak", "agents", nil},
		{"iso-402", "iso", "anth", "402", "doc", nil},
		{"iso-fail", "iso", "anth", "fail", "doc", nil},
		{"iso-codex", "iso", "codexL2", "ok", "doc", nil},
		{"iso-odd", "iso", "odd", "ok", "doc", nil},
		{"deny-ok", "deny", "anth", "ok", "", nil},
		{"deny-leak", "deny", "anth", "leak", "", nil},
		{"deny-fail", "deny", "anth", "fail", "", nil},
		{"deny-402", "deny", "anth", "402", "", nil},
		{"deny-grok-leak", "deny", "grokAll", "leak", "", nil},
		{"deny-odd", "deny", "odd", "ok", "", nil},
		{"codex-rules", "codex", "anth", "ok", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := b523Setup(t, tc.doc)
			c := e.ctx(cfgs[tc.cfg], tc.mode)
			var rows []doctorRow
			var ok bool
			switch tc.kind {
			case "ping":
				missing := map[string]bool{}
				for _, p := range tc.missing {
					missing[p] = true
				}
				rows, ok = doctorPingRoles(c, missing)
			case "iso":
				rows, ok = doctorProbeIsolation(c)
			case "deny":
				rows, ok = doctorProbePathDeny(c, e.runs)
			default:
				rows, ok = doctorProbeCodexRules(c, filepath.Join(e.dir, "tmp", "proc-1"))
			}
			if !ok {
				t.Fatalf("handed over, want Node's rows")
			}
			if got, want := b523Rows(rows), b523Golden(t, tc.name, "rows"); got != want {
				t.Errorf("rows:\n%s\nwant (Node):\n%s", got, want)
			}
			if got, want := e.argv(t), b523Golden(t, tc.name, "argv"); got != want {
				t.Errorf("argv differs from Node's:\n%s\nwant:\n%s", got, want)
			}
			// nothing is left behind: prompt files, canary, session dirs
			for _, d := range []string{e.runs, filepath.Join(e.runRoot, "sessions")} {
				if left, _ := os.ReadDir(d); len(left) != 0 {
					t.Errorf("%s left %d entries", d, len(left))
				}
			}
			left, _ := os.ReadDir(e.work)
			for _, f := range left {
				if !strings.HasPrefix(f.Name(), "repo-") {
					t.Errorf("work dir left %s", f.Name())
				}
			}
		})
	}
}

// B523: the Codex rules probe writes the session home under
// <run root>/codex-homes (rules read-only, the fake auth copied) and removes
// it; it never touches the CODEX_HOME it copies from.
func TestB523DoctorCodexRulesHome(t *testing.T) {
	e := b523Setup(t, "")
	runRoot := filepath.Join(e.dir, "tmp", "proc-1")
	rows, ok := doctorProbeCodexRules(e.ctx(b523Cfgs()["anth"], "ok"), runRoot)
	if !ok || b523Rows(rows) != "OK codex-rules | codex rules | 83 execpolicy rules via CODEX_HOME=<session>/rules/code-forge.rules\n" {
		t.Fatalf("rows %q ok=%v", b523Rows(rows), ok)
	}
	if left, _ := os.ReadDir(filepath.Join(runRoot, "codex-homes")); len(left) != 0 {
		t.Errorf("codex-homes left %d entries", len(left))
	}
	if data, err := os.ReadFile(filepath.Join(e.codexHome, "auth.json")); err != nil || string(data) != `{"fake":"not-a-secret"}` {
		t.Errorf("the source auth.json changed: %q %v", data, err)
	}
	// a run root inside the real Codex home: the build refuses, WARN
	inside := filepath.Join(e.codexHome, "run")
	rows, ok = doctorProbeCodexRules(e.ctx(b523Cfgs()["anth"], "ok"), inside)
	if !ok || b523Rows(rows) != "WARN codex-rules | codex rules | codex: forbidden list is prose-only\n" {
		t.Fatalf("inside the real home: rows %q ok=%v", b523Rows(rows), ok)
	}
}

// B523: a rate-limited ping waits twice (2 s, 6 s backoff) and retries, then
// reports the last answer, as spawnSession's step loop does.
func TestB523DoctorPingRateLimited(t *testing.T) {
	e := b523Setup(t, "")
	cfg := map[string]any{"provider": "anthropic", "levels": map[string]any{"L0": map[string]any{"model": "claude-haiku-9"}}}
	c := e.ctx(cfg, "429")
	var waits []float64
	c.sleep = func(ms float64) { waits = append(waits, ms) }
	rows, ok := doctorPingRoles(c, map[string]bool{})
	if !ok {
		t.Fatal("handed over")
	}
	want := "FAIL ping.facts | ping facts (anthropic/claude-haiku-9) | ping=unavailable(rate-limited)\n" +
		"FAIL ping.coder | ping coder (anthropic/undefined) | ping=error(Error)\n" +
		"FAIL ping.reviewer | ping reviewer (anthropic/undefined) | ping=error(Error)\n" +
		"FAIL ping.s2 | ping s2 (anthropic/undefined) | ping=error(Error)\n"
	if got := b523Rows(rows); got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	if len(waits) != 2 || waits[0] < 1000 || waits[0] > 3000 || waits[1] < 3000 || waits[1] > 9000 {
		t.Errorf("waits %v", waits)
	}
	if n := strings.Count(e.argv(t), "=== claude"); n != 3 {
		t.Errorf("claude ran %d times, want 3", n)
	}
}

// B523: the cases Go does not port hand over before any session runs and
// before any probe file is written.
func TestB523DoctorProbesHandOver(t *testing.T) {
	codexCoder := map[string]any{"provider": "anthropic", "levels": map[string]any{"L0": map[string]any{"model": "a"}, "L1": map[string]any{"provider": "openai", "model": "gpt-9"}}}
	openBook := map[string]any{"provider": "anthropic", "review": map[string]any{"allow_open_book_codex": true},
		"levels": map[string]any{"L2": map[string]any{"provider": "openai", "model": "gpt-9"}}}
	cases := []struct {
		name, kind, doc string
		cfg             map[string]any
	}{
		{"codex coder ping", "ping", "", codexCoder},
		{"codex coder path deny", "deny", "", codexCoder},
		{"open-book codex reviewer ping", "ping", "", openBook},
		{"open-book codex isolation", "iso", "doc", openBook},
		{"levels not a map", "ping", "", map[string]any{"provider": "anthropic", "levels": []any{}}},
		{"level not a map", "deny", "", map[string]any{"provider": "anthropic", "levels": map[string]any{"L1": "x"}}},
		{"model not a scalar", "ping", "", map[string]any{"provider": "anthropic", "levels": map[string]any{"L0": map[string]any{"model": map[string]any{}}}}},
		{"fallback not a list", "iso", "doc", map[string]any{"provider": "anthropic", "levels": map[string]any{"L2": map[string]any{"model": "m", "fallback": "x"}}}},
		{"doc not UTF-8", "iso", "badutf8", b523Cfgs()["anth"]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := b523Setup(t, tc.doc)
			c := e.ctx(tc.cfg, "ok")
			var ok bool
			switch tc.kind {
			case "ping":
				_, ok = doctorPingRoles(c, map[string]bool{})
			case "iso":
				_, ok = doctorProbeIsolation(c)
			default:
				_, ok = doctorProbePathDeny(c, e.runs)
			}
			if ok {
				t.Fatal("ported, want a hand-over")
			}
			if data, _ := os.ReadFile(e.log); len(data) != 0 {
				t.Errorf("a CLI ran before the hand-over:\n%s", data)
			}
			for _, d := range []string{e.work, e.runs, e.runRoot} {
				if left, _ := os.ReadDir(d); len(left) != 0 {
					t.Errorf("%s has %d entries", d, len(left))
				}
			}
		})
	}
	// the open-book opt-in with no project doc: Node's INFO row, no session
	e := b523Setup(t, "")
	rows, ok := doctorProbeIsolation(e.ctx(openBook, "ok"))
	if !ok || b523Rows(rows) != "INFO isolation | isolation | skipped (the project has no CLAUDE.md or AGENTS.md to leak)\n" {
		t.Errorf("rows %q ok=%v", b523Rows(rows), ok)
	}
}
