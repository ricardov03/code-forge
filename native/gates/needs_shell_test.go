package gates

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNeedsShell(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("plain", "printf x\n", 0o755)
	write("bang", "#!/bin/sh\nprintf x\n", 0o755)
	write("noexec", "printf x\n", 0o644)
	for _, c := range []struct {
		argv []string
		want bool
	}{
		{[]string{"./plain"}, true},
		{[]string{"./bang"}, false},
		{[]string{"./noexec"}, false},
		{[]string{"./missing"}, false},
		{[]string{"/bin/sh", "-c", "true"}, false},
		{[]string{"{test_files}"}, false},
	} {
		if got := NeedsShell(dir, &Command{Argv: c.argv}); got != c.want {
			t.Errorf("NeedsShell(%v) = %v, want %v", c.argv, got, c.want)
		}
	}
}
