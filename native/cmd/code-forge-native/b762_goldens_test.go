package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// B762: checked-in goldens for the init/proof parity tables. Each golden in
// testdata/b762/<test>/<case>.<field> is what the Node CLI printed and wrote
// for that case (recorded from Node while it still existed, normalized the way
// the old *MatchNode test normalized both sides). These tests run only the Go
// side and compare it with the goldens: no Node CLI runs at test time.
//
// Two more masks make a golden hold on any checkout and release: the package
// root (this checkout, where both engines found the skill files) is <PKG>, and
// the package.json version is <VERSION>.

var b762Dir = filepath.Join("testdata", "b762")

var b762SlugBad = regexp.MustCompile(`[^a-z0-9.]+`)

// b762Slug turns a case name into a file name.
func b762Slug(name string) string {
	slug := strings.Trim(b762SlugBad.ReplaceAllString(strings.ToLower(name), "-"), "-.")
	if slug == "" {
		slug = "empty"
	}
	return slug
}

// b762ArgsCase names a case by its argv: a readable slug plus a digest of the
// exact argv, so two argvs never share a name.
func b762ArgsCase(args []string) string {
	data, _ := json.Marshal(args)
	sum := sha256.Sum256(data)
	slug := b762Slug(strings.Join(args, " "))
	if len(slug) > 48 {
		slug = slug[:48]
	}
	return slug + "-" + hex.EncodeToString(sum[:4])
}

// b762Norm masks the package root and version (see the file comment).
func b762Norm(t *testing.T, text string) string {
	t.Helper()
	repo := b458Repo(t)
	data, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Version string }
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Version == "" {
		t.Fatalf("package.json version: %v", err)
	}
	text = strings.ReplaceAll(text, repo, "<PKG>")
	return strings.ReplaceAll(text, pkg.Version, "<VERSION>")
}

// b762JSON is a stable, readable JSON text (sorted map keys, no HTML escapes).
func b762JSON(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// b762Field is one golden file of a case.
type b762Field struct{ name, value string }

var b762HostField = regexp.MustCompile(`("(?:arch|platform)"\s*:\s*)"[^"\\]*"`)

// b762HostNorm masks the host's values in an error-log row: the goldens were
// recorded on a Mac ("arch":"arm64", "platform":"darwin") and Linux CI writes
// "arch":"x64", "platform":"linux" (B766). The key and its spacing stay, so
// the compact and the indented JSON forms both keep their shape.
func b762HostNorm(s string) string {
	return b762HostField.ReplaceAllString(s, `${1}"<HOST>"`)
}

// b762Golden compares the Go side's fields of one case with its goldens. Both
// sides go through b762HostNorm first.
func b762Golden(t *testing.T, test, name string, got []b762Field) {
	t.Helper()
	dir := filepath.Join(b762Dir, test)
	for _, f := range got {
		file := filepath.Join(dir, name+"."+f.name)
		want, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("%s: no golden: %v", file, err)
			continue
		}
		if value, golden := b762HostNorm(f.value), b762HostNorm(string(want)); golden != value {
			t.Errorf("%s differs\n go:     %q\n golden: %q", file, value, golden)
		}
	}
}

// b762NoStrays fails when the golden dir holds a file of no known case, so a
// renamed or dropped case cannot leave an unchecked golden behind.
func b762NoStrays(t *testing.T, test string, names map[string]bool, fields []string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(b762Dir, test))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for name := range names {
		for _, f := range fields {
			known[name+"."+f] = true
		}
	}
	for _, e := range entries {
		if !known[e.Name()] {
			t.Errorf("%s/%s: golden of no case", test, e.Name())
		}
	}
	if len(entries) != len(known) {
		t.Errorf("%s: %d golden files, want %d", test, len(entries), len(known))
	}
}

// b762Names records each case name once.
func b762Names(t *testing.T, names map[string]bool, name string) string {
	t.Helper()
	if names[name] {
		t.Fatalf("two cases share the golden name %q", name)
	}
	names[name] = true
	return name
}

// --- field sets of each side type (shared with the one-time recorder) ---

var b762Fields458 = []string{"code", "stdout", "stderr", "crumbs", "errors"}

func b762Side458(t *testing.T, side b458Side) []b762Field {
	return []b762Field{
		{"code", strconv.Itoa(side.code)},
		{"stdout", b762Norm(t, side.stdout)},
		{"stderr", b762Norm(t, side.stderr)},
		{"crumbs", b762Norm(t, side.crumbs)},
		{"errors", b762Norm(t, b762JSON(t, side.errors))},
	}
}

