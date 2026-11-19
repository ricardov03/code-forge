// Package releaseaudit is the major-release audit gate (B802): the report
// format `code-forge audit commands` writes, and the check that
// `npm run release -- major` (cf-release) and `code-forge release check`
// (B799) run before a new major version. A major release needs a committed
// docs/audits/audit-<version>.md made by the audit tool with no open problem
// of severity high: each high problem has a block id whose commit has landed
// (a commit subject on HEAD starts with "<block>:").
package releaseaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Marker is the line that marks a report written by `code-forge audit commands`.
const Marker = "<!-- code-forge-audit: commands v1 -->"

// Severity of a problem row; a passed check has none.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Result of one check.
const (
	OK      = "ok"
	Problem = "problem"
)

// Row is one check of one command.
type Row struct {
	Command  string `json:"command"`
	Check    string `json:"check"`
	Result   string `json:"result"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Block    string `json:"block"`
}

// Report is one audit run.
type Report struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Rows    []Row  `json:"rows"`
}

// Path is the repository path of the audit report of one version.
func Path(version string) string { return "docs/audits/audit-" + version + ".md" }

// Command is the command that writes the audit report of one version.
func Command(version string) string {
	return "code-forge audit commands --out " + Path(version)
}

var versionRe = regexp.MustCompile(`^v?(\d+)\.\d+\.\d+$`)

// IsMajor is true when next starts a new major version after current
// (0.25.1 → 1.0.0, 1.4.2 → 2.0.0). Patch and minor bumps are not.
func IsMajor(current, next string) bool {
	c := versionRe.FindStringSubmatch(current)
	n := versionRe.FindStringSubmatch(next)
	if c == nil || n == nil {
		return false
	}
	cm, _ := strconv.Atoi(c[1])
	nm, _ := strconv.Atoi(n[1])
	return nm > cm
}

// Counts are the problem rows per severity.
func (r Report) Counts() map[string]int {
	out := map[string]int{High: 0, Medium: 0, Low: 0}
	for _, row := range r.Rows {
		if row.Result == Problem {
			out[row.Severity]++
		}
	}
	return out
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

// Render is the Markdown report.
func Render(r Report) string {
	counts := r.Counts()
	commands := map[string]bool{}
	for _, row := range r.Rows {
		commands[row.Command] = true
	}
	var b strings.Builder
	b.WriteString("# code-forge command audit\n\n" + Marker + "\n\n")
	b.WriteString("- Version audited: " + r.Version + "\n")
	b.WriteString("- Commit: " + r.Commit + "\n")
	b.WriteString("- Date: " + r.Date + "\n")
	b.WriteString("- Commands: " + strconv.Itoa(len(commands)) + "\n")
	b.WriteString("- Checks: " + strconv.Itoa(len(r.Rows)) + "\n")
	b.WriteString("- Problems: high " + strconv.Itoa(counts[High]) + ", medium " + strconv.Itoa(counts[Medium]) +
		", low " + strconv.Itoa(counts[Low]) + "\n\n")
	b.WriteString("A major release (`npm run release -- major`) needs this file committed with no open high problem: " +
		"give each high problem a block id in the Block column and land that block (a commit subject starting `<block>:`).\n\n")
	b.WriteString("| Command | Check | Result | Severity | Detail | Block |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, row := range r.Rows {
		b.WriteString("| `" + cell(row.Command) + "` | " + cell(row.Check) + " | " + cell(row.Result) + " | " +
			cell(row.Severity) + " | " + cell(row.Detail) + " | " + cell(row.Block) + " |\n")
	}
	return b.String()
}

// splitRow splits one Markdown table line on its unescaped pipes.
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// Parse reads the rows of a report. ok is false when the text has no Marker
// or no table (not a report the audit tool wrote).
func Parse(text string) (rows []Row, ok bool) {
	if !strings.Contains(text, Marker) {
		return nil, false
	}
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if !inTable {
			if strings.HasPrefix(trim, "|") {
				head := splitRow(trim)
				if len(head) >= 6 && strings.EqualFold(head[0], "Command") && strings.EqualFold(head[3], "Severity") {
					inTable = true
				}
			}
			continue
		}
		if !strings.HasPrefix(trim, "|") {
			if trim == "" {
				continue
			}
			break
		}
		c := splitRow(trim)
		if len(c) < 6 || strings.HasPrefix(c[0], "---") {
			continue
		}
		rows = append(rows, Row{Command: strings.Trim(c[0], "`"), Check: c[1], Result: strings.ToLower(c[2]),
			Severity: strings.ToLower(c[3]), Detail: c[4], Block: c[5]})
	}
	return rows, inTable
}

