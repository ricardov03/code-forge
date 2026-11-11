package blockopen

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/ledger"
)

func unique(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// CheckAfter is called under the publication lock. The CLI also calls it before
// loading outcomes, as Node does, so block dependencies retain error precedence.
func CheckAfter(record ledger.Object, id string, ids []string) error {
	if slices.Contains(ids, id) {
		return refusal("bad-after", "block "+id+" cannot name itself in --after")
	}
	blocking := []string{}
	for _, dependency := range unique(ids) {
		status := object(object(record.Get("blocks")).Get(dependency)).Get("status")
		if status == "closed" {
			continue
		}
		label := "not in run"
		if status != nil {
			var ok bool
			label, ok = status.(string)
			if !ok {
				return blockops.ErrHandover
			}
		}
		blocking = append(blocking, dependency+" ("+label+")")
	}
	if len(blocking) > 0 {
		return refusal("after-not-done", "block "+id+": --after dependencies not closed: "+strings.Join(blocking, ", "))
	}
	return nil
}

// Unsupported schema and YAML forms are delegated before any state mutation;
// valid pipeline contracts and their readiness errors are evaluated locally.
func AcceptedOutcomes(root, id string, ids []string) ([]any, error) {
	bad := func(problem string) error {
		return refusal("bad-after-outcome", "block "+id+": --after-outcome: "+problem)
	}
	data, err := os.ReadFile(filepath.Join(root, "code-forge.pipeline.yml"))
	if os.IsNotExist(err) {
		return nil, bad("no code-forge.pipeline.yml in " + root)
	}
	if err != nil {
		return nil, blockops.ErrHandover
	}
	v, err := ParseAcceptance(data)
	if err != nil {
		return nil, err
	}
	steps, ok := PipelineSteps(v)
	if !ok {
		return nil, blockops.ErrHandover
	}
	byID := map[string]ledger.Object{}
	for _, step := range steps {
		byID[text(step.Get("id"))] = step
	}
	seen := map[string]bool{}
	for _, step := range steps {
		name := text(step.Get("id"))
		if seen[name] {
			return nil, bad("duplicate step id " + quote(name))
		}
		seen[name] = true
	}
	for _, step := range steps {
		needs, _ := step.Get("needs").([]any)
		for _, need := range needs {
			if byID[text(need)] == nil {
				return nil, bad("step " + quote(text(step.Get("id"))) + ": unknown need " + quote(text(need)))
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return bad("cycle at step " + quote(name))
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		needs, _ := byID[name].Get("needs").([]any)
		for _, need := range needs {
			if err := visit(text(need)); err != nil {
				return err
			}
		}
		delete(visiting, name)
		visited[name] = true
		return nil
	}
	for _, step := range steps {
		if err := visit(text(step.Get("id"))); err != nil {
			return nil, err
		}
	}
	for _, name := range unique(ids) {
		if byID[name] == nil {
			return nil, bad("unknown step " + name)
		}
	}
	out := []any{}
	for _, name := range unique(ids) {
		file := filepath.Join(root, text(byID[name].Get("outcome")))
		problem, err := CheckOutcome(file, root)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			return nil, refusal("outcome-not-ready", "block "+id+": --after-outcome step "+name+": "+problem)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, blockops.ErrHandover
		}
		out = append(out, ledger.Object{{Key: "step", Value: name}, {Key: "sha256", Value: fmt.Sprintf("%x", sha256.Sum256(data))}})
	}
	return out, nil
}

func only(o ledger.Object, keys ...string) bool {
	if o == nil {
		return false
	}
	for _, f := range o {
		if !slices.Contains(keys, f.Key) {
			return false
		}
	}
	return true
}
func stringField(o ledger.Object, name string) bool { _, ok := o.Get(name).(string); return ok }
func stringList(v any) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, v := range a {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}

// PipelineSteps returns the steps of a parsed code-forge.pipeline.yml whose
// shape schema/pipeline.schema.json accepts; ok is false for any other shape,
// whose AJV diagnostics stay with Node.
func PipelineSteps(v any) ([]ledger.Object, bool) {
	pipeline := object(v)
	if !stringField(pipeline, "root") || !only(pipeline, "root", "steps") {
		return nil, false
	}
	values, ok := pipeline.Get("steps").([]any)
	if !ok || len(values) == 0 {
		return nil, false
	}
	steps := []ledger.Object{}
	for _, v := range values {
		step := object(v)
		name := text(step.Get("id"))
		outcome := text(step.Get("outcome"))
		if !only(step, "id", "skill", "needs", "outcome", "approval") || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(name) || !stringField(step, "skill") || !stringField(step, "outcome") || outcome == "" || strings.ContainsAny(outcome[:1], `/\`) || regexp.MustCompile(`^[A-Za-z]:`).MatchString(outcome) || strings.ContainsAny(outcome, "\r\n\u2028\u2029") || slices.Contains(strings.FieldsFunc(outcome, func(r rune) bool { return r == '/' || r == '\\' }), "..") || !slices.Contains([]any{"human", "none"}, step.Get("approval")) {
			return nil, false
		}
		for _, field := range step {
			if field.Key == "needs" && !stringList(field.Value) {
				return nil, false
			}
		}
		steps = append(steps, step)
	}
	return steps, true
}

// CheckOutcome is checkOutcome in src/outcome/outcome.mjs for a contract of
// a supported shape: the first problem, or "" when it is ready.
func CheckOutcome(file, root string) (string, error) {
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return "missing outcome", nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", blockops.ErrHandover
	}
	if !json.Valid(data) {
		return "Outcome " + quote(file) + " is not valid JSON", nil
	}
	o, err := ledger.DecodeObject(data)
	if err != nil || !validOutcome(o) {
		return "", blockops.ErrHandover
	}
	if o.Get("status") != "approved" {
		return "status " + quote(text(o.Get("status"))) + ": must be approved", nil
	}
	checks, _ := o.Get("checks").([]any)
	for _, v := range checks {
		check := object(v)
		if check.Get("result") == "fail" {
			return "check " + quote(text(check.Get("name"))) + ": result is fail", nil
		}
	}
	files, _ := o.Get("files").([]any)
	for _, v := range files {
		entry := object(v)
		name := text(entry.Get("path"))
		label := "file " + quote(name) + ": "
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || regexp.MustCompile(`^[A-Za-z]:[/\\]`).MatchString(name) || slices.Contains(strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
			return label + "path must be relative to root without ..", nil
		}
		target := filepath.Join(root, name)
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", blockops.ErrHandover
		}
		real, err := filepath.EvalSymlinks(target)
		if os.IsNotExist(err) {
			return label + "missing file", nil
		}
		if err != nil {
			return "", blockops.ErrHandover
		}
		rel, err := filepath.Rel(realRoot, real)
		if err != nil {
			return "", blockops.ErrHandover
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return label + "path leaves root through a symlink", nil
		}
		bytes, err := os.ReadFile(target)
		if err != nil {
			return "", blockops.ErrHandover
		}
		if text(entry.Get("sha256")) != fmt.Sprintf("%x", sha256.Sum256(bytes)) {
			return label + "sha256 does not match", nil
		}
	}
	return "", nil
}

// ValidOutcome reports a contract schema/outcome.schema.json accepts. Shape
// checks deliberately delegate invalid contracts to Node's AJV diagnostics.
func ValidOutcome(o ledger.Object) bool { return validOutcome(o) }

func validOutcome(o ledger.Object) bool {
	if !only(o, "skill", "version", "request", "status", "approved_by", "artifact", "files", "decisions", "checks", "cost_usd", "minutes") || !stringField(o, "skill") || !slices.Contains([]any{"draft", "approved", "blocked", "failed"}, o.Get("status")) {
		return false
	}
	files, ok := o.Get("files").([]any)
	if !ok {
		return false
	}
	for _, v := range files {
		f := object(v)
		if !only(f, "path", "sha256") || !stringField(f, "path") || !stringField(f, "sha256") {
			return false
		}
	}
	for _, f := range o {
		switch f.Key {
		case "version", "request":
			if _, ok := f.Value.(string); !ok {
				return false
			}
		case "approved_by", "artifact":
			if _, ok := f.Value.(string); f.Value != nil && !ok {
				return false
			}
		case "cost_usd", "minutes":
			if _, ok := f.Value.(float64); f.Value != nil && !ok {
				return false
			}
		case "decisions":
			if !stringList(f.Value) {
				return false
			}
		case "checks":
			checks, ok := f.Value.([]any)
			if !ok {
				return false
			}
			for _, v := range checks {
				c := object(v)
				if !only(c, "name", "result", "detail", "evidence") || !stringField(c, "name") || !slices.Contains([]any{"pass", "warn", "fail"}, c.Get("result")) {
					return false
				}
				for _, f := range c {
					if f.Key == "detail" && !stringList(f.Value) || f.Key == "evidence" && !stringField(c, "evidence") {
						return false
					}
				}
			}
		}
	}
	return true
}
