package gatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// b365Gate builds the B365 fixture: block B1 owns src/a.mjs and src/b.mjs,
// src/b.mjs is changed and approved, and prepare sets up src/a.mjs (given the
// repo and src/a.mjs's base hash) and returns its review rows. It returns the
// close file set and the gate's blockers as "code file finding".
func b365Gate(t *testing.T, prepare func(repo, baseHash string) []Row) ([]string, []string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitx.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	write := func(file, text string) {
		t.Helper()
		full := filepath.Join(repo, file)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitInit := exec.Command("git", "init", "-q", repo)
	gitInit.Dir = repo
	gitInit.Env = gitx.ChildEnv()
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	write("src/a.mjs", "export const a = 1;\n")
	write("src/b.mjs", "export const b = 1;\n")
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git("rev-parse", "HEAD"))
	baseHash, err := gitx.ContentHash(repo, "src/a.mjs")
	if err != nil {
		t.Fatal(err)
	}
	write("src/b.mjs", "export const b = 2;\n")
	bHash, err := gitx.ContentHash(repo, "src/b.mjs")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	raw := append([]Row{{"event": "review.approved", "file": "src/b.mjs", "content_hash": bHash}}, prepare(repo, baseHash)...)
	rows := []Row{}
	for _, r := range raw {
		r["run"], r["block"] = "r1", "B1"
		signed, err := sign.SignRow(r, key)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, signed)
	}
	block := ledger.Object{{Key: "owned_files", Value: []any{"src/a.mjs", "src/b.mjs"}}}
	changed, err := gitx.ChangedFiles(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := FileSet(repo, base, "", "B1", block, rows, changed)
	if err != nil {
		t.Fatal(err)
	}
	set := []string{}
	for _, f := range files {
		set = append(set, f.File)
	}
	blockers := []string{}
	for _, b := range Check("r1", "B1", files, rows, key, []any{}, []string{}, false).Blockers {
		blockers = append(blockers, strings.TrimSpace(b.Code+" "+b.File+" "+b.Finding))
	}
	return set, blockers
}

func noChangeRow(hash string) Row {
	return Row{"event": "review.result", "ticket": "no-change-ticket", "file": "src/a.mjs", "content_hash": hash, "status": "no_change", "approved": false, "open": []any{}}
}

// B365: an owned file unchanged since base whose only current-hash review row
// is a no_change result leaves the close file set; the changed file stays.
func TestB365NoChangeOnlyFileLeavesTheCloseSet(t *testing.T) {
	set, blockers := b365Gate(t, func(_, baseHash string) []Row { return []Row{noChangeRow(baseHash)} })
	if !slices.Equal(set, []string{"src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/b.mjs]", set)
	}
	if len(blockers) != 0 {
		t.Fatalf("blockers = %v, want none", blockers)
	}
}

