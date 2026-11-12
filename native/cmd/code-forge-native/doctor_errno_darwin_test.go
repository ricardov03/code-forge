package main

import (
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// b535NodeDarwinErrnos is Node 24's util.getSystemErrorMap() on darwin
// (number:name), the names libuv gives err.code there (B535).
const b535NodeDarwinErrnos = "" +
	"1:EPERM 2:ENOENT 3:ESRCH 4:EINTR 5:EIO 6:ENXIO 7:E2BIG 8:ENOEXEC 9:EBADF 12:ENOMEM 13:EACCES " +
	"14:EFAULT 16:EBUSY 17:EEXIST 18:EXDEV 19:ENODEV 20:ENOTDIR 21:EISDIR 22:EINVAL 23:ENFILE 24:EMFILE " +
	"25:ENOTTY 26:ETXTBSY 27:EFBIG 28:ENOSPC 29:ESPIPE 30:EROFS 31:EMLINK 32:EPIPE 34:ERANGE 35:EAGAIN " +
	"37:EALREADY 38:ENOTSOCK 39:EDESTADDRREQ 40:EMSGSIZE 41:EPROTOTYPE 42:ENOPROTOOPT 43:EPROTONOSUPPORT " +
	"44:ESOCKTNOSUPPORT 45:ENOTSUP 47:EAFNOSUPPORT 48:EADDRINUSE 49:EADDRNOTAVAIL 50:ENETDOWN " +
	"51:ENETUNREACH 53:ECONNABORTED 54:ECONNRESET 55:ENOBUFS 56:EISCONN 57:ENOTCONN 58:ESHUTDOWN " +
	"60:ETIMEDOUT 61:ECONNREFUSED 62:ELOOP 63:ENAMETOOLONG 64:EHOSTDOWN 65:EHOSTUNREACH 66:ENOTEMPTY " +
	"78:ENOSYS 79:EFTYPE 84:EOVERFLOW 89:ECANCELED 92:EILSEQ 96:ENODATA 100:EPROTO " +
	""

// Every errno 1..120 prints what Node prints on darwin: its libuv name, or
// "Unknown system error -<n>" for a number libuv does not map.
func TestB535DoctorErrnoNamesMatchNodeDarwin(t *testing.T) {
	names := map[int]string{}
	for _, pair := range strings.Fields(b535NodeDarwinErrnos) {
		no, name, _ := strings.Cut(pair, ":")
		n, err := strconv.Atoi(no)
		if err != nil {
			t.Fatal(err)
		}
		names[n] = name
	}
	if len(names) != 65 {
		t.Fatalf("the Node map has %d entries, want 65", len(names))
	}
	for n := 1; n <= 120; n++ {
		want, ok := names[n]
		if !ok {
			want = "Unknown system error -" + strconv.Itoa(n)
		}
		if got := dwpErrnoName(syscall.Errno(n)); got != want {
			t.Errorf("errno %d: Go %q, Node %q", n, got, want)
		}
	}
}
