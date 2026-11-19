package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ricardov03/code-forge/native/ramtmp"
)

// B924: gates.ram_tmp (auto | off, default auto = on for macOS). When on, the
// gate and test commands code-forge runs get TMPDIR=<RAM disk>/<run-block>-*,
// a fresh directory per gate run, removed after it. Any error prints one WARN
// per process and the gates keep the normal TMPDIR; it never fails a gate.

// gatesRamTmpOptions is the seam tests replace with a fake Mac.
var gatesRamTmpOptions = ramtmp.Defaults

var gatesRamTmpWarned sync.Once

// gatesRamTmpEnv returns the base env for the gate commands and a cleanup
// for the directory it made; nil env when ram_tmp is off or fell back.
func gatesRamTmpEnv(configured map[string]any, run, block string, warn io.Writer) (map[string]string, func()) {
	o := gatesRamTmpOptions()
	if on, _ := ramtmp.Enabled(ramtmp.Mode(configured["ram_tmp"]), o.GOOS); !on {
		return nil, func() {}
	}
	fallback := func(err error) (map[string]string, func()) {
		gatesRamTmpWarned.Do(func() {
			if warn == nil {
				warn = os.Stderr
			}
			fmt.Fprintf(warn, "warn: gates.ram_tmp: %s; the gates use the normal TMPDIR\n", strings.Join(strings.Fields(err.Error()), " "))
		})
		return nil, func() {}
	}
	volume, err := o.Ensure(ramtmp.DefaultSizeMB)
	if err != nil {
		return fallback(err)
	}
	if volume == "" {
		return nil, func() {}
	}
	name := strings.Trim(run+"-"+block, "-")
	dir, err := ramtmp.Dir(volume, name)
	if err != nil {
		return fallback(err)
	}
	return map[string]string{"TMPDIR": dir}, func() { _ = os.RemoveAll(dir) }
}

// dfRamTmpRow is the doctor row: ram_tmp on, off or the fallback reason.
// Read-only: it never makes a RAM disk.
func dfRamTmpRow(cfg map[string]any) doctorRow {
	gatesCfg, _ := cfg["gates"].(map[string]any)
	return doctorRow{"ram_tmp", "INFO", "ram_tmp", gatesRamTmpOptions().Status(ramtmp.Mode(gatesCfg["ram_tmp"]))}
}