var b762Fields502 = []string{"code", "stdout", "stderr", "initlog", "crumbs", "errors"}

func b762Side502(t *testing.T, side b502Side) []b762Field {
	return []b762Field{
		{"code", strconv.Itoa(side.code)},
		{"stdout", b762Norm(t, side.stdout)},
		{"stderr", b762Norm(t, side.stderr)},
		{"initlog", b762Norm(t, side.log)},
		{"crumbs", b762Norm(t, side.crumbs)},
		{"errors", b762Norm(t, side.errors)},
	}
}

var b762Fields527 = []string{"code", "stdout", "stderr", "initlog", "files"}

func b762Side527(t *testing.T, side b527Side) []b762Field {
	return []b762Field{
		{"code", strconv.Itoa(side.code)},
		{"stdout", b762Norm(t, side.stdout)},
		{"stderr", b762Norm(t, side.stderr)},
		{"initlog", b762Norm(t, side.log)},
		{"files", b762CopyMask(t, b762Norm(t, side.files))},
	}
}

var b762SkillDirRow = regexp.MustCompile(`(?m)^(\S+/skills/code-forge) d`)

// b762CopyMask: a harness copy of the skill (init --copy) lists every skill
// file with its bytes, so any skill doc edit would change the golden. When the
// copied tree under <dir>/skills/code-forge is byte-for-byte the package's
// skill/ tree (same entries, modes and bytes, as b527Tree prints them), its
// rows become one row "<dir>/skills/code-forge/** = <PKG>/skill". A copy that
// differs from the package is left as it is, so the golden still catches it.
func b762CopyMask(t *testing.T, files string) string {
	t.Helper()
	for _, m := range b762SkillDirRow.FindAllStringSubmatch(files, -1) {
		block, row := b762SkillBlock(t, m[1]), "\n"+m[1]+"/** = <PKG>/skill"
		if strings.Contains(files, block+"\n") {
			files = strings.Replace(files, block+"\n", row+"\n", 1)
		} else if strings.HasSuffix(files, block) {
			files = strings.TrimSuffix(files, block) + row
		}
	}
	return files
}

// b762SkillBlock is the rows b527Tree prints for an exact copy of the
// package's skill/ tree at prefix, masked as the goldens are.
func b762SkillBlock(t *testing.T, prefix string) string {
	t.Helper()
	src := filepath.Join(b458Repo(t), "skill")
	var rows []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == src {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		row := prefix + "/" + filepath.ToSlash(rel) + " " + info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			row = prefix + "/" + filepath.ToSlash(rel) + " L -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if len(data) > 4096 {
				sum := sha256.Sum256(data)
				row += " sha256:" + hex.EncodeToString(sum[:])
			} else {
				row += "\n" + b527ISO.ReplaceAllString(string(data), "<ISO>")
			}
		}
		rows = append(rows, b762Norm(t, row))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	return "\n" + strings.Join(rows, "\n")
}

var b762FieldsRun = []string{"code", "stdout", "stderr", "record", "rows", "files"}

// b762SideRun is a proof run call: exit, output, and the state it left (run
// record, ledger rows, workspace files), masked as TestB462ProofRunMatchesNode
// masked it. The test process's pid (a fixture's live gate_pid) is <PID>.
func b762SideRun(t *testing.T, code int, stdout, stderr string, state b462State) []b762Field {
	pid := regexp.MustCompile(`(gate_pid\\?"\s*:\s*)` + strconv.Itoa(os.Getpid()) + `\b`)
	mask := func(s string) string { return b762Norm(t, pid.ReplaceAllString(s, "${1}<PID>")) }
	return []b762Field{
		{"code", strconv.Itoa(code)},
		{"stdout", b762Norm(t, stdout)},
		{"stderr", b762Norm(t, stderr)},
		{"record", mask(b462Stamp.ReplaceAllString(state.record, "<T>"))},
		{"rows", mask(b762JSON(t, state.rows))},
		{"files", mask(b762JSON(t, state.files))},
	}
}

// b762RunNorm is the old test's output mask: the workspace and ISO stamps.
func b762RunNorm(s, repo string) string {
	return b462Stamp.ReplaceAllString(strings.ReplaceAll(s, repo, "<REPO>"), "<T>")
}

// --- the cases of each retired *MatchNode test ---

