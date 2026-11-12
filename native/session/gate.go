package session

// B482 step 0: spawn.mjs budgetGate, the run-pinned gate every attempt passes
// after its provider slot: budget.usd (B33/B41, src/ledger/spend.mjs) and the
// autopilot category cap (B48, src/autopilot/limits.mjs), under Node's
// `budget:<run>` lock file, with this process's reservations.

import (
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

// SpendUnreadableMessage is spend.mjs SPEND_UNREADABLE_MESSAGE.
const SpendUnreadableMessage = "budget.usd is set but the run's spend cannot be read; not starting a session"

// RecordUnknownMessage is limits.mjs RECORD_UNKNOWN_MESSAGE.
const RecordUnknownMessage = "autopilot: the run record cannot be read, so the autopilot budget is unknown; not starting a session"

// BudgetWarning is the error log's warning (logWarning) at 80 % of budget.usd.
const (
	BudgetWarningKind    = "budget_warning"
	BudgetWarningMessage = "budget.usd: 80% of the run budget is spent; new sessions stop at 100%"
)

// tokensOutEstimate is spend.mjs TOKENS_OUT_ESTIMATE (TOKENS_OUT_FALLBACK otherwise).
var tokensOutEstimate = map[string]map[string]float64{
	"coder":    {"L0": 8000, "L1": 16000, "L2": 32000, "L3": 64000},
	"author":   {"L0": 8000, "L1": 12000, "L2": 16000, "L3": 24000},
	"reviewer": {"L0": 4000, "L1": 6000, "L2": 8000, "L3": 12000},
	"facts":    {"L0": 4000, "L1": 6000, "L2": 8000, "L3": 12000},
	"judge":    {"L0": 2000, "L1": 3000, "L2": 4000, "L3": 6000},
	"s2":       {"L0": 1000, "L1": 1500, "L2": 2000, "L3": 3000},
}

const tokensOutFallback = 64000

// roleCategory is limits.mjs ROLE_CATEGORY.
var roleCategory = map[string]string{"coder": "coding", "reviewer": "review", "judge": "review", "s2": "review", "delegate": "review", "author": "review", "facts": "review"}

// reservations is spend.mjs's module map: key (a run, or autopilot:<category>:<run>) → id → USD.
var (
	resMu        sync.Mutex
	reservations = map[string]map[int]float64{}
	nextRes      int
	warnedMu     sync.Mutex
	warnedRuns   = map[string]bool{}
)

func reservedUSD(key string) float64 {
	resMu.Lock()
	defer resMu.Unlock()
	sum := 0.0
	// Node sums a Map in insertion order; ids grow, so ascending id is that order
	ids := make([]int, 0, len(reservations[key]))
	for id := range reservations[key] {
		ids = append(ids, id)
	}
	sortInts(ids)
	for _, id := range ids {
		sum += reservations[key][id]
	}
	return runstatus.RoundUSD(sum)
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

func reserve(key string, usd float64) func() {
	resMu.Lock()
	defer resMu.Unlock()
	nextRes++
	id := nextRes
	if reservations[key] == nil {
		reservations[key] = map[int]float64{}
	}
	if !isAmount(usd) {
		usd = 0
	}
	reservations[key][id] = usd
	return func() {
		resMu.Lock()
		defer resMu.Unlock()
		if m := reservations[key]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(reservations, key)
			}
		}
	}
}

// ResetReservations is resetBudgetReservations (tests).
func ResetReservations() {
	resMu.Lock()
	reservations = map[string]map[int]float64{}
	resMu.Unlock()
	warnedMu.Lock()
	warnedRuns = map[string]bool{}
	warnedMu.Unlock()
}

func isAmount(v float64) bool { return v >= 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// budgetUSDOf is spend.mjs budgetUsdOf.
func budgetUSDOf(cfg map[string]any) (float64, bool) {
	v, ok := section(cfg, "budget")["usd"].(float64)
	return v, ok && v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v)
}

// EstimateUSD is spend.mjs estimateSessionUsd.
func EstimateUSD(provider, level, role string, tokensIn float64) float64 {
	out, ok := tokensOutEstimate[role][level]
	if !ok {
		out = tokensOutFallback
	}
	p, ok := pricePerK[provider][level]
	if !ok {
		return 0
	}
	x := float64((tokensIn + out) / 1000 * p)
	usd := jsRound(float64(x*10000)) / 10000
	if !isAmount(usd) {
		return 0
	}
	return usd
}

