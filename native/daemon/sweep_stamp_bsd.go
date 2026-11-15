//go:build darwin || freebsd || netbsd

package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// sweepStamp names a stat result: a file with the same stamp was not
// written, replaced or chmodded since (B755). ctime moves on every content
// or mode change, even when mtime is set back.
func sweepStamp(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d", info.Size(), info.ModTime().UnixNano(), st.Ctimespec.Sec, st.Ctimespec.Nsec, info.Mode(), st.Dev, st.Ino)
}