// b762B462TierCases are TestB462ProofTierConfigMatchesNode's calls.
func b762B462TierCases() (names []string, configs, files []string) {
	for _, name := range sortedKeysB762(b462TierConfigs) {
		for _, file := range []string{"src/a.go", "lib/x.go", ""} {
			configs, files = append(configs, name), append(files, file)
			names = append(names, b762Slug(name+" "+file))
		}
	}
	return names, configs, files
}

func sortedKeysB762(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// b762B481RedGreen are the TestB481ProofRedGreenMatchesNode cases (the fake
// test modes only exist for the Go side).
var b762B481RedGreen = map[string]bool{"revert-proven": true, "case-pattern": true, "assertion-deletion": true, "lock-isolation": true, "lock-not-held": true, "journal-present": true, "setup-failed": true, "unsupported-test": true, "test-not-a-file": true, "revert-no-source": true, "not-open": true, "runners-empty": true}

// b762B502Cases are TestB502InitRefusalsMatchNode's table.
var b762B502Cases = []struct {
	name   string
	args   []string
	noHome bool
	setup  func(string)
}{
	{"unknown flag", []string{"--bogus"}, false, nil},
	{"positional", []string{"extra"}, false, nil},
	{"needs a value", []string{"--tools"}, false, nil},
	{"value starts with dash", []string{"--provider", "-g"}, false, nil},
	{"bad tools", []string{"--tools=all"}, false, nil},
	{"bad yes-tool", []string{"--yes-tool", "vim"}, false, nil},
	{"empty harness list", []string{"--harness", " , "}, false, nil},
	{"unknown harness", []string{"--harness", "claude,vim"}, false, nil},
	{"bad provider", []string{"--second-provider", "google"}, false, nil},
	{"level no key", []string{"--level", "=x"}, false, nil},
	{"level name", []string{"--level", "L4=x"}, false, nil},
	{"level provider", []string{"--level", "L2=m:high@google"}, false, nil},
	{"level effort", []string{"--level", "L2=m:@xai"}, false, nil},
	{"level model", []string{"--level", "L2=:high"}, false, nil},
	{"multimodel", []string{"--multimodel", "yes"}, false, nil},
	{"jev-ref", []string{"--jev-ref", "not-a-ref"}, false, nil},
	{"jev-env", []string{"--jev-env", "1BAD"}, false, nil},
	{"engine subprocess", []string{"--engine", "subprocess"}, false, nil},
	{"engine", []string{"--engine=cloud"}, false, nil},
	{"solo project", []string{"--solo-project", "01"}, false, nil},
	{"gate no key", []string{"--gate", "test"}, false, nil},
	{"gate name", []string{"--gate", "build=make"}, false, nil},
	{"gate quote", []string{"--gate", `test=npm "run`}, false, nil},
	{"gate empty", []string{"--gate", "test=  "}, false, nil},
	{"coder flag", []string{"--coder-may-push", "maybe"}, false, nil},
	{"proof key", []string{"--proof", "speed=1"}, false, nil},
	{"proof isolation", []string{"--proof", "isolation=copy"}, false, nil},
	{"boolean value", []string{"--skip-doctor=1"}, false, nil},
	{"repeated", []string{"--engine", "auto", "--engine", "solo"}, false, nil},
	{"scope exclusive", []string{"-g", "-p"}, false, nil},
	{"jev exclusive", []string{"--no-jev", "--jev-ref", "op://v/i/f", "--jev-env", "K"}, false, nil},
	{"no HOME", []string{"--no-interaction"}, true, nil},
	{"config is a directory", []string{"--no-interaction"}, false, func(cwd string) { _ = os.Mkdir(filepath.Join(cwd, ".code-forge.yml"), 0o755) }},
	{"config is a list", nil, false, b502WriteConfig("- a\n- b\n")},
	{"config is empty", nil, false, b502WriteConfig("")},
	{"config version newer", nil, false, b502WriteConfig("version: 2\n")},
	{"config version text", []string{"--no-interaction"}, false, b502WriteConfig("version: one\n")},
	{"draft PR without push", []string{"--no-interaction", "--coder-may-open-draft-pr", "yes", "--skip-doctor", "--no-jev"}, false, nil},
	{"draft PR, push no", []string{"--coder-may-open-draft-pr=yes", "--coder-may-push=no"}, false, nil},
	{"draft PR, existing push no", []string{"--coder-may-open-draft-pr", "yes"}, false, b502WriteConfig("version: 1\nautonomy:\n  coder_may_push: false\n")},
}

var b762InitArgs686 = []string{"--no-interaction", "--skip-doctor", "--no-jev"}

// --- the golden tests ---

// TestB762B458FormsGolden: the B458 forms (meta, facts, author, s2, jev and
// proof usage) against Node's recorded answers. A `<verb> --help` page wraps
// the recorded usage text, as TestB458FormsMatchNode did.
func TestB762B458FormsGolden(t *testing.T) {
	names := map[string]bool{}
	for _, args := range b458GoForms {
		name := b762Names(t, names, b762ArgsCase(args))
		side := b458Go(t, args)
		routed := append([]string{}, args...)
		if len(routed) > 0 && (routed[0] == "--version" || routed[0] == "-v") {
			routed[0] = "version"
		}
		got := b762Side458(t, side)
		if name2, ok := helpTarget(routed); ok && side.code == 0 {
			// the golden is Node's raw usage; Go prints it wrapped
			raw, err := os.ReadFile(filepath.Join(b762Dir, "B458FormsMatchNode", name+".stdout"))
			if err == nil && helpIsUsage(string(raw)) {
				want := b762Norm(t, helpWrapped(t, name2, string(raw)))
				if got[1].value != want {
					t.Errorf("%q: help page differs\n go:   %q\n want: %q", args, got[1].value, want)
				}
				got = append(got[:1], got[2:]...)
			}
		}
		b762Golden(t, "B458FormsMatchNode", name, got)
	}
	b762NoStrays(t, "B458FormsMatchNode", names, b762Fields458)
}

func TestB762B462ProofTierConfigGolden(t *testing.T) {
	names := map[string]bool{}
	caseNames, configs, files := b762B462TierCases()
	for i, name := range caseNames {
		b762Names(t, names, name)
		dir := b462TierDir(t, b462TierConfigs[configs[i]])
		side := b458Go(t, []string{"proof", "tier", "--file", files[i], "--risk", "0", "--cwd", dir})
		b762Golden(t, "B462ProofTierConfigMatchesNode", name, b762Side458(t, side))
	}
	b762NoStrays(t, "B462ProofTierConfigMatchesNode", names, b762Fields458)
}

func TestB762B462ProofRunGolden(t *testing.T) {
	names := map[string]bool{}
	for _, c := range b462Cases {
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			home, repo := b462Fixture(t, c)
			code, stdout, stderr := b462GoCall(t, home, repo, c.args)
			got := b762SideRun(t, code, b762RunNorm(stdout, repo), stderr, b462Collect(t, home, repo))
			b762Golden(t, "B462ProofRunMatchesNode", name, got)
		})
	}
	b762NoStrays(t, "B462ProofRunMatchesNode", names, b762FieldsRun)
}

