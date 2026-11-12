package gates

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

var Order = []string{"test", "lint", "types", "format"}

type Command struct {
	Argv         []string
	Env          map[string]string
	FailOnStdout bool
	Formatter    string
}
type Plan struct {
	Stack       string
	Commands    map[string]*Command
	Kind        string
	Names       []string
	Restricted  bool
	Custom      []Custom
	Rerun       bool
	Repeat      *Repeat
	Light       []string
	ReuseTest   bool
	ReuseReason string
}

type Custom struct {
	Name     string
	Command  *Command
	Evidence []string
}
type Repeat struct {
	N      int
	Base   string
	Block  ledger.Object
	OnSkip func(string)
	// Stop is the import-graph stop list for changed sources (B450).
	Stop []string
}

func argv(a ...string) *Command  { return &Command{Argv: a} }
func exists(root, p string) bool { _, e := os.Stat(filepath.Join(root, p)); return e == nil }
func manifest(root string) map[string]any {
	b, _ := os.ReadFile(filepath.Join(root, "package.json"))
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	return p
}
func Detect(root string) Plan {
	p := Plan{Stack: "unknown", Commands: map[string]*Command{}}
	if exists(root, "composer.json") && (exists(root, "vendor/bin/pest") || exists(root, "vendor/bin/phpunit")) {
		runner := "phpunit"
		if exists(root, "vendor/bin/pest") {
			runner = "pest"
		}
		p.Stack = "php-" + runner
		p.Commands["test"] = argv("vendor/bin/" + runner)
		if exists(root, "artisan") {
			p.Commands["test"] = argv("php", "artisan", "test", "--compact")
		}
		if exists(root, "phpstan.neon") || exists(root, "phpstan.neon.dist") {
			p.Commands["types"] = argv("vendor/bin/phpstan")
		}
		if exists(root, "pint.json") || exists(root, "vendor/bin/pint") {
			p.Commands["format"] = &Command{Argv: []string{"vendor/bin/pint", "--test"}, Formatter: "pint"}
		}
		return p
	}
	pkg := manifest(root)
	scripts, _ := pkg["scripts"].(map[string]any)
	if _, ok := scripts["test"].(string); ok {
		p.Stack = "node"
		pm := ""
		if s, ok := pkg["packageManager"].(string); ok {
			n := strings.Split(s, "@")[0]
			if Has([]string{"pnpm", "yarn", "npm", "bun"}, n) {
				pm = n
			}
		}
		if pm == "" {
			pm = "npm"
			if exists(root, "pnpm-lock.yaml") {
				pm = "pnpm"
			} else if exists(root, "yarn.lock") {
				pm = "yarn"
			}
		}
		p.Commands["test"] = argv(pm, "test")
		if pm == "bun" {
			p.Commands["test"] = argv(pm, "run", "test")
		}
		for _, set := range [][]string{{"lint", "lint", "lint:check"}, {"types", "typecheck", "vue-tsc"}, {"format", "format:check"}} {
			for _, name := range set[1:] {
				if _, ok := scripts[name].(string); ok {
					p.Commands[set[0]] = argv(pm, "run", name)
					break
				}
			}
		}
		if p.Commands["types"] == nil && exists(root, "tsconfig.json") {
			p.Commands["types"] = argv("npx", "tsc", "--noEmit")
		}
		prettier := false
		for _, section := range []string{"dependencies", "devDependencies"} {
			deps, _ := pkg[section].(map[string]any)
			// JavaScript truthiness (Node's hasDependency): 0 is false too.
			if v := deps["prettier"]; v != nil && v != "" && v != false && v != float64(0) {
				prettier = true
			}
		}
		if p.Commands["format"] == nil && (prettier || exists(root, ".prettierrc") || exists(root, ".prettierrc.json") || exists(root, ".prettierrc.js")) {
			p.Commands["format"] = &Command{Argv: []string{"npx", "prettier", "--check", "--ignore-unknown"}, Formatter: "prettier"}
		}
		return p
	}
	if exists(root, "Cargo.toml") {
		p.Stack = "rust"
		p.Commands["test"] = argv("cargo", "test")
		p.Commands["lint"] = argv("cargo", "clippy", "--", "-D", "warnings")
		p.Commands["format"] = argv("cargo", "fmt", "--check")
		return p
	}
	if b, e := os.ReadFile(filepath.Join(root, "pyproject.toml")); e == nil {
		p.Stack = "python"
		p.Commands["test"] = argv("pytest")
		p.Commands["lint"] = argv("ruff", "check")
		p.Commands["format"] = argv("ruff", "format", "--check")
		if regexp.MustCompile(`(?m)^\[tool\.mypy(\..+)?\]\s*$`).Match(b) {
			p.Commands["types"] = argv("mypy")
		}
		return p
	}
	if exists(root, "go.mod") {
		p.Stack = "go"
		p.Commands["test"] = argv("go", "test", "./...")
		p.Commands["lint"] = argv("go", "vet", "./...")
		p.Commands["format"] = &Command{Argv: []string{"gofmt", "-l", "."}, FailOnStdout: true}
	}
	return p
}

