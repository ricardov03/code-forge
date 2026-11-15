package main

import (
	"fmt"
	"io"
)

// B689: the top-level `code-forge land`. src/cli/land.mjs holds block land's
// body (landBlock) and exports no default handler, so the Node router prints
// its USAGE for a leading --help or -h and, for every other form, refuses with
// `code-forge: verb "land" has no default export function` and exit 1 (with
// the breadcrumb and errors.jsonl entry of any failed verb). Go does the same
// and never hands it to Node. `block land` is block_land.go.

const landUsage = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"

func init() {
	routes["land"] = smallVerb{name: "land", usage: landUsage, prepare: prepareLand}.run
}

func prepareLand([]string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	return func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, "code-forge: verb \"land\" has no default export function\n")
		return 1
	}, 0, true
}
