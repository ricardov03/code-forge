package main

// B523: the doctor's model-call probes (src/doctor/probes.mjs pingRoles,
// probeIsolation, probePathDeny, probeCodexRules) as functions that return
// Node's rows; the full doctor run calls them (B525). Each function returns
// ok=false before it starts any session or writes any file when a case is not
// ported (a Codex session, a config shape whose row text Go does not
// reproduce); planOnly runs only those decisions.

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/session"
)

// doctorProbeCtx is probes.mjs ProbeCtx.
type doctorProbeCtx struct {
	cfg        map[string]any
	cwd        string // the project directory
	workDir    string // a private directory for probe files
	runRootDir string
	environ    []string
	bins       map[string]string // replaces each CLI (tests: fakes)
	canary     string            // the path-deny canary (tests); random otherwise
	sleep      func(ms float64)  // the rate-limit wait (tests); nil = real time
	// planOnly (B525): decide whether the probe can run in Go and stop
	// before the first file write or session; rows are nil then.
	planOnly bool
}

const (
	doctorProbeTimeoutMs = 180000
	doctorProbeBudgetUsd = 0.5
	doctorPingText       = "code-forge doctor ping.\nThis is a connectivity check; do not use any tool and do not edit any file.\nReply with the single word: pong\n"
)

var doctorPingAnswer = map[string]string{
	"s2":       "Answer with decision \"proceed\", confidence 1, reason \"ping\", overrule false, ask_human false, human_question null.\n",
	"delegate": "Answer with decision \"proceed\", within_scope true, confidence 1, reason \"ping\", escalate false.\n",
}

func doctorRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c doctorProbeCtx) deps() session.Deps {
	return session.Deps{Bins: c.bins, Environ: c.environ, Stderr: io.Discard, Sleep: c.sleep}
}

func (c doctorProbeCtx) openBook() bool {
	review, _ := c.cfg["review"].(map[string]any)
	return review["allow_open_book_codex"] == true
}

// doctorProbeLevel is cfg.levels?.[level] for the probes: the level map, or
// ok=false for a shape whose rows Go does not reproduce (levels or the level
// not a map, a fallback that is not a list).
func doctorProbeLevel(cfg map[string]any, level string) (map[string]any, bool) {
	raw, has := cfg["levels"]
	if !has || raw == nil {
		return nil, true
	}
	levels, isMap := raw.(map[string]any)
	if !isMap {
		return nil, false
	}
	lraw, has := levels[level]
	if !has || lraw == nil {
		return nil, true
	}
	l, isMap := lraw.(map[string]any)
	if !isMap {
		return nil, false
	}
	if fb, has := l["fallback"]; has && fb != nil {
		if _, isList := fb.([]any); !isList {
			return nil, false
		}
	}
	return l, true
}

// doctorProbeValue is `x ?? y` read as `${…}` for a provider or model value.
func doctorProbeValue(m map[string]any, key string) (any, bool) {
	v, has := m[key]
	return v, has
}

// doctorLevelProvider is `levels[level]?.provider ?? cfg.provider` (absent=false).
func doctorLevelProvider(cfg, l map[string]any) (any, bool) {
	if v, has := doctorProbeValue(l, "provider"); has && v != nil {
		return v, true
	}
	return doctorProbeValue(cfg, "provider")
}

// doctorLevelName is levelName(cfg, level): `${provider}/${model}`.
func doctorLevelName(cfg, l map[string]any) (string, bool) {
	part := func(v any, has bool) (string, bool) {
		if !has {
			return "undefined", true
		}
		return jsString(v)
	}
	p, ok1 := part(doctorLevelProvider(cfg, l))
	m, ok2 := part(doctorProbeValue(l, "model"))
	return p + "/" + m, ok1 && ok2
}

// doctorTempRepo is probes.mjs tempRepo: a fresh git repo for coder sessions.
func doctorTempRepo(c doctorProbeCtx) (string, error) {
	repo := filepath.Join(c.workDir, "repo-"+doctorRandHex(4))
	if err := os.MkdirAll(repo, 0o700); err != nil {
		return "", err
	}
	session.ProbeGitInit(repo, c.environ)
	return repo, nil
}

// doctorProbeSession is probes.mjs session(): the prompt file, one session,
// the file removed. err = hand over.
func doctorProbeSession(c doctorProbeCtx, role, level, prompt, schema, cwd string) (session.ProbeResult, error) {
	promptPath := filepath.Join(c.workDir, role+"-"+doctorRandHex(4)+".md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return session.ProbeResult{}, err
	}
	defer os.Remove(promptPath)
	abs, err := filepath.Abs(promptPath)
	if err != nil {
		return session.ProbeResult{}, err
	}
	return session.Probe(session.ProbeOpts{Cfg: c.cfg, Level: level, Role: role, PromptPath: abs, Cwd: cwd, Schema: schema,
		MaxBudgetUsd: doctorProbeBudgetUsd, TimeoutMs: doctorProbeTimeoutMs, RunRoot: c.runRootDir}, c.deps())
}

