package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B450: a detected formatter (pint, prettier) runs on the existing supported
// Git files only, in argv batches under a byte budget, as Node's runGates does
// (formatFiles, formatBatches and outputTail in src/gates/run.mjs).

// FormatArgvBudgetBytes leaves room below typical ARG_MAX limits for the
// inherited environment and spawn overhead.
var FormatArgvBudgetBytes = 100 * 1024

var (
	prettierFile = regexp.MustCompile(`(?i)\.(?:[cm]?[jt]sx?|json5?|jsonc|css|scss|less|html?|vue|md|mdx|markdown|ya?ml|graphql|gql|hbs|handlebars|mustache|mjml)$`)
	pintFile     = regexp.MustCompile(`(?i)\.php$`)
	lineBreak    = regexp.MustCompile(`\r?\n`)
)

// formatGit runs git with no GIT_* variable, a 30 s limit, and the given exit
// codes as success. failure is Node's `stderr || error || signal || 'git failed'`.
func formatGit(cwd, input string, okCodes []int, args ...string) (stdout string, failure string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := childreg.Run(cmd)
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", err.Error()
		}
		code = exit.ExitCode()
		if code < 0 {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return "", signalName(status.Signal())
			}
		}
	}
	for _, ok := range okCodes {
		if code == ok {
			return out.String(), ""
		}
	}
	if errOut.Len() > 0 {
		return "", errOut.String()
	}
	return "", "git failed"
}

// formatFiles lists the files a detected formatter checks, as "./<path>": Git
// files that are not ignored, supported by the formatter, regular and not
// behind a link. A changed scope keeps only its changed files.
func formatFiles(cwd, formatter string, s Scope) ([]string, error) {
	changed := s.Kind == "changed"
	args := []string{"ls-files", "-z", "--cached"}
	if changed {
		args = append(args, "--others")
	}
	args = append(args, "--exclude-standard")
	listed, failure := formatGit(cwd, "", []int{0}, args...)
	if failure != "" {
		return nil, fmt.Errorf("format: cannot list Git files: %s", failure)
	}
	wanted := map[string]bool{}
	for _, f := range s.Changed {
		wanted[f] = true
	}
	seen := map[string]bool{}
	files := []string{}
	for _, f := range strings.Split(listed, "\x00") {
		if f == "" || seen[f] || (changed && !wanted[f]) {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return []string{}, nil
	}
	ignored, failure := formatGit(cwd, strings.Join(files, "\x00")+"\x00", []int{0, 1}, "check-ignore", "--no-index", "-z", "--stdin")
	if failure != "" {
		return nil, fmt.Errorf("format: cannot check Git ignores: %s", failure)
	}
	excluded := map[string]bool{}
	for _, f := range strings.Split(ignored, "\x00") {
		excluded[f] = true
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	selected := []string{}
	for _, f := range files {
		if excluded[f] {
			continue
		}
		var supported bool
		if formatter == "pint" {
			supported = pintFile.MatchString(f)
		} else {
			base := filepath.Base(f)
			supported = prettierFile.MatchString(f) || base == ".prettierrc" || base == ".babelrc" || base == ".eslintrc" || base == ".stylelintrc"
		}
		if !supported {
			continue
		}
		absolute := filepath.Join(root, f)
		st, err := os.Lstat(absolute)
		if err == nil && st.Mode().IsRegular() {
			var real string
			real, err = filepath.EvalSymlinks(absolute)
			if err == nil && real != absolute {
				continue
			}
		} else if err == nil {
			continue
		}
		if err != nil {
			if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return nil, err
		}
		// Leading dashes cannot become options, and paths are not globs.
		selected = append(selected, "./"+f)
	}
	return selected, nil
}

// formatBatches repeats the command in every batch and counts UTF-8 bytes
// plus one terminator per item against budget.
func formatBatches(prefix, files []string, budget int) ([][]string, error) {
	prefixBytes := 0
	for _, a := range prefix {
		prefixBytes += len(a) + 1
	}
	batches := [][]string{}
	batch := append([]string{}, prefix...)
	size := prefixBytes
	for _, f := range files {
		n := len(f) + 1
		if prefixBytes+n > budget {
			return nil, fmt.Errorf("format: command and file exceed argv byte budget: %s", f)
		}
		if size+n > budget {
			batches = append(batches, batch)
			batch = append([]string{}, prefix...)
			size = prefixBytes
		}
		batch = append(batch, f)
		size += n
	}
	return append(batches, batch), nil
}

// outputTail is a stream's last 10 lines with one newline, or "".
func outputTail(stream string) string {
	lines := lineBreak.Split(trim(stream), -1)
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	tail := strings.Join(lines, "\n")
	if tail == "" {
		return ""
	}
	return tail + "\n"
}

// runBatches runs each batch and keeps the first timeout, else the first
// failure, else the first outcome, with every batch's output joined; with
// more than one batch each output is its tail.
func runBatches(ctx context.Context, batches [][]string, c *Command, cwd, run string, timeout float64) ledger.Object {
	outcomes := []ledger.Object{}
	for _, batch := range batches {
		o := execute(ctx, batch, c.Env, cwd, run, timeout, c.FailOnStdout)
		if len(batches) > 1 {
			o.Set("stdout", outputTail(fmt.Sprint(o.Get("stdout"))))
			o.Set("stderr", outputTail(fmt.Sprint(o.Get("stderr"))))
		}
		outcomes = append(outcomes, o)
		if ctx.Err() != nil {
			break
		}
	}
	chosen := outcomes[0]
	found := false
	for _, o := range outcomes {
		if o.Get("timed_out") == true {
			chosen, found = o, true
			break
		}
	}
	if !found {
		for _, o := range outcomes {
			if o.Get("ok") != true {
				chosen = o
				break
			}
		}
	}
	out := chosen.Clone()
	var stdout, stderr strings.Builder
	for _, o := range outcomes {
		stdout.WriteString(fmt.Sprint(o.Get("stdout")))
		stderr.WriteString(fmt.Sprint(o.Get("stderr")))
	}
	out.Set("stdout", stdout.String())
	out.Set("stderr", stderr.String())
	return out
}
