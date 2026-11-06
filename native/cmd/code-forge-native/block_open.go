package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worktree"
)

func init() { blockSubs["open"] = runBlockOpen }

// Keep the original argv until every pre-publication check has been mapped.
func runBlockOpen(args []string) int {
	fallback := func(reason handoverReason) int { return blockHandover(append([]string{"open"}, args...), reason) }
	var flags map[string]any
	var id string
	failure := func(err error) int {
		var publishing *blockops.PublicationError
		if errors.As(err, &publishing) {
			fmt.Fprintln(os.Stderr, "block open:", blockIOMessage(err))
			return 1
		}
		if errors.Is(err, blockops.ErrHandover) {
			return fallback(hOpenStateChanged)
		}
		var se *state.Error
		var ge *gitx.Error
		if reviewWaitLogging() || !errors.As(err, &se) && !errors.As(err, &ge) {
			return fallback(hOpenErrorLog)
		}
		if se != nil && flags["json"] == true && id != "" && slices.Contains([]string{"after-not-done", "bad-after", "outcome-not-ready", "bad-after-outcome"}, se.Code) {
			out, _ := ledger.Stringify(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "refused"}, {Key: "code", Value: se.Code}, {Key: "message", Value: se.Message}}, false)
			fmt.Fprintln(os.Stdout, string(out))
		}
		fmt.Fprintln(os.Stderr, "block open:", err.Error())
		if se != nil && se.Code == "usage" {
			return 2
		}
		return 1
	}
	usage := func(message string) int { return failure(&state.Error{Code: "usage", Message: message}) }
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return fallback(hOpenHelp)
		}
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name, _, _ := strings.Cut(arg[2:], "=")
		if !slices.Contains([]string{"run", "level", "owned", "acceptance", "tests", "brief", "base", "attempt", "lines", "kind", "json", "remove-worktree", "after", "after-outcome"}, name) {
			return fallback(handoverFlagReason("open", name, hOpenUnsupportedFlag))
		}
	}
	var positionals []string
	var message string
	flags, positionals, message = blockOpenFlags(args)
	if message != "" {
		return usage(message)
	}
	tests, _ := flags["tests"].([]string)
	for _, file := range tests {
		if strings.ContainsAny(file, "*?") {
			return usage(`--tests takes exact paths (a file or a directory); "*" and "?" are refused: ` + statusQuote(file))
		}
		if strings.HasPrefix(file, "-") {
			return usage(`--tests: a path may not start with "-": ` + statusQuote(file))
		}
	}
	run := blockString(flags["run"])
	if len(positionals) == 0 || positionals[0] == "" || run == "" {
		return usage("a block id and --run are required")
	}
	id = positionals[0]
	// JS regexp end anchors and inherited object keys need Node's semantics.
	if id == "__proto__" || strings.HasSuffix(id, "\n") || strings.HasSuffix(blockString(flags["level"]), "\n") {
		return fallback(hOpenJsSemantics)
	}
	files, hasOwned := flags["owned"].([]string)
	acceptanceFile, hasAcceptance := flags["acceptance"].(string)
	if !hasAcceptance || !hasOwned {
		return usage("block open needs --level, --owned and --acceptance")
	}
	if flags["remove-worktree"] == true {
		return usage("--remove-worktree is a block close flag")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback(hOpenHome)
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return failure(err)
	}
	safe := func(r ledger.Object) bool {
		if !blockops.QuietExpiry(r) || !blockStorageSafe(home, run, r) {
			return false
		}
		if _, ok := blockSnapshot(r); !ok {
			return false
		}
		blocks, ok := r.Get("blocks").(ledger.Object)
		if !ok {
			return false
		}
		for _, f := range blocks {
			b, ok := f.Value.(ledger.Object)
			if !ok {
				return false
			}
			owned, ok := b.Get("owned_files").([]any)
			if !ok {
				return false
			}
			for _, v := range owned {
				if _, ok := v.(string); !ok {
					return false
				}
			}
			if t := b.Get("worktree"); t != nil {
				tree, ok := t.(ledger.Object)
				if !ok {
					return false
				}
				for _, key := range []string{"path", "branch", "base_sha"} {
					if blockString(tree.Get(key)) == "" {
						return false
					}
				}
			}
		}
		return true
	}
	if !safe(record) {
		return fallback(blockStateHandoverReason(record, "open", hOpenState))
	}
	root := blockString(blockObject(blockObject(blockObject(record.Get("blocks")).Get(id)).Get("worktree")).Get("path"))
	if root == "" {
		root = blockString(record.Get("workspace"))
	}
	if err := blockopen.WhitespacePaths(root, files, "owned"); err != nil {
		return failure(err)
	}
	if err := blockopen.WhitespacePaths(root, tests, "tests"); err != nil {
		return failure(err)
	}
	cfg, _ := blockSnapshot(record)
	level, source, decision, err := blockopen.PickLevel(home, run, id, flags, record, cfg)
	if err != nil {
		if errors.Is(err, blockops.ErrHandover) {
			return fallback(hOpenNoLevel)
		}
		return failure(err)
	}
	// B48 is a proven no-op here: QuietExpiry excludes every grant and every
	// active approval, so neither check takes a lock, signs a row or emits text.
	data, err := os.ReadFile(acceptanceFile)
	if err != nil {
		return fallback(hOpenAcceptanceRead)
	}
	acceptance, err := blockopen.ParseAcceptance(data)
	if err != nil {
		return fallback(hOpenAcceptanceFormat)
	}
	opts := blockopen.Options{Home: home, Run: run, ID: id, Level: level, Kind: blockString(flags["kind"]), Owned: files, Tests: tests, Acceptance: acceptance, Attempt: 1, Floor: "L1", Safe: safe, Decision: decision}
	opts.After, _ = flags["after"].([]string)
	if brief, ok := flags["brief"].(string); ok {
		absolute, err := filepath.EvalSymlinks(brief)
		if err != nil {
			return fallback(hOpenBriefResolve)
		}
		absolute, err = filepath.Abs(absolute)
		if err != nil {
			return fallback(hOpenBriefAbsolute)
		}
		relative, err := filepath.Rel(blockString(record.Get("workspace")), absolute)
		if err != nil {
			return fallback(hOpenBriefRelative)
		}
		if strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return usage("--brief must be a file inside the workspace")
		}
		body, err := os.ReadFile(absolute)
		if err != nil {
			return fallback(hOpenBriefRead)
		}
		opts.BriefPath, opts.BriefData = relative, body
	}
	proof := blockMap(cfg["proof"])
	if proof["isolation"] == "worktree" {
		wt := blockMap(proof["worktree"])
		branch, ok := wt["branch"].(string)
		if !ok {
			branch = "forge/{run}/{block}"
		}
		// Empty configured values have semantics distinct from an absent setting.
		if wt["dir"] == "" || wt["base"] == "" {
			return fallback(hOpenWorktreeSettings)
		}
		opts.Settings = &worktree.Settings{Dir: blockString(wt["dir"]), Branch: branch, Base: blockString(wt["base"])}
	}
	if steps, ok := flags["after-outcome"].([]string); ok {
		if err := blockopen.CheckAfter(record, id, opts.After); err != nil {
			if errors.Is(err, blockops.ErrHandover) {
				return fallback(hOpenAfter)
			}
			return failure(err)
		}
		opts.AfterOutcome, err = blockopen.AcceptedOutcomes(blockString(record.Get("workspace")), id, steps)
		if err != nil {
			if errors.Is(err, blockops.ErrHandover) {
				return fallback(hOpenAfterOutcome)
			}
			return failure(err)
		}
	}
	for _, name := range []string{"attempt", "lines"} {
		if raw, ok := flags[name].(string); ok {
			n, err := strconv.ParseInt(raw, 10, 64)
			if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(raw) || err != nil || n > 9007199254740991 {
				return usage("--" + name + " must be a positive integer")
			}
			if name == "attempt" {
				opts.Attempt = n
			} else {
				opts.Lines = n
			}
		}
	}
	if base, ok := flags["base"].(string); ok {
		opts.Base, opts.BaseExplicit = base, true
	}
	if live, err := config.Load(blockString(record.Get("workspace"))); err == nil {
		floor := blockString(blockMap(live["levels"])["coder_floor_docs"])
		if slices.Contains([]string{"L0", "L1", "L2"}, floor) {
			opts.Floor = floor
		}
	}
	external := blockMap(blockMap(cfg["review"])["external"])
	if external["required"] == true && strings.TrimSpace(blockString(external["name"])) != "" {
		opts.GuardWhy = "review.external.required is true"
	} else if blockMap(cfg["hooks"])["on_block_end"] != nil {
		opts.GuardWhy = "hooks.on_block_end is set"
	} else if proof["runners"] != nil {
		opts.GuardWhy = "proof.runners is set"
	}
	result, err := blockopen.Open(opts)
	if err != nil {
		return failure(err)
	}
	blockBreadcrumb("open")
	b := result.Block
	tree := blockObject(b.Get("worktree"))
	testCount := len(tests)
	if flags["json"] == true {
		out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "open"}, {Key: "level", Value: b.Get("level")}, {Key: "asked", Value: opts.Level}, {Key: "source", Value: source}, {Key: "kind", Value: b.Get("kind")}, {Key: "attempt", Value: opts.Attempt}}
		if result.Kept > 0 {
			out.Set("kept_claims", result.Kept)
		}
		out.Set("base_sha", b.Get("base_sha"))
		out.Set("tests", testCount)
		var wt, brief any
		if tree != nil {
			wt = ledger.Object{{Key: "path", Value: tree.Get("path")}, {Key: "branch", Value: tree.Get("branch")}, {Key: "reused", Value: result.Reused}}
		}
		if result.Pointer != "" {
			brief = result.Pointer
		}
		out.Set("worktree", wt)
		out.Set("brief", brief)
		encoded, _ := ledger.Stringify(out, false)
		fmt.Fprintln(os.Stdout, string(encoded))
	} else {
		raised := ""
		if b.Get("level") != opts.Level {
			raised = fmt.Sprintf(" (%s floor, asked %s)", blockString(b.Get("kind")), opts.Level)
		}
		chosen := ""
		if source == "rules" {
			chosen = " (rules lane)"
		} else if source == "autopilot" {
			chosen = " (autopilot level)"
		}
		testText := ""
		if testCount > 0 {
			plural := "s"
			if testCount == 1 {
				plural = ""
			}
			testText = fmt.Sprintf(" · %d test file%s", testCount, plural)
		}
		fmt.Fprintf(os.Stdout, "block %s open · %s%s%s · %s · attempt %d · base %s%s\n", id, blockString(b.Get("level")), raised, chosen, blockString(b.Get("kind")), opts.Attempt, blockString(b.Get("base_sha"))[:12], testText)
		if result.Kept > 0 {
			fmt.Fprintf(os.Stdout, "kept claims: %d\n", result.Kept)
		}
		if tree != nil {
			reuse := ""
			if result.Reused {
				reuse = " · reused"
			}
			fmt.Fprintf(os.Stdout, "worktree %s · branch %s%s\n", blockString(tree.Get("path")), blockString(tree.Get("branch")), reuse)
		}
		pointer := result.Pointer
		if pointer == "" {
			pointer = "brief: none given (--brief <file>)"
		}
		fmt.Fprintln(os.Stdout, pointer)
	}
	return 0
}
