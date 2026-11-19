package main

// The release site's data in Go (B690d): scripts/gen-site-data.mjs without Node. The tagged
// CHANGELOG.md and docs/reference/commands.md, plus aggregate proof from the local ledger, written
// as JSON.stringify(data, null, 2) + "\n" with the same keys, order and numbers.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/runstatus"
)

const siteDataUsage = "usage: cf-release site-data --tag <vX.Y.Z> [--out <file>] [--ledger-dir <dir>]"

// sdWS is JavaScript's \s inside a character class.
const sdWS = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	sdTagRe       = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	sdReleaseRe   = regexp.MustCompile(`^## \[(\d+\.\d+\.\d+)\][` + sdWS + `]*[—–-][` + sdWS + `]*(\d{4}-\d{2}-\d{2})[` + sdWS + `]*$`)
	sdHeadingRe   = regexp.MustCompile(`^###[` + sdWS + `]+([^\n\r\x{2028}\x{2029}]+?)[` + sdWS + `]*$`)
	sdBulletRe    = regexp.MustCompile(`^[-*][` + sdWS + `]+[^` + sdWS + `]`)
	sdBulletPreRe = regexp.MustCompile(`^[-*][` + sdWS + `]+`)
	sdUsageRe     = regexp.MustCompile("```(?:text)?[" + sdWS + "]*\n((?s:.)*?)\n```")
	sdCommandRe   = regexp.MustCompile(`^## [^` + sdWS + `]`)
	sdLedgerRe    = regexp.MustCompile(`^code-forge(?:\.\d+)?\.jsonl$`)
	sdLineRe      = regexp.MustCompile(`\r?\n`)
)

type siteDataOpts struct{ tag, out, ledgerDir string }

func siteDataArgs(args []string) (siteDataOpts, error) {
	home, _ := os.UserHomeDir()
	opts := siteDataOpts{ledgerDir: filepath.Join(home, ".code-forge", "ledger")}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag != "--tag" && flag != "--out" && flag != "--ledger-dir" {
			return opts, errors.New(siteDataUsage)
		}
		i++
		value := ""
		if i < len(args) {
			value = args[i]
		}
		if value == "" || strings.HasPrefix(value, "--") {
			return opts, errors.New(flag + " needs a value\n" + siteDataUsage)
		}
		switch flag {
		case "--tag":
			opts.tag = value
		case "--out":
			opts.out = value
		default:
			opts.ledgerDir = value
		}
	}
	if !sdTagRe.MatchString(opts.tag) {
		return opts, errors.New("invalid or missing tag " + jsQuote(opts.tag) + "\n" + siteDataUsage)
	}
	return opts, nil
}

// sdAtTag is the file's text at the tag, read by git with no repository-picking variables.
func sdAtTag(root, tag, file string) (string, error) {
	r := spawn(root, []string{"git", "show", "refs/tags/" + tag + ":" + file}, 15*time.Second, envWith(repoEnvKeys))
	if r.failed() || r.code != 0 || len(r.stdout) > 16*1024*1024 {
		return "", errors.New("cannot read " + file + " at tag " + tag + " (unknown tag or missing tagged file)")
	}
	return strings.ToValidUTF8(r.stdout, "�"), nil
}

func sdLines(text string) []string { return sdLineRe.Split(text, -1) }

// sdSections keeps entry Markdown, including continuation lines and nested lists.
func sdSections(lines []string) []any {
	var sections []any
	var section *jsObject
	var entry []string
	flush := func() {
		if section != nil && len(entry) > 0 {
			entries, _ := section.get("entries")
			section.set("entries", append(entries.([]any), jsTrimEnd(strings.Join(entry, "\n"))))
		}
		entry = nil
	}
	open := func(name string) {
		section = &jsObject{vals: map[string]any{}}
		section.set("name", name)
		section.set("entries", []any{})
		sections = append(sections, section)
	}
	for _, line := range lines {
		if m := sdHeadingRe.FindStringSubmatch(line); m != nil {
			flush()
			open(m[1])
		} else if sdBulletRe.MatchString(line) {
			flush()
			if section == nil {
				open("")
			}
			entry = append(entry, sdBulletPreRe.ReplaceAllString(line, ""))
		} else if len(entry) > 0 {
			entry = append(entry, line)
		}
	}
	flush()
	if sections == nil {
		return []any{}
	}
	return sections
}