// Resolve is ResolveErr's plan and whether it has no refusal.
func Resolve(cwd string, configured map[string]any) (Plan, bool) {
	p, err := ResolveErr(cwd, configured)
	return p, err == nil
}

// ResolveErr is the detected plan with every configured fixed gate on top. A
// gate Node could not spawn (not an argv array, an object without argv, a NUL
// byte, an env value that is not a string) is refused with one message per
// problem, joined by "; " (B450). Keys Node does not read are ignored, as in
// Node; an env that is not a non-empty map is no env.
func ResolveErr(cwd string, configured map[string]any) (Plan, error) {
	p := Detect(cwd)
	p.Rerun = configured["rerun_failed"] != float64(0) && configured["rerun_failed"] != json.Number("0")
	messages := []string{}
	for _, name := range Order {
		v, present := configured[name]
		if !present {
			continue
		}
		if v == nil {
			p.Commands[name] = nil
			continue
		}
		c, problems := command(name, v)
		messages = append(messages, problems...)
		if c != nil {
			p.Commands[name] = c
		}
	}
	if len(messages) > 0 {
		return p, errors.New(strings.Join(messages, "; "))
	}
	return p, nil
}

// command reads one configured gate: an argv array or an {argv, env,
// failOnStdout} object.
func command(key string, v any) (*Command, []string) {
	c := &Command{}
	switch v := v.(type) {
	case []any:
		argv, ok := argvArray(v)
		if !ok {
			return nil, []string{fmt.Sprintf("gates.%s must be a non-empty argv array of strings, an {argv, env} object, or null", key)}
		}
		c.Argv = argv
	case map[string]any:
		argv, ok := argvArray(v["argv"])
		if !ok {
			return nil, []string{fmt.Sprintf("gates.%s.argv must be a non-empty argv array of strings", key)}
		}
		c.Argv = argv
		c.FailOnStdout = v["failOnStdout"] == true
		if env, ok := v["env"].(map[string]any); ok && len(env) > 0 {
			messages := []string{}
			c.Env = map[string]string{}
			for _, k := range sortedKeys(env) {
				s, ok := env[k].(string)
				switch {
				case k == "" || strings.ContainsAny(k, "=\x00"):
					messages = append(messages, fmt.Sprintf("gates.%s.env: %s is not a variable name (letters, digits and _, not starting with a digit)", key, quoted(k)))
				case !ok:
					messages = append(messages, fmt.Sprintf("gates.%s.env.%s must be a string", key, k))
				case strings.ContainsRune(s, 0):
					messages = append(messages, fmt.Sprintf("gates.%s.env.%s must not contain a NUL byte", key, k))
				default:
					c.Env[k] = s
				}
			}
			if len(messages) > 0 {
				return nil, messages
			}
		}
	default:
		return nil, []string{fmt.Sprintf("gates.%s must be a non-empty argv array of strings, an {argv, env} object, or null", key)}
	}
	for _, s := range c.Argv {
		if strings.ContainsRune(s, 0) {
			return nil, []string{fmt.Sprintf("gates.%s.argv must not contain a NUL byte", key)}
		}
	}
	return c, nil
}
