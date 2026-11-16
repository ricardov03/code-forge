package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// B764: Node's fs error text is libuv's uv_err_name + uv_strerror for any
// errno, and report --export runs Node's recursive mkdir (MKDirpAsync in
// node_file.cc), so Go prints what Node prints on the same OS.

// b764UVTexts is a fixed copy of libuv's UV_ERRNO_MAP (deps/uv/include/uv.h
// in Node v24.22.1) for errnos a file call can return.
var b764UVTexts = []struct {
	no         syscall.Errno
	name, text string
}{
	{syscall.EACCES, "EACCES", "permission denied"},
	{syscall.EEXIST, "EEXIST", "file already exists"},
	{syscall.ENOTDIR, "ENOTDIR", "not a directory"},
	{syscall.EISDIR, "EISDIR", "illegal operation on a directory"},
	{syscall.ELOOP, "ELOOP", "too many symbolic links encountered"},
	{syscall.ENAMETOOLONG, "ENAMETOOLONG", "name too long"},
	{syscall.ENOTEMPTY, "ENOTEMPTY", "directory not empty"},
	{syscall.EBUSY, "EBUSY", "resource busy or locked"},
	{syscall.EXDEV, "EXDEV", "cross-device link not permitted"},
	{syscall.EMFILE, "EMFILE", "too many open files"},
	{syscall.ENFILE, "ENFILE", "file table overflow"},
	{syscall.ETXTBSY, "ETXTBSY", "text file is busy"},
	{syscall.EINVAL, "EINVAL", "invalid argument"},
	{syscall.EIO, "EIO", "i/o error"},
	{syscall.EFBIG, "EFBIG", "file too large"},
	{syscall.EMLINK, "EMLINK", "too many links"},
	{syscall.EAGAIN, "EAGAIN", "resource temporarily unavailable"},
	{syscall.ENOMEM, "ENOMEM", "not enough memory"},
	{syscall.ENXIO, "ENXIO", "no such device or address"},
	{syscall.EOVERFLOW, "EOVERFLOW", "value too large for defined data type"},
	{syscall.EPERM, "EPERM", "operation not permitted"},
	{syscall.ENOENT, "ENOENT", "no such file or directory"},
	{syscall.ENOSPC, "ENOSPC", "no space left on device"},
	{syscall.EROFS, "EROFS", "read-only file system"},
}

func TestB764FsMessageIsLibuvText(t *testing.T) {
	for _, c := range b764UVTexts {
		err := &os.PathError{Op: "mkdir", Path: "p/", Err: c.no}
		want := c.name + ": " + c.text + ", mkdir 'p/'"
		if got := blockIOMessage(err); got != want {
			t.Errorf("%s: got %q want %q", c.name, got, want)
		}
	}
	// An errno libuv does not map: uv_err_name and uv_strerror both print
	// "Unknown system error <-errno>" (uv-common.c).
	unknown := syscall.EDEADLK
	want := fmt.Sprintf("Unknown system error -%d: Unknown system error -%d, open 'x'", int(unknown), int(unknown))
	if got := blockIOMessage(&os.PathError{Op: "open", Path: "x", Err: unknown}); got != want {
		t.Errorf("unknown: got %q want %q", got, want)
	}
	// Every errno libuv names on this platform has its libuv text.
	for _, list := range [][]dwpErrnoEntry{dwpErrnoNames, dwpErrnoPlatform} {
		for _, e := range list {
			if text := uvStrerror(e.no); strings.HasPrefix(text, "Unknown system error") {
				t.Errorf("%s: no libuv text", e.name)
			}
		}
	}
}

// Node's mkdir(dir, {recursive: true}) errors that Go handed over before
// (goldens recorded from Node in testdata/b744/b764-*.txt, cwd = HOME).
func TestB764ReportExportMkdirRunsNodeAlgorithm(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, c := range []struct {
		name string
		dir  string
	}{
		{"b764-report-export-loop-sub", "loop/sub"},
		{"b764-report-export-loop", "loop"},
		{"b764-report-export-long", long},
		{"b764-report-export-dangling", "dl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b744Check(t, c.name, func(h string) {
				b744Ledger(h)
				if err := os.Symlink("loop", filepath.Join(h, "loop")); err != nil {
					panic(err)
				}
				if err := os.Symlink("nowhere", filepath.Join(h, "dl")); err != nil {
					panic(err)
				}
			}, "report", "--slug", "t", "--export", c.dir)
		})
	}
}

