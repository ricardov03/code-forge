package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

var outcomeSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// outcomeSlugFor is slugFor in src/config/load.mjs.
func outcomeSlugFor(cfg map[string]any, workspace string) string {
	if project, ok := cfg["project"].(map[string]any); ok {
		if slug, ok := project["slug"].(string); ok && slug != "" {
			return slug
		}
	}
	derived := strings.ToLower(filepath.Base(config.ProjectRootFor(workspace)))
	derived = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(derived, "-")
	derived = regexp.MustCompile(`^-+|-+$`).ReplaceAllString(derived, "")
	if derived == "" {
		return "project"
	}
	return derived
}

// prepareOutcomeRecord is recordOutcome in src/cli/outcome.mjs for an outcome
// that passed its check: the signed outcome.recorded row is written under the
// run lock to the project ledger, with Node's output and exit codes.
func prepareOutcomeRecord(file, root string, step any, haveStep, asJSON bool) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, hOutcomeRecordUnported, false
	}
	pipe := loadPipelineGo(root)
	if !pipe.supported {
		return nil, hOutcomeRecordUnported, false
	}
	if !haveStep && pipe.exists && len(pipe.problems) == 0 {
		target := absPath(file)
		for _, s := range pipe.steps {
			if filepath.Join(root, s.Get("outcome").(string)) == target {
				step = s.Get("id").(string)
				break
			}
		}
	}
	finish := func(problem string, already bool, row ledger.Object) func(stdout, stderr io.Writer) int {
		return func(stdout, stderr io.Writer) int {
			ok := problem == ""
			switch {
			case asJSON:
				out := ledger.Object{{Key: "ok", Value: ok}}
				if ok {
					out = append(out, ledger.Field{Key: "problems", Value: []any{}}, ledger.Field{Key: "already_recorded", Value: already})
					if row != nil {
						out = append(out, ledger.Field{Key: "row", Value: row})
					}
				} else {
					out = append(out, ledger.Field{Key: "problems", Value: []any{problem}})
				}
				data, _ := ledger.Stringify(out, false)
				fmt.Fprintf(stdout, "%s\n", data)
			case !ok:
				fmt.Fprintf(stderr, "%s\n", problem)
			case already:
				fmt.Fprint(stdout, "already recorded\n")
			default:
				fmt.Fprint(stdout, "recorded\n")
			}
			if ok {
				return 0
			}
			return 1
		}
	}
	cfg, _, err := config.LoadOrdered(root)
	if err != nil {
		failure, known := err.(*config.Error)
		switch {
		case !known || failure.Kind == "parse-error":
			return nil, hOutcomeRecordUnported, false
		case failure.Kind != "not-found":
			return finish("project config: "+failure.Message, false, nil), 0, true
		}
		cfg = nil
	}
	slug := outcomeSlugFor(cfg, root)
	if !outcomeSlugPattern.MatchString(slug) {
		return finish("ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got "+jsQuote(slug)+")", false, nil), 0, true
	}
	return func(stdout, stderr io.Writer) int {
		problem, already, row := outcomeRecordRow(home, file, slug, step)
		return finish(problem, already, row)(stdout, stderr)
	}, 0, true
}

func outcomeRecordRow(home, file, slug string, step any) (string, bool, ledger.Object) {
	data, err := os.ReadFile(file)
	if err != nil {
		return err.Error(), false, nil
	}
	outcome, err := ledger.DecodeObject(data)
	if err != nil {
		return err.Error(), false, nil
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(home, ".code-forge", "ledger")
	rows, err := ledger.ReadAllRows(dir, slug)
	if err != nil {
		return err.Error(), false, nil
	}
	run := ""
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok && m["event"] == "run.start" {
			if id, ok := m["run"].(string); ok {
				run = id
			}
		}
	}
	if run == "" {
		return "no project run to sign the outcome; start a run first", false, nil
	}
	already := false
	var written ledger.Object
	err = state.WithRunLock(home, run, func() error {
		current, err := ledger.ReadAllRows(dir, slug)
		if err != nil {
			return err
		}
		for _, r := range current {
			m, ok := r.(map[string]any)
			if !ok || m["event"] != "outcome.recorded" || m["sha256"] != hash {
				continue
			}
			if got, has := m["step"]; has && got == step {
				already = true
				return nil
			}
		}
		row := ledger.Object{{Key: "run", Value: run}, {Key: "event", Value: "outcome.recorded"},
			{Key: "skill", Value: outcome.Get("skill")}, {Key: "step", Value: step},
			{Key: "status", Value: outcome.Get("status")}, {Key: "sha256", Value: hash},
			{Key: "cost_usd", Value: outcome.Get("cost_usd")}, {Key: "minutes", Value: outcome.Get("minutes")}}
		written, err = outcomeSignRow(home, run, slug, row)
		return err
	})
	if err != nil {
		return err.Error(), false, nil
	}
	return "", already, written
}

// outcomeSignRow is writeSigned(run, (signed) => appendRow(signed, {slug})):
// loadKey's checks, signRow, then the config slug's ledger.
func outcomeSignRow(home, run, slug string, row ledger.Object) (ledger.Object, error) {
	keyFile := filepath.Join(home, ".code-forge", "runs", run+".key")
	info, err := os.Stat(keyFile)
	if os.IsNotExist(err) {
		return nil, &state.Error{Code: "no-key", Message: fmt.Sprintf("run %s has no signer key", run)}
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &state.Error{Code: "bad-key-mode", Message: fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, info.Mode().Perm())}
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, &state.Error{Code: "bad-key", Message: fmt.Sprintf("run %s signer key is not 32 bytes", run)}
	}
	signed, err := sign.SignRow(row.Map(), key)
	if err != nil {
		return nil, err
	}
	row.Set("ts", signed["ts"])
	row.Set("mac", signed["mac"])
	return ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, row, ledger.DefaultRotateAtBytes)
}
