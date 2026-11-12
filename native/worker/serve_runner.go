package worker

// B489: what loop.mjs createWorker gives its pool besides runOne: the
// config in force, the signed `ledger` writer, the scheduling hints and the
// pooled session spawn.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// ConfigInForce is snapshotFor(readRun(run), ”)?.config: nil when the run
// has no snapshot; bad when the snapshot is missing or altered (Node throws).
func (w *TicketRunner) ConfigInForce() (cfg map[string]any, bad bool, err error) {
	data, err := os.ReadFile(filepath.Join(w.Home, ".code-forge", "runs", w.RunID+".json"))
	if err != nil {
		return nil, false, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, false, err
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, false, nil
	}
	return w.configInForce(data)
}

// Ledger is createWorker's `ledger`: the row with the run first, signed with
// the run key, appended to the project ledger.
func (w *TicketRunner) Ledger(row ledger.Object) error {
	out := ledger.Object{{Key: "run", Value: w.RunID}}
	for _, f := range row {
		out.Set(f.Key, f.Value)
	}
	signed, err := w.signObject(out)
	if err != nil {
		return err
	}
	return w.writeRow(signed)
}

// Settings is poolSettings' read: the snapshot in force, else the boot
// config (an unreadable record keeps the boot config).
func (w *TicketRunner) Settings() (map[string]any, any) {
	rec, _, err := w.readRecord()
	if err != nil || rec == nil {
		return w.Cfg, nil
	}
	cfg, bad, err := w.ConfigInForce()
	if err != nil || bad || cfg == nil {
		return w.Cfg, rec
	}
	return cfg, rec
}

// FileKey is fileKeyOf: the (block, file) key of a ticket that reads valid.
func (w *TicketRunner) FileKey(id string) (FileKey, bool) {
	_, t, err := w.decideRead(id)
	if err != nil || t == nil {
		return FileKey{}, false
	}
	block, _ := t.Get("block").(string)
	file, _ := t.Get("file").(string)
	return FileKey{Key: FileLockKey(block, file), Block: block}, true
}

// ProviderHint is providerHint without size routing: levels.L2.provider ??
// provider of the config in force; "" (no hint) when routes are set or the
// config is unknown. A hint only orders admission; spawn enforces the caps.
func (w *TicketRunner) ProviderHint(_ string, record any) string {
	cfg := w.Cfg
	if record != nil {
		if inForce, bad, err := w.ConfigInForce(); err == nil && !bad && inForce != nil {
			cfg = inForce
		}
	}
	if routes, ok := reviewSection(cfg)["routes"].([]any); ok && len(routes) > 0 {
		return ""
	}
	levels, _ := cfg["levels"].(map[string]any)
	l2, _ := levels["L2"].(map[string]any)
	if s, ok := l2["provider"].(string); ok {
		return s
	}
	if s, ok := cfg["provider"].(string); ok {
		return s
	}
	return ""
}

// pooled is spawnPooled: one session of ticket id waits for its provider
// slot, then runs with provider_concurrency capped at provider_parallel.
func (w *TicketRunner) pooled(id string, cfg map[string]any, spawn func(review.SpawnOpts) (ledger.Object, error)) func(review.SpawnOpts) (ledger.Object, error) {
	if w.Pool == nil {
		return spawn
	}
	return func(in review.SpawnOpts) (ledger.Object, error) {
		c := in.Cfg
		if c == nil {
			c = cfg
		}
		release := w.Pool.AcquireSession(id, SessionProvider(c, in.Level))
		defer release()
		in.Cfg = w.Pool.SessionCfg(c)
		return spawn(in)
	}
}

// RunRecord is readRun for checkIdle: an error when the record is missing or
// does not parse (Node's checkIdle catches and keeps the worker alive).
func (w *TicketRunner) RunRecord() (ledger.Object, error) {
	rec, syntax, err := w.readRecord()
	if err != nil {
		return nil, err
	}
	if rec == nil || syntax {
		return nil, errors.New("run record unreadable")
	}
	return rec, nil
}
