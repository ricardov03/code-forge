package main

import (
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B821: `autopilot found` runs by itself. A red gate of a run's block, a
// land that stops, a hand-over of a command that names a run and the FOUND
// lines of a coder report (block close, and block land through its close)
// each record a finding through apfAuto: only while an autopilot grant is
// active on that run, deduped by fingerprint, never failing the caller.

var apfFailTest = regexp.MustCompile(`(?m)^\s*--- FAIL: (Test[A-Za-z0-9_/]*)`)

// apfFailedTests are the distinct top-level Go test names a `--- FAIL:` line
// names in text, in first-seen order.
func apfFailedTests(text string) []string {
	out := []string{}
	for _, m := range apfFailTest.FindAllStringSubmatch(text, -1) {
		name, _, _ := strings.Cut(m[1], "/")
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// apfTail is the last max bytes of text, cut at a line start.
func apfTail(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	text = text[len(text)-max:]
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	}
	return "[…]\n" + text
}

// apfGateInputs is one finding per red gate of a block's gate run. The
// fingerprint is the gate and its failing tests, so the same red seen again
// (in this block or another) adds nothing.
func apfGateInputs(block string, results []any) []apfInput {
	inputs := []apfInput{}
	for _, v := range results {
		r, _ := v.(ledger.Object)
		if ok, isBool := r.Get("ok").(bool); !isBool || ok {
			continue
		}
		name := autopilotText(r.Get("gate"))
		stdout, _ := r.Get("stdout").(string)
		stderr, _ := r.Get("stderr").(string)
		output := stdout + "\n" + stderr
		tests := apfFailedTests(output)
		code := "no exit code"
		switch c := r.Get("code").(type) {
		case int:
			code = "exit " + strconv.Itoa(c)
		case float64:
			code = "exit " + strconv.FormatFloat(c, 'f', -1, 64)
		}
		title := "the " + name + " gate is red in block " + block
		key := "gate:" + name + ":block:" + block
		evidence := "gate " + name + " red (" + code + ") in block " + block
		if argv, _ := r.Get("argv").([]string); len(argv) > 0 {
			evidence += ": " + strings.Join(argv, " ")
		}
		if len(tests) > 0 {
			title = "the " + name + " gate is red: " + tests[0]
			if len(tests) > 1 {
				title += " (+" + strconv.Itoa(len(tests)-1) + " more)"
			}
			key = "gate:" + name + ":tests:" + strings.Join(tests, ",")
			for _, t := range tests {
				evidence += "\n--- FAIL: " + t
			}
		}
		if tail := apfTail(output, 2500); tail != "" {
			evidence += "\n\n" + tail
		}
		inputs = append(inputs, apfInput{source: "gate", title: title, evidence: evidence, from: block, fingerprint: key, labels: []string{}})
	}
	return inputs
}

// apfLandInput is the finding of a land that stopped (exit 1) with message,
// its last stderr text.
func apfLandInput(block, message string) apfInput {
	if strings.TrimSpace(message) == "" {
		message = "block " + block + " land stopped (exit 1) with no message"
	}
	return apfInput{source: "land", title: "block " + block + " failed to land", evidence: message, from: block, labels: []string{}}
}

// apfFoundGates: the gate run's red gates (B821).
func apfFoundGates(w io.Writer, home, runID, block string, results []any) {
	if runID == "" {
		return
	}
	if inputs := apfGateInputs(block, results); len(inputs) > 0 {
		apfAuto(w, home, runID, inputs...)
	}
}

// apfFoundReport: every FOUND line of a coder report, from block (B821).
// A bad FOUND line is a WARN on w.
func apfFoundReport(w io.Writer, home, runID, block, report string) {
	if runID == "" || !strings.Contains(report, "FOUND ") {
		return
	}
	inputs, warns := apfScan(report, block)
	for _, line := range warns {
		io.WriteString(w, line+"\n")
	}
	if len(inputs) > 0 {
		apfAuto(w, home, runID, inputs...)
	}
}

// apfArgRun is the --run value of a command's args ("" when none).
func apfArgRun(args []string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--run="); ok {
			return v
		}
		if a == "--run" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// apfFoundHandover: a refused command (hand-over) that names a run (B821).
// The refusal's own stderr line stays as it is: the outcome goes nowhere,
// the finding is in the run (autopilot found --list, the binnacle).
func apfFoundHandover(args []string, reason string) {
	runID := apfArgRun(args)
	if runID == "" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	verb, sub := handoverCommand(args)
	command := strings.TrimSpace(verb + " " + sub)
	block := ""
	if len(args) > 2 && apfIDPattern.MatchString(args[2]) {
		block = args[2]
	}
	apfAuto(io.Discard, home, runID, apfInput{
		source:      "handover",
		title:       "code-forge " + command + " hands over: " + reason,
		evidence:    "`code-forge " + command + "` was refused (hand-over reason: " + reason + "). Serve this case in Go, or change the step so it does not need it.",
		from:        block,
		fingerprint: "handover:" + command + ":" + reason,
		labels:      []string{},
	})
}