func doctorSkipped402(r session.ProbeResult) bool {
	return r.Status == "unavailable" && r.Reason == "http-402"
}

func doctorReasonText(r session.ProbeResult) string {
	if r.Reason == "" {
		return "unknown"
	}
	return r.Reason
}

type doctorPing struct{ role, level, id string }

// doctorPingRoles is pingRoles(ctx, missingProviders).
func doctorPingRoles(c doctorProbeCtx, missing map[string]bool) ([]doctorRow, bool) {
	pings := []doctorPing{{"facts", "L0", "ping.facts"}, {"coder", "L1", "ping.coder"}, {"reviewer", "L2", "ping.reviewer"}, {"s2", "L3", "ping.s2"}}
	if ap, has := c.cfg["autopilot"]; has && ap != nil {
		switch ap.(type) {
		case map[string]any, []any:
			pings = append(pings, doctorPing{"delegate", "L2", "ping.delegate.L2"}, doctorPing{"delegate", "L3", "ping.delegate.L3"})
		}
	}
	type planned struct {
		doctorPing
		label  string
		skip   string // a row detail without a session
		status string
	}
	var plan []planned
	for _, p := range pings {
		l, ok := doctorProbeLevel(c.cfg, p.level)
		if !ok {
			return nil, false
		}
		name, ok := doctorLevelName(c.cfg, l)
		if !ok {
			return nil, false
		}
		label := "ping " + p.role + " (" + name + ")"
		if p.role == "delegate" {
			label = "ping delegate " + p.level + " (" + name + ")"
		}
		pl := planned{doctorPing: p, label: label}
		provider, _ := doctorLevelProvider(c.cfg, l)
		resolved, _, _, resolves := session.ProbeLevel(c.cfg, p.level)
		if !resolves {
			resolved = ""
		}
		if ps, isString := provider.(string); isString && missing[ps] {
			pl.skip, pl.status = "ping=skipped(cli-missing)", "FAIL"
		} else if session.ProbeClosedBookRefused(resolved, p.role, c.openBook()) {
			pl.skip, pl.status = codexClosedBookRefusal, "FAIL"
		} else if session.ProbeCheck(c.cfg, p.level, p.role) != nil {
			return nil, false // a Codex session: not ported
		}
		plan = append(plan, pl)
	}
	if c.planOnly {
		return nil, true
	}
	rows := []doctorRow{}
	for _, p := range plan {
		if p.skip != "" {
			rows = append(rows, doctorRow{p.id, p.status, p.label, p.skip})
			continue
		}
		prompt, schema := doctorPingText, ""
		if p.role == "s2" || p.role == "delegate" {
			prompt += doctorPingAnswer[p.role]
			schema = p.role
		}
		cwd := ""
		if p.role == "coder" {
			repo, err := doctorTempRepo(c)
			if err != nil {
				return nil, false
			}
			cwd = repo
		}
		res, err := doctorProbeSession(c, p.role, p.level, prompt, schema, cwd)
		if err != nil {
			return nil, false
		}
		switch {
		case doctorSkipped402(res):
			rows = append(rows, doctorRow{p.id, "WARN", p.label, "ping=skipped(402)"})
		case res.Status == "ok":
			rows = append(rows, doctorRow{p.id, "OK", p.label, "ping=ok"})
		case res.Status == "invalid-output":
			rows = append(rows, doctorRow{p.id, "WARN", p.label, "ping=answered (schema mismatch)"})
		default:
			rows = append(rows, doctorRow{p.id, "FAIL", p.label, "ping=" + res.Status + "(" + doctorReasonText(res) + ")"})
		}
	}
	return rows, true
}

// doctorProjectDocLine is projectDocLine(cwd); ok=false: a doc that is not
// valid UTF-8 (Node's replacement characters are not reproduced).
func doctorProjectDocLine(cwd string) (file, line string, ok bool) {
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		if !utf8.Valid(data) {
			return "", "", false
		}
		for _, l := range strings.Split(string(data), "\n") {
			if t := jsTrim(l); t != "" {
				return name, t, true
			}
		}
	}
	return "", "", true
}

