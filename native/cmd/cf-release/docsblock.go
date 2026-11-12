package main

// `npm run docs-block -- <brief|stamp>`: the docs block input and its completion stamp before a
// release. Port of scripts/docs-block.mjs (B570).

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const docsStamp = "docs/release-docs.json"

func dbGit(root string, args ...string) result {
	return spawn(root, append([]string{"git"}, args...), 60*time.Second, gitChildEnv())
}

func dbGitOk(root string, args ...string) (string, error) {
	r := dbGit(root, args...)
	if r.code != 0 {
		return "", errors.New("git " + strings.Join(args, " ") + " failed: " + jsTrim(or(r.stderr, r.errMsg)))
	}
	return jsTrimEnd(r.stdout), nil
}

// latestReleaseTag is the nearest v* tag reachable from HEAD, lightweight tags included.
func latestReleaseTag(root string) (string, error) {
	tags, err := dbGitOk(root, "tag", "--merged", "HEAD", "--list", "v*")
	if err != nil || tags == "" {
		return "", err
	}
	return dbGitOk(root, "describe", "--tags", "--match", "v*", "--abbrev=0", "HEAD")
}

func filesWithExtension(root, dir, ext string) []string {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		file := dir + "/" + e.Name()
		if e.IsDir() {
			out = append(out, filesWithExtension(root, file, ext)...)
		} else if e.Type().IsRegular() && strings.HasSuffix(file, ext) {
			out = append(out, file)
		}
	}
	sort.Strings(out)
	return out
}

func (a *app) section(title string, lines []string) {
	body := "(none)"
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}
	a.out(title + ":\n" + body + "\n\n")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func bullets(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "- " + s
	}
	return out
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (a *app) brief(root, tag string) error {
	since := tag
	if since == "" {
		since = "repository start"
	}
	shown := tag
	if shown == "" {
		shown = "(none)"
	}
	a.out("Latest release tag: " + shown + "\n\n")
	rng := "HEAD"
	if tag != "" {
		rng = tag + "..HEAD"
	}
	log, err := dbGitOk(root, "log", "--reverse", "--format=- %h %s", rng)
	if err != nil {
		return err
	}
	a.section("Commits since "+since, nonEmptyLines(log))
	var changed string
	if tag != "" {
		changed, err = dbGitOk(root, "diff", "--name-only", "-z", tag, "HEAD")
	} else {
		changed, err = dbGitOk(root, "ls-tree", "-r", "--name-only", "-z", "HEAD")
	}
	if err != nil {
		return err
	}
	a.section("Files changed since "+since, bullets(splitNul(changed)))
	data, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return errors.New("ENOENT: no such file or directory, open '" + filepath.Join(root, "CHANGELOG.md") + "'")
	}
	lines := strings.Split(string(data), "\n")
	var unreleased []string
	if sec := findUnreleased(lines); sec != nil {
		unreleased = []string{jsTrim(strings.Join(lines[sec.start+1:sec.end], "\n"))}
	}
	a.section("CHANGELOG [Unreleased]", unreleased)
	var docs []string
	if exists(filepath.Join(root, "README.md")) {
		docs = append(docs, "README.md")
	}
	docs = append(docs, filesWithExtension(root, "docs", ".md")...)
	a.section("Docs", bullets(docs))
	a.section("Generated-doc checks", []string{"- node scripts/gen-config-doc.mjs --check", "- node scripts/gen-commands-doc.mjs --check"})
	siteDir := os.Getenv("CODE_FORGE_SITE_DIR")
	if siteDir == "" {
		siteDir = "../code-forge-site"
	}
	site := resolvePath(root, siteDir)
	if !exists(site) {
		a.section("Site pages a person must check", []string{"Site folder absent: " + site})
		return nil
	}
	a.section("Site pages a person must check", bullets(filesWithExtension(site, "src/pages", ".astro")))
	return nil
}

// resolvePath is path.resolve(base, p).
func resolvePath(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func docsBlockMain(a *app, argv []string, root string) int {
	if len(argv) != 1 || (argv[0] != "brief" && argv[0] != "stamp") {
		a.err("usage: npm run docs-block -- <brief|stamp>\n")
		return 2
	}
	err := func() error {
		if argv[0] == "stamp" {
			status, err := dbGitOk(root, "status", "--porcelain", "--untracked-files=normal")
			if err != nil {
				return err
			}
			if status != "" {
				return errors.New("the working tree is not clean; commit the docs before stamping")
			}
		}
		tag, err := latestReleaseTag(root)
		if err != nil {
			return err
		}
		if argv[0] == "brief" {
			return a.brief(root, tag)
		}
		through, err := dbGitOk(root, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			return err
		}
		text := "{\n  \"through\": " + jsQuote(through) + ",\n  \"tag\": " + jsQuote(tag) + "\n}\n"
		if err := os.WriteFile(filepath.Join(root, docsStamp), []byte(text), 0o644); err != nil {
			return err
		}
		a.out(docsStamp + "\n")
		return nil
	}()
	if err != nil {
		a.err("REFUSED: " + err.Error() + "\n")
		return 1
	}
	return 0
}
