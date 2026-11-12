package main

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

// B481: the config rules proofConfigAt used to hand over for, counted as
// validate.mjs counts them: proof-runners-invalid (rule 17, runnerMessages),
// the multimodel checks of review-routes-invalid (rule 18) and
// sandbox-dir-refused (rule 19, sandboxDirRefusals of sandbox-dirs.mjs).

var proofRunnerAdapters = map[string][]string{
	"junit":      {"{test_file}", "{test_id}", "{test_filter}", "{report}"},
	"swift":      {"{test_file}", "{test_id}", "{test_filter}", "{report}"},
	"xcodebuild": {"{workspace}", "{project}", "{scheme}", "{destination}", "{only_testing}", "{result_bundle}"},
}

var (
	proofRunnerTestTokens = []string{"{test_file}", "{test_id}", "{test_filter}"}
	proofRunnerAllTokens  = []string{"{test_file}", "{test_id}", "{test_filter}", "{report}", "{workspace}", "{project}", "{scheme}", "{destination}", "{only_testing}", "{result_bundle}"}
	proofRunnerKeys       = []string{"match", "adapter", "argv", "env", "timeout_s", "xcode"}
	proofXcodeKeys        = []string{"workspace", "project", "scheme", "destination", "test_target"}
	proofXcodeRequired    = []string{"scheme", "destination", "test_target"}
	proofXcodeTokens      = []string{"{scheme}", "{destination}", "{only_testing}", "{result_bundle}"}
	proofAnyReportTokens  = []string{"{report}", "{result_bundle}"}
	proofAnyTestTokens    = []string{"{test_file}", "{test_id}", "{test_filter}", "{only_testing}"}
	proofTokenShape       = regexp.MustCompile(`^\{[a-z_]+\}$`)
)

func proofAnyIn(items []string, list []string) bool {
	for _, item := range items {
		if proofIn(item, list...) {
			return true
		}
	}
	return false
}

// proofRunnersIssues is checkProofRunnersInvalid's count.
func proofRunnersIssues(cfg map[string]any) int {
	runners, present := jsObject(cfg["proof"])["runners"]
	if !present {
		return 0
	}
	list, ok := runners.([]any)
	if !ok || len(list) == 0 {
		return 1
	}
	n := 0
	for _, entry := range list {
		n += proofRunnerMessages(entry)
	}
	return n
}

// proofRunnerMessages counts runnerMessages of validate.mjs for one entry.
func proofRunnerMessages(entry any) int {
	runner, ok := entry.(map[string]any)
	if !ok {
		return 1
	}
	n := 0
	adapterName, _ := runner["adapter"].(string)
	known, hasAdapter := proofRunnerAdapters[adapterName]
	if !hasAdapter {
		adapterName = ""
		n++
	}
	for key := range runner {
		if !proofIn(key, proofRunnerKeys...) {
			n++
		}
	}
	if match, ok := runner["match"].([]any); !ok || len(match) == 0 {
		n++
	} else {
		for _, glob := range match {
			if text, ok := glob.(string); !ok || text == "" {
				n++
				break
			}
		}
	}
	argv, isArgv := proofIsArgv(runner["argv"])
	if !isArgv {
		n++
	} else {
		if proofTokenShape.MatchString(argv[0]) {
			n++
		}
		for j, item := range argv {
			token := proofTokenShape.MatchString(item)
			switch {
			case strings.Contains(item, "\x00"):
				n++
			case j > 0 && token && hasAdapter && !proofIn(item, known...):
				n++
			case j > 0 && token && !hasAdapter && !proofIn(item, proofRunnerAllTokens...):
				n++
			case !token && proofContainsToken(item):
				n++
			}
		}
		items := argv[1:]
		switch adapterName {
		case "junit", "swift":
			if !proofIn("{report}", items...) {
				n++
			}
			if !proofAnyIn(items, proofRunnerTestTokens) {
				n++
			}
		case "xcodebuild":
			for _, token := range proofXcodeTokens {
				if !proofIn(token, items...) {
					n++
				}
			}
		default:
			if !proofAnyIn(items, proofAnyReportTokens) {
				n++
			}
			if !proofAnyIn(items, proofAnyTestTokens) {
				n++
			}
		}
	}
	n += proofXcodeMessages(runner, adapterName, hasAdapter, argv, isArgv)
	if env, present := runner["env"]; present {
		object, ok := env.(map[string]any)
		if !ok {
			n++
		} else {
			for name, value := range object {
				text, isText := value.(string)
				if !proofEnvName.MatchString(name) || !isText || strings.Contains(text, "\x00") {
					n++
				}
			}
		}
	}
	if value, present := runner["timeout_s"]; present {
		if t, ok := jsInteger(value); !ok || t < 60 || t > 7200 {
			n++
		}
	}
	return n
}

