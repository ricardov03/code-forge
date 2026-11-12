package review

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type fixture struct {
	Commit map[string]string `json:"commit"`
	After  map[string]string `json:"after"`
	Cases  []struct {
		Name   string         `json:"name"`
		File   string         `json:"file"`
		Lens   string         `json:"lens"`
		Digest string         `json:"digest"`
		Facts  string         `json:"facts"`
		Cfg    map[string]any `json:"cfg"`
	} `json:"cases"`
}

func useRepoLenses(t *testing.T) {
	t.Helper()
	old := LensDir
	dir, err := filepath.Abs(filepath.Join("..", "..", "src", "review", "lenses"))
	if err != nil {
		t.Fatal(err)
	}
	LensDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { LensDir = old })
}

func readFixture(t *testing.T, name string) fixture {
	t.Helper()
	var fx fixture
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	return fx
}

// buildRepo makes the same repository the Node golden generator built.
func buildRepo(t *testing.T, fx fixture) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	put := func(files map[string]string) {
		for f, c := range files {
			full := filepath.Join(dir, f)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q")
	put(fx.Commit)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	put(fx.After)
	return dir
}

// TestPacketMatchesNodeGolden: every packet, refusal and size field equals what
// Node's readFileDiff + assemblePacket gave on the same repository.
func TestPacketMatchesNodeGolden(t *testing.T) {
	useRepoLenses(t)
	fx := readFixture(t, "packet_fixture.json")
	var golden map[string]map[string]any
	data, err := os.ReadFile(filepath.Join("testdata", "packet_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	repo := buildRepo(t, fx)
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			want := golden[c.Name]
			got := map[string]any{}
			d, err := ReadFileDiff(repo, c.File, "")
			if err == nil && trimJS(d.DiffText) == "" {
				got["status"] = "no_change"
			} else if err == nil {
				var p *Packet
				p, err = AssemblePacket(d, c.Lens, c.Digest, c.Facts, c.Cfg, "", "")
				if err == nil {
					got["status"] = p.Status
					got["tokensIn"] = float64(p.TokensIn)
					got["budget"] = float64(p.Budget)
					got["plusCount"] = float64(d.PlusCount)
					got["minusCount"] = float64(d.MinusCount)
					got["kind"] = d.Kind
					if p.Status == "ok" {
						got["text"] = p.Text
						headers := []any{}
						for _, h := range p.HunkHeaders {
							headers = append(headers, h)
						}
						got["hunkHeaders"] = headers
						got["contextMode"] = p.ContextMode
						got["contextLines"] = float64(p.ContextLine)
						got["digestLines"] = float64(p.DigestLines)
						got["overBudget"] = p.OverBudget
					}
				}
			}
			if err != nil {
				var pe *PacketError
				if !errors.As(err, &pe) {
					t.Fatalf("unexpected error %v", err)
				}
				got = map[string]any{"error": pe.Code, "message": pe.Message}
			}
			for k, w := range want {
				g, _ := json.Marshal(got[k])
				wb, _ := json.Marshal(w)
				if string(g) != string(wb) {
					t.Errorf("%s: got %s, Node %s", k, g, wb)
				}
			}
			if len(got) != len(want) {
				t.Errorf("fields: got %d, Node %d", len(got), len(want))
			}
		})
	}
}