func TestB762B481ProofConfigGolden(t *testing.T) {
	names := map[string]bool{}
	bodies := b481ConfigBodies(t)
	for _, key := range sortedKeysB762(bodies) {
		name := b762Names(t, names, b762Slug(key))
		args := []string{"proof", "tier", "--file", "src/a.go", "--risk", "0", "--cwd", b462TierDir(t, bodies[key])}
		b762Golden(t, "B481ProofConfigMatchesNode", name, b762Side458(t, b458Go(t, args)))
	}
	b762NoStrays(t, "B481ProofConfigMatchesNode", names, b762Fields458)
}

// TestB762B481ProofRedGreenGolden runs real node:test files through Go's
// red-green (the product runs `node --test` for a .mjs test; skipped without
// node on PATH, as the old test was) and compares with Node's CLI goldens.
func TestB762B481ProofRedGreenGolden(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH: red-green runs `node --test` on the fixture's test file")
	}
	names := map[string]bool{}
	for _, c := range b481Cases {
		if !b762B481RedGreen[c.name] {
			continue
		}
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			home, repo := b481Fixture(t, c)
			code, stdout, stderr := b462GoCall(t, home, repo, c.args)
			got := b762SideRun(t, code, b762RunNorm(stdout, repo), b762RunNorm(stderr, repo), b462Collect(t, home, repo))
			b762Golden(t, "B481ProofRedGreenMatchesNode", name, got)
		})
	}
	if len(names) != len(b762B481RedGreen) {
		t.Fatalf("%d red-green cases found, want %d", len(names), len(b762B481RedGreen))
	}
	b762NoStrays(t, "B481ProofRedGreenMatchesNode", names, b762FieldsRun)
}

// b762LexNode is the recorded answer of Node's deleteAssertions and parseRed
// for b481Lex and b481Runs.
type b762LexNode struct {
	Lex []struct {
		Out     *string `json:"out"`
		Code    string  `json:"code"`
		Message string  `json:"message"`
	} `json:"lex"`
	Red []struct {
		Verdict string `json:"verdict"`
		Kind    string `json:"kind"`
		Failed  any    `json:"failed"`
	} `json:"red"`
}

