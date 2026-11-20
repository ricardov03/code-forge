package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B1023: changed-test selection must also check the CLI's reference contract
// when a block changes flags, even if its regression test lives elsewhere.
// Count flag tokens in Go string literals, so comments and unrelated code
// edits do not widen the gate and a second use of an existing flag does.
func gatesFlagReferencesChanged(cwd, module string, block ledger.Object, changed []string) (bool, error) {
	base := gates.MergeBase(cwd, block)
	if base == "" {
		base, _ = block.Get("base_sha").(string)
	}
	if wt := gatesObj(block.Get("worktree")); wt.Get("base_sha") != nil {
		base, _ = wt.Get("base_sha").(string)
	}
	dir := filepath.Join(module, "cmd", "code-forge-native")
	for _, file := range changed {
		full := filepath.Join(cwd, filepath.FromSlash(file))
		if filepath.Dir(full) != dir || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		now, err := os.ReadFile(full)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		old := ""
		if base != "" {
			old, err = gitx.Run(cwd, "show", base+":"+filepath.ToSlash(file))
			if err != nil {
				// A new file has no blob at the base. Other git failures must not
				// silently skip the contract check.
				if _, e := gitx.Run(cwd, "cat-file", "-e", base+"^{commit}"); e != nil {
					return false, e
				}
				old = ""
			}
		}
		before, err := gatesFlagLiterals(old)
		if err != nil {
			return false, fmt.Errorf("base flags %s: %w", file, err)
		}
		after, err := gatesFlagLiterals(string(now))
		if err != nil {
			return false, fmt.Errorf("flags %s: %w", file, err)
		}
		if !reflect.DeepEqual(before, after) {
			return true, nil
		}
	}
	return false, nil
}

func gatesFlagLiterals(source string) (map[string]int, error) {
	flags := map[string]int{}
	if source == "" {
		return flags, nil
	}
	f, err := parser.ParseFile(token.NewFileSet(), "flags.go", source, 0)
	if err != nil {
		return nil, err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err == nil {
			for _, flag := range helpRefFlagRe.FindAllString(text, -1) {
				flags[flag]++
			}
		}
		return true
	})
	return flags, nil
}