// packetTokens is spend.mjs packetTokens with no system prompt text: ceil(bytes / 4).
func packetTokens(promptPath string) float64 {
	st, err := os.Stat(promptPath)
	if err != nil {
		return 0
	}
	return math.Ceil(float64(st.Size()) / 4)
}

// toFixed2 is Number.prototype.toFixed(2) for a finite number (ties away from zero).
func toFixed2(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) >= 1e21 {
		b, _ := ledger.Stringify(n, false)
		return string(b)
	}
	negative := n < 0
	r := new(big.Rat).SetFloat64(math.Abs(n))
	r.Mul(r, big.NewRat(100, 1))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if new(big.Int).Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	for len(s) < 3 {
		s = "0" + s
	}
	s = s[:len(s)-2] + "." + s[len(s)-2:]
	if negative && s != "0.00" {
		s = "-" + s
	}
	return s
}

func jsNumber(n float64) string {
	b, _ := ledger.Stringify(n, false)
	return string(b)
}

// categoryPlan is limits.mjs CategoryPlan.
type categoryPlan struct {
	category           string
	cap, stopAt        float64
	grantID, startedAt any
	project            any
	paused, rowPending bool
	startedAtMs        float64
	startedAtOK        bool
}

// readCategoryPlan is spawn.mjs readCategoryPlan over limits.mjs categoryPlan:
// nil for no record, no grant, an inactive grant or run, or no cap for the
// role's category; unknown for a record that cannot be read.
func readCategoryPlan(home, run, role string, now time.Time) (*categoryPlan, bool, error) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		var se *state.Error
		if errors.As(err, &se) && (se.Code == "no-run" || se.Code == "bad-run-id") {
			return nil, false, nil
		}
		return nil, true, nil
	}
	grant, ok := record.Get("autopilot").(ledger.Object)
	if !ok || record.Get("status") != "active" {
		return nil, false, nil
	}
	// grantState: stopped / expired by status, else now ≥ Date.parse(until)
	switch grant.Get("status") {
	case "stopped", "expired":
		return nil, false, nil
	}
	until, hasUntil := has(grant, "until")
	ms, valid, known := gatecheck.JSDateParse(until, hasUntil)
	if !known {
		return nil, false, unsupported("an autopilot grant whose until Date.parse may read differently")
	}
	if valid && float64(now.UnixMilli()) >= ms {
		return nil, false, nil
	}
	category, ok := roleCategory[role]
	if !ok {
		return nil, false, nil
	}
	caps, _ := grant.Get("caps").(ledger.Object)
	capValue, _ := caps.Get(category).(float64)
	if caps == nil || !(capValue > 0) {
		return nil, false, nil
	}
	stopAt := 1.0
	if n, ok := grant.Get("stop_at").(float64); ok && n > 0 && n <= 1 {
		stopAt = n
	}
	p := &categoryPlan{category: category, cap: capValue, stopAt: stopAt, grantID: grant.Get("grant_id"), startedAt: grant.Get("started_at"), project: record.Get("project")}
	if paused, ok := grant.Get("paused").(ledger.Object); ok {
		if pause, found := has(paused, category); found && pause != nil {
			p.paused = true
			if po, ok := pause.(ledger.Object); ok && po.Get("row_pending") == true {
				p.rowPending = true
			}
		}
	}
	startedAt, hasStarted := has(grant, "started_at")
	var sKnown bool
	p.startedAtMs, p.startedAtOK, sKnown = gatecheck.JSDateParse(startedAt, hasStarted)
	if !sKnown {
		return nil, false, unsupported("an autopilot grant whose started_at Date.parse may read differently")
	}
	return p, false, nil
}

// errRowNull is the TypeError Node throws reading `.run` of a null ledger row.
var errRowNull = errors.New("Cannot read properties of null (reading 'run')")

// runSpend is spend.mjs runSpend: (usd, unknown sessions).
func runSpend(rows []ledger.Row, run string) (float64, int, error) {
	sum, unknown := 0.0, 0
	for _, value := range rows {
		if value == nil {
			return 0, 0, errRowNull
		}
		row, ok := value.(map[string]any)
		if !ok || row["run"] != run || !isSpendRow(row) {
			continue
		}
		if cost, known := runstatus.RowUSD(row); known {
			sum += cost
		} else {
			unknown++
		}
	}
	return runstatus.RoundUSD(sum), unknown, nil
}

