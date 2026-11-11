package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/ledger"
)

const (
	listUsage   = "usage: code-forge list\n"
	removeUsage = "usage: code-forge remove [<harness>] [--scope project|global]\n"
)

func init() {
	routes["list"] = smallVerb{name: "list", usage: listUsage, prepare: prepareList}.run
	routes["remove"] = smallVerb{name: "remove", usage: removeUsage, prepare: prepareRemove}.run
}

// installHarness is one row of HARNESSES in src/install/harnesses.mjs.
type installHarness struct {
	id, label, command, homeMarker, projectSkillsDir string
	verified                                         bool
}

var installHarnesses = []installHarness{
	{"claude", "Claude Code", "claude", ".claude", ".claude/skills", true},
	{"codex", "Codex CLI", "codex", ".codex", ".agents/skills", true},
	{"grok", "Grok CLI", "grok", ".grok", ".agents/skills", true},
	{"gemini", "Gemini CLI", "gemini", ".gemini", ".agents/skills", false},
	{"cursor", "Cursor", "", ".cursor", ".agents/skills", true},
	{"copilot", "Copilot", "", ".copilot", ".agents/skills", true},
}

func installsFilePath(home string) string {
	return filepath.Join(home, ".code-forge", "installs.json")
}

// installRecord keeps the parsed row (with its own key order) for the
// rewrite, plus the fields the verbs read.
type installRecord struct {
	raw                            ledger.Object
	harness, scope, source, target string
}

func nonEmptyString(value any) (string, bool) {
	s, ok := value.(string)
	return s, ok && s != ""
}

// validInstallRecord is isValidRecord in src/install/link.mjs.
func validInstallRecord(value any) (installRecord, bool) {
	obj, ok := value.(ledger.Object)
	if !ok {
		return installRecord{}, false
	}
	has := func(key string) (any, bool) {
		for _, field := range obj {
			if field.Key == key {
				return field.Value, true
			}
		}
		return nil, false
	}
	harness, ok1 := nonEmptyString(obj.Get("harness"))
	scope, _ := obj.Get("scope").(string)
	method, _ := obj.Get("method").(string)
	source, ok2 := nonEmptyString(obj.Get("source"))
	target, ok3 := nonEmptyString(obj.Get("target"))
	_, ok4 := obj.Get("linked_at").(string)
	versionOK := true
	if version, present := has("version"); present {
		_, versionOK = nonEmptyString(version)
	}
	if !ok1 || (scope != "project" && scope != "global") || (method != "symlink" && method != "copy") ||
		!ok2 || !strings.HasPrefix(source, "/") || !ok3 || !strings.HasPrefix(target, "/") || !ok4 || !versionOK {
		return installRecord{}, false
	}
	return installRecord{raw: obj, harness: harness, scope: scope, source: source, target: target}, true
}

// readInstallRecords is readInstalls: a missing, corrupt or wrongly shaped
// file gives no rows, and bad rows are dropped.
func readInstallRecords(file string) []installRecord {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	parsed, err := ledger.DecodeValue(data)
	if err != nil {
		return nil
	}
	var list []any
	switch v := parsed.(type) {
	case []any:
		list = v
	case ledger.Object:
		list, _ = v.Get("installs").([]any)
	}
	var records []installRecord
	for _, value := range list {
		if record, ok := validInstallRecord(value); ok {
			records = append(records, record)
		}
	}
	return records
}

// commandOnPath is src/install/detect.mjs: an executable entry named command
// in a PATH directory, searched without spawning anything.
func commandOnPath(command, pathEnv string) bool {
	if command == "" {
		return false
	}
	for _, dir := range strings.Split(pathEnv, ":") {
		if dir == "" {
			continue
		}
		if syscall.Access(filepath.Join(dir, command), 1) == nil {
			return true
		}
	}
	return false
}