// The errno sequences each OS gives Node's MKDirpAsync, replayed through the
// Go port. "f/" with f a regular file: macOS mkdir gives ENOTDIR; Linux
// mkdir gives EEXIST (mkdir(2): "path already exists (not necessarily as a
// directory)"), then Node stats "f/", which is ENOTDIR (path_resolution(7):
// a trailing '/' forces the component to resolve to a directory), and
// Node returns that errno. Both print "ENOTDIR: not a directory, mkdir 'f/'".
func TestB764MkdirpErrnoSequences(t *testing.T) {
	type call struct {
		path  string
		err   syscall.Errno
		isDir bool
	}
	for _, c := range []struct {
		name         string
		dir          string
		mkdir, stat  []call
		wantPath     string
		wantErr      syscall.Errno
		wantLeftover int
	}{
		{"darwin f/", "f/", []call{{"f/", syscall.ENOTDIR, false}}, nil, "f/", syscall.ENOTDIR, 0},
		{"linux f/", "f/", []call{{"f/", syscall.EEXIST, false}}, []call{{"f/", syscall.ENOTDIR, false}}, "f/", syscall.ENOTDIR, 0},
		{"existing file", "blk", []call{{"blk", syscall.EEXIST, false}}, []call{{"blk", 0, false}}, "blk", syscall.EEXIST, 0},
		{"existing dir", "d", []call{{"d", syscall.EEXIST, false}}, []call{{"d", 0, true}}, "d", 0, 0},
		{"eacces parent", "ro/a/b", []call{{"ro/a/b", syscall.ENOENT, false}, {"ro/a", syscall.EACCES, false}}, nil, "ro/a", syscall.EACCES, 0},
		{"made by a racer", "a/b", []call{{"a/b", syscall.ENOENT, false}, {"a", syscall.EEXIST, false}, {"a/b", 0, false}}, []call{{"a", 0, true}}, "a/b", 0, 0},
		{"file on the way", "a/b", []call{{"a/b", syscall.ENOENT, false}, {"a", syscall.EEXIST, false}}, []call{{"a", 0, false}}, "a", syscall.ENOTDIR, 0},
		{"stat fails", "x", []call{{"x", syscall.EROFS, false}}, []call{{"x", syscall.ELOOP, false}}, "x", syscall.ELOOP, 0},
		{"exists as dir after other error", "x", []call{{"x", syscall.EROFS, false}}, []call{{"x", 0, true}}, "x", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			mkdirs, stats := c.mkdir, c.stat
			saved := rpMkdirpSys
			t.Cleanup(func() { rpMkdirpSys = saved })
			rpMkdirpSys.mkdir = func(p string) error {
				if len(mkdirs) == 0 || mkdirs[0].path != p {
					t.Fatalf("unexpected mkdir %q (left %+v)", p, mkdirs)
				}
				next := mkdirs[0]
				mkdirs = mkdirs[1:]
				if next.err == 0 {
					return nil
				}
				return next.err
			}
			rpMkdirpSys.stat = func(p string) (bool, error) {
				if len(stats) == 0 || stats[0].path != p {
					t.Fatalf("unexpected stat %q (left %+v)", p, stats)
				}
				next := stats[0]
				stats = stats[1:]
				if next.err == 0 {
					return next.isDir, nil
				}
				return false, next.err
			}
			path, errno := rpNodeMkdirp(c.dir)
			if errno != c.wantErr || (errno != 0 && path != c.wantPath) {
				t.Fatalf("got %q %v want %q %v", path, errno, c.wantPath, c.wantErr)
			}
			if len(mkdirs)+len(stats) != c.wantLeftover {
				t.Fatalf("calls not made: mkdir %+v stat %+v", mkdirs, stats)
			}
		})
	}
}

// The real OS gives one of the two sequences above for "f/" (CI runs this on
// ubuntu, the owner on macOS), so the report-export-trailing-file golden is
// Node's text on both.
func TestB764TrailingSlashFileErrnoOnThisOS(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var mkErr syscall.Errno
	if !errors.As(syscall.Mkdir(f+"/", 0o777), &mkErr) {
		t.Fatal("mkdir f/ succeeded")
	}
	var st syscall.Stat_t
	statErr := syscall.Stat(f+"/", &st)
	t.Logf("%s: mkdir f/ = %s, stat f/ = %v", runtime.GOOS, dwpErrnoName(mkErr), statErr)
	switch {
	case mkErr == syscall.ENOTDIR:
	case mkErr == syscall.EEXIST && errors.Is(statErr, syscall.ENOTDIR):
	default:
		t.Fatalf("new sequence: mkdir %v stat %v; record Node's text for this OS", mkErr, statErr)
	}
	path, errno := rpNodeMkdirp(f + "/")
	if got := blockIOMessage(&os.PathError{Op: "mkdir", Path: path, Err: errno}); got != "ENOTDIR: not a directory, mkdir '"+f+"/'" {
		t.Fatalf("got %q", got)
	}
}