func isSpendRow(row map[string]any) bool {
	event := row["event"]
	return (event == "session" || event == "session.background") && row["source"] != "shadow"
}

// categorySpend is limits.mjs categorySpend (usd only).
func categorySpend(rows []ledger.Row, run, category string, p *categoryPlan) (float64, error) {
	sum := 0.0
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || !isSpendRow(row) || row["run"] != run {
			continue
		}
		role, _ := row["role"].(string)
		if c, ok := roleCategory[role]; !ok || c != category {
			continue
		}
		if ts, isString := row["ts"].(string); isString {
			t, valid, known := gatecheck.JSDateParse(ts, true)
			if !known {
				return 0, unsupported("a ledger row whose ts Date.parse may read differently")
			}
			if valid && p.startedAtOK && t < p.startedAtMs {
				continue
			}
		}
		if cost, known := runstatus.RowUSD(row); known {
			sum += cost
		}
	}
	return runstatus.RoundUSD(sum), nil
}

type gateEnv struct {
	o        Opts
	deps     Deps
	home     string
	writeRow func(ledger.Object) error
	s        step
}

func (g gateEnv) nowTime() time.Time { return time.UnixMilli(int64(g.deps.now())).UTC() }

func (g gateEnv) readRows(slug string) ([]ledger.Row, error) {
	if g.deps.ReadRows != nil {
		return g.deps.ReadRows()
	}
	if slug == "" {
		return nil, errors.New("no slug")
	}
	return ledger.ReadAllRows(filepath.Join(g.home, ".code-forge", "ledger"), slug)
}

func (g gateEnv) refusal(reason, message string) ledger.Object {
	return ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: reason}, {Key: "message", Value: message}, {Key: "answer", Value: nil}}
}

// budgetGate is spawn.mjs budgetGate: a refusal result (without the step's
// fields), or the release of this attempt's reservations. An error is a
// throw, or (IsUnsupported) a case only Node serves, found before any write.
func budgetGate(o Opts, deps Deps, writeRow func(ledger.Object) error, s step) (ledger.Object, func(), error) {
	none := func() {}
	if o.Run == nil || *o.Run == "" {
		return nil, none, nil
	}
	run := *o.Run
	home := deps.Home
	if home == "" {
		home = os.Getenv("HOME")
	}
	g := gateEnv{o: o, deps: deps, home: home, writeRow: writeRow, s: s}
	budget, hasBudget := budgetUSDOf(o.Cfg)
	outside, unknown, err := readCategoryPlan(home, run, o.Role, g.nowTime())
	if err != nil {
		return nil, nil, err
	}
	if !hasBudget && outside == nil && !unknown {
		return nil, none, nil
	}
	estimate := EstimateUSD(s.provider, o.Level, o.Role, packetTokens(o.PromptPath))
	var refused ledger.Object
	var release func()
	lockErr := filelock.WithLock("budget:"+run, func() error {
		now := g.nowTime()
		p, unknown, err := readCategoryPlan(home, run, o.Role, now)
		if err != nil {
			return err
		}
		if unknown {
			deps.stderr("code-forge: " + RecordUnknownMessage + "\n")
			refused = g.refusal("autopilot-unknown", RecordUnknownMessage)
			return nil
		}
		if hasBudget {
			r, err := g.checkGate(budget, estimate, p)
			if err != nil || r != nil {
				refused = r
				return err
			}
		}
		releaseCategory := func() {}
		if p != nil {
			slug := o.Slug
			if slug == "" {
				slug, _ = p.project.(string)
			}
			r, rel, err := g.categoryGate(p, run, slug, now, estimate)
			if err != nil || r != nil {
				refused = r
				return err
			}
			releaseCategory = rel
		}
		releaseRun := func() {}
		if hasBudget {
			releaseRun = reserve(run, estimate)
		}
		var once sync.Once
		release = func() {
			once.Do(func() {
				releaseRun()
				releaseCategory()
			})
		}
		return nil
	}, filelock.Options{})
	if lockErr != nil {
		return nil, nil, lockErr
	}
	if refused != nil {
		return refused, nil, nil
	}
	return nil, release, nil
}

