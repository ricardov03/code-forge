//go:build linux || openbsd || dragonfly || solaris || illumos || aix

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
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d", info.Size(), info.ModTime().UnixNano(), st.Ctim.Sec, st.Ctim.Nsec, info.Mode(), st.Dev, st.Ino)
}
