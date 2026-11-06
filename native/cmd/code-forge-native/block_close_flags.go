package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
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
	nativeFlags := false
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		if name == "--auto-claim" || name == "--remove-worktree" || name == "--no-require-reviews" {
			nativeFlags = true
		}
	}
	if !nativeFlags {
		return blockCloseStep(args, nil)
	}
	if !closeTextSafe(strings.Join(args, "\n")) {
		return blockHandover(append([]string{"close"}, args...), hCloseUnsupportedFlag)
	}
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	reason := hCloseAutoClaim
	var endHook func()
	code := closeFlagsStep(args, &landStep{stdout: &stdout, stderr: stderr}, &reason, &endHook)
	if code == landHandover {
		return blockHandover(append([]string{"close"}, args...), reason)
	}
	before := gatesBreadcrumbName("block close")
	fmt.Fprint(os.Stdout, stdout.String())
	fmt.Fprint(os.Stderr, stderr.String())
	if endHook != nil {
		endHook()
	}
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure("block", "close", args, code, message, before)
	}
	return code
}

// Retain the last write, as the Node router does, rather than treating an earlier
// auto-claimed progress line as part of a later multiline refusal.
type closeErrorBuffer struct {
	bytes.Buffer
	last string
}

func (b *closeErrorBuffer) Write(data []byte) (int, error) {
	if text := strings.TrimSpace(string(data)); text != "" {
		b.last = text
	}
	return b.Buffer.Write(data)
}

