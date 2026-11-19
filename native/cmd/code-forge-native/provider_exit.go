package main

import (
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B876 (B801 audit P09): a provider session that ends without an answer
// (failed, timeout, unavailable) shows why: the last lines the provider CLI
// wrote on stderr (packet lines and PEM blocks removed, tokens masked), or
// that it wrote nothing, and the step that checks the CLI and its login.

const (
	providerExitMaxLines = 5
	providerExitMaxRunes = 300
	providerExitNext     = "code-forge doctor (checks each provider CLI and its login; code-forge logs --last 5 shows the error log)"
)

// providerExitTail is the provider's stderr as lines safe to print: a
// session's stderr_tail when it has one, else the tail of its stderr.
func providerExitTail(res ledger.Object, packet *string) []string {
	tail, _ := res.Get("stderr_tail").(string)
	if tail == "" {
		if raw, ok := res.Get("stderr").(string); ok && raw != "" {
			safe, _ := session.StderrTail(raw, packet)
			tail, _ = safe.(string)
		}
	}
	var lines []string
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > providerExitMaxRunes {
			line = string(r[:providerExitMaxRunes]) + "…"
		}
		lines = append(lines, maskTokens(line))
	}
	if len(lines) > providerExitMaxLines {
		lines = lines[len(lines)-providerExitMaxLines:]
	}
	return lines
}

// providerExitDetail is the text printed before "<verb>: <status> (<reason>)":
// the provider's stderr lines; for a plain non-zero exit with an empty
// stderr, a line that says the CLI printed nothing (a timeout or a
// classified unavailable reason already names the cause).
func providerExitDetail(verb string, res ledger.Object, packet *string) string {
	lines := providerExitTail(res, packet)
	if len(lines) == 0 {
		if status, _ := res.Get("status").(string); status == "failed" {
			return verb + ": the provider CLI printed nothing on stderr\n"
		}
		return ""
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(verb + ": provider stderr: " + line + "\n")
	}
	return b.String()
}
