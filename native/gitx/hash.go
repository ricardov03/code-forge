package gitx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const DeletedHash = "deleted"

func badPath(message string) error { return &Error{"bad-path", message} }
func realOrSelf(file string) string {
	if p, err := filepath.EvalSymlinks(file); err == nil {
		file = p
	}
	p, _ := filepath.Abs(file)
	return p
}

// AssertInside is Node's assertInsideRoot: the row-path checks, no symlink,
// and the realpath'd parent inside root.
func AssertInside(root, file string) error { return assertInside(root, file) }

func assertInside(root, file string) error {
	if file == "" || len(utf16.Encode([]rune(file))) > 4096 {
		return badPath("file must be a non-empty relative path")
	}
	if strings.ContainsAny(file, "\x00\\") {
		return badPath("file must be a POSIX path")
	}
	if strings.HasPrefix(file, "/") {
		return badPath("file must be relative to the repository root, not absolute")
	}
	parts := strings.Split(file, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return badPath("file must not contain \"\", \".\" or \"..\" segments")
		}
	}
	if p := strings.ToLower(parts[0]); p == ".git" || p == ".code-forge" {
		return badPath("file may not be under " + parts[0] + "/")
	}
	full := filepath.Join(root, file)
	if st, err := os.Lstat(full); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return badPath("the path is a symlink")
	}
	rel, err := filepath.Rel(realOrSelf(root), filepath.Join(realOrSelf(filepath.Dir(full)), filepath.Base(full)))
	if err != nil || rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return badPath("the path is outside the repository")
	}
	return nil
}

// ReadRegularFileNoFollow closes the lstat/open race and never blocks on a FIFO.
func ReadRegularFileNoFollow(root, file string) (data []byte, err error) {
	defer func() { err = fileReadError(err) }()
	if err := assertInside(root, file); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, file), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, badPath("the path is a symlink")
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, badPath("not a regular file")
	}
	if err := assertInside(root, file); err != nil {
		return nil, err
	}
	// Read from the checked descriptor, never reopen the pathname.
	return io.ReadAll(f)
}

// Preserve Node's fs errors for failures that are not WorkerError refusals.
func fileReadError(err error) error {
	var pe *os.PathError
	if !errors.As(err, &pe) {
		return err
	}
	codes := map[syscall.Errno]string{syscall.ENOTDIR: "ENOTDIR", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOENT: "ENOENT", syscall.EMFILE: "EMFILE", syscall.ENFILE: "ENFILE", syscall.EIO: "EIO", syscall.ENAMETOOLONG: "ENAMETOOLONG"}
	var errno syscall.Errno
	if !errors.As(err, &errno) || codes[errno] == "" {
		return err
	}
	message := errno.Error()
	if errno == syscall.ENAMETOOLONG {
		message = "name too long"
	}
	if errno == syscall.ENFILE {
		message = "file table overflow"
	}
	op := pe.Op
	if op == "stat" {
		op = "fstat"
	}
	detail := op
	if op == "open" {
		detail += fmt.Sprintf(" '%s'", pe.Path)
	}
	return &Error{codes[errno], fmt.Sprintf("%s: %s, %s", codes[errno], message, detail)}
}

func ContentHash(root, file string) (string, error) {
	data, err := ReadRegularFileNoFollow(root, file)
	if err != nil {
		return "", err
	}
	if data == nil {
		return DeletedHash, nil
	}
	return hash(data), nil
}
