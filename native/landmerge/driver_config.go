package landmerge

import (
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B990b: code-forge sets merge.union.driver itself instead of a manual
// `git config` step. The line names this install's binary and falls back to
// git's own union merge when that binary is gone or does not know
// `land merge-driver` (an older release, a removed dev build), so the line
// never breaks a merge: at worst a CHANGELOG keeps a repeated heading, as
// before B990. The repository's config file is shared by all its linked
// worktrees, so one line serves every lane of the clone.

// unionDriverMark is in every merge.union.driver line code-forge writes, and
// in the manual line docs/releasing.md gave before B990b.
const unionDriverMark = "land merge-driver %O %A %B %P"

// UnionDriverLine is the merge.union.driver value for the binary exe.
func UnionDriverLine(exe string) string {
	return shellQuote(exe) + " " + unionDriverMark + " 2>/dev/null || git merge-file --union %A %O %B"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// EnsureUnionDriver sets merge.union.driver in the repository at dir to
// UnionDriverLine(exe) when its CHANGELOG.md has the merge=union attribute,
// the binary is a code-forge one (never a go test binary) and the current
// value is unset or one code-forge wrote. A driver of the owner's own is
// kept. changed is true when it wrote the line.
func EnsureUnionDriver(dir, exe string) (changed bool, err error) {
	base := filepath.Base(exe)
	if exe == "" || !filepath.IsAbs(exe) || !strings.HasPrefix(base, "code-forge") || strings.HasSuffix(base, ".test") {
		return false, nil
	}
	attr, err := gitx.Run(dir, "check-attr", "merge", "--", "CHANGELOG.md")
	if err != nil || strings.TrimSpace(attr) != "CHANGELOG.md: merge: union" {
		return false, nil
	}
	want := UnionDriverLine(exe)
	current, _ := gitx.Run(dir, "config", "--local", "--get", "merge.union.driver")
	current = strings.TrimRight(current, "\n")
	if current == want {
		return false, nil
	}
	if current != "" && !strings.Contains(current, unionDriverMark) {
		return false, nil
	}
	if _, err := gitx.Run(dir, "config", "--local", "merge.union.driver", want); err != nil {
		return false, err
	}
	return true, nil
}
