package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/ledger"
)

const outcomeUsage = "usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]\n"

func init() {
	routes["outcome"] = smallVerb{
		name: "outcome", usage: outcomeUsage,
		subUsage: map[string]string{
			"seal":   "usage: code-forge outcome seal <outcome.json> [--root <dir>]\n",
			"check":  "usage: code-forge outcome check <outcome.json> [--root <dir>] [--json]\n",
			"record": "usage: code-forge outcome record <outcome.json> [--root <dir>] [--step <id>] [--json]\n",
		},
		prepare: prepareOutcome,
	}.run
}

// errnoName is the err.code Node reports for a failed fs call; ok is false
// for an error Go does not map to one code.
func errnoName(err error) (string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return "", false
	}
	names := map[syscall.Errno]string{
		syscall.ENOENT: "ENOENT", syscall.EISDIR: "EISDIR", syscall.ENOTDIR: "ENOTDIR",
		syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENAMETOOLONG: "ENAMETOOLONG",
	}
	name, ok := names[errno]
	return name, ok
}

var winDrivePath = regexp.MustCompile(`^[A-Za-z]:[/\\]`)

// outcomeHashFile is hashFile in src/outcome/outcome.mjs: the problem text
// it throws, or the sha256 of a file contained in root.
func outcomeHashFile(name, root string) (hash, problem string, ok bool) {
	label := "file " + jsQuote(name)
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || winDrivePath.MatchString(name) ||
		slices.Contains(strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		return "", label + ": path must be relative to root without ..", true
	}
	fsProblem := func(err error) (string, string, bool) {
		code, known := errnoName(err)
		if !known {
			return "", "", false
		}
		if code == "ENOENT" {
			return "", label + ": missing file", true
		}
		return "", label + ": cannot read (" + code + ")", true
	}
	target := filepath.Join(root, name)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fsProblem(err)
	}
	real, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fsProblem(err)
	}
	rel, err := filepath.Rel(realRoot, real)
	if err != nil {
		return "", "", false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", label + ": path leaves root through a symlink", true
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return fsProblem(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), "", true
}

// loadOutcomeFile is loadOutcome plus the schema check: a problem Node
// reports, or the parsed contract; ok is false where Node's wording (AJV,
// an unmapped errno, a decoding Go does not reproduce) is not ported.
func loadOutcomeFile(file string) (outcome ledger.Object, problem string, ok bool) {
	data, err := os.ReadFile(file)
	if err != nil {
		code, known := errnoName(err)
		if !known {
			return nil, "", false
		}
		return nil, "Cannot read outcome " + jsQuote(file) + " (" + code + ")", true
	}
	if !utf8.Valid(data) {
		return nil, "", false
	}
	if !json.Valid(data) {
		return nil, "Outcome " + jsQuote(file) + " is not valid JSON", true
	}
	outcome, err = ledger.DecodeObject(data)
	if err != nil || !blockopen.ValidOutcome(outcome) {
		return nil, "", false
	}
	return outcome, "", true
}

func outcomeFiles(outcome ledger.Object) []ledger.Object {
	var out []ledger.Object
	values, _ := outcome.Get("files").([]any)
	for _, v := range values {
		out = append(out, v.(ledger.Object))
	}
	return out
}

// checkOutcomeProblems is checkOutcome: every problem, in Node's order.
func checkOutcomeProblems(file, root string) ([]string, bool) {
	outcome, problem, ok := loadOutcomeFile(file)
	if !ok {
		return nil, false
	}
	if problem != "" {
		return []string{problem}, true
	}
	problems := []string{}
	if status := outcome.Get("status").(string); status != "approved" {
		problems = append(problems, "status "+jsQuote(status)+": must be approved")
	}
	checks, _ := outcome.Get("checks").([]any)
	for _, v := range checks {
		check := v.(ledger.Object)
		if check.Get("result") == "fail" {
			problems = append(problems, "check "+jsQuote(check.Get("name").(string))+": result is fail")
		}
	}
	for _, entry := range outcomeFiles(outcome) {
		hash, problem, ok := outcomeHashFile(entry.Get("path").(string), root)
		if !ok {
			return nil, false
		}
		if problem != "" {
			problems = append(problems, problem)
		} else if entry.Get("sha256") != hash {
			problems = append(problems, "file "+jsQuote(entry.Get("path").(string))+": sha256 does not match")
		}
	}
	return problems, true
}

