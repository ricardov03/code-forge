package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

const contextUsage = "usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]\n"

func init() { routes["context"] = runContext }

func runContext(args []string) int {
	fallback := func() int {
		// The shared table is owned by B282; context's unported cases retain
		// the existing public reason code rather than modifying that table.
		code, err := handover(append([]string{"context"}, args...), hUnrouted)
		if err != nil {
			fmt.Fprintln(os.Stderr, "code-forge-native:", err)
		}
		return code
	}
	if len(args) == 0 || args[0] != "pack" || reviewWaitLogging() {
		return fallback()
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return fallback()
		}
		if strings.HasPrefix(arg, "--") {
			name, _, _ := strings.Cut(arg[2:], "=")
			if name != "run" && name != "block" && name != "json" && name != "path-only" {
				return fallback()
			}
		}
	}
	usage := func(message string) int { fmt.Fprint(os.Stderr, "context: "+message+"\n"+contextUsage); return 2 }
	flags := map[string]string{}
	seen := map[string]bool{}
	positional := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positional = true
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		if seen[name] {
			return usage("--" + name + " given more than once")
		}
		seen[name] = true
		if name == "json" || name == "path-only" {
			if inline {
				return usage("--" + name + " takes no value")
			}
			continue
		}
		if inline {
			if value == "" {
				return usage("--" + name + " needs a value")
			}
		} else {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return usage("--" + name + " needs a value")
			}
			i++
			value = args[i]
		}
		flags[name] = value
	}
	if positional || !seen["run"] || !seen["block"] {
		return usage("context pack needs --run and --block")
	}
	run, id := flags["run"], flags["block"]
	// JS's $ also matches before a terminal line terminator; leave that edge
	// to Node rather than applying Go regexp's stricter end anchor.
	for _, ending := range []string{"\n", "\r", "\u2028", "\u2029"} {
		if strings.HasSuffix(run, ending) || strings.HasSuffix(id, ending) {
			return fallback()
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(id) {
		return usage("--block: " + statusQuote(id) + " is not a block id")
	}
	failure := func(err error) int {
		var ce *contextpack.Error
		var se *state.Error
		if !errors.As(err, &ce) && !errors.As(err, &se) {
			return fallback()
		}
		fmt.Fprintln(os.Stderr, "context: "+err.Error()+"; nothing was written")
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return failure(err)
	}
	// JSON.parse preserves lone UTF-16 surrogates; Go's JSON decoder replaces
	// them. Keep those records (and invalid UTF-8) on the Node path.
	raw, err := os.ReadFile(filepath.Join(home, ".code-forge", "runs", run+".json"))
	if err != nil || !utf8.Valid(raw) || ledgerSurrogatePattern.Match(raw) {
		return fallback()
	}
	blocks, ok := record.Get("blocks").(ledger.Object)
	if !ok {
		return fallback()
	}
	entryValue := blocks.Get(id)
	if entryValue == nil {
		for _, inherited := range []string{"constructor", "toString", "toLocaleString", "valueOf", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable"} {
			if id == inherited {
				return fallback()
			}
		}
		return failure(&contextpack.Error{Message: "run " + run + " has no block " + id})
	}
	entry, ok := entryValue.(ledger.Object)
	if !ok {
		return fallback()
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok {
		return fallback()
	}
	root, ok := reviewWaitRoot(context.Background(), workspace)
	if !ok {
		return fallback()
	}
	live, err := config.Load(root)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) && ce.Kind != "parse-error" {
			return failure(&contextpack.Error{Message: ce.Message})
		}
		return fallback()
	}
	// Resolve the installed CLI symlink, matching import.meta.url in Node.
	executable, err := os.Executable()
	if err != nil {
		return fallback()
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fallback()
	}
	bin, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(executable), "..", "..", "..", "bin", "code-forge.mjs"))
	if err != nil {
		return fallback()
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return fallback()
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		return fallback()
	}
	// A real explicit Node override may name a different install. Its execPath
	// belongs in the pack, even though Go never starts that executable.
	if override := os.Getenv("CODE_FORGE_NODE"); override != "" {
		if p, e := exec.LookPath(override); e == nil {
			if p, e = filepath.EvalSymlinks(p); e == nil {
				node = p
			}
		}
	}
	if filepath.Base(node) != "node" && filepath.Base(node) != "nodejs" {
		return fallback()
	}
	file, err := os.Open(node)
	if err != nil {
		return fallback()
	}
	var magic [2]byte
	n, readErr := file.Read(magic[:])
	_ = file.Close()
	if readErr != nil || n != len(magic) || string(magic[:]) == "#!" {
		return fallback()
	}
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "..", "package.json"))
	var info struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(metadata, &info) != nil || info.Version == "" {
		return fallback()
	}
	result, err := contextpack.Assemble(root, run, id, entry, live, node, bin, info.Version)
	if err != nil {
		return failure(err)
	}
	snapshot := live
	stConfig, hasConfig := record.Get("config").(ledger.Object)
	_, hasHash := stConfig.Get("hash").(string)
	if hasConfig && hasHash {
		snapshot, ok = statusConfig(record)
	}
	if !ok {
		if st, yes := record.Get("config").(ledger.Object); yes {
			if _, yes := st.Get("hash").(string); yes {
				return failure(&state.Error{Code: "config-snapshot", Message: fmt.Sprintf("run %v: the config snapshot in force does not match its hash", record.Get("run_id"))})
			}
		}
		return fallback()
	}
	tmp := blockMap(snapshot["tmp"])
	base, hasBase := tmp["root"].(string)
	if hasBase && !filepath.IsAbs(base) {
		return failure(&contextpack.Error{Message: "the run root cannot be used (tmp: tmp.root must be an absolute path)"})
	}
	if !hasBase {
		base = os.Getenv("CODE_FORGE_TMP_ROOT")
		if !filepath.IsAbs(base) {
			base = filepath.Join(os.TempDir(), "code-forge")
		}
	}
	runRoot := filepath.Join(base, run)
	st, err := os.Lstat(runRoot)
	// Creating/adopting a missing or untrusted root invokes Node's owner and
	// process registry protocol. Hand it over before Go writes anything.
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0022 != 0 {
		return fallback()
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fallback()
	}
	runRoot, err = filepath.EvalSymlinks(runRoot)
	if err != nil {
		return fallback()
	}
	result.Path = filepath.Join(runRoot, "context", id+".md")
	// Node's cleanup throws its own stack if a parent is a file. Preserve that
	// exceptional path in Node, before opening a pack temp or appending a row.
	if parent, e := os.Stat(filepath.Dir(result.Path)); e == nil && !parent.IsDir() {
		return fallback()
	}
	slug, ok := record.Get("project").(string)
	if !ok {
		return fallback()
	}
	last, err := contextpack.LastPack(home, run, slug, id)
	if err != nil {
		return failure(err)
	}
	message, err := contextpack.Publish(home, run, id, result, last)
	if err != nil {
		return failure(err)
	}
	if message != "" {
		fmt.Fprint(os.Stderr, message)
		return 1
	}
	if seen["json"] {
		out := ledger.Object{{Key: "path", Value: result.Path}, {Key: "tokens", Value: result.Tokens}, {Key: "lines", Value: result.Lines}, {Key: "cut", Value: result.Cut}, {Key: "sha256", Value: result.SHA256}}
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(os.Stdout, string(data))
	} else if seen["path-only"] {
		fmt.Fprintf(os.Stdout, "%s\ntokens ~%d\n", result.Path, result.Tokens)
	} else {
		fmt.Fprint(os.Stdout, result.Text)
	}
	return 0
}