func proofContainsToken(item string) bool {
	for _, token := range proofRunnerAllTokens {
		if strings.Contains(item, token) {
			return true
		}
	}
	return false
}

// proofXcodeMessages counts xcodeMessages of validate.mjs.
func proofXcodeMessages(runner map[string]any, adapter string, hasAdapter bool, argv []string, isArgv bool) int {
	xcode, present := runner["xcode"]
	if !present {
		if adapter == "xcodebuild" {
			return 1
		}
		return 0
	}
	if hasAdapter && adapter != "xcodebuild" {
		return 1
	}
	values, ok := xcode.(map[string]any)
	if !ok {
		return 1
	}
	n := 0
	for key := range values {
		if !proofIn(key, proofXcodeKeys...) {
			n++
		}
	}
	for _, key := range proofXcodeKeys {
		value, present := values[key]
		text, isText := value.(string)
		switch {
		case !present:
			if proofIn(key, proofXcodeRequired...) {
				n++
			}
		case !isText || text == "" || strings.HasPrefix(text, "-"):
			n++
		case strings.Contains(text, "\x00"):
			n++
		case key == "test_target" && strings.Contains(text, "/"):
			n++
		}
	}
	_, hasWorkspace := values["workspace"]
	_, hasProject := values["project"]
	if hasWorkspace && hasProject {
		n++
	}
	if isArgv {
		items := argv[1:]
		for _, key := range []string{"workspace", "project"} {
			_, set := values[key]
			inArgv := proofIn("{"+key+"}", items...)
			if inArgv != set {
				n++
			}
		}
	}
	return n
}

// proofRuleProvider is ruleProvider of review/route.mjs: the rule's entry
// replaces levels.<level>, so its provider is its own, else the top-level one;
// "" when resolveLevel would throw.
func proofRuleProvider(cfg, rule map[string]any) string {
	model, ok := rule["model"].(string)
	if !ok || model == "" {
		return ""
	}
	if fallback, present := rule["fallback"]; present && fallback != nil {
		if _, ok := fallback.([]any); !ok {
			return ""
		}
	}
	provider, present := rule["provider"]
	if !present || provider == nil {
		provider = cfg["provider"]
	}
	text, _ := provider.(string)
	return text
}

// proofRouteMultimodel counts the multimodel messages of one L2 route rule.
func proofRouteMultimodel(cfg, rule map[string]any) int {
	first := proofRuleProvider(cfg, rule)
	if first == "" {
		return 0
	}
	if second := jsObject(rule["second"]); second != nil {
		if own, present := second["provider"]; present && own == any(first) {
			return 1
		}
	}
	if second := proofSecondL2Provider(cfg); second == any(first) {
		return 1
	}
	return 0
}

// proofSandboxIssues is checkSandboxDirRefused's count: one per string
// add_dirs entry sandboxDirRefusals refuses.
func proofSandboxIssues(cfg map[string]any) int {
	sandbox := jsObject(jsObject(cfg["coder"])["sandbox"])
	dirs, ok := sandbox["add_dirs"].([]any)
	if sandbox == nil || !ok {
		return 0
	}
	trees := proofRefusedTrees(cfg)
	n := 0
	for _, entry := range dirs {
		if text, ok := entry.(string); ok && proofSandboxRefused(text, trees) {
			n++
		}
	}
	return n
}

type proofTrees struct {
	home, realHome string
	protected, run []string
}