func sdChangelog(text string) []any {
	lines := sdLines(text)
	type release struct {
		num [3]float64
		obj *jsObject
	}
	var releases []release
	for _, line := range lines {
		m := sdReleaseRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		section := findSection(lines, m[1])
		obj := &jsObject{vals: map[string]any{}}
		obj.set("version", m[1])
		obj.set("date", m[2])
		obj.set("sections", sdSections(lines[section.start+1:section.end]))
		var num [3]float64
		for i, part := range strings.Split(m[1], ".") {
			num[i], _ = strconv.ParseFloat(part, 64)
		}
		releases = append(releases, release{num, obj})
	}
	sort.SliceStable(releases, func(i, j int) bool {
		a, b := releases[i].num, releases[j].num
		for k := 0; k < 3; k++ {
			if a[k] != b[k] {
				return a[k] > b[k]
			}
		}
		return false
	})
	out := []any{}
	for _, r := range releases {
		out = append(out, r.obj)
	}
	return out
}

func sdUsage(body string) string {
	if m := sdUsageRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// sdSplitHeadings is body.split(/^### /m) for text whose only line breaks are "\n".
func sdSplitHeadings(body string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(body)-4; i++ {
		if (i == 0 || body[i-1] == '\n') && body[i:i+4] == "### " {
			parts = append(parts, body[start:i])
			start = i + 4
			i += 3
		}
	}
	return append(parts, body[start:])
}

func sdCommands(text string) []any {
	lines := sdLines(text)
	var headings []int
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if !fenced && sdCommandRe.MatchString(line) {
			headings = append(headings, i)
		}
	}
	out := []any{}
	for index, start := range headings {
		name := jsTrim(lines[start][3:])
		end := len(lines)
		if index+1 < len(headings) {
			end = headings[index+1]
		}
		parts := sdSplitHeadings(strings.Join(lines[start+1:end], "\n"))
		main := parts[0]
		subs := []any{}
		for _, part := range parts[1:] {
			heading, rest := "", part
			if nl := strings.IndexByte(part, '\n'); nl >= 0 {
				heading, rest = part[:nl], part[nl+1:]
			} else if part != "" {
				// part.slice(0, -1) with no newline: the last character goes.
				_, size := lastRune(part)
				heading = part[:len(part)-size]
			}
			heading = jsTrim(heading)
			if strings.HasPrefix(heading, name+" ") {
				heading = heading[len(name)+1:]
			}
			sub := &jsObject{vals: map[string]any{}}
			sub.set("name", heading)
			sub.set("usage", sdUsage(rest))
			subs = append(subs, sub)
		}
		description, _, _ := strings.Cut(main, "```")
		cmd := &jsObject{vals: map[string]any{}}
		cmd.set("name", name)
		cmd.set("description", jsTrim(description))
		cmd.set("usage", sdUsage(main))
		cmd.set("subcommands", subs)
		out = append(out, cmd)
	}
	return out
}

func lastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i]&0xC0 != 0x80 {
			r := []rune(s[i:])
			return r[0], len(s) - i
		}
	}
	return 0, 1
}

// sdJSONValue is JSON.parse's value with numbers as float64 (an overflow is ±Infinity).
func sdJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			list := []any{}
			for dec.More() {
				v, err := sdJSONValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		obj := map[string]any{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := sdJSONValue(dec)
			if err != nil {
				return nil, err
			}
			obj[key.(string)] = v
		}
		_, err := dec.Token()
		return obj, err
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil
	}
	return tok, nil
}