func prepareList(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	return func(stdout, stderr io.Writer) int {
		if len(args) > 0 {
			fmt.Fprint(stderr, listUsage)
			return 2
		}
		home := os.Getenv("HOME")
		var b strings.Builder
		b.WriteString("INSTALLED\nHARNESS\tSCOPE\tMETHOD\tTARGET\tRESOLVES\n")
		records := readInstallRecords(installsFilePath(home))
		for _, record := range records {
			resolves := "no"
			if _, err := os.Stat(record.target); err == nil {
				resolves = "yes"
			}
			method, _ := record.raw.Get("method").(string)
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", record.harness, record.scope, method, record.target, resolves)
		}
		if len(records) == 0 {
			b.WriteString("(none)\n")
		}
		b.WriteString("\nHARNESSES\nID\tLABEL\tDETECTED\tVERIFIED\tPROJECT PATH\n")
		for _, h := range installHarnesses {
			detected := commandOnPath(h.command, os.Getenv("PATH"))
			if info, err := os.Stat(filepath.Join(home, h.homeMarker)); err == nil && info.IsDir() {
				detected = true
			}
			verified := "UNVERIFIED"
			if h.verified {
				verified = "yes"
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", h.id, h.label, yesNo(detected), verified, h.projectSkillsDir)
		}
		fmt.Fprint(stdout, b.String())
		return 0
	}, 0, true
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// parseRemoveArgs is the strict `[<harness>] [--scope project|global]` parse.
func parseRemoveArgs(args []string) (harness, scope string, ok bool) {
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		harness, rest = rest[0], rest[1:]
	}
	if len(rest) == 0 {
		return harness, "", true
	}
	if rest[0] != "--scope" || len(rest) != 2 || (rest[1] != "project" && rest[1] != "global") {
		return "", "", false
	}
	return harness, rest[1], true
}

// prepareRemove is src/cli/remove.mjs with removeRecords. The safety check
// compares canonical paths case-insensitively, so it flags at least every
// target Node's realpath-based check flags; a run with any flagged target, or
// with a target or ledger Go could not finish removing or rewriting, goes to
// Node before anything is deleted.
func prepareRemove(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	harness, scope, ok := parseRemoveArgs(args)
	if !ok {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, removeUsage)
			return 2
		}, 0, true
	}
	home := os.Getenv("HOME")
	file := installsFilePath(home)
	if !cacheFileWritable(file) {
		return nil, hRemoveUnfinishable, false
	}
	records := readInstallRecords(file)
	matches := func(r installRecord) bool {
		return (harness == "" || r.harness == harness) && (scope == "" || r.scope == scope)
	}
	var candidates, kept []installRecord
	for _, record := range records {
		if matches(record) {
			candidates = append(candidates, record)
		} else {
			kept = append(kept, record)
		}
	}
	for _, record := range candidates {
		if !installTargetSafe(record.target, record.source, home) {
			return nil, hRemoveUnsafeTarget, false
		}
		if !removable(record.target) {
			return nil, hRemoveUnfinishable, false
		}
	}
	return func(stdout, _ io.Writer) int {
		for _, record := range candidates {
			if err := os.RemoveAll(record.target); err != nil {
				panic(err)
			}
		}
		rows := make([]any, 0, len(kept))
		for _, record := range kept {
			rows = append(rows, record.raw)
		}
		writeInstallsFile(file, rows)
		var b strings.Builder
		for _, record := range candidates {
			fmt.Fprintf(&b, "removed %s (%s) %s\n", record.harness, record.scope, record.target)
		}
		if len(candidates) == 0 {
			b.WriteString("nothing to remove\n")
		}
		fmt.Fprint(stdout, b.String())
		return 0
	}, 0, true
}

// writeInstallsFile is writeInstallsAtomically: a sibling temp file renamed
// over the ledger, `{"version":1,"installs":[...]}` pretty-printed.
func writeInstallsFile(file string, rows []any) {
	data, err := ledger.Stringify(ledger.Object{{Key: "version", Value: float64(1)}, {Key: "installs", Value: rows}}, true)
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		panic(err)
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	temp := filepath.Join(filepath.Dir(file), ".installs.json."+hex.EncodeToString(buf)+".tmp")
	if err := os.WriteFile(temp, append(data, '\n'), 0o666); err != nil {
		panic(err)
	}
	if err := os.Rename(temp, file); err != nil {
		_ = os.Remove(temp)
		panic(err)
	}
}

// removable reports that rm -rf of target can finish: nothing there, or an
// entry in a writable directory whose whole tree (when it is a real
// directory) can be read and emptied.
func removable(target string) bool {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || !unixWritable(filepath.Dir(target)) {
		return false
	}
	if !info.IsDir() {
		return true
	}
	ok := true
	_ = filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if d.IsDir() && syscall.Access(path, 7) != nil {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}

// canonicalExistingPrefix is canonicalizeExistingPrefix: the nearest existing
// ancestor resolved, the missing tail joined back literally.
func canonicalExistingPrefix(p string) string {
	current := p
	var trailing []string
	for {
		if real, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(trailing) - 1; i >= 0; i-- {
				real = filepath.Join(real, trailing[i])
			}
			return real
		}
		parent := filepath.Dir(current)
		if parent == current {
			return p
		}
		trailing = append(trailing, filepath.Base(current))
		current = parent
	}
}

// canonicalTargetLocation is canonicalizeTargetLocation: a symlink or a
// missing target is located by its resolved parent, never followed.
func canonicalTargetLocation(target string) string {
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink == 0 {
		if real, err := filepath.EvalSymlinks(target); err == nil {
			return real
		}
	}
	return filepath.Join(canonicalExistingPrefix(filepath.Dir(target)), filepath.Base(target))
}

func protectedPathForms(p string) []string {
	followed, location := canonicalExistingPrefix(p), canonicalTargetLocation(p)
	if followed == location {
		return []string{followed}
	}
	return []string{followed, location}
}

// installTargetSafe is checkSafeToDelete, compared without case so it never
// passes a target the case-correcting Node check refuses.
func installTargetSafe(target, source, home string) bool {
	t := strings.ToLower(canonicalTargetLocation(filepath.Clean(target)))
	if t == "/" {
		return false
	}
	inside := func(ancestor, p string) bool {
		if ancestor == p {
			return false
		}
		if !strings.HasSuffix(ancestor, "/") {
			ancestor += "/"
		}
		return strings.HasPrefix(p, ancestor)
	}
	var protected []string
	if home != "" {
		protected = append(protected, protectedPathForms(absPath(home))...)
	}
	protected = append(protected, protectedPathForms(filepath.Clean(source))...)
	for _, form := range protected {
		form = strings.ToLower(form)
		if t == form || inside(t, form) {
			return false
		}
	}
	return true
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}
