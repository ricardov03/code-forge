package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const upgradeUsage = "usage: code-forge upgrade [--source <path>] [--plain]\n"

func init() {
	routes["upgrade"] = smallVerb{name: "upgrade", usage: upgradeUsage, prepare: prepareUpgrade}.run
}

// prepareUpgrade is src/cli/upgrade.mjs up to its install records: usage
// and a --source that is missing or not a directory answer here. Re-pointing
// the recorded installs, the release notes and the docs sync stay in Node,
// as does the default source (the package's own skill directory).
func prepareUpgrade(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	refuse := func(code int, text string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, text)
			return code
		}, 0, true
	}
	plain, source, hasSource := false, "", false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--plain" && !plain:
			plain = true
		case args[i] == "--source" && !hasSource:
			if i+1 >= len(args) || args[i+1] == "" || len(args[i+1]) >= 2 && args[i+1][:2] == "--" {
				return refuse(2, upgradeUsage)
			}
			source, hasSource = args[i+1], true
			i++
		default:
			return refuse(2, upgradeUsage)
		}
	}
	if !hasSource {
		return nil, hUpgradeRelink, false
	}
	resolved := filepath.Clean(source)
	if !filepath.IsAbs(source) {
		resolved = filepath.Join(physicalCwd(), source)
	}
	// Node strips terminal control sequences from what it prints; such a
	// path keeps Node's own output.
	for _, r := range resolved {
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return nil, hUpgradeControlChars, false
		}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return refuse(1, "upgrade: source \""+resolved+"\" does not exist\n")
	}
	if !info.IsDir() {
		return refuse(1, "upgrade: source \""+resolved+"\" is not a directory\n")
	}
	return nil, hUpgradeRelink, false
}
