// Package ramtmp keeps test temp files on a macOS RAM disk with no
// file-event log (B924). Go tests make thousands of temp git directories
// under $TMPDIR; on macOS fseventsd logs every change and can take a whole
// CPU. A RAM disk named cf-tmp with .fseventsd/no_log at its root keeps
// those writes out of the event log and off the SSD.
//
// Ensure reuses /Volumes/cf-tmp when it is mounted, else makes it with
// `hdiutil attach -nomount ram://<sectors>` and `diskutil erasevolume APFS
// cf-tmp <dev>` (no sudo). On any other OS it is a no-op that returns "".
// Every command runs through an injectable Runner, so tests never create a
// real RAM disk.
package ramtmp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Volume is the RAM disk's volume name; scripts reuse /Volumes/cf-tmp.
const Volume = "cf-tmp"

// DefaultSizeMB is the RAM disk size code-forge asks for. macOS allocates
// its pages as they are written, up to this size.
const DefaultSizeMB = 2048

// Runner runs one command and returns its stdout. A non-nil error means the
// command failed or could not start.
type Runner func(name string, args ...string) (string, error)

// Options are the seams Ensure uses. Defaults() gives the real ones.
type Options struct {
	GOOS     string                 // runtime.GOOS
	Root     string                 // where volumes mount: /Volumes
	Run      Runner                 // runs hdiutil and diskutil
	Mounted  func(path string) bool // path is a mounted volume root
	LookPath func(name string) (string, error)
	LockPath string // a file locked while a RAM disk is made ("" = none)
}

// Defaults are the real seams: this OS, /Volumes, exec and a mount check by
// device number.
func Defaults() Options {
	return Options{
		GOOS:     runtime.GOOS,
		Root:     "/Volumes",
		Run:      execRun,
		Mounted:  mounted,
		LookPath: exec.LookPath,
		LockPath: filepath.Join(os.TempDir(), "code-forge-cf-tmp.lock"),
	}
}

// Ensure is Defaults().Ensure(sizeMB).
func Ensure(sizeMB int) (string, error) { return Defaults().Ensure(sizeMB) }

// Path is the volume root under o.Root.
func (o Options) Path() string { return filepath.Join(o.Root, Volume) }

// Ensure returns the RAM disk root, making the disk when it is not mounted.
// Not darwin: "", nil.
func (o Options) Ensure(sizeMB int) (string, error) {
	if o.GOOS != "darwin" {
		return "", nil
	}
	if sizeMB <= 0 {
		sizeMB = DefaultSizeMB
	}
	path := o.Path()
	if o.Mounted(path) {
		return path, Mark(path)
	}
	unlock := o.lock()
	defer unlock()
	// Another process may have made it while this one waited for the lock.
	if o.Mounted(path) {
		return path, Mark(path)
	}
	out, err := o.Run("hdiutil", "attach", "-nomount", "ram://"+strconv.Itoa(sizeMB*2048))
	if err != nil {
		return "", fmt.Errorf("hdiutil attach failed: %s", oneLine(err.Error()))
	}
	dev := ""
	if fields := strings.Fields(out); len(fields) > 0 {
		dev = fields[0]
	}
	if !regexp.MustCompile(`^/dev/disk[0-9]+$`).MatchString(dev) {
		return "", fmt.Errorf("hdiutil attach printed no /dev/disk device: %q", oneLine(out))
	}
	if _, err := o.Run("diskutil", "erasevolume", "APFS", Volume, dev); err != nil {
		_, _ = o.Run("hdiutil", "detach", dev)
		return "", fmt.Errorf("diskutil erasevolume failed: %s", oneLine(err.Error()))
	}
	if !o.Mounted(path) {
		_, _ = o.Run("hdiutil", "detach", dev)
		return "", fmt.Errorf("%s is not mounted after diskutil erasevolume", path)
	}
	return path, Mark(path)
}

// Mark writes .fseventsd/no_log (fseventsd keeps no event log for the
// volume) and .metadata_never_index (Spotlight skips it) at the volume root.
func Mark(path string) error {
	if err := os.MkdirAll(filepath.Join(path, ".fseventsd"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{filepath.Join(".fseventsd", "no_log"), ".metadata_never_index"} {
		file := filepath.Join(path, name)
		if _, err := os.Stat(file); err == nil {
			continue
		}
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// EnvMode is the CODE_FORGE_RAM_TMP variable; auto or off there overrides
// gates.ram_tmp (a test binary sets off so no test makes a real RAM disk).
const EnvMode = "CODE_FORGE_RAM_TMP"

// Mode is the effective gates.ram_tmp: CODE_FORGE_RAM_TMP when it is auto or
// off, else the configured value.
func Mode(configured any) any {
	if v := os.Getenv(EnvMode); v == "auto" || v == "off" {
		return v
	}
	return configured
}

// Enabled reads gates.ram_tmp: absent or "auto" is on for darwin and off
// elsewhere; "off" is off. The string says why it is off.
func Enabled(mode any, goos string) (bool, string) {
	switch mode {
	case nil, "auto":
		if goos != "darwin" {
			return false, "not macOS"
		}
		return true, ""
	case "off":
		if os.Getenv(EnvMode) == "off" {
			return false, EnvMode + "=off"
		}
		return false, "gates.ram_tmp: off"
	}
	return false, fmt.Sprintf("gates.ram_tmp must be auto or off, got %v", mode)
}

// Status is the doctor's read-only view: it never makes a RAM disk.
func (o Options) Status(mode any) string {
	on, why := Enabled(mode, o.GOOS)
	if !on {
		return "off (" + why + ")"
	}
	path := o.Path()
	if o.Mounted(path) {
		if _, err := os.Stat(filepath.Join(path, ".fseventsd", "no_log")); err != nil {
			return "on: " + path + " mounted (.fseventsd/no_log is written on the next gate)"
		}
		return "on: " + path + " mounted, .fseventsd/no_log present"
	}
	for _, tool := range []string{"hdiutil", "diskutil"} {
		if _, err := o.LookPath(tool); err != nil {
			return "fallback: " + tool + " not found; gates use the normal TMPDIR"
		}
	}
	return "on (auto): " + path + " is made on the first gate"
}

// Dir makes a fresh directory for one run or block on the volume.
func Dir(volume, name string) (string, error) {
	name = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(name, "_")
	if name == "" || strings.Trim(name, ".") == "" {
		name = "gates"
	}
	return os.MkdirTemp(volume, name+"-")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func execRun(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return string(out), fmt.Errorf("%s: %s", err, oneLine(string(exit.Stderr)))
		}
		return string(out), err
	}
	return string(out), nil
}

// mounted: path is a directory on another device than its parent.
func mounted(path string) bool {
	var self, parent syscall.Stat_t
	if syscall.Stat(path, &self) != nil || syscall.Stat(filepath.Dir(path), &parent) != nil {
		return false
	}
	return self.Dev != parent.Dev
}

func (o Options) lock() func() {
	if o.LockPath == "" {
		return func() {}
	}
	f, err := os.OpenFile(o.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}
