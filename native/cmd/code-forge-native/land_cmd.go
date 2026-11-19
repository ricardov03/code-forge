package main

import (
	"fmt"
	"io"
)

// B689: the top-level `code-forge land`. src/cli/land.mjs held block land's
// body and no default handler, so Node refused every form but --help with
// `verb "land" has no default export function`, exit 1. B879 (B801 audit
// P07): `land` alone is a usage error: exit 2, the usage, a line that names
// the real command and its next line (the error log keeps the refusal line).
// --help and -h still print the usage, exit 0. `block land` is block_land.go.

const landUsage = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"

const landRefusal = "land: not a command on its own; code-forge block land lands a closed block\n"

func init() {
	routes["land"] = smallVerb{name: "land", usage: landUsage, prepare: prepareLand}.run
}

func prepareLand([]string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	return func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, landUsage)
		fmt.Fprint(stderr, landRefusal+refusalNext("code-forge block land <id> --run <run>"))
		return 2
	}, 0, true
}
