package main

import (
	"runtime"
	"strings"
	"unicode/utf16"
)

// B568: glob path claims (src/session/facts.mjs globRule and the glob hint of
// buildFactsPacket). A VERIFIED glob row is checked with Node's
// path.matchesGlob (minimatch: nocase on macOS and Windows, nocaseMagicOnly,
// windowsPathsNoEscape, nonegate, nocomment, optimizationLevel 2).
// B577: fcMatchesGlob runs the minimatch port (facts_minimatch.go) and
// braceExpand (facts_brace.go); only the shapes fcMM marks unmodelled hand
// over before the run.

// fcGlobNocase is minimatch's nocase default for path.matchesGlob.
var fcGlobNocase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// fcGlobUnmodelled: a glob fcMatchesGlob does not model.
func fcGlobUnmodelled(token string) bool { return fcNewMM(token, fcGlobNocase).unmodelled }

// fcPosixDirname is path.posix.dirname.
func fcPosixDirname(p string) string {
	if p == "" {
		return "."
	}
	abs := p[0] == '/'
	end := len(p)
	for end > 1 && p[end-1] == '/' {
		end--
	}
	i := strings.LastIndex(p[:end], "/")
	if i < 0 {
		return "."
	}
	for i > 0 && p[i-1] == '/' {
		i--
	}
	if i == 0 {
		if abs {
			return "/"
		}
		return "."
	}
	return p[:i]
}

// fcPosixBasename is path.posix.basename.
func fcPosixBasename(p string) string {
	end := len(p)
	for end > 0 && p[end-1] == '/' {
		end--
	}
	if end == 0 {
		return ""
	}
	return p[strings.LastIndex(p[:end], "/")+1 : end]
}

// fcShellQuote is shellQuote.
func fcShellQuote(value string, always bool) string {
	if !always && value != "" && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-") == "" {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// fcGlobHint is buildFactsPacket's check hint for a glob path claim.
func fcGlobHint(token string) string {
	dir := fcPosixDirname(token)
	if fcHasGlob(dir) {
		return " [check with ls -d " + fcShellQuote(token, false) + "]"
	}
	return " [check with find " + fcShellQuote(dir, false) + " -name " + fcShellQuote(fcPosixBasename(token), true) + "]"
}

const fcGlobProofMessage = "a glob needs find <literal dir> -name <quoted pattern> or ls -d <exact glob>; not counted"

// fcFindNameQuoted is /-name (['"])[^'"]+\1$/.
func fcFindNameQuoted(command string) bool {
	if command == "" {
		return false
	}
	q := command[len(command)-1]
	if q != '\'' && q != '"' {
		return false
	}
	body := command[:len(command)-1]
	open := strings.LastIndexAny(body, `'"`)
	if open < 0 || body[open] != q || open == len(body)-1 {
		return false
	}
	return strings.HasSuffix(body[:open], "-name ")
}

// fcGlobRule is globRule.
func fcGlobRule(row fcFact, claim fcClaim) string {
	token := claim.token
	if claim.hasCheckPath {
		token = claim.checkPath
	}
	split := fcSplitCommandGlob(row.command, true)
	var argv []string
	if len(split.segments) > 0 {
		argv = split.segments[0]
	}
	dir := fcPosixDirname(token)
	ls := len(argv) == 3 && argv[0] == "ls" && argv[1] == "-d" && argv[2] == token
	find := !fcHasGlob(dir) && len(argv) == 4 && argv[0] == "find" && argv[1] == dir && argv[2] == "-name" &&
		argv[3] == fcPosixBasename(token) && fcReadOnlyViolation(row.command) == "" && fcFindNameQuoted(row.command)
	if split.refused != "" || len(split.separators) > 0 || len(split.segments) != 1 || (!ls && !find) || fcSegmentViolation(argv) != "" {
		return fcGlobProofMessage
	}
	if row.tag == "NOT-FOUND" {
		if strings.Contains(strings.ToLower(row.excerpt), "no such file") {
			return ""
		}
		return "a NOT-FOUND glob needs No such file output; not counted"
	}
	pattern := fcRelPosix(token)
	for _, line := range fcGlobLines(row.excerpt) {
		if fcMatchesGlob(fcRelPosix(fcTrim(line)), pattern, fcGlobNocase) {
			return ""
		}
	}
	return "VERIFIED glob without a matching output path; not counted"
}

// fcGlobLines is text.split(/\r?\n/).
func fcGlobLines(text string) []string {
	parts := strings.Split(text, "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

func fcUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// fcMatchesGlob is path.matchesGlob(file, pattern).
func fcMatchesGlob(file, pattern string, nocase bool) bool {
	return fcNewMM(pattern, nocase).match(file)
}
