//go:build unix

package testslot

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

func unlockFile(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// alive is false when the pid is gone, a zombie, or a different process
// (its start time differs from the one recorded in the ticket).
func alive(t Ticket) bool {
	if t.PID <= 0 || t.PID > math.MaxInt32 {
		return false
	}
	if syscall.Kill(t.PID, 0) == syscall.ESRCH {
		return false
	}
	start, gone := processStart(t.PID)
	if gone {
		return false
	}
	return t.Start == "" || start == "" || start == t.Start
}

// processStart returns a stable start-time token for pid ("" when unknown),
// and gone when the process does not exist or is a zombie. It makes no child
// process: darwin reads kinfo_proc by sysctl, linux reads /proc/<pid>/stat.
func processStart(pid int) (start string, gone bool) {
	switch runtime.GOOS {
	case "darwin":
		return darwinStart(pid)
	case "linux":
		return linuxStart(pid)
	}
	return "", false
}

// darwinSysctl is SYS___SYSCTL on darwin amd64 and arm64. It is named by
// number so this file also compiles for linux, which never calls it.
const darwinSysctl = 202

func sysctl(mib []int32, out []byte, name []byte) (int, error) {
	n := uintptr(len(out))
	var newp unsafe.Pointer
	if len(name) > 0 {
		newp = unsafe.Pointer(&name[0])
	}
	_, _, errno := syscall.Syscall6(darwinSysctl, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&out[0])), uintptr(unsafe.Pointer(&n)), uintptr(newp), uintptr(len(name)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func darwinStart(pid int) (string, bool) {
	// CTL_KERN, KERN_PROC, KERN_PROC_PID: one kinfo_proc (648 bytes). Its
	// extern_proc starts with p_starttime (timeval); p_stat is at byte 36.
	buf := make([]byte, 1024)
	n, err := sysctl([]int32{1, 14, 1, int32(pid)}, buf, nil)
	if err != nil {
		return "", false
	}
	if n < 37 || buf[36] == 5 { // no such process, or SZOMB
		return "", true
	}
	sec := int64(binary.LittleEndian.Uint64(buf[0:8]))
	usec := int32(binary.LittleEndian.Uint32(buf[8:12]))
	return fmt.Sprintf("darwin:%d.%06d", sec, usec), false
}

func linuxStart(pid int) (string, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", os.IsNotExist(err)
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 > len(s) {
		return "", false
	}
	fields := strings.Fields(s[i+2:]) // fields[0] is field 3 (state)
	if len(fields) < 20 {
		return "", false
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return "", true
	}
	return "linux:" + fields[19], false // field 22: starttime in clock ticks
}

// SystemResources probes cores, the 1-minute load and free memory. Free
// memory on darwin counts free, speculative, purgeable and file-backed pages
// (what the system can hand out without swapping); on linux MemAvailable.
func SystemResources() Resources {
	r := Resources{Cores: runtime.NumCPU()}
	switch runtime.GOOS {
	case "darwin":
		load := sysctlName("vm.loadavg") // struct loadavg { uint32 ldavg[3]; long fscale; }
		page := leUint(sysctlName("hw.pagesize"))
		if len(load) < 24 || page == 0 {
			return r
		}
		scale := binary.LittleEndian.Uint64(load[16:24])
		if scale == 0 {
			return r
		}
		r.Load1 = float64(binary.LittleEndian.Uint32(load[0:4])) / float64(scale)
		pages := uint64(0)
		for _, name := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_purgeable_count", "vm.page_pageable_external_count"} {
			pages += leUint(sysctlName(name))
		}
		r.FreeBytes, r.Known = pages*page, true
	case "linux":
		load, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return r
		}
		fields := strings.Fields(string(load))
		if len(fields) == 0 {
			return r
		}
		l1, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return r
		}
		mem, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return r
		}
		for _, line := range strings.Split(string(mem), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemAvailable:" {
				if kb, err := strconv.ParseUint(f[1], 10, 64); err == nil {
					r.Load1, r.FreeBytes, r.Known = l1, kb*1024, true
				}
			}
		}
	}
	return r
}

// sysctlName reads a darwin sysctl by name (name2mib, then the value).
func sysctlName(name string) []byte {
	mib := make([]int32, 12)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&mib[0])), len(mib)*4)
	n, err := sysctl([]int32{0, 3}, raw, []byte(name))
	if err != nil || n < 4 {
		return nil
	}
	out := make([]byte, 64)
	m, err := sysctl(mib[:n/4], out, nil)
	if err != nil {
		return nil
	}
	return out[:m]
}

func leUint(b []byte) uint64 {
	if len(b) > 8 {
		b = b[:8]
	}
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}