func closeFlagsStep(args []string, step *landStep, reason *handoverReason, endHook ...*func()) int {
	stdout, stderr := step.writers()
	closeStep := func(s *landStep) int { return blockCloseStepWithReason(args, s, reason, endHook...) }
	flags, pos, message := closeFlags(args)
	if flags["auto-claim"] != true || message != "" || len(pos) == 0 {
		return closeStep(step)
	}
	fallback := func(why handoverReason) int { *reason = why; return landHandover }
	// Run the entire close's read-only boundary first, including unsupported
	// flag/config combinations. Even a refused enqueue must not precede it.
	dry := &landStep{preflight: true}
	preflight := closeStep(dry)
	if preflight == landHandover {
		return preflight
	}
	if preflight != 0 || flags["no-require-reviews"] == true {
		return closeStep(step)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback(hCloseHome)
	}
	run, id := blockString(flags["run"]), pos[0]
	record, err := state.ReadRun(home, run)
	if err != nil {
		return fallback(hCloseError)
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	main, err := gitx.RepoTop(blockString(record.Get("workspace")))
	if err != nil {
		return fallback(hCloseRepo)
	}
	top := blockString(blockObject(entry.Get("worktree")).Get("path"))
	if top == "" {
		top = main
	}
	files, err := landClaimFiles(home, run, id, top, main, record, entry)
	if err != nil {
		return fallback(hCloseAutoClaim)
	}
	if len(files) == 0 {
		return closeStep(step)
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
	if closeStep(dry) == landHandover {
		return landHandover
	}
	if err := closeAutoClaim(home, run, id, top, record, files, io.Discard, true); err != nil {
		return fallback(hCloseAutoClaim)
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
	daemonJoin := false
	if willJoin {
		daemonJoin = reviewWaitBlockStep(joinArgs, &landStep{preflight: true}) != landHandover
		if !daemonJoin {
			if _, _, err := closeLocalJoin(home, run, id, main, top, predicted, true); err != nil {
				return fallback(hCloseAutoClaim)
			}
		}
	}
	// No handover is permitted after this point, including concurrent changes.
	fail := func(err error) int { fmt.Fprintln(stderr, "block close:", err); return 1 }
	print := stdout
	if flags["json"] == true {
		print = stderr
	}
	if err := closeAutoClaim(home, run, id, top, record, files, print, false); err != nil {
		return fail(err)
	}
	var joined landResult
	if daemonJoin {
		joined = landCapture(func(s *landStep) int { return reviewWaitBlockStep(joinArgs, s) }, nil)
	}
	if !daemonJoin || joined.code == landHandover {
		data, code, err := closeLocalJoin(home, run, id, main, top, nil, false)
		if err != nil {
			return fail(err)
		}
		encoded, _ := ledger.Stringify(data, false)
		joined = landResult{code: code, data: data, output: string(encoded) + "\n"}
	}
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
	code := closeStep(step)
	if code == landHandover {
		return fail(fmt.Errorf("auto-claim review failed"))
	}
	return code
}

// The daemon is an accelerator, not a requirement for auto-claim. Ordinary
// current-content tickets use the same queue identities and completion markers
// in-process. Reopen/stopped dialects are rejected during read-only preflight.
func closeLocalJoin(home, run, id, main, top string, predicted ledger.Object, preflight bool) (ledger.Object, int, error) {
	deadline := time.Now().Add(90 * time.Second)
	down := 0
	for {
		record := predicted
		if !preflight {
			var ok bool
			record, ok = reviewWaitJSON(filepath.Join(main, ".code-forge/runs", run+".json"))
			if !ok || record == nil || record.Get("run_id") != run {
				return nil, 1, fmt.Errorf("the block scope cannot be read")
			}
		}
		entry := blockObject(blockObject(record.Get("blocks")).Get(id))
		cfg, ok := blockSnapshot(record)
		if !ok {
			return nil, 1, fmt.Errorf("unsupported block scope")
		}
		idx := queue.Index{Dir: filepath.Join(main, ".code-forge/queue")}
		if err := idx.Refresh(); err != nil {
			return nil, 1, err
		}
		values, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
		if err != nil {
			return nil, 1, err
		}
		for _, value := range values {
			row, _ := value.(map[string]any)
			if row["run"] == run && row["block"] == id && row["event"] == "review.reopened" {
				return nil, 1, fmt.Errorf("reopened review requires Node")
			}
		}
		worker, ok := reviewEnqueueWorker(main)
		if !ok {
			return nil, 1, fmt.Errorf("unsupported worker")
		}
		live := worker != nil && worker.Get("run") == run
		if live {
			down = 0
		} else {
			down++
		}
		changed, err := gitx.ChangedFiles(top, blockString(entry.Get("base_sha")))
		if err != nil {
			return nil, 1, err
		}
		ownedFiles, _ := entry.Get("owned_files").([]any)
		var namedTree *string
		if top != main {
			namedTree = &top
		}
		tickets := idx.Block(run, id, namedTree, nil)
		files := []any{}
		done := true
		for _, file := range changed {
			if !owned.OwnsFile(ownedFiles, blockObject(entry.Get("owned_kinds")), file) {
				continue
			}
			out := ledger.Object{{Key: "file", Value: file}, {Key: "ticket", Value: nil}, {Key: "status", Value: "not_queued"}, {Key: "outcome", Value: nil}}
			rule, supported := reviewq.Exclusion(cfg, file)
			if !supported {
				return nil, 1, fmt.Errorf("unsupported review exclusion")
			}
			if rule != "" {
				out.Set("status", "excluded")
				out.Set("rule", rule)
				files = append(files, out)
				continue
			}
			hash, err := gitx.ContentHash(top, file)
			if err != nil {
				return nil, 1, err
			}
			parts := ticket.Parts{Run: run, Block: id, File: file, ContentHash: hash, Tree: namedTree}
			identity, err := queue.Identity(idx.Dir, parts)
			if err != nil {
				return nil, 1, err
			}
			for _, previous := range tickets {
				if previous["file"] != file {
					continue
				}
				marker, err := idx.Marker(blockString(previous["ticket"]))
				if err != nil || marker["status"] == "stopped" || previous["ticket"] != identity && marker["open_findings"] == true {
					return nil, 1, fmt.Errorf("stopped or superseded review requires Node")
				}
			}
			marker, err := idx.Marker(identity)
			if err != nil {
				return nil, 1, err
			}
			if preflight {
				continue
			}
			present := false
			for _, previous := range tickets {
				present = present || previous["ticket"] == identity
			}
			if !present && live {
				if err := queue.Publish(idx.Dir, parts, identity, time.Now()); err != nil {
					return nil, 1, err
				}
				present = true
			}
			if present {
				out.Set("ticket", identity)
				out.Set("status", "pending")
				if live && marker != nil {
					marker, err = idx.RetryOnce(main, identity, marker)
					if err != nil {
						return nil, 1, err
					}
				}
				if marker != nil {
					out.Set("status", "done")
					outcome := "unavailable"
					if marker["status"] == "reviewed" {
						outcome = "approved"
						if marker["open_findings"] == true || marker["approved"] == false {
							outcome = "findings"
						}
					} else if marker["status"] == "split_required" || marker["status"] == "stopped" {
						outcome = blockString(marker["status"])
					}
					out.Set("outcome", outcome)
					for _, field := range []string{"queued_at", "started_at", "finished_at", "duration_ms"} {
						if value, has := marker[field]; has {
							out.Set(field, value)
						}
					}
				}
			}
			done = done && out.Get("status") == "done"
			files = append(files, out)
		}
		if preflight {
			return nil, 0, nil
		}
		out := ledger.Object{{Key: "block", Value: id}, {Key: "files", Value: files}, {Key: "done", Value: false}}
		if down >= 2 {
			out.Set("status", "worker_down")
			return out, 3, nil
		}
		for _, value := range files {
			file := blockObject(value)
			if live && file.Get("outcome") == "unavailable" {
				out.Set("reason", "review_unavailable")
				out.Set("next", fmt.Sprintf("review-file %s --fresh re-runs it; if --fresh refuses, block reopen %s --run %s --file %s --reason <text>", file.Get("file"), id, run, file.Get("file")))
				return out, 0, nil
			}
			if live && file.Get("outcome") == "split_required" {
				out.Set("reason", "split_required")
				out.Set("next", fmt.Sprintf("The owner raises review.budgets for %s, runs run reload, then review-file %s --fresh.", file.Get("file"), file.Get("file")))
				return out, 0, nil
			}
		}
		if live && done {
			out.Set("done", true)
			return out, 0, nil
		}
		if live && !time.Now().Before(deadline) {
			out.Set("reason", "wait_timeout")
			return out, 0, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}
