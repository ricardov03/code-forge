package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/state"
)

// Only verbs explicitly registered here run in Go. Internal accelerators are
// deliberately separate from this public route table.
var routes = map[string]func([]string) int{}

func run(args []string) (int, error) {
	state.SetLockVerb(lockVerbOf(args))
	if len(args) > 0 {
		if args[0] == "internal" {
			return runInternal(args[1:])
		}
		if route, ok := routes[args[0]]; ok {
			return route(args[1:]), nil
		}
	}
	return handover(args)
}

func handover(args []string, reasons ...handoverReason) (int, error) {
	reason := defaultHandoverReason(args)
	if len(reasons) > 0 {
		reason = reasons[0]
	}
	recordHandover(args, handoverReasons[reason])
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	nodePath, err := exec.LookPath(node)
	if err != nil {
		return 127, fmt.Errorf("node executable %q not found: %w", node, err)
	}
	executable, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("locate package executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return 1, fmt.Errorf("resolve package executable: %w", err)
	}
	script := filepath.Join(filepath.Dir(executable), "..", "..", "..", "bin", "code-forge.mjs")
	argv := append([]string{node, script}, args...)
	if err := syscall.Exec(nodePath, argv, os.Environ()); err != nil {
		return 126, fmt.Errorf("exec node: %w", err)
	}
	return 0, nil // Exec replaces this process on success.
}

// lockVerbOf names a verb for the run-lock owner file: its first one or two
// words, such as "block land" or "gates run" (B393).
func lockVerbOf(args []string) string {
	words := []string{}
	for _, arg := range args {
		if len(words) == 2 || strings.HasPrefix(arg, "-") {
			break
		}
		words = append(words, arg)
	}
	return strings.Join(words, " ")
}
