package worker

import (
	"os"
	"syscall"
)

const errNotDir = syscall.ENOTDIR

// ctimeMs is fs.Stats ctimeMs (inode change time, milliseconds).
func ctimeMs(st os.FileInfo) float64 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return float64(s.Ctimespec.Sec)*1e3 + float64(s.Ctimespec.Nsec)/1e6
	}
	return msOf(st.ModTime())
}
