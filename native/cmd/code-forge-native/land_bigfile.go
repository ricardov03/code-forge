package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B986: a `go build` in native/ once wrote the 24 MB native/code-forge-native
// binary into a commit. The built binaries are gitignored, and land refuses to
// push (CI branch or main) or merge commits that add a file over
// land.max_file_mb (default 5 MB, the B606 size limit of the main npm
// package; 0 turns the check off).

const landBigFileDefaultMB = 5

// landBigFileLimit is land.max_file_mb from the run-start config, in bytes;
// 0 means off.
func landBigFileLimit(cfg map[string]any) int64 {
	mb := float64(landBigFileDefaultMB)
	if raw, ok := blockMap(cfg["land"])["max_file_mb"].(float64); ok && raw >= 0 {
		mb = raw
	}
	return int64(mb * 1000 * 1000)
}

type landBigFile struct {
	path string
	size int64
}

// landBigFiles lists the files whose blobs are reachable from rev but not from
// the excluded revs (git rev-list --not arguments) and are larger than limit.
func landBigFiles(dir string, limit int64, rev string, not ...string) ([]landBigFile, error) {
	if limit <= 0 {
		return nil, nil
	}
	args := append([]string{"rev-list", "--objects", rev, "--not"}, not...)
	listed, err := gitx.Run(dir, args...)
	if err != nil {
		return nil, err
	}
	input := strings.Builder{}
	for _, line := range strings.Split(listed, "\n") {
		// Blob and tree lines carry a path after the object name.
		if sha, path, ok := strings.Cut(strings.TrimSpace(line), " "); ok && path != "" {
			input.WriteString(sha + " " + path + "\n")
		}
	}
	if input.Len() == 0 {
		return nil, nil
	}
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch-check=%(objecttype) %(objectsize) %(rest)")
	cmd.Env = gitx.ChildEnv()
	cmd.Stdin = strings.NewReader(input.String())
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file: %v", err)
	}
	big := []landBigFile{}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 || fields[0] != "blob" {
			continue
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && size > limit {
			big = append(big, landBigFile{path: fields[2], size: size})
		}
	}
	sort.Slice(big, func(i, j int) bool { return big[i].path < big[j].path })
	return big, nil
}

// landBigFileRefusal is the refusal text for commits in rev (not in the
// excluded revs) that add a file over limit; "" when there is none or the
// check is off. A git error is not a refusal: the push or merge reports it.
func landBigFileRefusal(dir string, limit int64, rev string, not ...string) string {
	big, err := landBigFiles(dir, limit, rev, not...)
	if err != nil || len(big) == 0 {
		return ""
	}
	names := []string{}
	for _, f := range big {
		names = append(names, fmt.Sprintf("%s (%.1f MB)", f.path, float64(f.size)/1e6))
	}
	return fmt.Sprintf("commits add a file over land.max_file_mb (%g MB): %s; remove it from the commits (git rm --cached <file>, add it to .gitignore, amend) or raise land.max_file_mb in .code-forge.yml", float64(limit)/1e6, strings.Join(names, ", "))
}

// landBigFileError is a push refused by the size check: nothing reached the
// remote, and a retry fails the same way until the commits change.
type landBigFileError struct{ reason string }

func (e *landBigFileError) Error() string { return e.reason }
