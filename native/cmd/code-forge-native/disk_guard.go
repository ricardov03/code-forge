package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/diskguard"
	"github.com/ricardov03/code-forge/native/gitx"
)

// B961: land, gates run and run start check the free disk first. Under
// 2 x land.min_free_gb (default 15) they trim Go build cache files unused for
// 24 h; under land.min_free_gb they refuse with the next step.

// diskGuardMinGB is land.min_free_gb from the config; 0 turns the guard off.
func diskGuardMinGB(cfg map[string]any) float64 {
	if v, ok := blockMap(cfg["land"])["min_free_gb"].(float64); ok && v >= 0 {
		return v
	}
	return diskguard.DefaultMinGB
}

// diskGuardCache is the Go build cache dir ($GOCACHE, else go env GOCACHE);
// "" when the cache is off or go is not on PATH.
func diskGuardCache() string {
	dir := os.Getenv("GOCACHE")
	if dir == "" {
		cmd := exec.Command("go", "env", "GOCACHE")
		cmd.Env = gitx.ChildEnv()
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		dir = strings.TrimSpace(string(out))
	}
	if dir == "off" {
		return ""
	}
	return dir
}

// diskGuard checks the disk that holds path. It prints a trim note to print
// and returns the refusal ("" when the disk has room or cannot be measured).
func diskGuard(cfg map[string]any, path string, print io.Writer) string {
	result, err := diskguard.Check(path, diskGuardMinGB(cfg), diskGuardCache, time.Now())
	if err != nil {
		return ""
	}
	if line := result.TrimLine(); line != "" && print != nil {
		fmt.Fprintln(print, line)
	}
	return result.Refusal()
}

// doctorDiskRow is the B961 doctor row: FAIL under land.min_free_gb (land,
// gates and run start refuse), WARN under twice that (they trim the Go
// cache first), OK otherwise. ok is false when the guard is off or the disk
// cannot be measured. Doctor never trims.
func doctorDiskRow(cfg map[string]any, path string) (doctorRow, bool) {
	min := diskGuardMinGB(cfg)
	if min <= 0 {
		return doctorRow{}, false
	}
	free, err := diskguard.Free(path)
	if err != nil {
		return doctorRow{}, false
	}
	gb := float64(free) / 1e9
	const id, label = "disk-free", "free disk"
	switch {
	case gb < min:
		return doctorRow{id, "FAIL", label, fmt.Sprintf("%.1f GB, under land.min_free_gb (%g GB): land, gates run and run start refuse; free disk space (go clean -cache, old worktrees) or lower land.min_free_gb", gb, min)}, true
	case gb < 2*min:
		return doctorRow{id, "WARN", label, fmt.Sprintf("%.1f GB, under 2 x land.min_free_gb (%g GB): land, gates run and run start trim Go cache files unused for 24 h", gb, min)}, true
	}
	return doctorRow{id, "OK", label, fmt.Sprintf("%.1f GB (land.min_free_gb %g GB)", gb, min)}, true
}