// OpenHigh lists the high problem rows that are still open: no block id,
// or a block id landed() does not know.
func OpenHigh(rows []Row, landed func(block string) bool) []Row {
	var open []Row
	for _, row := range rows {
		if row.Result != Problem || row.Severity != High {
			continue
		}
		if row.Block == "" || !landed(row.Block) {
			open = append(open, row)
		}
	}
	return open
}

// Repo is what Check reads from a repository.
type Repo interface {
	// Committed returns the file's text at HEAD (ok false when HEAD has no such file).
	Committed(path string) (string, bool)
	// OnDisk is true when the work tree has the file.
	OnDisk(path string) bool
	// Landed is true when a commit on HEAD starts with "<block>:".
	Landed(block string) bool
}

// Check is the major-release gate: "" when version's audit report is
// committed and has no open high problem, else the failure with its fix.
func Check(repo Repo, version string) string {
	path := Path(version)
	run := Command(version)
	text, ok := repo.Committed(path)
	if !ok {
		if repo.OnDisk(path) {
			return "a major release needs the command audit " + path + " committed: commit it (git add " + path + " && git commit), then release again"
		}
		return "a major release needs the command audit " + path + ": run `" + run + "`, give each high problem a landed block, commit the file, then release again"
	}
	rows, ok := Parse(text)
	if !ok {
		return path + " was not written by the audit tool (no audit marker or table): run `" + run + "` and commit it"
	}
	open := OpenHigh(rows, repo.Landed)
	if len(open) == 0 {
		return ""
	}
	names := make([]string, 0, len(open))
	for i, row := range open {
		if i == 5 {
			names = append(names, "…")
			break
		}
		state := "no block"
		if row.Block != "" {
			state = row.Block + " not landed"
		}
		names = append(names, row.Command+" "+row.Check+" ("+state+")")
	}
	return path + " has " + strconv.Itoa(len(open)) + " open high problem(s): " + strings.Join(names, ", ") +
		"; fix them, put each block id in the Block column and land it, or run `" + run + "` again"
}

// GitRepo reads a repository with the git on PATH.
type GitRepo struct {
	Root     string
	subjects map[string]bool
}

func (g *GitRepo) git(args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Root
	out, err := cmd.Output()
	return string(out), err == nil
}

// Committed implements Repo.
func (g *GitRepo) Committed(path string) (string, bool) {
	return g.git("show", "HEAD:"+path)
}

// OnDisk implements Repo.
func (g *GitRepo) OnDisk(path string) bool {
	info, err := os.Stat(filepath.Join(g.Root, filepath.FromSlash(path)))
	return err == nil && !info.IsDir()
}

// Landed implements Repo: a commit subject on HEAD starts with "<block>:".
func (g *GitRepo) Landed(block string) bool {
	if g.subjects == nil {
		g.subjects = map[string]bool{}
		out, _ := g.git("log", "--format=%s", "HEAD")
		for _, subject := range strings.Split(out, "\n") {
			if id, _, found := strings.Cut(subject, ":"); found && id != "" && !strings.ContainsAny(id, " \t") {
				g.subjects[id] = true
			}
		}
	}
	return g.subjects[strings.TrimSpace(block)]
}

// CheckRoot is Check on the git repository at root.
func CheckRoot(root, version string) string {
	return Check(&GitRepo{Root: root}, version)
}
