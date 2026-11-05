package gates

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]")
var tapFailure = regexp.MustCompile(`^\s*not ok\s+\d+\s+- (.*)`)
var tapEnd = regexp.MustCompile(`^\s*(?:\.\.\.|(?:not )?ok\s+\d+\b|# Subtest:)`)
var location = regexp.MustCompile(`^\s*location: ['"]?(.*?)['"]?\s*$`)
var fileExtension = regexp.MustCompile(`\.(?:[cm]?[jt]sx?|php|py|go|rs|swift)(?:\s|$)`)
var failureHeaders = []*regexp.Regexp{
	regexp.MustCompile(`^\s*test at (.+)`),
	regexp.MustCompile(`^\s*(?:FAIL|FAILED)\s+(.+?)(?:::|\s+[-–]|$)`),
	regexp.MustCompile(`^\s*✖ (.+?\.(?:[cm]?[jt]sx?|php|py|go|rs|swift))(?:\s+\(|$)`),
}

func failedTestFiles(output, cwd string) []string {
	cwd, _ = filepath.Abs(cwd)
	lines := strings.Split(strings.ReplaceAll(ansi.ReplaceAllString(output, ""), "\r\n", "\n"), "\n")
	candidates := []string{}
	for i, line := range lines {
		if tap := tapFailure.FindStringSubmatch(line); tap != nil {
			if regexp.MustCompile(`(?i)\s+#\s*(?:TODO|SKIP)\b`).MatchString(tap[1]) {
				continue
			}
			loc := ""
			parent := false
			for j := i + 1; j < len(lines) && !tapEnd.MatchString(lines[j]); j++ {
				if m := location.FindStringSubmatch(lines[j]); m != nil {
					loc = m[1]
				}
				if regexp.MustCompile(`\bfailureType: ['"]?subtestsFailed\b`).MatchString(lines[j]) {
					parent = true
				}
			}
			if loc != "" {
				candidates = append(candidates, loc)
			} else if fileExtension.MatchString(tap[1]) {
				candidates = append(candidates, tap[1])
			} else if !parent {
				return nil
			}
		}
		for _, re := range failureHeaders {
			if m := re.FindStringSubmatch(line); m != nil {
				candidates = append(candidates, m[1])
				break
			}
		}
	}
	files := []string{}
	for _, candidate := range candidates {
		candidate = regexp.MustCompile(`\s+\([\d.]+\s*m?s\)$`).ReplaceAllString(candidate, "")
		candidate = regexp.MustCompile(`:\d+(?::\d+)?$`).ReplaceAllString(candidate, "")
		if strings.HasPrefix(candidate, "file:") {
			u, e := url.Parse(candidate)
			if e != nil || u.Host != "" && u.Host != "localhost" || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
				return nil
			}
			candidate = u.Path
		}
		absolute := candidate
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(cwd, candidate)
		}
		file, e := filepath.Rel(cwd, absolute)
		if e != nil || file == "." || strings.HasPrefix(file, "../") || filepath.IsAbs(file) || strings.HasPrefix(file, "-") {
			return nil
		}
		st, e := os.Lstat(filepath.Join(cwd, file))
		if e != nil || !st.Mode().IsRegular() {
			return nil
		}
		files = append(files, file)
	}
	return unique(files)
}

func failedFileArgv(original []string, file string) []string {
	node := (filepath.Base(original[0]) == "node" || filepath.Base(original[0]) == "node.exe") && Has(original, "--test")
	expand := func() []string {
		if !Has(original, "{test_files}") {
			return nil
		}
		p, e := prepare(Plan{Commands: map[string]*Command{"test": {Argv: original}}}, Scope{Kind: "related", Files: []string{file}})
		if e != nil {
			return nil
		}
		return p[0].argv
	}
	if !node {
		return expand()
	}
	valued := []string{"--import", "--require", "-r", "--loader", "--experimental-loader", "--conditions", "-C", "--test-reporter", "--test-reporter-destination", "--test-name-pattern", "--test-skip-pattern", "--test-shard", "--test-timeout", "--test-concurrency", "--test-isolation", "--experimental-test-isolation", "--env-file", "--env-file-if-exists", "--disable-warning"}
	flags := []string{"--test", "--experimental-test-module-mocks", "--experimental-strip-types", "--experimental-transform-types", "--enable-source-maps", "--no-warnings", "--trace-warnings", "--test-force-exit", "--test-only", "--experimental-test-coverage", "--test-update-snapshots"}
	argv := []string{original[0], "--test-concurrency=1"}
	testIndex := 0
	for i, a := range original {
		if a == "--test" {
			testIndex = i
			break
		}
	}
	for i := 1; i < len(original); i++ {
		a := original[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--test-concurrency=") || strings.HasPrefix(a, "--test-shard=") {
			continue
		}
		if Has(valued, a) {
			if i+1 >= len(original) {
				return nil
			}
			i++
			if a != "--test-concurrency" && a != "--test-shard" {
				argv = append(argv, a, original[i])
			}
		} else if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && !Has(flags, a) {
				return nil
			}
			argv = append(argv, a)
		} else if i < testIndex {
			return expand()
		}
	}
	return append(argv, file)
}
