package main

import (
	"os"
	"syscall"
)

// sweepCtimeMs is fs.Stats.ctimeMs (status change time) on macOS.
func sweepCtimeMs(info os.FileInfo) float64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return float64(st.Ctimespec.Sec)*1000 + float64(st.Ctimespec.Nsec)/1e6
}
