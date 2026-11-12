package main

import "syscall"

// lrKernelRelease is os.release() on macOS (uname -r).
func lrKernelRelease() string {
	release, err := syscall.Sysctl("kern.osrelease")
	if err != nil {
		return ""
	}
	return release
}