var proofProtectedHomeDirs = []string{".code-forge", ".codex", ".ssh", ".gnupg", ".aws", ".config", filepath.Join("Library", "Keychains")}

// proofTmpBase is tmpBase(cfg.tmp.root) with validate.mjs's fallback to the
// default base on a bad root.
func proofTmpBase(cfg map[string]any) string {
	if root, present := jsObject(cfg["tmp"])["root"]; present && root != nil {
		if text, ok := root.(string); ok && filepath.IsAbs(text) {
			return text
		}
	}
	if env := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(env) {
		return env
	}
	return filepath.Join(proofOSTmpdir(), "code-forge")
}

// proofOSTmpdir is os.tmpdir() on POSIX: TMPDIR, TMP or TEMP without a
// trailing slash, else /tmp.
func proofOSTmpdir() string {
	dir := "/tmp"
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		if value := os.Getenv(name); value != "" {
			dir = value
			break
		}
	}
	if len(dir) > 1 && strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	return dir
}

func proofHome() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

func proofRefusedTrees(cfg map[string]any) proofTrees {
	home := proofHome()
	trees := proofTrees{home: home, realHome: proofResolveTree(home)}
	for _, dir := range proofProtectedHomeDirs {
		trees.protected = append(trees.protected, proofResolveTree(filepath.Join(home, dir)))
	}
	if codex := os.Getenv("CODEX_HOME"); filepath.IsAbs(codex) {
		trees.protected = append(trees.protected, proofResolveTree(codex))
	}
	if base := proofTmpBase(cfg); filepath.IsAbs(base) {
		trees.run = append(trees.run, proofResolveTree(base))
	}
	return trees
}

// proofSandboxRefused is checkOne of sandbox-dirs.mjs: true for a refusal.
func proofSandboxRefused(entry string, trees proofTrees) bool {
	var expanded string
	switch {
	case entry == "" || strings.Contains(entry, "\x00"):
		return true
	case strings.HasPrefix(entry, "~/"):
		abs, _ := filepath.Abs(trees.home)
		expanded = abs + string(filepath.Separator) + entry[2:]
	case filepath.IsAbs(entry):
		expanded = entry
	default:
		return true
	}
	hops := 0
	real, err := proofResolvePath(expanded, &hops)
	if err != nil || real == "/" || proofSame(real, trees.realHome) || proofWithin(trees.realHome, real) {
		return true
	}
	for _, tree := range append(append([]string{}, trees.protected...), trees.run...) {
		if proofWithin(real, tree) || proofWithin(tree, real) {
			return true
		}
	}
	return false
}

var errProofUnresolvable = errors.New("unresolvable")

// proofNative is realpathSync.native, or p when it fails.
func proofNative(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// proofResolvePath is resolvePath of sandbox-dirs.mjs: a component walk with
// lstat, following each symlink (at most 40), ".." from the resolved dir; a
// missing component keeps the rest as written.
func proofResolvePath(p string, hops *int) (string, error) {
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	cur := "/"
	for i, part := range parts {
		if part == ".." {
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		st, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return filepath.Join(append([]string{proofNative(cur)}, parts[i:]...)...), nil
			}
			return "", errProofUnresolvable
		}
		if st.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		*hops++
		if *hops > 40 {
			return "", errProofUnresolvable
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", errProofUnresolvable
		}
		if !filepath.IsAbs(target) {
			target = cur + "/" + target
		}
		if cur, err = proofResolvePath(target, hops); err != nil {
			return "", err
		}
	}
	return proofNative(cur), nil
}

func proofResolveTree(p string) string {
	abs, _ := filepath.Abs(p)
	hops := 0
	if real, err := proofResolvePath(abs, &hops); err == nil {
		return real
	}
	return abs
}

func proofFold(p string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

func proofSame(a, b string) bool { return proofFold(a) == proofFold(b) }

// proofWithin: a is b or inside it.
func proofWithin(a, b string) bool {
	rel, err := filepath.Rel(proofFold(b), proofFold(a))
	if err != nil {
		return false
	}
	return rel == "." || !(rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel))
}
