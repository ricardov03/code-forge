package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ricardov03/code-forge/native/docsync"
)

func init() { routes["docs"] = runDocs }

const docsUsage = "usage: code-forge docs sync|path [--json]"

// runDocs serves `docs sync` and `docs path`. Node has no docs verb, so bad
// input is a usage error here instead of a handover.
func runDocs(args []string) int {
	if len(args) == 0 || (args[0] != "sync" && args[0] != "path") {
		fmt.Fprintln(safeStderr, docsUsage)
		return 2
	}
	asJSON := false
	for _, arg := range args[1:] {
		if arg != "--json" || asJSON {
			fmt.Fprintln(safeStderr, docsUsage)
			return 2
		}
		asJSON = true
	}
	home, _ := os.UserHomeDir()
	root, err := docsync.PackageRoot()
	want := ""
	if err != nil {
		root = ""
	} else {
		want, _ = docsync.InstalledVersion(root)
	}
	if args[0] == "sync" {
		result := docsync.Result{Status: "none", Reason: "no home directory"}
		if home != "" {
			result = docsync.Sync(context.Background(), docsync.URL(), home, root, want)
		}
		if asJSON {
			_ = json.NewEncoder(safeStdout).Encode(result)
		} else {
			fmt.Fprintln(safeStdout, result.Line())
		}
		return 0
	}
	path, source, ok := "", "", false
	if home != "" && root != "" {
		path, source, ok = docsync.Best(home, root, want)
	}
	if !ok {
		fmt.Fprintln(safeStderr, "docs: none; run code-forge docs sync")
		return 1
	}
	if asJSON {
		_ = json.NewEncoder(safeStdout).Encode(map[string]string{"path": path, "source": source})
	} else {
		fmt.Fprintln(safeStdout, path)
	}
	return 0
}