// prepareOutcome is src/cli/outcome.mjs. seal and check run in Go; record
// runs its check in Go and goes to Node only to sign a passing outcome.
func prepareOutcome(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usage := func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, outcomeUsage)
		return 2
	}
	if len(args) == 0 || (args[0] != "seal" && args[0] != "check" && args[0] != "record") {
		return usage, 0, true
	}
	command, rest := args[0], args[1:]
	var file, root, explicitStep string
	haveFile, haveRoot, haveStep, asJSON := false, false, false, false
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "--root":
			if haveRoot || i+1 >= len(rest) || rest[i+1] == "" || strings.HasPrefix(rest[i+1], "-") {
				return usage, 0, true
			}
			root, haveRoot = rest[i+1], true
			i++
		case arg == "--step" && command == "record":
			if haveStep || i+1 >= len(rest) || rest[i+1] == "" || strings.HasPrefix(rest[i+1], "-") {
				return usage, 0, true
			}
			explicitStep, haveStep = rest[i+1], true
			i++
		case arg == "--json" && command != "seal" && !asJSON:
			asJSON = true
		case strings.HasPrefix(arg, "-") || haveFile:
			return usage, 0, true
		default:
			file, haveFile = arg, true
		}
	}
	if file == "" {
		return usage, 0, true
	}
	if !haveRoot {
		root, _ = os.Getwd()
	}
	root = absPath(root)

	var problems []string
	var sealed ledger.Object
	if command == "seal" {
		outcome, problem, ok := loadOutcomeFile(file)
		if !ok {
			return nil, hOutcomeUnsupported, false
		}
		if problem != "" {
			problems = []string{problem}
		} else {
			entries := outcomeFiles(outcome)
			hashes := make([]string, len(entries))
			for i, entry := range entries {
				hash, problem, ok := outcomeHashFile(entry.Get("path").(string), root)
				if !ok {
					return nil, hOutcomeUnsupported, false
				}
				if problem != "" {
					problems = []string{problem}
					break
				}
				hashes[i] = hash
			}
			if problems == nil {
				if syscall.Access(file, 2) != nil {
					return nil, hOutcomeUnsupported, false
				}
				for i, entry := range entries {
					entry.Set("sha256", hashes[i])
				}
				sealed = outcome
			}
		}
	} else {
		var ok bool
		if problems, ok = checkOutcomeProblems(file, root); !ok {
			return nil, hOutcomeUnsupported, false
		}
		if command == "record" && len(problems) == 0 {
			var step any
			if haveStep {
				step = explicitStep
			}
			return prepareOutcomeRecord(file, root, step, haveStep, asJSON)
		}
	}

	return func(stdout, stderr io.Writer) int {
		if sealed != nil {
			data, err := ledger.Stringify(sealed, true)
			if err != nil {
				panic(err)
			}
			if err := os.WriteFile(file, append(data, '\n'), 0o666); err != nil {
				panic(err)
			}
		}
		ok := len(problems) == 0
		switch {
		case asJSON:
			list := make([]any, 0, len(problems))
			for _, p := range problems {
				list = append(list, p)
			}
			out, _ := ledger.Stringify(ledger.Object{{Key: "ok", Value: ok}, {Key: "problems", Value: list}}, false)
			fmt.Fprintf(stdout, "%s\n", out)
		case !ok:
			for _, p := range problems {
				fmt.Fprintf(stderr, "%s\n", p)
			}
		case command == "check":
			fmt.Fprint(stdout, "ok\n")
		}
		if ok {
			return 0
		}
		return 1
	}, 0, true
}
