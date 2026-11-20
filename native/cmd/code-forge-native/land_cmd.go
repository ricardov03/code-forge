package main

import (
	"fmt"
	"io"

	"github.com/ricardov03/code-forge/native/landmerge"
)

// B689: the top-level `code-forge land`. src/cli/land.mjs held block land's
// body and no default handler, so Node refused every form but --help with
// `verb "land" has no default export function`, exit 1. B879 (B801 audit
// P07): `land` alone is a usage error: exit 2, the usage, a line that names
// the real command and its next line (the error log keeps the refusal line).
// --help and -h still print the usage, exit 0. `block land` is block_land.go.

const landUsage = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"

const landRefusal = "land: not a command on its own; code-forge block land lands a closed block\n"

// B990: `land merge-driver <base> <ours> <theirs> [<path>]` is a git merge
// driver for the `merge=union` attribute (landmerge.MergeDriver): git's union
// merge, then a CHANGELOG.md's repeated ### headings folded, so a plain
// `git merge` (a wave combine) keeps one heading per kind like a land does.
const landMergeDriverUsage = "usage: code-forge land merge-driver <base> <ours> <theirs> [<path>]\n" +
	"  Git merge driver for merge=union files: union merge, then one ### heading per kind in CHANGELOG.md.\n" +
	"  Enable: git config merge.union.driver 'code-forge land merge-driver %O %A %B %P'\n"

func init() {
	routes["land"] = smallVerb{name: "land", usage: landUsage, subs: []string{"merge-driver"},
		subUsage: map[string]string{"merge-driver": landMergeDriverUsage}, prepare: prepareLand}.run
}

func prepareLand(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	if len(args) > 0 && args[0] == "merge-driver" {
		return func(_, stderr io.Writer) int {
			files := args[1:]
			for _, a := range files {
				if len(a) > 1 && a[0] == '-' {
					files = nil
					break
				}
			}
			if len(files) < 3 || len(files) > 4 {
				fmt.Fprint(stderr, landMergeDriverUsage)
				return 2
			}
			name := ""
			if len(files) == 4 {
				name = files[3]
			}
			if err := landmerge.MergeDriver(files[0], files[1], files[2], name); err != nil {
				fmt.Fprintf(stderr, "land merge-driver: %v\n", err)
				return 1
			}
			return 0
		}, 0, true
	}
	return func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, landUsage)
		fmt.Fprint(stderr, landRefusal+refusalNext("code-forge block land <id> --run <run>"))
		return 2
	}, 0, true
}
