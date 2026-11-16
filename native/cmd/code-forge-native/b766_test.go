package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B766: the B762 goldens hold on Linux CI. The rows below are the CI text of
// run 43780274030 (Go on Linux) and the recorded Mac golden of the same case.

func TestB766HostNormSameOnLinuxAndMac(t *testing.T) {
	pairs := [][2]string{
		// compact form (B502 .errors)
		{
			`[{"arch":"x64","before":[],"cleaned":[],"exit":1,"flags":[],"fp":"6bb54ee69f44","kind":"error","platform":"linux","verb":"init"}]`,
			`[{"arch":"arm64","before":[],"cleaned":[],"exit":1,"flags":[],"fp":"6bb54ee69f44","kind":"error","platform":"darwin","verb":"init"}]`,
		},
		// indented form (B458/B462/B481 .errors)
		{
			"[\n {\n  \"arch\": \"x64\",\n  \"exit\": 2,\n  \"platform\": \"linux\",\n  \"verb\": \"proof\"\n }\n]\n",
			"[\n {\n  \"arch\": \"arm64\",\n  \"exit\": 2,\n  \"platform\": \"darwin\",\n  \"verb\": \"proof\"\n }\n]\n",
		},
	}
	for _, p := range pairs {
		linux, mac := b762HostNorm(p[0]), b762HostNorm(p[1])
		if linux != mac {
			t.Fatalf("host rows differ after the mask:\n linux: %q\n mac:   %q", linux, mac)
		}
		for _, host := range []string{"x64", "arm64", "linux", "darwin"} {
			if strings.Contains(linux, `"`+host+`"`) {
				t.Fatalf("%q left in %q", host, linux)
			}
		}
	}
	// a non-host field still counts: the mask is not a blanket pass
	a := b762HostNorm(`{"arch":"x64","exit":1}`)
	b := b762HostNorm(`{"arch":"arm64","exit":2}`)
	if a == b {
		t.Fatalf("exit codes 1 and 2 compare equal: %q", a)
	}
}

// TestB766B548GoldenOnLinuxHost runs a B548 tools-step case the way Linux CI
// ran it (toolOS linux, no Solo.app): it differs from the Mac golden exactly
// in the CI's three tool rows; with b762MacTools it matches the golden.
func TestB766B548GoldenOnLinuxHost(t *testing.T) {
	var c b548Case
	for _, s := range b548Shapes {
		if s.name == "tools step, no npm" {
			c = s.b548Case
		}
	}
	if c.name == "" {
		t.Fatal("no B548 case \"tools step, no npm\"")
	}
	golden, err := os.ReadFile(filepath.Join(b762Dir, "B548InitStepsMatchNode", b762Slug(c.name)+".initlog"))
	if err != nil {
		t.Fatal(err)
	}

	savedOS, savedApps := toolOS, toolSystemApps
	toolOS, toolSystemApps = "linux", filepath.Join(t.TempDir(), "no-apps")
	linux := b548Go(t, c)
	toolOS, toolSystemApps = savedOS, savedApps
	for _, row := range []string{
		"tool grok: missing — https://x.ai/build (or: code-forge tools install)",
		"tool op: missing — https://developer.1password.com/docs/cli/get-started/ (or: code-forge tools install)",
		"tool solo: missing — https://soloterm.com (desktop app, then add its MCP entry) (or: code-forge tools install)",
	} {
		if !strings.Contains(linux.log, row) {
			t.Fatalf("Linux run lacks the CI row %q:\n%s", row, linux.log)
		}
	}
	if linux.log == string(golden) {
		t.Fatal("the Linux run already matches the Mac golden: this test no longer shows the CI host")
	}

	t.Run("pinned", func(t *testing.T) {
		toolOS, toolSystemApps = "linux", filepath.Join(t.TempDir(), "no-apps")
		t.Cleanup(func() { toolOS, toolSystemApps = savedOS, savedApps })
		b762MacTools(t)
		if got := b548Go(t, c); got.log != string(golden) {
			t.Fatalf("pinned run differs from the golden:\n go:     %q\n golden: %q", got.log, golden)
		}
	})
}
