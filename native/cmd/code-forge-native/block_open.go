package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
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
// boUserHome is the user database home folder (a seam for tests, B581).
var boUserHome = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

func runBlockOpen(args []string) int {
	// --allow-api-mismatch is native only: Node has no api-fit check, so a
	// handover drops the flag instead of refusing it as unknown.
	allowAPIMismatch := slices.Contains(args, "--allow-api-mismatch")
	logArgs := args
	args = slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return arg == "--allow-api-mismatch" })
	// A case the Go engine does not serve yet still runs in Node (B466 ports
	// it); help, flags, errors and the error log stay in Go (B451).
	fallback := func(reason handoverReason) int { return blockHandover(append([]string{"open"}, args...), reason) }
	var flags map[string]any
	var id string
	// Node's router logs a failed verb with its last stderr write, trimmed.
	logged := func(code int, text string) int {
		before := gatesBreadcrumbName("block open")
		fmt.Fprintln(os.Stderr, text)
		nativeVerbFailure("block", "open", logArgs, code, jsTrim(text), before)
		return code
	}
	failure := func(err error) int {
		var publishing *blockops.PublicationError
		if errors.As(err, &publishing) {
			return logged(1, "block open: "+blockIOMessage(err))
		}
		if errors.Is(err, blockops.ErrHandover) {
			return fallback(hOpenStateChanged)
		}
		var se *state.Error
		var ge *gitx.Error
		if !errors.As(err, &se) && !errors.As(err, &ge) {
			// Node's readFile names the path for open errors, not read errors.
			return logged(1, "block open: "+closeTranscriptReadMessage(err))
		}
		if se != nil && flags["json"] == true && id != "" && slices.Contains([]string{"after-not-done", "bad-after", "outcome-not-ready", "bad-after-outcome", "api-fit", "bad-uses"}, se.Code) {
			out, _ := ledger.Stringify(ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "refused"}, {Key: "code", Value: se.Code}, {Key: "message", Value: se.Message}}, false)
			fmt.Fprintln(os.Stdout, string(out))
		}
		if se != nil && (se.Code == "usage" || se.Code == "api-fit") {
			return logged(2, "block open: "+err.Error())
		}
		return logged(1, "block open: "+err.Error())
	}
	usage := func(message string) int { return failure(&state.Error{Code: "usage", Message: message}) }
	// Node prints the verb usage for an exact --help or -h anywhere in args,
	// before breadcrumbs or the error log.
	if slices.Contains(logArgs, "--help") || slices.Contains(logArgs, "-h") {
		fmt.Fprint(os.Stdout, blockOpenUsage())
		return 0
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
	files, hasOwned := flags["owned"].([]string)
	acceptanceFile, hasAcceptance := flags["acceptance"].(string)
	if !hasAcceptance || !hasOwned {
		return usage("block open needs --level, --owned and --acceptance")
	}
	if flags["remove-worktree"] == true {
		return usage("--remove-worktree is a block close flag")
	}
	// Node's os.homedir(): $HOME as set (an empty one too), else the user
	// database (B581).
	home, hasHome := os.LookupEnv("HOME")
	if !hasHome {
		var err error
		if home, err = boUserHome(); err != nil {
			// libuv's lookup error text is not reproduced byte-equal.
			return fallback(hOpenHomeLookup)
		}
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return failure(err)
	}
	safe := func(r ledger.Object) bool {
		if !autopilot.Supported(r) || !blockStorageSafe(home, run, r) {
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
	grant, _ := autopilot.Read(record, time.Now())
	grantState := ""
	if grant.Grant != nil {
		grantState, _ = autopilot.GrantState(grant.Grant, time.Now())
	}
	// A YAML form Go does not serve hands over before anything is written; a
	// read or parse error is reported where Node reads the file (B581).
	if data, err := os.ReadFile(acceptanceFile); err == nil {
		if _, err := blockopen.ParseYAMLJS(data); errors.Is(err, blockopen.ErrYAMLForm) {
			return fallback(hOpenAcceptanceYAML)
		}
	}
	if grantState == "active" {
		data, err := os.ReadFile(acceptanceFile)
		if err != nil {
			return failure(err)
		}
		value, err := blockopen.ParseYAMLJS(data)
		if errors.Is(err, blockopen.ErrYAMLForm) {
			return fallback(hOpenAcceptanceYAML)
		}
		if err != nil {
			return failure(err)
		}
		clauses, err := blockopen.Acceptance(value)
		if err != nil {
			return failure(err)
		}
		human := []string{}
		for _, clause := range clauses {
			c := blockObject(clause)
			if c.Get("kind") == "human_check" {
				human = append(human, statusQuote(blockString(c.Get("clause"))))
			}
		}
		if grant.State == "active" && len(human) > 0 {
			return failure(&state.Error{Code: "autopilot-human-check", Message: "autopilot-human-check: an active autopilot grant cannot wait on human_check clauses: " + strings.Join(human, ", ") + "; turn each into a clause with tests or ask the owner"})
		}
	}
	deferExpiry := flags["after"] != nil || flags["after-outcome"] != nil
	if !deferExpiry {
		blockGrantExpiry(home, run, os.Stderr)
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
		if errors.Is(err, blockopen.ErrLevelLedger) {
			return fallback(hOpenLevelLedger)
		}
		return failure(err)
	}
	data, err := os.ReadFile(acceptanceFile)
	if err != nil {
		return failure(err)
	}
	acceptance, err := blockopen.ParseYAMLJS(data)
	if errors.Is(err, blockopen.ErrYAMLForm) {
		return fallback(hOpenAcceptanceYAML)
	}
	if err != nil {
		return failure(err)
	}
	opts := blockopen.Options{Home: home, Run: run, ID: id, Level: level, Kind: blockString(flags["kind"]), Owned: files, Tests: tests, Acceptance: acceptance, Attempt: 1, Floor: "L1", Safe: safe, Decision: decision}
	opts.After, _ = flags["after"].([]string)
	opts.AllowAPIMismatch = allowAPIMismatch
	if brief, ok := flags["brief"].(string); ok {
		absolute, err := filepath.EvalSymlinks(brief)
		if err != nil {
			// Node's realpath names the path as given, whatever part failed
			// (B470); Go's link limit is realpath's ELOOP (B581).
			var errno syscall.Errno
			if !errors.As(err, &errno) {
				errno = syscall.ELOOP
			}
			return failure(&os.PathError{Op: "realpath", Path: brief, Err: errno})
		}
		// path.relative resolves both sides against the working directory.
		workspace := blockString(record.Get("workspace"))
		var cwdErr error
		if absolute, err = filepath.Abs(absolute); err != nil {
			cwdErr = err
		} else if workspace, err = filepath.Abs(workspace); err != nil {
			cwdErr = err
		}
		if cwdErr != nil {
			// Node's process.cwd() failure text (uv_cwd) is not reproduced.
			return fallback(hOpenBriefCwd)
		}
		relative, err := filepath.Rel(workspace, absolute)
		if err != nil {
			return fallback(hOpenBriefCwd)
		}
		if strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return usage("--brief must be a file inside the workspace")
		}
		body, err := os.ReadFile(absolute)
		if err != nil {
			// Node's readFile: open errors name the path, read errors do not (B470).
			return logged(1, "block open: "+closeTranscriptReadMessage(err))
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
		// An empty configured value is not an absent one (B581).
		dir, dirSet := wt["dir"].(string)
		base, baseSet := wt["base"].(string)
		opts.Settings = &worktree.Settings{Dir: dir, Branch: branch, Base: base, DirSet: dirSet, BaseSet: baseSet}
	}
	if steps, ok := flags["after-outcome"].([]string); ok {
		if err := blockopen.CheckAfter(record, id, opts.After); err != nil {
			return failure(err)
		}
		opts.AfterOutcome, err = blockopen.AcceptedOutcomes(blockString(record.Get("workspace")), id, steps, checkOutcomeProblems)
		switch {
		case errors.Is(err, blockopen.ErrPipelineForm):
			return fallback(hOpenPipelineForm)
		case errors.Is(err, blockopen.ErrOutcomeForm):
			return fallback(hOpenOutcomeForm)
		case err != nil:
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
	if deferExpiry {
		blockGrantExpiry(home, run, os.Stderr)
	}
	blockBreadcrumb("open")
	for _, note := range result.Notes {
		fmt.Fprintln(os.Stderr, note)
	}
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
		if additive := b.Get("additive"); additive != nil {
			out.Set("additive", additive)
		}
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
		for _, rule := range result.Rules {
			fmt.Fprintln(os.Stdout, rule)
		}
	}
	return 0
}

// blockOpenUsage is Node's verbUsage("open"), with its --acceptance and
// --brief notes.
func blockOpenUsage() string {
	return blockVerbUsage("open", "<id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract|light] [--tests <paths…>] [--after <ids…>] [--after-outcome <ids…>] [--allow-api-mismatch]") +
		"  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n" +
		"                       Human checks use {clause, kind: human_check} without tests.\n" +
		"  --brief <file>       A file inside the repository, not inline text.\n" +
		"                       Its uses: lines (<file>: <symbol>(<param names>)) must fit the base:\n" +
		"                       each named parameter exists, else exit 2 with an api-fit: line.\n" +
		"                       Its extends: lines (<file>: <symbol>) own that file for additive change.\n" +
		"  --allow-api-mismatch  Open anyway; print each api-fit: line as a note (native only).\n"
}
