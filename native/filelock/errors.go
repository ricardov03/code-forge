package filelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// Error retains Node's stable filesystem code and message as well as the cause.
type Error struct {
	Code, Message string
	Cause         error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func nodeError(err error, operation, file string) error {
	if err == nil {
		return nil
	}
	var existing *Error
	if errors.As(err, &existing) {
		return err
	}
	var pe *os.PathError
	if !errors.As(err, &pe) {
		return err
	}
	code, message := "", ""
	switch {
	case errors.Is(err, syscall.ENOENT):
		code, message = "ENOENT", "no such file or directory"
	case errors.Is(err, syscall.EEXIST):
		code, message = "EEXIST", "file already exists"
	case errors.Is(err, syscall.ENOTDIR):
		code, message = "ENOTDIR", "not a directory"
	case errors.Is(err, syscall.EISDIR):
		code, message = "EISDIR", "illegal operation on a directory"
	case errors.Is(err, syscall.EACCES):
		code, message = "EACCES", "permission denied"
	case errors.Is(err, syscall.EPERM):
		code, message = "EPERM", "operation not permitted"
	case errors.Is(err, syscall.EROFS):
		code, message = "EROFS", "read-only file system"
	case errors.Is(err, syscall.ENOSPC):
		code, message = "ENOSPC", "no space left on device"
	case errors.Is(err, syscall.EMFILE):
		code, message = "EMFILE", "too many open files"
	case errors.Is(err, syscall.ENFILE):
		code, message = "ENFILE", "file table overflow"
	case errors.Is(err, syscall.ELOOP):
		code, message = "ELOOP", "too many symbolic links encountered"
	case errors.Is(err, syscall.ENAMETOOLONG):
		code, message = "ENAMETOOLONG", "name too long"
	case errors.Is(err, syscall.EINVAL):
		code, message = "EINVAL", "invalid argument"
	default:
		return err
	}
	if operation == "" {
		operation = pe.Op
	}
	switch operation {
	case "remove":
		operation = "unlink"
	case "sync":
		operation = "fsync"
	}
	if file == "" {
		file = pe.Path
	}
	detail := operation
	switch operation {
	case "read", "write", "close", "fsync", "ftruncate", "fstat":
		// Node's fd-based errors carry no path, even when Go has one.
	default:
		detail += fmt.Sprintf(" '%s'", file)
	}
	return &Error{code, fmt.Sprintf("%s: %s, %s", code, message, detail), err}
}
