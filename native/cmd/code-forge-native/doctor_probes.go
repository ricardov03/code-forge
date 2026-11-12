package main

// B505: the provider CLI probes of the full doctor run (src/doctor/probes.mjs)
// in Go: configuredProviders, the presence/version/--help flag probe per CLI
// (probeCli + src/engines/probe.mjs probeHelpText) and the ping rows.

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// doctorProbeTimeout is the 20 s exec timeout of probeCli's two calls.
var doctorProbeTimeout = 20 * time.Second

// doctorCLINames is provider-cli.mjs PROVIDER_CLI_NAMES.
var doctorCLINames = map[string]string{"anthropic": "claude", "openai": "codex", "xai": "grok"}

// doctorRequiredFlags is probe.mjs REQUIRED_FLAGS.
var doctorRequiredFlags = map[string][]string{
	"claude": {"-p, --print", "--model <model>", "--effort <level>", "--permission-mode <mode>", "--output-format <format>",
		"--no-session-persistence", "--max-budget-usd <amount>", "--disallowedTools, --disallowed-tools <tools...>",
		"--fallback-model <model>", "--safe-mode", "--tools <tools...>", "--strict-mcp-config", "--json-schema <schema>",
		"--system-prompt <prompt>", "--restricted"},
	"codex": {"-m, --model <MODEL>", "-c, --config <key=value>", "-s, --sandbox <SANDBOX_MODE>", "-C, --cd <DIR>", "--json",
		"-o, --output-last-message <FILE>", "--ephemeral", "--ignore-rules", "--ignore-user-config", "--skip-git-repo-check",
		"--output-schema <FILE>"},
	"grok": {"--prompt-file <PATH>", "-m, --model <MODEL>", "--reasoning-effort <EFFORT>", "--permission-mode <MODE>",
		"--cwd <CWD>", "--deny <RULE>", "--json-schema <SCHEMA>", "--disallowed-tools <TOOLS>", "--no-plan", "--no-subagents",
		"--max-turns <N>", "--system-prompt-override <PROMPT>"},
}

// doctorConfiguredProviders is configuredProviders: every provider a level
// (L0..L3) or one of its fallbacks names, first-seen order.
func doctorConfiguredProviders(cfg map[string]any) []string {
	var out []string
	add := func(p any) {
		s, ok := p.(string)
		if !ok {
			return
		}
		for _, have := range out {
			if have == s {
				return
			}
		}
		out = append(out, s)
	}
	levels, _ := cfg["levels"].(map[string]any)
	for _, name := range []string{"L0", "L1", "L2", "L3"} {
		raw := levels[name]
		level, ok := raw.(map[string]any)
		if !ok {
			if raw != nil && raw != false && raw != "" && raw != float64(0) {
				add(cfg["provider"]) // a truthy non-mapping has no provider or fallback
			}
			continue
		}
		if p, has := level["provider"]; has && p != nil {
			add(p)
		} else {
			add(cfg["provider"])
		}
		list, _ := level["fallback"].([]any)
		for _, f := range list {
			if entry, ok := f.(map[string]any); ok {
				add(entry["provider"])
			}
		}
	}
	return out
}

// doctorWordChar is probe.mjs WORD_CHAR_RE for one UTF-16 unit.
func doctorWordChar(u uint16) bool {
	return u == '-' || (u >= '0' && u <= '9') || (u >= 'A' && u <= 'Z') || (u >= 'a' && u <= 'z')
}

// doctorHelpMissing is probeHelpText(cli, help).missing: the required flags
// not found as a whole token (no [A-Za-z0-9-] right before or after).
func doctorHelpMissing(cli, help string) []string {
	hay := utf16.Encode([]rune(help))
	var missing []string
	for _, flag := range doctorRequiredFlags[cli] {
		needle := utf16.Encode([]rune(flag))
		found := false
		for at := 0; at+len(needle) <= len(hay) && !found; at++ {
			match := true
			for j := range needle {
				if hay[at+j] != needle[j] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
			before := at > 0 && doctorWordChar(hay[at-1])
			after := at+len(needle) < len(hay) && doctorWordChar(hay[at+len(needle)])
			found = !before && !after
		}
		if !found {
			missing = append(missing, flag)
		}
	}
	return missing
}

// doctorProbeCli is probeCli(ctx, cli) with bin the CLI to run (the name on
// PATH, or an absolute path). ok is false when the version line holds a cut
// Go cannot print as Node does (a surrogate pair split at 80 units).
func doctorProbeCli(cli, bin, pathEnv string) (rows []doctorRow, present, ok bool) {
	if filepath.IsAbs(bin) {
		_, err := os.Stat(bin)
		present = err == nil
	} else {
		// execFile looks the name up on the probe env's PATH.
		for _, dir := range strings.Split(pathEnv, ":") {
			if dir != "" && commandOnPath(bin, dir) {
				bin, present = filepath.Join(dir, bin), true
				break
			}
		}
	}
	if !present {
		return []doctorRow{{"cli." + cli, "FAIL", cli, "not on PATH"}}, false, true
	}
	version := runChild([]string{bin, "--version"}, doctorProbeTimeout)
	first, _, _ := strings.Cut(jsTrim(nodeUTF8(version.stdout)), "\n")
	if version.ok {
		cut, fits := jsSlice16(first, 80)
		if !fits {
			return nil, true, false
		}
		rows = append(rows, doctorRow{"cli." + cli, "OK", cli, "present, " + cut})
	} else {
		rows = append(rows, doctorRow{"cli." + cli, "WARN", cli, "present, --version failed"})
	}
	argv := []string{bin, "--help"}
	if cli == "codex" {
		argv = []string{bin, "exec", "--help"}
	}
	help := runChild(argv, doctorProbeTimeout)
	// The Codex coder argv conflict (codexCoderFlagConflict) never fires: the
	// built coder argv carries no --approve-for-me (CODER_APPROVAL_POLICY).
	if missing := doctorHelpMissing(cli, nodeUTF8(help.stdout)); len(missing) > 0 {
		rows = append(rows, doctorRow{"flags." + cli, "FAIL", cli + " flags", "missing from --help: " + strings.Join(missing, " | ")})
	} else {
		rows = append(rows, doctorRow{"flags." + cli, "OK", cli + " flags", "every builder flag is in --help"})
	}
	return rows, true, true
}
