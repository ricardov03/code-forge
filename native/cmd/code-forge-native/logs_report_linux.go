package main

import "syscall"

// lrKernelRelease is os.release() on Linux (uname -r).
func lrKernelRelease() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return ""
	}
	out := []byte{}
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
