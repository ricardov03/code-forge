package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ramtmp"
)

// b924FakeMac is a darwin ramtmp.Options under root: hdiutil and diskutil
// are recorded in root/calls, and diskutil "mounts" root/cf-tmp by making it.
// fail names a command prefix that fails. No real RAM disk is ever made.
func b924FakeMac(root, fail string) ramtmp.Options {
	return ramtmp.Options{
		GOOS: "darwin",
		Root: root,
		Run: func(name string, args ...string) (string, error) {
			line := name + " " + strings.Join(args, " ")
			f, err := os.OpenFile(filepath.Join(root, "calls"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				_, _ = f.WriteString(line + "\n")
				f.Close()
			}
			if fail != "" && strings.HasPrefix(line, fail) {
				return "", errors.New("exit status 1: fake failure")
			}
			if name == "hdiutil" && args[0] == "attach" {
				return "/dev/disk42\n", nil
			}
			if name == "diskutil" {
				return "", os.MkdirAll(filepath.Join(root, ramtmp.Volume), 0o755)
			}
			return "", nil
		},
		Mounted: func(path string) bool {
			st, err := os.Stat(path)
			return err == nil && st.IsDir()
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

// TestB924GatesProcess is the child: code-forge with a fake Mac seam.
func TestB924GatesProcess(t *testing.T) {
	root := os.Getenv("CF_B924_FAKE_MAC")
	if root == "" {
		return
	}
	// TestMain sets CODE_FORGE_RAM_TMP=off for every test binary; this child
	// takes the mode from CF_B924_MODE ("" = the config decides).
	_ = os.Setenv("CODE_FORGE_RAM_TMP", os.Getenv("CF_B924_MODE"))
	gatesRamTmpOptions = func() ramtmp.Options { return b924FakeMac(root, os.Getenv("CF_B924_FAIL")) }
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_GATES_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	code, _ := run(args)
	os.Exit(code)
}

func b924Call(t *testing.T, f b450Fixture, mac string, env []string, args ...string) (int, string, string) {
	t.Helper()
	encoded, _ := json.Marshal(args)
	exe, err := os.Executable()
	f.must(err)
	cmd := exec.Command(exe, "-test.run=^TestB924GatesProcess$")
	cmd.Dir = f.repo
	base := []string{}
	for _, kv := range envWithoutGit() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "CODE_FORGE_TMP_ROOT=") && !strings.HasPrefix(kv, "CODE_FORGE_RAM_TMP=") {
			base = append(base, kv)
		}
	}
	cmd.Env = append(append(base, "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_B924_FAKE_MAC="+mac, "CF_GATES_ARGS="+string(encoded)), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func b924Config(mode any) map[string]any {
	gates := map[string]any{"test": []string{"/bin/sh", "-c", `test -d "$TMPDIR" && printf %s "$TMPDIR"`}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}
	if mode != nil {
		gates["ram_tmp"] = mode
	}
	return map[string]any{"gates": gates}
}

func b924Calls(mac string) []string {
	data, _ := os.ReadFile(filepath.Join(mac, "calls"))
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(data)), " ", "_"))
}

// auto on a Mac: the first gates run attaches and erases once, the gate's
// TMPDIR is a fresh dir on the volume (removed after), the volume carries
// .fseventsd/no_log; the second run reuses the mounted volume.
func TestB924GatesRunAutoUsesRamDisk(t *testing.T) {
	for _, mode := range []any{"auto"} {
		f := b450Setup(t, b924Config(mode), nil)
		mac := t.TempDir()
		volume := filepath.Join(mac, ramtmp.Volume)
		for round := 1; round <= 2; round++ {
			code, out, errOut := b924Call(t, f, mac, nil, "gates", "run", "--run", "fixture", "--block", "B1")
			r := f.result(out)
			if code != 0 || !r.AllOK || len(r.Results) == 0 {
				t.Fatalf("mode %v round %d: exit=%d stderr=%q stdout=%s", mode, round, code, errOut, out)
			}
			tmp := r.Results[0].Stdout
			if filepath.Dir(tmp) != volume || !strings.HasPrefix(filepath.Base(tmp), "fixture-B1-") {
				t.Fatalf("mode %v round %d: gate TMPDIR = %q, want a fixture-B1-* dir under %s", mode, round, tmp, volume)
			}
			if _, err := os.Stat(tmp); !os.IsNotExist(err) {
				t.Errorf("the per-run TMPDIR %s must be removed after the gates; stat err %v", tmp, err)
			}
			if strings.Contains(errOut, "ram_tmp") {
				t.Errorf("no warn expected: %q", errOut)
			}
		}
		want := "hdiutil_attach_-nomount_ram://4194304 diskutil_erasevolume_APFS_cf-tmp_/dev/disk42"
		if got := strings.Join(b924Calls(mac), " "); got != want {
			t.Fatalf("mode %v: calls = %q, want attach+erase once then reuse: %q", mode, got, want)
		}
		if _, err := os.Stat(filepath.Join(volume, ".fseventsd", "no_log")); err != nil {
			t.Errorf(".fseventsd/no_log missing: %v", err)
		}
	}
}

// An attach error prints one warn line and the gate runs green on the
// normal TMPDIR.
func TestB924GatesRunRamDiskErrorFallsBack(t *testing.T) {
	f := b450Setup(t, b924Config("auto"), nil)
	mac := t.TempDir()
	code, out, errOut := b924Call(t, f, mac, []string{"CF_B924_FAIL=hdiutil attach"}, "gates", "run", "--run", "fixture", "--block", "B1")
	r := f.result(out)
	if code != 0 || !r.AllOK {
		t.Fatalf("a RAM disk error must never fail the gate: exit=%d stderr=%q stdout=%s", code, errOut, out)
	}
	if strings.HasPrefix(r.Results[0].Stdout, mac) || r.Results[0].Stdout == "" {
		t.Errorf("gate TMPDIR = %q, want the normal TMPDIR", r.Results[0].Stdout)
	}
	if n := strings.Count(errOut, "warn: gates.ram_tmp: hdiutil attach failed"); n != 1 || !strings.Contains(errOut, "the gates use the normal TMPDIR") {
		t.Errorf("want one warn line, got %d in %q", n, errOut)
	}
}

// off (config or CODE_FORGE_RAM_TMP) and a non-Mac OS run no command.
func TestB924GatesRunOffRunsNoCommand(t *testing.T) {
	cases := []struct {
		name string
		mode any
		env  []string
	}{{"config off", "off", nil}, {"env off", "auto", []string{"CF_B924_MODE=off"}}}
	for _, c := range cases {
		f := b450Setup(t, b924Config(c.mode), nil)
		mac := t.TempDir()
		code, out, errOut := b924Call(t, f, mac, c.env, "gates", "run", "--run", "fixture", "--block", "B1")
		r := f.result(out)
		if code != 0 || !r.AllOK || strings.HasPrefix(r.Results[0].Stdout, mac) {
			t.Fatalf("%s: exit=%d stderr=%q stdout=%s", c.name, code, errOut, out)
		}
		if calls := b924Calls(mac); len(calls) != 0 {
			t.Errorf("%s: calls = %q, want none", c.name, calls)
		}
	}
	saved := gatesRamTmpOptions
	t.Cleanup(func() { gatesRamTmpOptions = saved })
	mac := t.TempDir()
	gatesRamTmpOptions = func() ramtmp.Options { o := b924FakeMac(mac, ""); o.GOOS = "linux"; return o }
	t.Setenv("CODE_FORGE_RAM_TMP", "")
	if env, done := gatesRamTmpEnv(map[string]any{"ram_tmp": "auto"}, "r", "B1", nil); env != nil || len(b924Calls(mac)) != 0 {
		t.Errorf("linux: env %v calls %q", env, b924Calls(mac))
	} else {
		done()
	}
}

// The doctor row says on, off or the fallback reason, and never makes a disk.
func TestB924DoctorRamTmpRow(t *testing.T) {
	saved := gatesRamTmpOptions
	t.Cleanup(func() { gatesRamTmpOptions = saved })
	mac := t.TempDir()
	fake := b924FakeMac(mac, "")
	gatesRamTmpOptions = func() ramtmp.Options { return fake }
	t.Setenv("CODE_FORGE_RAM_TMP", "")
	check := func(cfg map[string]any, want string) {
		t.Helper()
		row := dfRamTmpRow(cfg)
		if row.id != "ram_tmp" || row.status != "INFO" || !strings.HasPrefix(row.detail, want) {
			t.Errorf("row = %+v, want detail starting %q", row, want)
		}
	}
	check(map[string]any{"gates": map[string]any{"ram_tmp": "off"}}, "off (gates.ram_tmp: off)")
	auto := map[string]any{"gates": map[string]any{"ram_tmp": "auto"}}
	check(map[string]any{}, "off (gates.ram_tmp: off)")
	check(auto, "on (auto): "+filepath.Join(mac, "cf-tmp")+" is made on the first gate")
	if len(b924Calls(mac)) != 0 {
		t.Fatalf("doctor ran %q", b924Calls(mac))
	}
	fake.LookPath = func(string) (string, error) { return "", errors.New("no") }
	check(auto, "fallback: hdiutil not found")
	fake.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if _, err := fake.Ensure(64); err != nil {
		t.Fatal(err)
	}
	check(map[string]any{"gates": map[string]any{"ram_tmp": "auto"}}, "on: "+filepath.Join(mac, "cf-tmp")+" mounted, .fseventsd/no_log present")
	t.Setenv("CODE_FORGE_RAM_TMP", "off")
	check(auto, "off (CODE_FORGE_RAM_TMP=off)")
}

// B791 rule for B924: config fix knows gates.ram_tmp (no unknown-key line),
// keeps it, and still fixes an old shape next to it.
func TestB924ConfigFixKeepsRamTmp(t *testing.T) {
	old := strings.Replace(b791Old, "  format: null\n", "  format: null\n  ram_tmp: off # no RAM disk\n", 1)
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}

// B940: with no gates.ram_tmp in the config a gates run on a Mac keeps the
// normal TMPDIR and runs no hdiutil or diskutil command; the doctor row says off.
func TestB940GatesRunDefaultIsOff(t *testing.T) {
	f := b450Setup(t, b924Config(nil), nil)
	mac := t.TempDir()
	code, out, errOut := b924Call(t, f, mac, nil, "gates", "run", "--run", "fixture", "--block", "B1")
	r := f.result(out)
	if code != 0 || !r.AllOK || len(r.Results) == 0 || r.Results[0].Stdout == "" || strings.HasPrefix(r.Results[0].Stdout, mac) {
		t.Fatalf("exit=%d stderr=%q stdout=%s", code, errOut, out)
	}
	if calls := b924Calls(mac); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
	saved := gatesRamTmpOptions
	t.Cleanup(func() { gatesRamTmpOptions = saved })
	gatesRamTmpOptions = func() ramtmp.Options { return b924FakeMac(mac, "") }
	t.Setenv("CODE_FORGE_RAM_TMP", "")
	if row := dfRamTmpRow(map[string]any{}); row.detail != "off (gates.ram_tmp: off)" {
		t.Errorf("doctor row = %+v", row)
	}
}
