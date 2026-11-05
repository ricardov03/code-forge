package gates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var Order = []string{"test", "lint", "types", "format"}

type Command struct {
	Argv         []string
	Env          map[string]string
	FailOnStdout bool
	Formatter    string
}
type Plan struct {
	Stack      string
	Commands   map[string]*Command
	Kind       string
	Names      []string
	Restricted bool
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
			if v := deps["prettier"]; v != nil && v != "" && v != false {
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

// Resolve refuses shapes outside the runner's exact command representation.
func Resolve(cwd string, configured map[string]any) (Plan, bool) {
	p := Detect(cwd)
	for _, name := range Order {
		v, present := configured[name]
		if !present {
			continue
		}
		if v == nil {
			p.Commands[name] = nil
			continue
		}
		c := &Command{}
		switch v := v.(type) {
		case []any:
			c.Argv = Strings(v)
			if len(c.Argv) != len(v) {
				return p, false
			}
		case map[string]any:
			for k := range v {
				if k != "argv" && k != "env" && k != "failOnStdout" {
					return p, false
				}
			}
			a, ok := v["argv"].([]any)
			if !ok {
				return p, false
			}
			c.Argv = Strings(a)
			if len(c.Argv) != len(a) {
				return p, false
			}
			c.FailOnStdout = v["failOnStdout"] == true
			if v["env"] != nil {
				env, ok := v["env"].(map[string]any)
				if !ok {
					return p, false
				}
				c.Env = map[string]string{}
				for k, v := range env {
					s, ok := v.(string)
					if !ok {
						return p, false
					}
					c.Env[k] = s
				}
			}
		default:
			return p, false
		}
		if len(c.Argv) == 0 {
			return p, false
		}
		for _, s := range c.Argv {
			if strings.ContainsRune(s, 0) {
				return p, false
			}
		}
		p.Commands[name] = c
	}
	for _, c := range p.Commands {
		if c != nil && c.Formatter != "" {
			return p, false
		}
	} // Git formatter batching stays with Node.
	return p, true
}
