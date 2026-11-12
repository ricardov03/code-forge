// Command cf-release is the maintainer release tooling behind `npm run release`, `npm run
// release:site`, `npm run docs-block` and `npm run changelog` (B570). It is not shipped in the
// package: scripts/cf-release.sh builds it from source on every call.
package main

import (
	"fmt"
	"os"
)

const mainUsage = "usage: cf-release <release|release-site|docs-block|changelog> [args...]"

func run(a *app, args []string, root string) int {
	if len(args) == 0 {
		a.err(mainUsage + "\n")
		return 2
	}
	switch args[0] {
	case "release":
		return releaseMain(a, args[1:], root)
	case "release-site":
		return releaseSiteMain(a, args[1:], root)
	case "docs-block":
		return docsBlockMain(a, args[1:], root)
	case "changelog":
		return changelogMain(a, args[1:], root)
	}
	a.err(mainUsage + "\n")
	return 2
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(run(newApp(os.Stdout, os.Stderr), os.Args[1:], root))
}