// checkGate is spawn.mjs checkGate + spend.mjs checkBudget. p is the plan
// under the lock: a warning that needs the error log with no Deps.Warn is
// found before anything is written.
func (g gateEnv) checkGate(budget, estimate float64, p *categoryPlan) (ledger.Object, error) {
	run := *g.o.Run
	rows, err := g.readRows(g.o.Slug)
	if err != nil || rows == nil {
		g.deps.stderr("code-forge: " + SpendUnreadableMessage + "\n")
		if g.writeRow != nil {
			_ = g.writeRow(ledger.Object{{Key: "event", Value: "budget.refused"}, {Key: "run", Value: run}, {Key: "block", Value: strPtr(g.o.Block)}, {Key: "role", Value: g.o.Role},
				{Key: "budget_usd", Value: budget}, {Key: "spent_usd", Value: nil}, {Key: "reason", Value: "spend-unreadable"}})
		}
		out := g.refusal("budget", SpendUnreadableMessage)
		out.Set("spent_usd", nil)
		out.Set("budget_usd", budget)
		return out, nil
	}
	spent, unknownSessions, err := runSpend(rows, run)
	if err != nil {
		return nil, err
	}
	held := reservedUSD(run)
	next := estimate
	base := ledger.Object{{Key: "run", Value: run}, {Key: "block", Value: strPtr(g.o.Block)}}
	if g.o.Role != "" {
		base.Set("role", g.o.Role)
	}
	base.Set("budget_usd", budget)
	base.Set("spent_usd", spent)
	base.Set("unknown_usd_sessions", float64(unknownSessions))
	if spent >= budget || runstatus.RoundUSD(spent+held+next) > budget {
		var message string
		switch {
		case spent >= budget:
			message = "budget.usd " + toFixed2(budget) + " reached (spent " + toFixed2(spent) + "); raise budget.usd or end the run"
		case next > budget:
			message = "this session's estimated cost (about " + toFixed2(next) + ") is above budget.usd " + toFixed2(budget) + "; raise budget.usd"
		case held > 0:
			message = "budget.usd " + toFixed2(budget) + " would be exceeded: spent " + toFixed2(spent) + ", reserved by running sessions " + toFixed2(held) + ", this session about " + toFixed2(next) + "; it starts when running sessions finish or when budget.usd is raised"
		default:
			message = "budget.usd " + toFixed2(budget) + " would be exceeded: spent " + toFixed2(spent) + ", this session about " + toFixed2(next) + "; raise budget.usd or end the run"
		}
		g.deps.stderr("code-forge: " + message + "\n")
		if g.writeRow != nil {
			row := ledger.Object{{Key: "event", Value: "budget.refused"}}
			row = append(row, base...)
			if held > 0 || next > 0 {
				row.Set("reserved_usd", held)
				row.Set("estimate_usd", next)
			}
			_ = g.writeRow(row)
		}
		out := g.refusal("budget", message)
		out.Set("spent_usd", spent)
		out.Set("budget_usd", budget)
		return out, nil
	}
	if spent >= runstatus.RoundUSD(0.8*budget) && !hasWarningRow(rows, run) {
		if g.deps.Warn == nil {
			return nil, unsupported("the budget.usd 80% warning (the error log's warning line)")
		}
		recorded := false
		if g.writeRow != nil {
			row := ledger.Object{{Key: "event", Value: "budget.warning"}}
			row = append(row, base...)
			recorded = g.writeRow(row) == nil
		}
		warnedMu.Lock()
		show := recorded || !warnedRuns[run]
		if show {
			warnedRuns[run] = true
		}
		warnedMu.Unlock()
		if show {
			g.deps.stderr("code-forge: budget.usd: spent " + toFixed2(spent) + " of " + toFixed2(budget) + " (≥ 80%); new sessions stop at 100%\n")
			g.deps.Warn(BudgetWarningKind, BudgetWarningMessage)
		}
	}
	return nil, nil
}

func hasWarningRow(rows []ledger.Row, run string) bool {
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row["event"] == "budget.warning" && row["run"] == run {
			return true
		}
	}
	return false
}

