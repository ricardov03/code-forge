package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

// Extend the shared parser only for close's auto-claim boolean. Complete token
// groups retain the shared usage precedence, duplicate and inline-value rules.
func closeFlags(args []string) (map[string]any, []string, string) {
	flags, pos := map[string]any{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, _, inline := strings.Cut(arg[2:], "=")
		multi := name == "owned" || name == "tests"
		if _, has := flags[name]; has && !multi {
			return flags, pos, "--" + name + " given more than once"
		}
		if name == "auto-claim" {
			if inline {
				return flags, pos, "--auto-claim takes no value"
			}
			flags[name] = true
			continue
		}
		end := i + 1
		part, _, message := blockFlags(args[i:end])
		if multi {
			for end < len(args) && !strings.HasPrefix(args[end], "--") {
				end++
			}
		} else if !inline && strings.HasSuffix(message, " needs a value") && end < len(args) && !strings.HasPrefix(args[end], "--") {
			end++
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

func closeMayHaveTerminal() bool {
	for _, file := range []*os.File{os.Stdin, os.Stdout} {
		st, err := file.Stat()
		if err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func closeWithFlags(args []string) int {
	flags, pos, message := closeFlags(args)
	if flags["auto-claim"] != true || message != "" || len(pos) == 0 {
		return blockCloseStep(args, nil)
	}
	fallback := func() int { return blockHandover(append([]string{"close"}, args...)) }
	// Run the entire close's read-only boundary first, including unsupported
	// flag/config combinations. Even a refused enqueue must not precede it.
	dry := &landStep{preflight: true}
	preflight := blockCloseStep(args, dry)
	if preflight == landHandover {
		return fallback()
	}
	if preflight != 0 || flags["no-require-reviews"] == true {
		return blockCloseStep(args, nil)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback()
	}
	run, id := blockString(flags["run"]), pos[0]
	record, err := state.ReadRun(home, run)
	if err != nil {
		return fallback()
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	main := blockString(record.Get("workspace"))
	top := blockString(blockObject(entry.Get("worktree")).Get("path"))
	if top == "" {
		top = main
	}
	files, err := landClaimFiles(home, run, id, top, main, record, entry)
	if err != nil {
		return fallback()
	}
	if len(files) == 0 {
		return blockCloseStep(args, nil)
	}
	// Recheck close against predicted ownership before any claim is signed.
	encoded, _ := ledger.Stringify(record, false)
	predicted, _ := ledger.DecodeObject(encoded)
	blocks := blockObject(predicted.Get("blocks"))
	block := blockObject(blocks.Get(id))
	claimed, _ := block.Get("owned_files").([]any)
	kinds := blockObject(block.Get("owned_kinds")).Clone()
	for _, file := range files {
		claimed = append(claimed, file)
		kinds.Set(file, owned.PathKind(top, file))
	}
	block.Set("owned_files", claimed)
	block.Set("owned_kinds", kinds)
	blocks.Set(id, block)
	predicted.Set("blocks", blocks)
	dry.record = predicted
	if blockCloseStep(args, dry) == landHandover {
		return fallback()
	}
	if err := landAutoClaim(home, run, id, top, record, files, io.Discard, true); err != nil {
		return fallback()
	}
	// An enqueue refusal ends auto-claim immediately; it needs no join support.
	willJoin := true
	for _, file := range files {
		if reviewEnqueueStep([]string{file, "--block", id, "--run", run, "--cwd", top}, &landStep{preflight: true}) != 0 {
			willJoin = false
			break
		}
	}
	joinArgs := []string{"--wait-block", id, "--run", run, "--cwd", top}
	if willJoin && reviewWaitBlockStep(joinArgs, &landStep{preflight: true}) == landHandover {
		return fallback()
	}
	// No handover is permitted after this point, including concurrent changes.
	blockBreadcrumb("close")
	fail := func(err error) int { fmt.Fprintln(os.Stderr, "block close:", err); return 1 }
	print := io.Writer(os.Stdout)
	if flags["json"] == true {
		print = os.Stderr
	}
	if err := landAutoClaim(home, run, id, top, record, files, print, false); err != nil {
		return fail(err)
	}
	joined := landCapture(func(s *landStep) int { return reviewWaitBlockStep(joinArgs, s) }, nil)
	if joined.code != 0 {
		return fail(fmt.Errorf("%s", landFirst(joined.error, joined.output, "auto-claim review failed")))
	}
	if joined.data.Get("done") != true {
		return fail(fmt.Errorf("auto-claim reviews did not finish: %s", strings.TrimSpace(joined.output)))
	}
	values, _ := joined.data.Get("files").([]any)
	for _, value := range values {
		file := blockObject(value)
		if file.Get("status") == "excluded" || file.Get("outcome") == "approved" {
			continue
		}
		detail := blockString(file.Get("outcome"))
		if file.Get("outcome") == nil {
			detail = blockString(file.Get("status"))
		}
		if ticket := blockString(file.Get("ticket")); ticket != "" {
			result := landTicket(main, ticket)
			if result.code != 0 {
				return fail(fmt.Errorf("%s", landFirst(result.error, result.output, "auto-claim review failed")))
			}
			detail = result.output
		}
		return fail(fmt.Errorf("auto-claim review refused %s: %s", file.Get("file"), detail))
	}
	result := landCapture(func(s *landStep) int { return blockCloseStep(args, s) }, nil)
	fmt.Fprint(os.Stdout, result.output)
	fmt.Fprint(os.Stderr, result.error)
	if result.code == landHandover {
		return fail(fmt.Errorf("auto-claim review failed"))
	}
	return result.code
}
