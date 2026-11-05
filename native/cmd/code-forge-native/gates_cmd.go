package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

func init() { routes["gates"] = runGates }
func gatesHandover(args []string) int {
	code, e := handover(append([]string{"gates"}, args...))
	if e != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", e)
	}
	return code
}
func gatesFlag(args []string, key string) (string, bool, bool) {
	for i, s := range args {
		if s == key {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", true, false
			}
			return args[i+1], true, true
		}
	}
	return "", false, true
}

type gatesOptions struct {
	cwd, run, block, slug        string
	cwdGiven, slugGiven, changed bool
	timeout                      float64
	timeoutGiven                 bool
}

func gatesParse(args []string) (gatesOptions, string) {
	o := gatesOptions{}
	o.cwd, _ = os.Getwd()
	v, given, ok := gatesFlag(args, "--cwd")
	if !ok {
		return o, "--cwd requires a value"
	}
	if given {
		o.cwd = v
		o.cwdGiven = true
	}
	v, given, ok = gatesFlag(args, "--timeout-ms")
	if !ok {
		return o, "--timeout-ms requires a value"
	}
	if given {
		n, e := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if strings.TrimSpace(v) == "" {
			n = 0
			e = nil
		}
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
			return o, fmt.Sprintf("--timeout-ms must be a finite number > 0, got \"%s\"", v)
		}
		o.timeout = n
		o.timeoutGiven = true
	}
	o.slug, o.slugGiven, ok = gatesFlag(args, "--slug")
	if !ok {
		return o, "--slug requires a value"
	}
	var r, b bool
	o.run, r, ok = gatesFlag(args, "--run")
	if !ok {
		return o, "--run <id> and --block <id> go together, each with a value"
	}
	o.block, b, ok = gatesFlag(args, "--block")
	if !ok || r != b {
		return o, "--run <id> and --block <id> go together, each with a value"
	}
	for _, s := range args {
		if s == "--changed" {
			o.changed = true
		}
	}
	if o.changed && !r {
		return o, "--changed needs --run <id> and --block <id>"
	}
	return o, ""
}
func runGates(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		return gatesHandover(args)
	}
	// The Node entry owns help and logging, including failure fingerprinting.
	if reviewWaitLogging() || os.Getenv("HOME") == "" {
		return gatesHandover(args)
	}
	for i := 1; i < len(args); i++ {
		s := args[i]
		switch s {
		case "--run", "--block", "--cwd", "--slug", "--timeout-ms":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		case "--changed", "--json":
		default:
			if !strings.HasPrefix(s, "--cwd=") {
				return gatesHandover(args)
			}
		}
	}
	o, message := gatesParse(args)
	if message != "" {
		fmt.Fprintln(os.Stderr, "gates run:", message)
		return 2
	}
	if o.run == "" && o.block == "" {
		return gatesHandover(args)
	}
	code, safe := gatesNative(args, o)
	if !safe {
		return gatesHandover(args)
	}
	return code
}