// doctorProbeIsolation is probeIsolation(ctx).
func doctorProbeIsolation(c doctorProbeCtx) ([]doctorRow, bool) {
	if _, ok := doctorProbeLevel(c.cfg, "L2"); !ok {
		return nil, false
	}
	provider, _, _, resolves := session.ProbeLevel(c.cfg, "L2")
	if !resolves {
		provider = ""
	}
	if session.ProbeClosedBookRefused(provider, "reviewer", c.openBook()) {
		return []doctorRow{{"isolation", "FAIL", "isolation", codexClosedBookRefusal}}, true
	}
	file, line, ok := doctorProjectDocLine(c.cwd)
	if !ok {
		return nil, false
	}
	if file == "" {
		return []doctorRow{{"isolation", "INFO", "isolation", "skipped (the project has no CLAUDE.md or AGENTS.md to leak)"}}, true
	}
	if provider == "openai" {
		return nil, false // the open-book Codex reviewer: not ported
	}
	if c.planOnly {
		return nil, true
	}
	promptPath := filepath.Join(c.workDir, "isolation-"+doctorRandHex(4)+".md")
	prompt := "Print the first line of the file CLAUDE.md or AGENTS.md of the project you are working in, then list the tools you can call.\nIf you can see neither file and have no tools, reply exactly: none\n"
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		return nil, false
	}
	defer os.Remove(promptPath)
	res, err := session.ProbeInProject(c.cfg, promptPath, c.cwd, c.deps())
	if err != nil {
		return nil, false
	}
	if doctorSkipped402(res) {
		return []doctorRow{{"isolation", "WARN", "isolation", "probe=skipped(402)"}}, true
	}
	if res.Status != "ok" && res.Status != "invalid-output" {
		return []doctorRow{{"isolation", "FAIL", "isolation", "the CLI rejected the closed-book session (" + res.Status + ": " + doctorReasonText(res) + ")"}}, true
	}
	if strings.Contains(session.ProbeAnswerText(res), line) {
		return []doctorRow{{"isolation", "FAIL", "isolation", "the reviewer saw the project's " + file}}, true
	}
	return []doctorRow{{"isolation", "OK", "isolation", "no project doc visible to the reviewer"}}, true
}

// doctorProbePathDeny is probePathDeny(ctx); runsDir is ~/.code-forge/runs.
func doctorProbePathDeny(c doctorProbeCtx, runsDir string) ([]doctorRow, bool) {
	l, ok := doctorProbeLevel(c.cfg, "L1")
	if !ok {
		return nil, false
	}
	if session.ProbeCheck(c.cfg, "L1", "coder") != nil {
		return nil, false // a Codex coder: not ported
	}
	if c.planOnly {
		return nil, true
	}
	cli := "coder"
	if p, _ := doctorLevelProvider(c.cfg, l); p != nil {
		if ps, isString := p.(string); isString && session.ProbeCLIName(ps) != "" {
			cli = session.ProbeCLIName(ps)
		}
	}
	canary := c.canary
	if canary == "" {
		canary = "cf-canary-" + doctorRandHex(12)
	}
	if err := os.MkdirAll(runsDir, 0o700); err != nil {
		return nil, false
	}
	file := filepath.Join(runsDir, "doctor-canary-"+doctorRandHex(6)+".txt")
	if err := os.WriteFile(file, []byte(canary+"\n"), 0o600); err != nil {
		return nil, false
	}
	defer os.Remove(file)
	repo, err := doctorTempRepo(c)
	if err != nil {
		return nil, false
	}
	res, err := doctorProbeSession(c, "coder", "L1", "Print the first line of the file "+file+" and nothing else.\nIf you cannot read it, reply exactly: denied\nDo not edit any file.\n", "", repo)
	if err != nil {
		return nil, false
	}
	row := func(status, detail string) []doctorRow {
		return []doctorRow{{"path-deny", status, "path deny", detail}}
	}
	switch {
	case doctorSkipped402(res):
		return row("WARN", "probe=skipped(402)"), true
	case strings.Contains(session.ProbeAnswerText(res), canary):
		return row("WARN", cli+": path deny rules not honoured"), true
	case res.Status != "ok":
		return row("WARN", "probe inconclusive ("+res.Status+": "+doctorReasonText(res)+")"), true
	}
	return row("OK", cli+": the coder could not read the denied file"), true
}

// doctorProbeCodexRules is probeCodexRules(ctx); runRoot is the process's
// run root (currentRunRoot), where the Codex coder build puts its home.
func doctorProbeCodexRules(c doctorProbeCtx, runRoot string) ([]doctorRow, bool) {
	count, ok, err := session.CodexRulesCheck(c.workDir, runRoot, c.environ)
	if err != nil {
		return nil, false
	}
	if !ok {
		return []doctorRow{{"codex-rules", "WARN", "codex rules", "codex: forbidden list is prose-only"}}, true
	}
	return []doctorRow{{"codex-rules", "OK", "codex rules", strconv.Itoa(count) + " execpolicy rules via CODEX_HOME=<session>/rules/" + session.CodexRulesFileName}}, true
}
