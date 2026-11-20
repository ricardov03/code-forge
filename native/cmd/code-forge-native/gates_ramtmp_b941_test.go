package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// B941: gates.ram_tmp_mb sets the size of the RAM disk a gates run makes
// (sectors = MB * 2048); without it the default size is asked for.
func TestB941RamTmpSizeReachesEnsure(t *testing.T) {
	for _, c := range []struct {
		size any
		want string
	}{{512, "ram://1048576"}, {nil, "ram://4194304"}} {
		cfg := b924Config("auto")
		if c.size != nil {
			cfg["gates"].(map[string]any)["ram_tmp_mb"] = c.size
		}
		f := b450Setup(t, cfg, nil)
		mac := t.TempDir()
		code, out, errOut := b924Call(t, f, mac, nil, "gates", "run", "--run", "fixture", "--block", "B1")
		if r := f.result(out); code != 0 || !r.AllOK {
			t.Fatalf("size %v: exit=%d stderr=%q stdout=%s", c.size, code, errOut, out)
		}
		calls := b924Calls(mac)
		if len(calls) == 0 || calls[0] != "hdiutil_attach_-nomount_"+c.want {
			t.Errorf("size %v: calls = %q, want attach with %s", c.size, calls, c.want)
		}
	}
}

// B791 rule for B941: config fix knows gates.ram_tmp_mb and keeps it.
func TestB941ConfigFixKeepsRamTmpMB(t *testing.T) {
	old := strings.Replace(b791Old, "  format: null\n", "  format: null\n  ram_tmp_mb: 1024 # small Mac\n", 1)
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}

// B941: init writes gates.ram_tmp_mb after gates.ram_tmp and keeps a size
// already set.
func TestB941InitWritesRamTmpMB(t *testing.T) {
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	if cfg := b939Config(t, side); !strings.Contains(cfg, "\n  ram_tmp_mb: 2048\nproof:\n") {
		t.Fatalf("gates.ram_tmp_mb: 2048 is not in the new config:\n%s", cfg)
	}
	side = b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, b527Files(map[string]string{
		".code-forge.yml": "version: 1\ngates:\n  ram_tmp_mb: 512\n",
	}))
	if cfg := b939Config(t, side); !strings.Contains(cfg, "  ram_tmp_mb: 512\n") || strings.Count(cfg, "ram_tmp_mb:") != 1 {
		t.Fatalf("init changed gates.ram_tmp_mb: 512:\n%s", cfg)
	}
}
