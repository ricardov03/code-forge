package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// B989: CODE_FORGE_INIT_GOLDEN_RECORD=1 records the init goldens again from
// the Go output (like CODE_FORGE_B801_RECORD for the doc example pins), so a
// config default that changes what `code-forge init` writes is one command:
//
//	CODE_FORGE_INIT_GOLDEN_RECORD=1 go test ./cmd/code-forge-native -run '^(TestB528InitWizardMatchesNode|TestB611InitWizardJevAndDoctorMatchNode|TestB703InitWizardMatchesNode|TestB726InitWizardMatchesNode|TestB762.*Golden)$' -count=1
//
// It covers testdata/b528, b611, b703, b726 (wizard JSON goldens) and b762
// (field goldens). Only a golden that differs is written, so a record on an
// unchanged tree leaves every file as it is; a b762 golden of no case is
// removed. Review the diff before committing: the goldens were Node's output.
const b989RecordEnv = "CODE_FORGE_INIT_GOLDEN_RECORD"

func b989Recording() bool { return os.Getenv(b989RecordEnv) == "1" }

func b989Write(t *testing.T, file string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %s", file)
}

// b989WizardCheck compares one wizard run with its JSON golden (b528, b611,
// b703, b726). Recording writes got in the goldens' format when they differ.
func b989WizardCheck(t *testing.T, file string, want, got b528Golden) {
	t.Helper()
	want.Files = b607LinkVersion(t, b552SkillRows(t, want.Files, ""))
	if b989Recording() {
		if got != want {
			data, _ := json.MarshalIndent(got, "", "  ")
			b989Write(t, file, append(data, '\n'))
		}
		return
	}
	if got.Code != want.Code {
		t.Errorf("exit %d, Node %d", got.Code, want.Code)
	}
	if got.Transcript != want.Transcript {
		t.Errorf("transcript differs from Node:\n%s", b528Diff(want.Transcript, got.Transcript))
	}
	if got.Log != want.Log {
		t.Errorf("init log differs from Node:\n%s", b528Diff(want.Log, got.Log))
	}
	if got.Files != want.Files {
		t.Errorf("files differ from Node:\n%s", b528Diff(want.Files, got.Files))
	}
	if t.Failed() {
		t.Logf("%s: if the change is intended, %s=1 records the goldens again", file, b989RecordEnv)
	}
}

// b989CopyDir copies the regular files of src into a new temp dir.
func b989CopyDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func b989Snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = string(data)
		}
		return nil
	})
	return out
}

// A stale wizard golden is recorded back from Go's output; the goldens Go
// still matches are not touched; the next plain run passes.
func TestB989RecordRewritesWizardGolden(t *testing.T) {
	orig := b528Dir
	t.Cleanup(func() { b528Dir = orig })
	b528Dir = b989CopyDir(t, orig)
	before := b989Snapshot(t, b528Dir)
	stale := filepath.Join(b528Dir, "defaults.json")
	var g b528Golden
	if err := json.Unmarshal([]byte(before["defaults.json"]), &g); err != nil {
		t.Fatal(err)
	}
	g.Log += "stale line from an old default\n"
	data, _ := json.MarshalIndent(g, "", "  ")
	if err := os.WriteFile(stale, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := map[string]int64{}
	for name := range before {
		info, _ := os.Stat(filepath.Join(b528Dir, name))
		stamp[name] = info.ModTime().UnixNano()
	}

	t.Setenv(b989RecordEnv, "1")
	TestB528InitWizardMatchesNode(t)
	after := b989Snapshot(t, b528Dir)
	for name, text := range before {
		if after[name] != text {
			t.Errorf("%s after record differs from the checked-in golden", name)
		}
		if name == "defaults.json" {
			continue
		}
		if info, _ := os.Stat(filepath.Join(b528Dir, name)); info.ModTime().UnixNano() != stamp[name] {
			t.Errorf("%s was written although Go matched it", name)
		}
	}

	t.Setenv(b989RecordEnv, "")
	TestB528InitWizardMatchesNode(t)
}

// A stale b762 field golden is recorded back, a missing one is written and
// a golden of no case is removed; the next plain run passes.
func TestB989RecordRewritesB762Golden(t *testing.T) {
	const test = "B502InitRefusalsMatchNode"
	orig := b762Dir
	t.Cleanup(func() { b762Dir = orig })
	b762Dir = b989CopyDir(t, orig)
	dir := filepath.Join(b762Dir, test)
	before := b989Snapshot(t, dir)
	if len(before) == 0 {
		t.Fatal("no goldens copied")
	}
	if err := os.WriteFile(filepath.Join(dir, "unknown-flag.stderr"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "unknown-flag.code")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone-case.stdout"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(b989RecordEnv, "1")
	TestB762B502InitRefusalsGolden(t)
	after := b989Snapshot(t, dir)
	if len(after) != len(before) {
		t.Errorf("%d goldens after record, want %d", len(after), len(before))
	}
	for name, text := range before {
		if after[name] != text {
			t.Errorf("%s after record differs from the checked-in golden", name)
		}
	}

	t.Setenv(b989RecordEnv, "")
	TestB762B502InitRefusalsGolden(t)
}
