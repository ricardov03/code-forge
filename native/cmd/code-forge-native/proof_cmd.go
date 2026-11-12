package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// B458: `proof` help, usage errors, `baseline` and `proof tier` run in Go, as
// src/cli/proof.mjs. B462: `proof tier` with a project config, `lock`,
// `unlock`, `restore` and `export --remove` too (proof_config.go,
// proof_run.go). A call Go has no port for yet hands over to Node with its
// own reason.
func init() { routes["proof"] = runProofVerb }

const proofUsage = "usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]\n" +
	"       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>\n" +
	"       code-forge proof restore <block> --run <r>\n" +
	"       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]\n"

var proofSubUsage = map[string]string{
	"tier":      "usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]\n",
	"export":    "usage: code-forge proof export <block> --run <r> [--remove]\n",
	"lock":      "usage: code-forge proof lock <block> --run <r>\n",
	"unlock":    "usage: code-forge proof unlock <block> --run <r>\n",
	"restore":   "usage: code-forge proof restore <block> --run <r>\n",
	"red-green": "usage: code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]\n",
}

// proofOutcome is one Go-served answer: stdout, stderr and the exit code.
type proofOutcome struct {
	stdout, stderr string
	code           int
}

func proofRefuse(sub, message string, code int) *proofOutcome {
	return &proofOutcome{stderr: "proof " + sub + ": " + message + "\n", code: code}
}

// posixNormalize is Node's path.posix.normalize for a relative path.
func posixNormalize(p string) string {
	trailing := strings.HasSuffix(p, "/")
	clean := path.Clean(p)
	if clean == "." {
		if trailing {
			return "./"
		}
		return "."
	}
	if trailing {
		return clean + "/"
	}
	return clean
}

// proofPlan decides a proof call. It returns the Go answer, or nil when the
// call must run in Node (with the hand-over reason).
func proofPlan(args []string) (*proofOutcome, handoverReason) {
	if len(args) == 0 {
		return &proofOutcome{stderr: proofUsage, code: 2}, 0
	}
	sub, rest := args[0], args[1:]
	usage := func(message string) (*proofOutcome, handoverReason) { return proofRefuse(sub, message, 2), 0 }
	switch sub {
	case "baseline":
		return &proofOutcome{stderr: "proof baseline: held on Q16 (block B10c) — the gate enforces red→green only\n", code: 2}, 0
	case "tier":
		flags, positionals, message := metaParseFlags(rest, metaFlagSpec{values: []string{"file", "risk", "cwd"}, booleans: []string{"security"}})
		if message != "" {
			return usage(message)
		}
		file, okFile := flags["file"].(string)
		risk, okRisk := flags["risk"].(string)
		if len(positionals) > 0 || !okFile || !okRisk {
			return usage("proof tier needs --file and --risk")
		}
		if len(risk) != 1 || risk[0] < '0' || risk[0] > '3' {
			return usage("--risk must be 0, 1, 2 or 3")
		}
		dir, ok := flags["cwd"].(string)
		if !ok {
			dir, _ = os.Getwd()
		}
		cfg, refusal, handover := proofConfigAt(dir)
		if handover != 0 {
			return nil, handover
		}
		if refusal != "" {
			return proofRefuse(sub, refusal, 1), 0
		}
		if file == "" {
			return proofRefuse(sub, "tierFor: file must be a non-empty repo-relative path", 1), 0
		}
		highPaths, _ := jsObject(jsObject(jsObject(cfg["proof"])["tiers"])["high"])["paths"].([]any)
		tier, reason := "light", "default"
		if flags["security"] == true {
			tier, reason = "high", "security"
		} else if risk[0] >= '2' {
			tier, reason = "high", "risk"
		} else if len(highPaths) > 0 && owned.OwnsFile(highPaths, nil, file) {
			tier, reason = "high", "path"
		}
		out, _ := ledger.Stringify(ledger.Object{{Key: "file", Value: file}, {Key: "tier", Value: tier}, {Key: "reason", Value: reason}}, false)
		return &proofOutcome{stdout: string(out) + "\n"}, 0
	case "restore", "export", "lock", "unlock":
		spec := metaFlagSpec{values: []string{"run"}}
		if sub == "export" {
			spec.booleans = []string{"remove"}
		}
		flags, positionals, message := metaParseFlags(rest, spec)
		if message != "" {
			return usage(message)
		}
		runID, ok := flags["run"].(string)
		if len(positionals) != 1 || positionals[0] == "" || !ok {
			return usage("proof " + sub + " needs one block id and --run")
		}
		return proofRunSub(sub, runID, positionals[0], flags["remove"] == true)
	case "red-green":
		flags, positionals, message := metaParseFlags(rest, metaFlagSpec{values: []string{"run", "test", "mechanism"}})
		if message != "" {
			return usage(message)
		}
		_, okRun := flags["run"].(string)
		test, okTest := flags["test"].(string)
		if len(positionals) != 1 || !okRun || !okTest {
			return usage("proof red-green needs one block id, --run and --test <file[::case]>")
		}
		if mechanism, ok := flags["mechanism"].(string); ok && mechanism != "revert" && mechanism != "assertion-deletion" {
			return usage("--mechanism must be one of revert, assertion-deletion")
		}
		file := test
		if sep := strings.Index(test, "::"); sep >= 0 {
			if test[sep+2:] == "" {
				return usage("--test <file>::<case> needs a case name after ::")
			}
			file = test[:sep]
		}
		if file == "" || strings.HasPrefix(file, "/") || strings.ContainsAny(file, "\\\x00") {
			return usage("--test must be a repo-relative path")
		}
		normalized := posixNormalize(file)
		if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
			return usage("--test must stay inside the repository (no .. escape)")
		}
		if normalized == ".code-forge" || strings.HasPrefix(normalized, ".code-forge/") {
			return usage("--test cannot be under .code-forge/")
		}
		return nil, hProofRun
	}
	return &proofOutcome{stderr: proofUsage, code: 2}, 0
}

func runProofVerb(args []string) int {
	if metaHelp(args, proofUsage, proofSubUsage) {
		return 0
	}
	outcome, reason := proofPlan(args)
	if outcome == nil {
		return metaHandover("proof", args, reason)
	}
	sub := metaSub(args, "tier", "export", "lock", "unlock", "restore", "red-green")
	return metaServe("proof", sub, args, func(stdout, stderr io.Writer) int {
		fmt.Fprint(stdout, outcome.stdout)
		fmt.Fprint(stderr, outcome.stderr)
		return outcome.code
	})
}
