package ramtmp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMac is a darwin Options whose volume root is a temp dir and whose
// commands are recorded; "mounting" makes the volume directory.
type fakeMac struct {
	calls   []string
	mounted map[string]bool
	attach  string
	failOn  string
}

func (f *fakeMac) options(t *testing.T) Options {
	root := t.TempDir()
	return Options{
		GOOS: "darwin",
		Root: root,
		Run: func(name string, args ...string) (string, error) {
			line := name + " " + strings.Join(args, " ")
			f.calls = append(f.calls, line)
			if f.failOn != "" && strings.HasPrefix(line, f.failOn) {
				return "", errors.New("exit status 1: boom")
			}
			switch {
			case name == "hdiutil" && args[0] == "attach":
				return f.attach, nil
			case name == "diskutil":
				path := filepath.Join(root, Volume)
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				f.mounted[path] = true
			}
			return "", nil
		},
		Mounted:  func(path string) bool { return f.mounted[path] },
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}
}

func newFake() *fakeMac {
	return &fakeMac{mounted: map[string]bool{}, attach: "/dev/disk9          \t\n"}
}

func TestB924EnsureAttachesOnceThenReuses(t *testing.T) {
	f := newFake()
	o := f.options(t)
	path, err := o.Ensure(512)
	if err != nil || path != filepath.Join(o.Root, Volume) {
		t.Fatalf("Ensure = %q, %v", path, err)
	}
	want := []string{"hdiutil attach -nomount ram://1048576", "diskutil erasevolume APFS cf-tmp /dev/disk9"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", f.calls, want)
	}
	for _, name := range []string{".fseventsd/no_log", ".metadata_never_index"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	again, err := o.Ensure(512)
	if err != nil || again != path || len(f.calls) != 2 {
		t.Fatalf("second Ensure = %q, %v, calls %q (want reuse, no new command)", again, err, f.calls)
	}
}

func TestB924EnsureReusesMountedVolumeAndMarksIt(t *testing.T) {
	f := newFake()
	o := f.options(t)
	path := filepath.Join(o.Root, Volume)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	f.mounted[path] = true
	got, err := o.Ensure(0)
	if err != nil || got != path || len(f.calls) != 0 {
		t.Fatalf("Ensure = %q, %v, calls %q", got, err, f.calls)
	}
	if _, err := os.Stat(filepath.Join(path, ".fseventsd", "no_log")); err != nil {
		t.Errorf("no_log not written on a reused volume: %v", err)
	}
}

func TestB924EnsureErrorsDetachAndReport(t *testing.T) {
	f := newFake()
	f.failOn = "diskutil"
	o := f.options(t)
	if path, err := o.Ensure(64); err == nil || path != "" || !strings.Contains(err.Error(), "diskutil erasevolume failed") {
		t.Fatalf("Ensure = %q, %v", path, err)
	}
	if last := f.calls[len(f.calls)-1]; last != "hdiutil detach /dev/disk9" {
		t.Errorf("a failed erase must detach the RAM device; calls %q", f.calls)
	}
	g := newFake()
	g.attach = "garbage"
	if _, err := g.options(t).Ensure(64); err == nil || !strings.Contains(err.Error(), "no /dev/disk device") {
		t.Fatalf("bad attach output: %v", err)
	}
	h := newFake()
	h.failOn = "hdiutil attach"
	if _, err := h.options(t).Ensure(64); err == nil || !strings.Contains(err.Error(), "hdiutil attach failed") {
		t.Fatalf("attach failure: %v", err)
	}
}

func TestB924EnsureIsNoOpOffMac(t *testing.T) {
	f := newFake()
	o := f.options(t)
	o.GOOS = "linux"
	if path, err := o.Ensure(64); path != "" || err != nil || len(f.calls) != 0 {
		t.Fatalf("linux Ensure = %q, %v, calls %q", path, err, f.calls)
	}
}

func TestB924EnabledAndStatus(t *testing.T) {
	cases := []struct {
		mode any
		goos string
		on   bool
	}{{nil, "darwin", true}, {"auto", "darwin", true}, {"auto", "linux", false}, {"off", "darwin", false}, {"yes", "darwin", false}}
	for _, c := range cases {
		if on, _ := Enabled(c.mode, c.goos); on != c.on {
			t.Errorf("Enabled(%v, %s) = %v", c.mode, c.goos, on)
		}
	}
	f := newFake()
	o := f.options(t)
	if s := o.Status("off"); s != "off (gates.ram_tmp: off)" {
		t.Errorf("off status %q", s)
	}
	if s := o.Status(nil); !strings.HasPrefix(s, "on (auto)") || len(f.calls) != 0 {
		t.Errorf("auto status %q calls %q", s, f.calls)
	}
	o.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if s := o.Status(nil); !strings.HasPrefix(s, "fallback: hdiutil not found") {
		t.Errorf("fallback status %q", s)
	}
	if _, err := o.Ensure(64); err != nil {
		t.Fatal(err)
	}
	if s := o.Status("auto"); !strings.Contains(s, "no_log present") {
		t.Errorf("mounted status %q", s)
	}
}

func TestB924DirIsFreshPerRun(t *testing.T) {
	root := t.TempDir()
	a, err := Dir(root, "r1/../B9")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Dir(root, "r1/../B9")
	if a == b || filepath.Dir(a) != root || !strings.HasPrefix(filepath.Base(a), "r1_.._B9-") {
		t.Fatalf("Dir = %q, %q", a, b)
	}
}
