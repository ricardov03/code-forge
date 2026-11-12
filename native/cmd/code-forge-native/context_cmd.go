package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

const contextUsage = "usage: code-forge context pack --run <id> --block <id> [--json] [--path-only]\n"

func init() { routes["context"] = runContext }

// contextErrWriter keeps the last non-blank stderr write, as Node's captureStderr.
type contextErrWriter struct{ last string }

func (w *contextErrWriter) Write(p []byte) (int, error) {
	if text := strings.TrimSpace(string(p)); text != "" {
		w.last = text
	}
	return os.Stderr.Write(p)
}

func runContext(args []string) int {
	// The router prints help before config, breadcrumbs or logging.
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") || len(args) > 1 && args[0] == "pack" && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(os.Stdout, contextUsage)
		return 0
	}
	contextApplyTmpRoot()
	sub := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if arg == "pack" {
				sub = "pack"
			}
			break
		}
	}
	name := "context"
	if sub != "" {
		name = "context pack"
	}
	before := gatesBreadcrumbName(name)
	stderr := &contextErrWriter{}
	code := contextPack(args, stderr)
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure("context", sub, args, code, message, before)
	}
	return code
}

// contextApplyTmpRoot is applyTmpRoot in bin/code-forge.mjs: an unset
// CODE_FORGE_TMP_ROOT takes the cwd project's tmp.root, resolved against cwd.
func contextApplyTmpRoot() {
	if os.Getenv("CODE_FORGE_TMP_ROOT") != "" {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	loaded, err := config.Load(cwd)
	if err != nil {
		return
	}
	if root, ok := blockMap(loaded["tmp"])["root"].(string); ok && root != "" {
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		_ = os.Setenv("CODE_FORGE_TMP_ROOT", filepath.Clean(root))
	}
}

var contextFlagNames = map[string]bool{"run": true, "block": true, "json": true, "path-only": true}

func contextPack(args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "pack" {
		fmt.Fprint(stderr, contextUsage)
		return 2
	}
	usage := func(message string) int { fmt.Fprint(stderr, "context: "+message+"\n"+contextUsage); return 2 }
	// parseFlags in src/state/cli-args.mjs: one pass, the first problem wins.
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
		if !contextFlagNames[name] {
			return usage("unknown flag " + statusQuote(arg))
		}
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
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(id) {
		return usage("--block: " + statusQuote(id) + " is not a block id")
	}
	refuse := func(message string) int {
		fmt.Fprintln(stderr, "context: "+message+"; nothing was written")
		return 1
	}
	failure := func(err error) int {
		var ce *contextpack.Error
		var se *state.Error
		if errors.As(err, &ce) || errors.As(err, &se) {
			return refuse(err.Error())
		}
		return refuse(contextpack.NodeError(err))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return refuse("the home folder cannot be found (" + err.Error() + "); set HOME")
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		var se *state.Error
		if errors.As(err, &se) {
			return failure(err)
		}
		return refuse("the run record of " + run + " cannot be read (" + contextpack.NodeError(err) + ")")
	}
	// record.blocks?.[block]: only an own, truthy entry is a block. A truthy
	// value that is not a mapping has no fields, as in Node.
	blocks, _ := record.Get("blocks").(ledger.Object)
	var entry ledger.Object
	switch v := blocks.Get(id).(type) {
	case ledger.Object:
		entry = v
	case nil, bool, string, float64, json.Number, int, int64:
		if !contextTruthy(v) {
			return failure(&contextpack.Error{Message: "run " + run + " has no block " + id})
		}
		entry = ledger.Object{}
	default:
		entry = ledger.Object{}
	}
	// repoRootOf(undefined) runs git in the process folder.
	workspace, _ := record.Get("workspace").(string)
	root, refusal, ok := reviewMainRoot(context.Background(), workspace)
	if !ok {
		return refuse("the repository's top level cannot be resolved")
	}
	if refusal != nil {
		message, _ := refusal.Get("message").(string)
		return refuse(message)
	}
	live, err := config.Load(root)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			return failure(&contextpack.Error{Message: ce.Message})
		}
		return failure(err)
	}
	// The header names this install's node and launcher, as Node's
	// process.execPath and import.meta.url do; Go never starts that node.
	executable, err := contextExecutable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return refuse("this install's executable cannot be found (" + err.Error() + ")")
	}
	bin, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(executable), "..", "..", "..", "bin", "code-forge.mjs"))
	if err != nil {
		return refuse("this install's bin/code-forge.mjs cannot be found (" + contextpack.Errno(err) + "); reinstall code-forge")
	}
	// B469: a CODE_FORGE_NODE that resolves names the header's node; one that
	// does not (e.g. /nonexistent) is skipped for node on PATH, as before B452.
	// Go never starts it, and Node's own pack always has an execPath.
	node, err := contextNode()
	if err != nil {
		return refuse("the node executable " + statusQuote("node") + " for the pack header cannot be found on PATH; install Node or set CODE_FORGE_NODE")
	}
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(bin), "..", "package.json"))
	var info struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(metadata, &info) != nil || info.Version == "" {
		return refuse("this install's package.json has no version; reinstall code-forge")
	}
	result, err := contextpack.Assemble(root, run, id, entry, live, node, bin, info.Version)
	if err != nil {
		return failure(err)
	}
	snapshot := live
	if st, yes := record.Get("config").(ledger.Object); yes {
		if _, yes := st.Get("hash").(string); yes {
			var ok bool
			if snapshot, ok = statusConfig(record); !ok {
				return failure(&state.Error{Code: "config-snapshot", Message: fmt.Sprintf("run %s: the config snapshot in force does not match its hash", gatecheck.JSString(record.Get("run_id"), contextHas(record, "run_id")))})
			}
		}
	}
	var tmpRoot *string
	if root, ok := blockMap(snapshot["tmp"])["root"].(string); ok {
		tmpRoot = &root
	}
	runRoot, err := contextRunRoot(run, tmpRoot)
	if err != nil {
		return failure(&contextpack.Error{Message: "the run root cannot be used (" + err.Error() + ")"})
	}
	result.Path = filepath.Join(runRoot, "context", id+".md")
	slug, ok := record.Get("project").(string)
	if !ok {
		return refuse("run " + run + " names no project")
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
		fmt.Fprint(stderr, message)
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

// contextExecutable locates this install; tests point it at a fake package.
var contextExecutable = os.Executable

func contextHas(o ledger.Object, key string) bool {
	for _, f := range o {
		if f.Key == key {
			return true
		}
	}
	return false
}

func contextTruthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && v == v
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	}
	return true
}

var contextRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// contextUntrusted is untrustedReason in src/util/tmp.mjs (mask 0o022).
func contextUntrusted(dir string) string {
	st, err := os.Lstat(dir)
	if err != nil {
		return "missing"
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if !st.IsDir() {
		return "not a directory"
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return "foreign owner"
	}
	if st.Mode().Perm()&0o022 != 0 {
		return "group/other writable"
	}
	return ""
}

// contextRunRoot is runRootFor in src/session/spawn.mjs: an existing trusted
// <base>/<run> is reused; otherwise runRoot makes it and records this process
// in owner.json, as Node does.
func contextRunRoot(run string, root *string) (string, error) {
	if !contextRunID.MatchString(run) {
		return "", errors.New(`a run id is one path segment of letters, digits, ".", "_" or "-"`)
	}
	var base string
	if root == nil {
		base = os.Getenv("CODE_FORGE_TMP_ROOT")
		if !filepath.IsAbs(base) {
			base = filepath.Join(contextTempDir(), "code-forge")
		}
	} else if !filepath.IsAbs(*root) {
		return "", errors.New("tmp: tmp.root must be an absolute path")
	} else {
		base = *root
	}
	dir := filepath.Join(base, run)
	if contextUntrusted(dir) == "" {
		return filepath.EvalSymlinks(dir)
	}
	for attempt := 0; ; attempt++ {
		if err := contextMkdirAll(dir); err != nil {
			return "", err
		}
		if reason := contextUntrusted(base); reason != "" {
			return "", fmt.Errorf("tmp: refusing untrusted tmp base (%s): %s", reason, base)
		}
		reason := contextUntrusted(dir)
		if reason == "missing" && attempt < 2 {
			continue
		}
		if reason != "" {
			return "", fmt.Errorf("tmp: refusing untrusted run root (%s): %s", reason, dir)
		}
		err := contextWriteOwner(dir)
		if err == nil {
			return filepath.EvalSymlinks(dir)
		}
		if !errors.Is(err, os.ErrNotExist) || attempt >= 2 {
			return "", errors.New(contextpack.NodeError(err))
		}
	}
}

// os.tmpdir(): TMPDIR without its trailing slash, else /tmp.
func contextTempDir() string {
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		return "/tmp"
	}
	if len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	return dir
}

// mkdirSync(dir, {recursive: true, mode: 0o700}): an existing non-directory
// in the path is EEXIST for the leaf and ENOTDIR above it.
func contextMkdirAll(dir string) error {
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return fmt.Errorf("EEXIST: file already exists, mkdir '%s'", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			pe.Op = "mkdir"
			pe.Path = dir
		}
		return errors.New(contextpack.NodeError(err))
	}
	return nil
}

func contextWriteOwner(dir string) error {
	start := "unknown"
	if t := filelock.ProcessStartTime(os.Getpid()); t != nil {
		start = *t
	}
	owner := ledger.Object{{Key: "pid", Value: os.Getpid()}, {Key: "start_time", Value: start}, {Key: "created_at", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}}
	data, err := ledger.Stringify(owner, false)
	if err != nil {
		return err
	}
	var random [6]byte
	_, _ = rand.Read(random[:])
	temp := filepath.Join(dir, fmt.Sprintf(".owner.json.%d.%s.tmp", os.Getpid(), hex.EncodeToString(random[:])))
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp, filepath.Join(dir, "owner.json"))
	}
	if err != nil {
		_ = os.Remove(temp)
	}
	return err
}

// contextNode resolves the node named in the pack header: CODE_FORGE_NODE
// when it resolves, else node on PATH (B469).
func contextNode() (string, error) {
	if override := os.Getenv("CODE_FORGE_NODE"); override != "" {
		if p, err := exec.LookPath(override); err == nil {
			if p, err = filepath.EvalSymlinks(p); err == nil {
				return p, nil
			}
		}
	}
	p, err := exec.LookPath("node")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}
