package blockopen

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

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
			label = jsValueString(status)
		}
		blocking = append(blocking, dependency+" ("+label+")")
	}
	if len(blocking) > 0 {
		return refusal("after-not-done", "block "+id+": --after dependencies not closed: "+strings.Join(blocking, ", "))
	}
	return nil
}

// ErrPipelineForm: code-forge.pipeline.yml has a YAML form, a shape outside
// schema/pipeline.schema.json (AJV's wording stays with Node) or a read
// error code the port does not name (B581).
var ErrPipelineForm = errors.New("block open: a pipeline form Go does not serve")

// ErrOutcomeForm: an outcome contract checkOutcome reports in wording Go
// does not reproduce: AJV diagnostics, bytes that are not UTF-8 or an
// unnamed read error code (B581).
var ErrOutcomeForm = errors.New("block open: an outcome form Go does not serve")

// OutcomeCheck is checkOutcome in src/outcome/outcome.mjs: every problem in
// Node's order; ok is false where Go does not reproduce Node's wording.
type OutcomeCheck func(file, root string) (problems []string, ok bool)

// boErrnoCode is the err.code Node reports for a failed fs call; ok is false
// for an error the port does not name.
func boErrnoCode(err error) (string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "", false
	}
	name, ok := map[syscall.Errno]string{
		syscall.ENOENT: "ENOENT", syscall.EISDIR: "EISDIR", syscall.ENOTDIR: "ENOTDIR", syscall.EACCES: "EACCES",
		syscall.EPERM: "EPERM", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.ELOOP: "ELOOP",
	}[errno]
	return name, ok
}

// AcceptedOutcomes is acceptedOutcomes in src/cli/block.mjs: the pipeline
// is read and checked, then every named step's outcome, before state open
// writes anything. Forms whose wording stays with Node return
// ErrPipelineForm or ErrOutcomeForm (B581).
func AcceptedOutcomes(root, id string, ids []string, check OutcomeCheck) ([]any, error) {
	bad := func(problem string) error {
		return refusal("bad-after-outcome", "block "+id+": --after-outcome: "+problem)
	}
	oneLine := strings.NewReplacer("\r", " ", "\n", " ")
	data, err := os.ReadFile(filepath.Join(root, "code-forge.pipeline.yml"))
	if errors.Is(err, syscall.ENOENT) {
		return nil, bad("no code-forge.pipeline.yml in " + root)
	}
	if err != nil {
		code, ok := boErrnoCode(err)
		if !ok {
			return nil, ErrPipelineForm
		}
		return nil, bad("Cannot read pipeline (" + code + ")")
	}
	v, err := ParseYAMLJS(data)
	if errors.Is(err, ErrYAMLForm) {
		return nil, ErrPipelineForm
	}
	if err != nil {
		return nil, bad(oneLine.Replace("Invalid pipeline YAML: " + err.Error()))
	}
	steps, ok := PipelineSteps(v)
	if !ok {
		return nil, ErrPipelineForm
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
		notReady := func(problem string) error {
			return refusal("outcome-not-ready", "block "+id+": --after-outcome step "+name+": "+problem)
		}
		file := filepath.Join(root, text(byID[name].Get("outcome")))
		// existsSync: any stat error is a missing outcome.
		if _, err := os.Stat(file); err != nil {
			return nil, notReady("missing outcome")
		}
		problems, ok := check(file, root)
		if !ok {
			return nil, ErrOutcomeForm
		}
		if len(problems) > 0 {
			return nil, notReady(problems[0])
		}
		data, err := os.ReadFile(file)
		if err != nil {
			code, ok := boErrnoCode(err)
			if !ok {
				return nil, ErrOutcomeForm
			}
			return nil, notReady("cannot read outcome (" + code + ")")
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

// jsValueString is JavaScript's String(value) for a decoded JSON value, as a
// template literal prints it (B581).
func jsValueString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		data, _ := ledger.Stringify(x, false)
		return string(data)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = jsValueString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
