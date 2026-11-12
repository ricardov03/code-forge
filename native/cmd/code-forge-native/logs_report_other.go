//go:build !darwin && !linux

package main

// lrKernelRelease is unknown here; `logs report` stays in Node.
func lrKernelRelease() string { return "" }
