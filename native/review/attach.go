package review

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
)

// Port of packet.mjs attachFiles and attachRefusal: the needs_file round's
// section of requested files, read-only, appended after the packet.

// AttachDiagnostics are attachFiles' counts: readable attachments, policy
// refusals, and the class of each read error (never content).
type AttachDiagnostics struct {
	Attached, PolicyRefused int
	Errors                  []string
}

// AttachFiles is Node's attachFiles: the section to append.
func AttachFiles(repoRoot string, paths []string, budgetTokens int, ownedFiles, changedFiles []string, d *AttachDiagnostics) string {
	out := []string{"## attached files (read-only, requested in needs_file)"}
	left := budgetTokens * 4
	if len(paths) > 10 {
		paths = paths[:10]
	}
	for _, raw := range paths {
		if err := AssertPacketPath(repoRoot, raw); err != nil {
			var pe *PacketError
			if errors.As(err, &pe) && pe.Message == "not a regular file" {
				out = append(out, "### "+raw+" (not a file)")
			} else {
				out = append(out, "### (refused: not a repo-relative path)")
			}
			continue
		}
		rel := raw
		if reason, policy, refused := attachRefusal(repoRoot, rel, ownedFiles, changedFiles); refused {
			if policy {
				d.PolicyRefused++
			}
			out = append(out, "### "+rel+" ("+reason+")")
			continue
		}
		data, err := gitx.ReadRegularFileNoFollow(repoRoot, rel)
		if err != nil {
			var ge *gitx.Error
			if errors.As(err, &ge) && ge.Code == "bad-path" {
				out = append(out, "### "+rel+" (refused: "+ge.Message+")")
			} else {
				// a Node fs error is of class Error
				d.Errors = append(d.Errors, "Error")
				out = append(out, "### "+rel+" (error: Error)")
			}
			continue
		}
		if data == nil {
			out = append(out, "### "+rel+" (missing)")
			continue
		}
		text := decodeUTF8(data)
		lines := SplitLines(&text)
		d.Attached++
		out = append(out, "### "+rel)
		for i, l := range lines {
			line := strconv.Itoa(i+1) + "| " + l
			left -= len(line) + 1
			if left < 0 {
				out = append(out, "(trimmed: over budget)")
				break
			}
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// attachRefusal is why rel may not be attached (refused false: it may).
func attachRefusal(repoRoot, rel string, ownedFiles, changedFiles []string) (reason string, policy, refused bool) {
	if IsSecretLike(rel) {
		return "refused: secret-like path", true, true
	}
	st, err := os.Lstat(filepath.Join(repoRoot, rel))
	if err != nil {
		return "missing", false, true
	}
	if !st.Mode().IsRegular() {
		return "not a file", false, true
	}
	listed, _, ok := gitOK(repoRoot, reviewGitEnv(), nil, "ls-files", "-z", "--", rel)
	if !ok {
		return "refused: git failed", true, true
	}
	if listed == rel+"\x00" {
		return "", false, false
	}
	if listed != "" {
		return "refused: git failed", true, true
	}
	if !slices.Contains(ownedFiles, rel) {
		return "refused: untracked, not owned by this block", true, true
	}
	if !slices.Contains(changedFiles, rel) {
		return "refused: untracked, not changed in this block", true, true
	}
	_, code, ok := gitOK(repoRoot, gitx.ChildEnv(), []int{1}, "check-ignore", "-q", "--", rel)
	if !ok {
		return "refused: git failed", true, true
	}
	if code == 0 {
		return "refused: gitignored", true, true
	}
	return "", false, false
}

// decodeUTF8 is Buffer.toString('utf8'): each maximal invalid subpart becomes
// one U+FFFD (the WHATWG decoder), unlike strings.ToValidUTF8.
func decodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, w := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || w > 1 {
			sb.Write(b[i : i+w])
			i += w
			continue
		}
		sb.WriteRune(utf8.RuneError)
		i += 1 + validContinuations(b[i:])
	}
	return sb.String()
}

// validContinuations counts the bytes after b[0] that still fit a
// well-formed sequence led by b[0] (the rest of its maximal subpart).
func validContinuations(b []byte) int {
	lo, hi, need := byte(0x80), byte(0xBF), 0
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c == 0xE0:
		lo, need = 0xA0, 2
	case c == 0xED:
		hi, need = 0x9F, 2
	case c >= 0xE1 && c <= 0xEF:
		need = 2
	case c == 0xF0:
		lo, need = 0x90, 3
	case c == 0xF4:
		hi, need = 0x8F, 3
	case c >= 0xF1 && c <= 0xF3:
		need = 3
	}
	n := 0
	for n < need && 1+n < len(b) {
		c := b[1+n]
		if c < lo || c > hi {
			break
		}
		n++
		lo, hi = 0x80, 0xBF
	}
	return n
}

// cutUTF16 is .slice(0, n) in UTF-16 units (a cut surrogate pair keeps
// U+FFFD, as cut120).
func cutUTF16(s string, max int) string {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > max {
			if n < max {
				return s[:i] + "�"
			}
			return s[:i]
		}
		n += w
	}
	return s
}
