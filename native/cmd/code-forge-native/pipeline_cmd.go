package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/ledger"
)

const pipelineUsage = "usage: code-forge pipeline status|next [--json] [--root <dir>]\n"

func init() {
	routes["pipeline"] = smallVerb{
		name: "pipeline", usage: pipelineUsage,
		subUsage: map[string]string{
			"status": "usage: code-forge pipeline status [--json] [--root <dir>]\n",
			"next":   "usage: code-forge pipeline next [--json] [--root <dir>]\n",
		},
		prepare: preparePipeline,
	}.run
}

func jsQuote(s string) string { b, _ := ledger.Stringify(s, false); return string(b) }

type pipelineRow struct {
	id, skill, state, problem string
	needs                     []string
}

// preparePipeline is src/cli/pipeline.mjs with loadPipeline and
// pipelineStatus. Every read happens here, so a pipeline or outcome whose
// diagnostics come from YAML or AJV wording goes to Node before any output.
func preparePipeline(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usage := func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, pipelineUsage)
		return 2
	}
	if len(args) == 0 || (args[0] != "status" && args[0] != "next") {
		return usage, 0, true
	}
	command, rest := args[0], args[1:]
	root, haveRoot, asJSON := "", false, false
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--root" {
			if haveRoot || i+1 >= len(rest) || rest[i+1] == "" || strings.HasPrefix(rest[i+1], "-") {
				return usage, 0, true
			}
			root, haveRoot = rest[i+1], true
			i++
		} else if rest[i] == "--json" && !asJSON {
			asJSON = true
		} else {
			return usage, 0, true
		}
	}
	if !haveRoot {
		root, _ = os.Getwd()
	}
	root = absPath(root)

	data, err := os.ReadFile(filepath.Join(root, "code-forge.pipeline.yml"))
	if errors.Is(err, syscall.ENOENT) {
		return func(stdout, _ io.Writer) int {
			if asJSON {
				fmt.Fprint(stdout, "{\"exists\":false}\n")
			} else {
				fmt.Fprintf(stdout, "no code-forge.pipeline.yml in %s\n", root)
			}
			return 0
		}, 0, true
	}
	problemsExit := func(problems []string) func(_, stderr io.Writer) int {
		return func(_, stderr io.Writer) int {
			for _, problem := range problems {
				fmt.Fprintf(stderr, "%s\n", problem)
			}
			return 1
		}
	}
	if errors.Is(err, syscall.EISDIR) {
		return problemsExit([]string{"Cannot read pipeline (EISDIR)"}), 0, true
	}
	if err != nil {
		return nil, hPipelineUnsupported, false
	}
	parsed, err := blockopen.ParseAcceptance(data)
	if err != nil {
		return nil, hPipelineUnsupported, false
	}
	steps, ok := blockopen.PipelineSteps(parsed)
	if !ok {
		return nil, hPipelineUnsupported, false
	}
	needsOf := func(step ledger.Object) []string {
		out := []string{}
		values, _ := step.Get("needs").([]any)
		for _, v := range values {
			out = append(out, v.(string))
		}
		return out
	}
	byID := map[string]ledger.Object{}
	var problems []string
	for _, step := range steps {
		id := step.Get("id").(string)
		if _, dup := byID[id]; dup {
			problems = append(problems, "duplicate step id "+jsQuote(id))
		}
		byID[id] = step
	}
	for _, step := range steps {
		for _, need := range needsOf(step) {
			if _, known := byID[need]; !known {
				problems = append(problems, "step "+jsQuote(step.Get("id").(string))+": unknown need "+jsQuote(need))
			}
		}
	}
	if len(problems) == 0 {
		visiting, visited := map[string]bool{}, map[string]bool{}
		var visit func(id string)
		visit = func(id string) {
			if visiting[id] {
				problems = append(problems, "cycle at step "+jsQuote(id))
				return
			}
			if visited[id] {
				return
			}
			visiting[id] = true
			for _, need := range needsOf(byID[id]) {
				visit(need)
			}
			delete(visiting, id)
			visited[id] = true
		}
		for _, step := range steps {
			visit(step.Get("id").(string))
		}
	}
	if len(problems) > 0 {
		return problemsExit(problems), 0, true
	}

	rows := map[string]*pipelineRow{}
	unsupported := false
	var evaluate func(step ledger.Object) *pipelineRow
	evaluate = func(step ledger.Object) *pipelineRow {
		id := step.Get("id").(string)
		if row, done := rows[id]; done {
			return row
		}
		row := &pipelineRow{id: id, skill: step.Get("skill").(string), state: "waiting", needs: needsOf(step)}
		ready := true
		for _, need := range row.needs {
			if evaluate(byID[need]).state != "approved" {
				ready = false
				break
			}
		}
		if ready {
			row.state, row.problem = pipelineOutcomeState(filepath.Join(root, step.Get("outcome").(string)), root, &unsupported)
		}
		rows[id] = row
		return row
	}
	ordered := make([]*pipelineRow, 0, len(steps))
	for _, step := range steps {
		ordered = append(ordered, evaluate(step))
	}
	if unsupported {
		return nil, hPipelineUnsupported, false
	}

	return func(stdout, _ io.Writer) int {
		var b strings.Builder
		if command == "status" {
			if asJSON {
				list := make([]any, 0, len(ordered))
				for _, row := range ordered {
					o := ledger.Object{{Key: "id", Value: row.id}, {Key: "skill", Value: row.skill}, {Key: "state", Value: row.state}}
					if row.problem != "" {
						o = append(o, ledger.Field{Key: "problem", Value: row.problem})
					}
					list = append(list, o)
				}
				out, _ := ledger.Stringify(ledger.Object{{Key: "exists", Value: true}, {Key: "steps", Value: list}}, false)
				b.Write(out)
				b.WriteString("\n")
			} else {
				for _, row := range ordered {
					fmt.Fprintf(&b, "%s  %s  %s", row.id, row.state, row.skill)
					if row.problem != "" {
						b.WriteString("  — " + row.problem)
					}
					b.WriteString("\n")
				}
			}
			fmt.Fprint(stdout, b.String())
			return 0
		}
		ready := []any{}
		waiting := []any{}
		var waitingText []string
		done := true
		for _, row := range ordered {
			if row.state == "ready" {
				ready = append(ready, row.id)
			}
			if row.state != "approved" {
				done = false
			}
			if row.state == "waiting" {
				needs := []any{}
				var names []string
				for _, need := range row.needs {
					if rows[need].state != "approved" {
						needs = append(needs, need)
						names = append(names, need)
					}
				}
				waiting = append(waiting, ledger.Object{{Key: "id", Value: row.id}, {Key: "needs", Value: needs}})
				waitingText = append(waitingText, row.id+"  waiting for "+strings.Join(names, ", "))
			}
		}
		switch {
		case asJSON:
			out, _ := ledger.Stringify(ledger.Object{{Key: "exists", Value: true}, {Key: "ready", Value: ready}, {Key: "waiting", Value: waiting}, {Key: "done", Value: done}}, false)
			b.Write(out)
			b.WriteString("\n")
		case len(ready) > 0:
			for _, id := range ready {
				b.WriteString(id.(string) + "\n")
			}
		case done:
			b.WriteString("all steps approved\n")
		default:
			b.WriteString("nothing ready\n")
			for _, line := range waitingText {
				b.WriteString(line + "\n")
			}
		}
		fmt.Fprint(stdout, b.String())
		return 0
	}, 0, true
}

// pipelineOutcomeState is one step of pipelineStatus once its needs are
// approved: ready while the outcome file is absent, else its status, or
// invalid with the first problem.
func pipelineOutcomeState(file, root string, unsupported *bool) (string, string) {
	if _, err := os.Stat(file); err != nil {
		return "ready", ""
	}
	data, err := os.ReadFile(file)
	if errors.Is(err, syscall.EISDIR) {
		return "invalid", "Cannot read outcome " + jsQuote(file) + " (EISDIR)"
	}
	if err != nil {
		*unsupported = true
		return "", ""
	}
	if !json.Valid(data) {
		return "invalid", "Outcome " + jsQuote(file) + " is not valid JSON"
	}
	outcome, err := ledger.DecodeObject(data)
	if err != nil || !blockopen.ValidOutcome(outcome) {
		*unsupported = true
		return "", ""
	}
	status := outcome.Get("status").(string)
	if status != "approved" {
		return status, ""
	}
	problem, err := blockopen.CheckOutcome(file, root)
	if err != nil {
		*unsupported = true
		return "", ""
	}
	if problem != "" {
		return "invalid", problem
	}
	return "approved", ""
}
