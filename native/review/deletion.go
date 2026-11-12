package review

// B537: src/review/deletion.mjs — the `## deleted file` note of a pure
// deletion: the tree paths that still name the deleted file's basename.

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	delMaxReferences = 20
	delMinBasename   = 3
)

var delControl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

type delRefs struct {
	status string // found, none or not_checked
	paths  []string
	more   int
}

// delFindReferences is findReferences().
func delFindReferences(repoRoot, file string) delRefs {
	base := path.Base(file)
	if utf16Len(base) < delMinBasename || delControl.MatchString(file) {
		return delRefs{status: "not_checked"}
	}
	out, code, ok := gitCapped(repoRoot, reviewGitEnv(), nil, []int{0, 1}, capDefaultBytes, "grep", "-l", "-z", "--untracked", "-F", "-e", base, "--", ".")
	if !ok || (code != 0 && code != 1) {
		return delRefs{status: "not_checked"}
	}
	matched := []string{}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && p != file && !IsSecretLike(p) {
			matched = append(matched, p)
		}
	}
	matched = unique(matched)
	unsafe := 0
	clean := []string{}
	for _, p := range matched {
		if delControl.MatchString(p) {
			unsafe++
		} else {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 && unsafe == 0 {
		return delRefs{status: "none"}
	}
	sortUTF16(clean)
	more := unsafe
	if len(clean) > delMaxReferences {
		more += len(clean) - delMaxReferences
		clean = clean[:delMaxReferences]
	}
	return delRefs{"found", clean, more}
}

// renderDeletionNote is renderDeletionNote().
func renderDeletionNote(file string, refs delRefs) (string, error) {
	shown := file
	if delControl.MatchString(file) {
		s, err := jsonString(file)
		if err != nil {
			return "", err
		}
		shown = s
	}
	base, err := jsonString(path.Base(file))
	if err != nil {
		return "", err
	}
	out := []string{"## deleted file", "this file is deleted: " + shown, "check that nothing still depends on it. tree paths that still name " + base + ":"}
	switch refs.status {
	case "not_checked":
		out = append(out, "references not checked")
	case "none":
		out = append(out, "no references found")
	default:
		for _, p := range refs.paths {
			out = append(out, "- "+p)
		}
		if refs.more > 0 {
			out = append(out, "("+strconv.Itoa(refs.more)+" more not listed)")
		}
	}
	return strings.Join(out, "\n"), nil
}

// deletionNote is deletionNote(): never fails short of the JSON encoding.
func deletionNote(repoRoot, file string) (string, error) {
	return renderDeletionNote(file, delFindReferences(repoRoot, file))
}
