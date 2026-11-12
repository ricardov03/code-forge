//go:build cfpanicseam

package main

import "os"

// Test-only build (B508): CODE_FORGE_TEST_PANIC=1 makes every command panic
// before it starts, with a value that must never reach the user.
func init() {
	panicSeam = func([]string) {
		if os.Getenv("CODE_FORGE_TEST_PANIC") == "1" {
			panic("b508 seam panic: token=sk-test-SECRET-do-not-print")
		}
	}
}