// categoryGate is limits.mjs categoryGate.
func (g gateEnv) categoryGate(p *categoryPlan, run, slug string, now time.Time, estimate float64) (ledger.Object, func(), error) {
	refuse := func(message string, extra ledger.Object) ledger.Object {
		g.deps.stderr("code-forge: " + message + "\n")
		out := ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: "autopilot-paused"}, {Key: "message", Value: message}, {Key: "answer", Value: nil},
			{Key: "category", Value: p.category}, {Key: "cap_usd", Value: p.cap}}
		return append(out, extra...)
	}
	rows, err := g.readRows(slug)
	if err != nil || rows == nil {
		return refuse("autopilot: the "+p.category+" spend cannot be read; not starting a session", ledger.Object{{Key: "spent_usd", Value: nil}}), nil, nil
	}
	key := "autopilot:" + p.category + ":" + run
	spent, err := categorySpend(rows, run, p.category, p)
	if err != nil {
		return nil, nil, err
	}
	reserved := reservedUSD(key)
	total := runstatus.RoundUSD(spent + reserved)
	if p.paused || total >= runstatus.RoundUSD(p.stopAt*p.cap) {
		if !p.paused || p.rowPending {
			if err := g.recordPause(p, run, spent, reserved, now); err != nil {
				return nil, nil, err
			}
		}
		message := "autopilot paused: " + p.category + " spend " + toFixed2(total) + " of " + toFixed2(p.cap) + " reached the stop at " + jsNumber(p.stopAt) + "; waiting for the owner"
		return refuse(message, ledger.Object{{Key: "spent_usd", Value: spent}, {Key: "reserved_usd", Value: reserved}}), nil, nil
	}
	return nil, reserve(key, estimate), nil
}

// recordPause is limits.mjs recordPause: the paused state saved first
// (row_pending), then the one signed autopilot.pause row, then the mark cleared.
func (g gateEnv) recordPause(p *categoryPlan, run string, spent, reserved float64, now time.Time) error {
	write := g.writeRow
	if write == nil {
		project, _ := p.project.(string)
		write = func(row ledger.Object) error {
			_, err := ledger.AppendObject(filepath.Join(g.home, ".code-forge", "ledger"), project, row, ledger.DefaultRotateAtBytes)
			return err
		}
	}
	return state.WithRunLock(g.home, run, func() error {
		record, err := state.ReadRun(g.home, run)
		if err != nil {
			return err
		}
		grant, ok := record.Get("autopilot").(ledger.Object)
		if !ok || !sameJSON(grant.Get("grant_id"), p.grantID) {
			return nil
		}
		paused, _ := grant.Get("paused").(ledger.Object)
		var pause ledger.Object
		if paused != nil {
			if v, found := has(paused, p.category); found && v != nil {
				po, isObject := v.(ledger.Object)
				if !isObject {
					return unsupported("a paused category that is not an object")
				}
				pause = po
			}
		}
		if pause != nil && pause.Get("row_pending") != true {
			return nil
		}
		if pause == nil {
			pause = ledger.Object{{Key: "at", Value: now.Format("2006-01-02T15:04:05.000Z")}, {Key: "spent_usd", Value: spent}, {Key: "reserved_usd", Value: reserved},
				{Key: "cap_usd", Value: p.cap}, {Key: "stop_at", Value: p.stopAt}, {Key: "role", Value: g.o.Role}, {Key: "block", Value: strPtr(g.o.Block)}, {Key: "row_pending", Value: true}}
			next := ledger.Object{}
			if paused != nil {
				next = paused.Clone()
			}
			next.Set(p.category, pause)
			grant.Set("paused", next)
			record.Set("autopilot", grant)
			if err := state.SaveRun(g.home, record); err != nil {
				return err
			}
		}
		row := ledger.Object{{Key: "event", Value: "autopilot.pause"}, {Key: "grant_id", Value: p.grantID}, {Key: "category", Value: p.category},
			{Key: "spent_usd", Value: pause.Get("spent_usd")}, {Key: "reserved_usd", Value: pause.Get("reserved_usd")}, {Key: "cap_usd", Value: pause.Get("cap_usd")},
			{Key: "stop_at", Value: pause.Get("stop_at")}, {Key: "role", Value: pause.Get("role")}, {Key: "block", Value: pause.Get("block")}, {Key: "ts", Value: pause.Get("at")}}
		signed, err := state.SignRow(g.home, run, row)
		if err != nil {
			return nil // still pending: the next check writes it (Node's catch)
		}
		if write(signed) != nil {
			return nil
		}
		pause.Set("row_pending", false)
		return state.SaveRun(g.home, record)
	})
}

func sameJSON(a, b any) bool {
	x, err1 := ledger.Stringify(a, false)
	y, err2 := ledger.Stringify(b, false)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// has is hasOwn for an ordered object: the value and whether the key is there.
func has(o ledger.Object, key string) (any, bool) {
	for _, f := range o {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}
