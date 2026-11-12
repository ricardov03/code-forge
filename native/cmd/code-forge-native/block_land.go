package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/state"
)

func init() { blockSubs["land"] = healWrap([]string{"block", "land"}, runBlockLand) }

const landArguments = "block land takes one block id, --run, --report, --transcript and optional --max/--merge/--no-merge-gate/--json"

// landFlags extends the shared parser without changing any other block's flags.
func landFlags(args []string) (map[string]any, []string, string) {
	flags, pos := map[string]any{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, _, inline := strings.Cut(arg[2:], "=")
		if name == "merge" || name == "no-merge-gate" || name == "push" || name == "watch-ci" || name == "copy-links" {
			if _, exists := flags[name]; exists {
				return flags, pos, "--" + name + " given more than once"
			}
			if inline {
				return flags, pos, "--" + name + " takes no value"
			}
			flags[name] = true
			continue
		}
		// Use the shared parser for each complete token group, preserving error
		// precedence and keeping a following land boolean out of value slots.
		end := i + 1
		part, _, message := blockFlags(args[i:end])
		multi := name == "owned" || name == "tests"
		if multi {
			for end < len(args) && !strings.HasPrefix(args[end], "--") {
				end++
			}
		} else if !inline && strings.HasSuffix(message, " needs a value") && end < len(args) && !strings.HasPrefix(args[end], "--") {
			end++
		}
		if _, exists := flags[name]; exists && !multi {
			return flags, pos, "--" + name + " given more than once"
		}
		if end > i+1 {
			part, _, message = blockFlags(args[i:end])
		}
		if message != "" {
			return flags, pos, message
		}
		for key, value := range part {
			if multi {
				previous, _ := flags[key].([]string)
				value = append(previous, value.([]string)...)
			}
			flags[key] = value
		}
		i = end - 1
	}
	return flags, pos, ""
}

func landMax(raw string) (int64, bool) {
	if regexp.MustCompile(`^\d{1,7}m$`).MatchString(raw) {
		n, _ := strconv.ParseInt(strings.TrimSuffix(raw, "m"), 10, 64)
		raw = strconv.FormatInt(n*60, 10) + "s"
	}
	m := reviewWaitMax.FindStringSubmatch(raw)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	if m[2] != "ms" {
		n *= 1000
	}
	return n, true
}

func runBlockLand(args []string) int {
	fallback := func(reason handoverReason) int {
		// The legacy dispatcher predates --copy-links. Carry this native flag
		// across the fallback boundary without changing that shared parser.
		forward := []string{"land"}
		copyLinks := false
		for _, arg := range args {
			if arg == "--copy-links" {
				copyLinks = true
			} else {
				forward = append(forward, arg)
			}
		}
		if copyLinks {
			previous, exists := os.LookupEnv("CODE_FORGE_LAND_COPY_LINKS")
			_ = os.Setenv("CODE_FORGE_LAND_COPY_LINKS", "1")
			defer func() {
				if exists {
					_ = os.Setenv("CODE_FORGE_LAND_COPY_LINKS", previous)
				} else {
					_ = os.Unsetenv("CODE_FORGE_LAND_COPY_LINKS")
				}
			}()
		}
		return blockHandover(forward, reason)
	}
	fail := func(message string, code int) int {
		before := gatesBreadcrumbName("block")
		text := "block land: " + message + "\n"
		fmt.Fprint(os.Stderr, text)
		nativeVerbFailure("block", "", append([]string{"land"}, args...), code, strings.TrimSpace(text), before)
		return code
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(os.Stdout, blockLandUsage())
			return 0
		}
	}
	flags, pos, message := landFlags(args)
	if message != "" {
		return fail(message, 2)
	}
	run := blockString(flags["run"])
	if len(pos) == 0 || pos[0] == "" || run == "" {
		return fail("a block id and --run are required", 2)
	}
	allowed := " run report transcript max merge no-merge-gate push watch-ci copy-links json "
	valid := true
	for name := range flags {
		valid = valid && strings.Contains(allowed, " "+name+" ")
	}
	_, report := flags["report"].(string)
	_, transcript := flags["transcript"].(string)
	if !valid || len(pos) == 1 && (!report || !transcript) {
		return fail(landArguments, 2)
	}
	if raw, ok := flags["max"].(string); ok {
		if _, ok := landMax(raw); !ok {
			return fail("--max must look like 90s, 30m or 1500ms", 2)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback(hLandHome)
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		var se *state.Error
		var errno syscall.Errno
		if !errors.As(err, &se) && errors.As(err, &errno) {
			// Node's readRun rethrows readFile's error: a read error has no path (B470).
			return fail(closeTranscriptReadMessage(err), 1)
		}
		if se == nil {
			return fallback(hLandReadRun)
		}
		return fail(err.Error(), 1)
	}
	if message := landPushCheck(flags, record, len(pos) > 1); message != "" {
		return fail(message, 2)
	}
	// B430: a heal retry of a failed push: every block landed, push only.
	if done, code := landPushOnly(flags, home, run, record, pos); done {
		return code
	}
	if len(pos) > 1 {
		// A stray trailing argument remains a usage error. Batch operands must
		// name distinct blocks in this run, preserving the legacy malformed-call
		// output.
		seen := map[string]bool{}
		for _, id := range pos {
			if seen[id] || blockObject(record.Get("blocks")).Get(id) == nil {
				return fail(landArguments, 2)
			}
			seen[id] = true
		}
		return landBatch(flags, home, run, pos)
	}
	id := pos[0]
	value := blockObject(record.Get("blocks")).Get(id)
	entry := blockObject(value)
	if value != nil && entry == nil {
		return fallback(hLandEntryShape)
	}
	return landExecuteWithReason(args, flags, home, run, id, record, entry, fallback)
}

// blockLandUsage is Node's verbUsage("land").
func blockLandUsage() string {
	return blockVerbUsage("land", "<id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links]") +
		"  Join block reviews, run changed gates in its worktree, then close.\n" +
		"  --max <dur>  Total review wait (default 30m); accepts 90s, 30m or 1500ms.\n" +
		"  --merge     Run the merge gate on a trial merge, then merge with --no-ff in the clean main checkout.\n" +
		"              The merge gate runs the Go test selection for the merge, never the full suite;\n" +
		"              it runs nothing when a green gate already covers the merged tree (gate_reused).\n" +
		"  --no-merge-gate  Skip the merge gate with a warning (one block only; a batch refuses it).\n" +
		"  --push      After the merge, git push origin <base> from the main checkout\n" +
		"              (one block: needs --merge; land.push: true does the same).\n" +
		"  --watch-ci  After the push, start a detached code-forge ci watch (needs --push or land.push).\n" +
		"  --copy-links  Copy gates.merge_gate_links paths into the merge-gate tree instead of linking them.\n" +
		"  block land <id> <id>... --run <r> lands closed blocks in order with one merge gate (--merge implied).\n"
}
