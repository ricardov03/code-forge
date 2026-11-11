package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

var lightSecretPath = regexp.MustCompile(`(?i)(^|/)\.env|\.(pem|key|p12)$|(^|/)(credentials|id_rsa|id_ed25519|id_ecdsa)`)

// lightLimits is Node's light block check, shared by block ready and block
// close: the block's owned changed files and their diff +/- lines against the
// base, measured against light.max_files (5) and light.max_lines (300). It
// returns the light_limits blocker when either limit is passed. ok is false
// for a path or diff the Node packet reader refuses with its own diagnostic.
func lightLimits(top, base, prefix string, entry ledger.Object, changed []string, cfg map[string]any) ([]gatecheck.Blocker, bool) {
	lines, count := 0, 0
	for _, file := range changed {
		if !gatecheck.Owns(prefix, entry, file) {
			continue
		}
		if strings.ContainsAny(file, "*?[]") || strings.HasPrefix(file, ":") || lightSecretPath.MatchString(file) {
			return nil, false
		}
		count++
		env := append(gitx.ChildEnv(), "GIT_LITERAL_PATHSPECS=1")
		listed, e := gitx.WithEnv(top, env, "ls-files", "-z", "--", file)
		if e != nil {
			return nil, false
		}
		argv := []string{"diff", "--no-color", "--no-ext-diff", base, "--", file}
		if listed == "" {
			argv = []string{"diff", "--no-color", "--no-ext-diff", "--no-index", "--", "/dev/null", file}
		}
		diff, e := gitx.WithEnv(top, env, argv...)
		if e != nil {
			command, ok := e.(*gitx.CommandError)
			if !ok || listed != "" || command.Code != 1 {
				return nil, false
			}
		}
		hunk := false
		for _, line := range strings.Split(diff, "\n") {
			if strings.HasPrefix(line, "@@") {
				hunk = true
			} else if hunk && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
				lines++
			}
		}
	}
	maxFiles, maxLines := float64(5), float64(300)
	if v, ok := blockMap(cfg["light"])["max_files"].(float64); ok {
		maxFiles = v
	}
	if v, ok := blockMap(cfg["light"])["max_lines"].(float64); ok {
		maxLines = v
	}
	if float64(count) > maxFiles || float64(lines) > maxLines {
		return []gatecheck.Blocker{{Code: "light_limits", Detail: fmt.Sprintf("%d files, %d lines exceed light block limits", count, lines)}}, true
	}
	return nil, true
}