// B365 guard: a file changed since base with only a no_change row of its old
// hash is still in the set and still unreviewed.
func TestB365ChangedFileWithStaleNoChangeIsUnreviewed(t *testing.T) {
	set, blockers := b365Gate(t, func(repo, baseHash string) []Row {
		if err := os.WriteFile(filepath.Join(repo, "src/a.mjs"), []byte("export const a = 2;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return []Row{noChangeRow(baseHash)}
	})
	if !slices.Equal(set, []string{"src/a.mjs", "src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/a.mjs src/b.mjs]", set)
	}
	if !slices.Equal(blockers, []string{"unreviewed src/a.mjs"}) {
		t.Fatalf("blockers = %v, want [unreviewed src/a.mjs]", blockers)
	}
}

// B365 guard: a file unchanged since base whose current hash also has a
// refusal with a finding stays in the set and is still refused.
func TestB365UnchangedFileWithFindingIsRefused(t *testing.T) {
	set, blockers := b365Gate(t, func(_, baseHash string) []Row {
		refusal := Row{"event": "review.result", "ticket": "finding-ticket", "file": "src/a.mjs", "content_hash": baseHash, "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}}
		return []Row{noChangeRow(baseHash), refusal}
	})
	if !slices.Equal(set, []string{"src/a.mjs", "src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/a.mjs src/b.mjs]", set)
	}
	if !slices.Equal(blockers, []string{"unreviewed src/a.mjs", "not_approved src/a.mjs F1"}) {
		t.Fatalf("blockers = %v, want [unreviewed src/a.mjs, not_approved src/a.mjs F1]", blockers)
	}
}

// B365: a block with an empty diff since base whose only review of its owned
// file is a no_change result has no reviewed work, so the close refuses
// no_changes; a refusal row of the same bytes keeps the file as work.
func TestB365NoChangeOnlyIsNoWork(t *testing.T) {
	repo := t.TempDir()
	gitInit := exec.Command("git", "init", "-q", repo)
	gitInit.Dir = repo
	gitInit.Env = gitx.ChildEnv()
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src/a.mjs"), []byte("export const a = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"}} {
		if _, err := gitx.Run(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	base, err := gitx.Run(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)
	hash, err := gitx.ContentHash(repo, "src/a.mjs")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	signAll := func(raw ...Row) []Row {
		rows := []Row{}
		for _, r := range raw {
			r["run"], r["block"] = "r1", "B1"
			signed, err := sign.SignRow(r, key)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, signed)
		}
		return rows
	}
	block := ledger.Object{{Key: "owned_files", Value: []any{"src/a.mjs"}}}
	gate := func(rows []Row) ([]string, []string) {
		changed, err := gitx.ChangedFiles(repo, base)
		if err != nil {
			t.Fatal(err)
		}
		files, _, err := FileSet(repo, base, "", "B1", block, rows, changed)
		if err != nil {
			t.Fatal(err)
		}
		work := ReviewedWork(repo, rows, "B1")
		// the close's own rule (block_ready.go, block_close.go)
		noChanges := len(files) == 0 && !slices.ContainsFunc(work, func(f string) bool { return Owns("", block, f) })
		blockers := []string{}
		for _, b := range Check("r1", "B1", files, rows, key, []any{}, []string{}, noChanges).Blockers {
			blockers = append(blockers, strings.TrimSpace(b.Code+" "+b.File+" "+b.Finding))
		}
		return work, blockers
	}
	work, blockers := gate(signAll(noChangeRow(hash)))
	if len(work) != 0 || !slices.Equal(blockers, []string{"no_changes"}) {
		t.Fatalf("no_change only: work = %v, blockers = %v; want [], [no_changes]", work, blockers)
	}
	refusal := Row{"event": "review.result", "ticket": "finding-ticket", "file": "src/a.mjs", "content_hash": hash, "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}}
	work, blockers = gate(signAll(noChangeRow(hash), refusal))
	if !slices.Equal(work, []string{"src/a.mjs"}) || !slices.Equal(blockers, []string{"unreviewed src/a.mjs", "not_approved src/a.mjs F1"}) {
		t.Fatalf("with a refusal: work = %v, blockers = %v; want [src/a.mjs], [unreviewed src/a.mjs, not_approved src/a.mjs F1]", work, blockers)
	}
}

// b380Rows signs raw rows of run r1 block B1 in order.
func b380Rows(t *testing.T, key []byte, raw ...Row) []Row {
	t.Helper()
	rows := []Row{}
	for _, r := range raw {
		r["run"], r["block"] = "r1", "B1"
		signed, err := sign.SignRow(r, key)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, signed)
	}
	return rows
}

func b380Codes(res Result) []string {
	codes := []string{}
	for _, b := range res.Blockers {
		codes = append(codes, strings.TrimSpace(b.Code+" "+b.File+" "+b.Finding))
	}
	return codes
}

func b380Eval(rows []Row, key []byte, hits []Blocker, transcript string) Result {
	return Evaluate(Input{Run: "r1", Block: "B1", Files: []File{{File: "a.mjs", Hash: "h2"}}, Rows: rows, Key: key,
		HighPaths: []any{}, Unowned: []string{}, Hits: hits, Transcript: transcript})
}

// B380: localeLess is String.prototype.localeCompare < 0 (values measured with Node 24).
func TestB380LocaleLessMatchesNode(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"a", "B", true}, {"B", "a", false}, {"a", "A", true}, {"A", "a", false}, {"_", "a", true}, {"a", "_", false},
		{"-", "_", false}, {`["b"]`, `["a","z"]`, false}, {`["a",null]`, `["a","b"]`, false}, {"abc", "ab", false},
		{"F1", "f1", false}, {"1", "a", true}, {"x.mjs", "x-mjs", false}, {"[", `"`, false}, {"Z", "z", false}, {"a b", "ab", true},
	} {
		less, ok := localeLess(c.a, c.b)
		if !ok || less != c.less {
			t.Errorf("localeLess(%q, %q) = %v, %v; Node says %v", c.a, c.b, less, ok, c.less)
		}
	}
	if _, ok := localeLess("café", "cafe"); ok {
		t.Error("non-ASCII text must be unknown, not guessed")
	}
}

// B380: jsDateParse equals Date.parse on the exact ISO subset and refuses to guess elsewhere.
func TestB380JSDateParseMatchesNode(t *testing.T) {
	for _, c := range []struct {
		in string
		ms float64
	}{
		{"2000-01-01T00:00:00.000Z", 946684800000}, {"2000-01-01T00:00:00+01:00", 946681200000},
		{"2000-01-01", 946684800000}, {"2026-11-10T08:05:59.5Z", 1794297959500},
	} {
		ms, ok, known := jsDateParse(c.in, true)
		if !ok || !known || ms != c.ms {
			t.Errorf("jsDateParse(%q) = %v %v %v; Node says %v", c.in, ms, ok, known, c.ms)
		}
	}
	for _, in := range []string{"Nov 10 2026", "2000-02-30", "2000-01-01T00:00:00+24:00"} {
		if _, _, known := jsDateParse(in, true); known {
			t.Errorf("jsDateParse(%q) must be unknown", in)
		}
	}
	if _, ok, known := jsDateParse(nil, true); ok || !known {
		t.Error("null is NaN in V8")
	}
}

// B380: an agreeing agent waiver of a warning passes the gate until a human unwaives it,
// and require_ack needs the signed digest of the counted waivers.
func TestB380AgentWaiverUnwaiveAndAck(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	base := []Row{
		{"event": "review.round", "file": "a.mjs", "content_hash": "h2", "open_after": 2},
		{"event": "review.result", "file": "a.mjs", "content_hash": "h2", "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}, map[string]any{"id": "F2", "severity": "warning"}}},
		{"event": "review.waived", "file": "a.mjs", "finding": "F2", "severity": "warning", "verdict": "agree", "by": "agent", "reason": "style"},
		{"event": "review.waived", "file": "a.mjs", "finding": "F1", "severity": "warning", "verdict": "agree", "by": "agent", "reason": "style"},
	}
	rows := b380Rows(t, key, base...)
	res := b380Eval(rows, key, nil, "")
	if got := b380Codes(res); len(got) != 0 || res.Unknown || len(res.Waived) != 1 {
		t.Fatalf("agent-waived file passes as reviewed_by_waiver: %v %+v", got, res)
	}
	if got := len(AgentWaivers(rows, "B1", key)); got != 2 {
		t.Fatalf("counted agent waivers = %d, want 2", got)
	}
	if acked, known := AgentWaiversAcked(false, "r1", "B1", rows, key); !acked || !known {
		t.Fatal("no require_ack: acked")
	}
	if acked, known := AgentWaiversAcked(true, "r1", "B1", rows, key); acked || !known {
		t.Fatal("require_ack without a digest row is not acked")
	}
	// Node sorts [file, finding, mac] by the localeCompare of their JSON text: F1 before F2.
	set := []any{}
	for _, r := range []Row{rows[3], rows[2]} {
		set = append(set, []any{r["file"], r["finding"], r["mac"]})
	}
	data, err := ledger.Stringify(set, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	acked := append(slices.Clone(rows), b380Rows(t, key, Row{"event": "agent_waivers.acked", "digest": hex.EncodeToString(sum[:]), "by": "human"})...)
	if ok, known := AgentWaiversAcked(true, "r1", "B1", acked, key); !ok || !known {
		t.Fatal("the signed human digest row acknowledges the waivers")
	}
	wrong := append(slices.Clone(rows), b380Rows(t, key, Row{"event": "agent_waivers.acked", "digest": strings.Repeat("0", 64), "by": "human"})...)
	if ok, _ := AgentWaiversAcked(true, "r1", "B1", wrong, key); ok {
		t.Fatal("a stale digest does not acknowledge")
	}
	unwaived := append(slices.Clone(rows), b380Rows(t, key, Row{"event": "review.unwaived", "file": "a.mjs", "finding": "F1", "by": "human", "reason": "needs a fix"})...)
	if got := b380Codes(b380Eval(unwaived, key, nil, "")); !slices.Contains(got, "not_approved a.mjs F1") {
		t.Fatalf("unwaived finding refuses again: %v", got)
	}
	if got := len(AgentWaivers(unwaived, "B1", key)); got != 1 {
		t.Fatalf("unwaived waiver is no longer counted: %d", got)
	}
	foreign := []Row{}
	for _, r := range base {
		c := maps.Clone(r)
		c["file"] = "café.mjs"
		foreign = append(foreign, c)
	}
	foreign = append(foreign, Row{"event": "agent_waivers.acked", "digest": strings.Repeat("0", 64), "by": "human"})
	if _, known := AgentWaiversAcked(true, "r1", "B1", b380Rows(t, key, foreign...), key); known {
		t.Fatal("a digest order that needs non-ASCII collation is unknown, so the caller hands over")
	}
}

// B380: a signed human rule_break acceptance of the exact transcript line clears that hit.
func TestB380AcceptedHitClearsRuleBreak(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	transcript := "git clean -fd\n"
	hits, ok := Transcript(transcript)
	if !ok || len(hits) != 1 {
		t.Fatalf("one transcript hit: %v %v", hits, ok)
	}
	approved := b380Rows(t, key, Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"})
	if got := b380Codes(b380Eval(approved, key, hits, transcript)); !slices.Contains(got, "rule_break") {
		t.Fatalf("unaccepted hit refuses: %v", got)
	}
	sum := sha256.Sum256([]byte("git clean -fd"))
	rows := append(slices.Clone(approved), b380Rows(t, key, Row{"event": "rule_break.accepted", "rule": "git-clean", "line": 1, "line_sha256": hex.EncodeToString(sum[:]), "reason": "scratch dir", "by": "human"})...)
	res := b380Eval(rows, key, hits, transcript)
	if got := b380Codes(res); len(got) != 0 {
		t.Fatalf("accepted hit passes: %v", got)
	}
	if len(res.Accepted) != 1 || res.Accepted[0].Rule != "git-clean" || res.Accepted[0].Line != 1 || res.Accepted[0].Reason != "scratch dir" {
		t.Fatalf("accepted hit is reported: %+v", res.Accepted)
	}
}

// B380: review caps, agent reopens and agent waivers give Node's verdicts. The
// expected values were measured with Node's checkBlockReviews on the same rows.
func TestB380CapReopenAndWaiverMatchNode(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	capRow := func(h string) Row {
		return Row{"event": "review.cap", "file": "a.mjs", "content_hash": h, "open": []any{"F1"}}
	}
	approved := func() Row { return Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"} }
	reopen := func(v string) Row {
		return Row{"event": "review.reopened", "file": "a.mjs", "from": "stopped", "from_hash": "h1", "to_hash": "h2", "reason": "fixed", "by": "agent", "verdict": map[string]any{"verdict": v, "reason": "x"}}
	}
	result := func() Row {
		return Row{"event": "review.result", "file": "a.mjs", "content_hash": "h2", "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}}
	}
	waived := func() Row {
		return Row{"event": "review.waived", "file": "a.mjs", "finding": "F1", "severity": "warning", "verdict": "agree", "by": "agent", "reason": "style"}
	}
	round := func() Row {
		return Row{"event": "review.round", "file": "a.mjs", "content_hash": "h2", "open_after": 1}
	}
	for _, c := range []struct {
		label  string
		rows   []Row
		codes  []string
		waived int
	}{
		{"cap-h1+approved", []Row{capRow("h1"), approved()}, []string{}, 0},
		{"cap-h2+approved", []Row{capRow("h2"), approved()}, []string{}, 0},
		{"approved+cap-h2", []Row{approved(), capRow("h2")}, []string{"not_approved a.mjs F1"}, 0},
		{"cap-h2+reopen+approved", []Row{capRow("h2"), reopen("agree"), approved()}, []string{}, 0},
		{"cap-h1-alone", []Row{capRow("h1")}, []string{"unreviewed a.mjs", "review_cap a.mjs F1"}, 0},
		{"result+waived", []Row{result(), waived()}, []string{"unreviewed a.mjs"}, 0},
		{"round+result+waived", []Row{round(), result(), waived()}, []string{}, 1},
		{"cap+agree+round+result+waived", []Row{capRow("h2"), reopen("agree"), round(), result(), waived()}, []string{}, 1},
		{"cap+disagree+round+result+waived", []Row{capRow("h2"), reopen("disagree"), round(), result(), waived()}, []string{}, 1},
	} {
		res := b380Eval(b380Rows(t, key, c.rows...), key, nil, "")
		if got := b380Codes(res); res.Unknown || !slices.Equal(got, c.codes) || len(res.Waived) != c.waived {
			t.Errorf("%s: blockers %v waived %d unknown %v; Node: %v waived %d", c.label, got, len(res.Waived), res.Unknown, c.codes, c.waived)
		}
	}
	rows := b380Rows(t, key, capRow("h2"), reopen("agree"), reopen("disagree"), approved())
	if got := AgentReopens(rows, "B1", key); len(got) != 2 {
		t.Fatalf("every verified agent reopen is reported: %v", got)
	}
}

// B390: an autopilot rule_break acceptance clears the hit only inside a signed
// autopilot.grant window of the run; without that grant row it does not.
func TestB390AutopilotAcceptedHitNeedsGrant(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	transcript := "git clean -fd\n"
	hits, ok := Transcript(transcript)
	if !ok || len(hits) != 1 {
		t.Fatalf("one transcript hit: %v %v", hits, ok)
	}
	sum := sha256.Sum256([]byte("git clean -fd"))
	accept := func() Row {
		return Row{"event": "rule_break.accepted", "rule": "git-clean", "line": 1, "line_sha256": hex.EncodeToString(sum[:]), "reason": "scratch dir", "by": "autopilot", "grant_id": "ag-1", "ts": "2026-11-11T10:00:00.000Z"}
	}
	approved := b380Rows(t, key, Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"})
	noGrant := append(slices.Clone(approved), b380Rows(t, key, accept())...)
	if got := b380Codes(b380Eval(noGrant, key, hits, transcript)); !slices.Contains(got, "rule_break") {
		t.Fatalf("an autopilot acceptance without a grant row must not clear the hit: %v", got)
	}
	grant := func(from, until string) Row {
		return Row{"event": "autopilot.grant", "grant_id": "ag-1", "scopes": []any{}, "deny": []any{}, "delegate": "L2", "until": until, "ts": from}
	}
	outside := append(slices.Clone(approved), b380Rows(t, key, grant("2026-11-11T11:00:00.000Z", "2026-11-11T12:00:00.000Z"), accept())...)
	if got := b380Codes(b380Eval(outside, key, hits, transcript)); !slices.Contains(got, "rule_break") {
		t.Fatalf("an acceptance outside the grant window must not clear the hit: %v", got)
	}
	inside := append(slices.Clone(approved), b380Rows(t, key, grant("2026-11-11T09:00:00.000Z", "2026-11-11T12:00:00.000Z"), accept())...)
	res := b380Eval(inside, key, hits, transcript)
	if got := b380Codes(res); len(got) != 0 {
		t.Fatalf("an acceptance inside the grant clears the hit: %v", got)
	}
	if len(res.Accepted) != 1 || res.Accepted[0].By != "autopilot" {
		t.Fatalf("accepted hit names autopilot: %+v", res.Accepted)
	}
}