func sdParseRow(line string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	v, err := sdJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

// sdRound2 is Math.round(n * 100) / 100.
func sdRound2(n float64) float64 {
	x := float64(n * 100)
	floor := math.Floor(x)
	if x-floor >= .5 {
		floor++
	}
	return floor / 100
}

func sdProof(dir, date string) (*jsObject, error) {
	bad := errors.New("cannot read ledger directory " + dir)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, bad
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, bad
	}
	var files []string
	for _, e := range entries {
		if sdLedgerRe.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	rotation := func(file string) float64 {
		if file == "code-forge.jsonl" {
			return -1
		}
		n, _ := strconv.ParseFloat(strings.Split(file, ".")[1], 64)
		return n
	}
	sort.SliceStable(files, func(i, j int) bool {
		ri, rj := rotation(files[i]), rotation(files[j])
		if ri != rj {
			return ri > rj
		}
		return files[i] < files[j]
	})
	var rows []map[string]any
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return nil, fsErr("open", filepath.Join(dir, file), err)
		}
		for i, line := range sdLines(strings.ToValidUTF8(string(data), "�")) {
			if jsTrim(line) == "" {
				continue
			}
			v, err := sdParseRow(line)
			if err != nil {
				return nil, errors.New("invalid JSON in ledger file " + file + ", line " + strconv.Itoa(i+1))
			}
			row, ok := v.(map[string]any)
			if !ok {
				return nil, errors.New("invalid row in ledger file " + file + ", line " + strconv.Itoa(i+1))
			}
			rows = append(rows, row)
		}
	}
	runs := map[string]bool{}
	blocks := map[[2]string]bool{}
	sum := 0.0
	for _, row := range rows {
		run, _ := row["run"].(string)
		if run != "" {
			runs[run] = true
		}
		block, _ := row["block"].(string)
		if row["event"] == "block.close" && row["status"] == "complete" && run != "" && block != "" {
			blocks[[2]string{run, block}] = true
		}
		// buildSpend(rows).total.usd: spend rows, never shadow; an unknown price counts 0.
		if (row["event"] == "session" || row["event"] == "session.background") && row["source"] != "shadow" {
			if cost, known := runstatus.RowUSD(row); known {
				sum += cost
			}
		}
	}
	proof := &jsObject{vals: map[string]any{}}
	proof.set("runs", jsNum(len(runs)))
	proof.set("blocks", jsNum(len(blocks)))
	proof.set("spend", jsNum(sdRound2(runstatus.RoundUSD(sum))))
	proof.set("date", date)
	sdpSamples(proof, rows)
	return proof, nil
}

// siteDataJSON is the site data for the tag, as the Node script printed it.
func siteDataJSON(root, tag, ledgerDir string) (string, error) {
	changelogText, err := sdAtTag(root, tag, "CHANGELOG.md")
	if err != nil {
		return "", err
	}
	changelog := sdChangelog(changelogText)
	var release *jsObject
	for _, entry := range changelog {
		if v, _ := entry.(*jsObject).get("version"); v == tag[1:] {
			release = entry.(*jsObject)
			break
		}
	}
	if release == nil {
		return "", errors.New("no dated CHANGELOG release for tag " + tag)
	}
	commandsText, err := sdAtTag(root, tag, "docs/reference/commands.md")
	if err != nil {
		return "", err
	}
	date, _ := release.get("date")
	proof, err := sdProof(ledgerDir, date.(string))
	if err != nil {
		return "", err
	}
	version, _ := release.get("version")
	data := &jsObject{vals: map[string]any{}}
	data.set("version", version)
	data.set("date", date)
	data.set("changelog", changelog)
	data.set("commands", sdCommands(commandsText))
	data.set("proof", proof)
	return jsStringify(data, "  ") + "\n", nil
}

func siteDataMain(a *app, argv []string, root string) int {
	opts, err := siteDataArgs(argv)
	if err == nil {
		var text string
		if text, err = siteDataJSON(root, opts.tag, opts.ledgerDir); err == nil {
			if opts.out == "" {
				a.out(text)
				return 0
			}
			if err = os.WriteFile(resolvePath(root, opts.out), []byte(text), 0o644); err == nil {
				return 0
			}
		}
	}
	var buf bytes.Buffer
	buf.WriteString("site-data: " + err.Error() + "\n")
	a.err(buf.String())
	return 1
}
