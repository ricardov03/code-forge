package gitx

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/ledger"
)

const Unreadable = "unreadable"

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func fileHash(full string) (string, error) {
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SortStrings follows JS Array.sort's UTF-16 code-unit ordering.
func SortStrings(files []string) {
	sort.Slice(files, func(i, j int) bool {
		a, b := utf16.Encode([]rune(files[i])), utf16.Encode([]rune(files[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

func SameStamp(a, b string) bool { return a == b && a != Unreadable }

// TreeStamp never follows the path's symlink and never returns a read error.
func TreeStamp(top, file string) string {
	full := filepath.Join(top, file)
	st, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "deleted"
		}
		return Unreadable
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err == nil {
			return "link:" + hash([]byte(target))
		}
		return Unreadable
	}
	if st.Mode().IsRegular() {
		sum, err := fileHash(full)
		if err != nil {
			return Unreadable
		}
		bit := "-"
		if st.Mode().Perm()&0111 != 0 {
			bit = "x"
		}
		return "file:" + bit + ":" + sum
	}
	if st.IsDir() {
		if _, err := os.Stat(filepath.Join(full, ".git")); err == nil {
			return repoStamp(full)
		}
		out, err := Run(top, "ls-files", "-s", "-z", "--", file)
		if err != nil {
			return Unreadable
		}
		for _, entry := range strings.Split(out, "\x00") {
			if strings.HasSuffix(entry, "\t"+file) {
				if m := gitlink.FindStringSubmatch(entry); m != nil {
					return "gitlink:" + m[1]
				}
				break
			}
		}
	}
	return Unreadable
}

var gitlink = regexp.MustCompile(`^160000 ([0-9a-f]{40,64}) \d+\t`)

func repoStamp(dir string) string {
	head, e1 := Run(dir, "rev-parse", "--verify", "HEAD")
	status, e2 := Run(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	diff, e3 := Run(dir, "diff", "HEAD", "--binary")
	untracked, e4 := Run(dir, "ls-files", "-z", "--others", "--exclude-standard")
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return Unreadable
	}
	h := sha256.New()
	io.WriteString(h, status+"\x00diff\x00"+diff)
	files := nonempty(untracked)
	SortStrings(files)
	for _, rel := range files {
		inner := filepath.Join(dir, rel)
		st, err := os.Lstat(inner)
		if err != nil {
			return Unreadable
		}
		value := "other"
		if st.Mode().IsRegular() {
			value, err = fileHash(inner)
		} else if st.Mode()&os.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(inner)
			value = "link:" + hash([]byte(target))
		}
		if err != nil {
			return Unreadable
		}
		io.WriteString(h, "\x00"+rel+"\x00"+value)
	}
	return "repo:" + strings.TrimSpace(head) + ":" + hex.EncodeToString(h.Sum(nil))
}

func nonempty(out string) []string {
	result := []string{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			result = append(result, f)
		}
	}
	return result
}
func union(a, b string, normalize bool) ([]string, error) {
	set := map[string]bool{}
	files := []string{}
	for _, f := range append(nonempty(a), nonempty(b)...) {
		if normalize {
			if path.IsAbs(f) || strings.ContainsAny(f, "\\\x00") {
				return nil, &Error{"bad-path", "a block file must be a repo-relative POSIX path"}
			}
			trailingSlash := strings.HasSuffix(f, "/")
			f = path.Clean(f)
			if f == "." || f == ".." || strings.HasPrefix(f, "../") {
				return nil, &Error{"bad-path", "a block file must stay inside the repository"}
			}
			if trailingSlash {
				f += "/"
			}
		}
		if !set[f] {
			files = append(files, f)
			set[f] = true
		}
	}
	SortStrings(files)
	return files, nil
}

var commitID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func ChangedFiles(root, base string) ([]string, error) {
	if !commitID.MatchString(base) {
		return nil, &Error{"bad-ref", "the block base must be a commit id"}
	}
	a, e1 := Run(root, "diff", "--name-only", "-z", "--no-renames", base, "--")
	b, e2 := Run(root, "ls-files", "-z", "--others", "--exclude-standard")
	if e1 != nil || e2 != nil {
		return nil, &Error{"git-failed", "git could not list the block file set"}
	}
	return union(a, b, true)
}

func TreeAtOpen(cwd, base string) (ledger.Object, error) {
	top, err := RepoTop(cwd)
	if err != nil {
		return nil, err
	}
	a, err := blockGit(top, "diff", "--name-only", "-z", "--no-renames", base, "--")
	if err != nil {
		return nil, err
	}
	b, err := blockGit(top, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	files, _ := union(a, b, false)
	out := ledger.Object{}
	for _, file := range files {
		out.Set(file, TreeStamp(top, file))
	}
	return out, nil
}
