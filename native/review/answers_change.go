package review

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
)

// AnswersChange is answers.mjs answeringChange (B609): the answering file's
// change, built from the bytes that were hashed (current, already UTF-8):
// its diff against its text at base (nil: HEAD), or its full content when
// that commit has no such file. kind is "new" or "tracked". A secret-like or
// refused path, a bad base or a git failure is an error (the caller's
// packet-unreadable); a base text that is not UTF-8 is ErrUnsupported.
func AnswersChange(root, file string, base *string, current, workDir string, rnd io.Reader) (kind, text string, err error) {
	if err := AssertPacketPath(root, file); err != nil {
		return "", "", err
	}
	if IsSecretLike(file) {
		return "", "", errors.New("secret-like path")
	}
	ref := "HEAD"
	if base != nil {
		ref = *base
	}
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", "", errors.New("the base must be a commit id")
	}
	listed, code, ok := gitOK(root, reviewGitEnv(), nil, "ls-tree", "-z", ref, "--", file)
	if !ok {
		return "", "", fmt.Errorf("git ls-tree failed (exit %d)", code)
	}
	if listed == "" {
		return "new", current, nil
	}
	shown, code, ok := gitOK(root, gitx.ChildEnv(), nil, "show", ref+":"+file)
	if !ok {
		return "", "", fmt.Errorf("git show failed (exit %d)", code)
	}
	if !utf8.ValidString(shown) {
		return "", "", unsupported("the answering file's base text is not UTF-8")
	}
	diff, err := fixHunkDiff(file, shown, current, workDir, Deps{Rand: rnd})
	if err != nil {
		return "", "", err
	}
	return "tracked", diff.DiffText, nil
}