func TestB762B481LexAndParseGolden(t *testing.T) {
	b762NoStrays(t, "B481LexAndParseMatchNode", map[string]bool{"all": true}, []string{"node"})
	data, err := os.ReadFile(filepath.Join(b762Dir, "B481LexAndParseMatchNode", "all.node"))
	if err != nil {
		t.Fatal(err)
	}
	var want b762LexNode
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("golden %s: %v", data, err)
	}
	if len(want.Lex) != len(b481Lex) || len(want.Red) != len(b481Runs) {
		t.Fatalf("golden has %d lex and %d red answers, want %d and %d", len(want.Lex), len(want.Red), len(b481Lex), len(b481Runs))
	}
	for i, src := range b481Lex {
		got, err := proofDeleteAssertions("t/x.test.mjs", []byte(src))
		w := want.Lex[i]
		if w.Out != nil {
			if err != nil || base64.StdEncoding.EncodeToString(got) != *w.Out {
				node, _ := base64.StdEncoding.DecodeString(*w.Out)
				t.Errorf("lex %d: go %q %v, node %q", i, got, err, node)
			}
			continue
		}
		var pe *proofError
		if !errors.As(err, &pe) || pe.code != w.Code || pe.message != w.Message {
			t.Errorf("lex %d: go %v, node %s %s", i, err, w.Code, w.Message)
		}
	}
	for i, r := range b481Runs {
		got := proofParseRed(&proofExecResult{stdout: r.stdout, stderr: r.stderr, code: r.code, signal: r.signal, timedOut: r.timedOut})
		w := want.Red[i]
		if got.verdict != w.Verdict || got.kind != w.Kind || !reflect.DeepEqual(got.failed, w.Failed) {
			t.Errorf("parse %d: go %+v, node %+v", i, got, w)
		}
	}
}

func TestB762B502InitRefusalsGolden(t *testing.T) {
	names := map[string]bool{}
	for _, c := range b762B502Cases {
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			side := b502Go(t, c.args, c.noHome, c.setup)
			if side.code == 0 {
				t.Fatalf("%q: a refusal exited 0", c.args)
			}
			b762Golden(t, "B502InitRefusalsMatchNode", name, b762Side502(t, side))
		})
	}
	b762NoStrays(t, "B502InitRefusalsMatchNode", names, b762Fields502)
}

func TestB762B527InitSetupGolden(t *testing.T) {
	names := map[string]bool{}
	for _, c := range b527Shapes {
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			side := b527Go(t, append([]string{"--no-interaction", "--skip-doctor"}, c.args...), c.setup)
			b762Golden(t, "B527InitSetupMatchesNode", name, b762Side527(t, side))
		})
	}
	b762NoStrays(t, "B527InitSetupMatchesNode", names, b762Fields527)
}

// b762MacTools pins the tools step to the host the B548 goldens were recorded
// on (B766): a Mac (toolOS darwin: grok and op hint "install Homebrew first",
// as no brew is on the test PATH) with Solo.app in the system app folder (a
// temp folder here, never /Applications). Linux CI otherwise prints the
// manual URL hints and "tool solo: missing".
func b762MacTools(t *testing.T) {
	t.Helper()
	savedOS, savedApps := toolOS, toolSystemApps
	toolOS, toolSystemApps = "darwin", filepath.Join(t.TempDir(), "Applications")
	t.Cleanup(func() { toolOS, toolSystemApps = savedOS, savedApps })
	if err := os.MkdirAll(filepath.Join(toolSystemApps, "Solo.app"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestB762B548InitStepsGolden(t *testing.T) {
	b762MacTools(t)
	names := map[string]bool{}
	for _, c := range b548Shapes {
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			b762Golden(t, "B548InitStepsMatchNode", name, b762Side527(t, b548Go(t, c.b548Case)))
		})
	}
	b762NoStrays(t, "B548InitStepsMatchNode", names, b762Fields527)
}

func TestB762B686InitYAMLConfigsGolden(t *testing.T) {
	names := map[string]bool{}
	for _, c := range b686Shapes {
		name := b762Names(t, names, b762Slug(c.name))
		t.Run(c.name, func(t *testing.T) {
			b762Golden(t, "B686InitYAMLConfigsMatchNode", name, b762Side527(t, b686Go(t, b762InitArgs686, c.config, c.user)))
		})
	}
	b762NoStrays(t, "B686InitYAMLConfigsMatchNode", names, b762Fields527)
}
