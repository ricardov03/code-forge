package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitAndTreeHelpers(t *testing.T) {
	t.Setenv("GIT_DIR", "/nonexistent")
	t.Setenv("GIT_INDEX_FILE", "/nonexistent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent")
	root := t.TempDir()
	g := func(args ...string) string {
		t.Helper()
		out, err := Run(root, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	g("init", "-qb", "main")
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	g("add", "file")
	g("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := g("rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	files, err := ChangedFiles(root, base)
	if err != nil || !reflect.DeepEqual(files, []string{"file", "link"}) {
		t.Fatalf("want exactly file, link: %v %v", files, err)
	}
	atOpen, err := TreeAtOpen(root, base)
	if err != nil || len(atOpen) != 2 {
		t.Fatalf("two opening stamps: %v %v", atOpen, err)
	}
	if got := TreeStamp(root, "file"); got != "file:x:"+hash([]byte("changed")) || atOpen.Get("file") != got {
		t.Fatalf("executable streamed hash: %s %v", got, atOpen)
	}
	if got := TreeStamp(root, "link"); got != "link:"+hash([]byte("file")) {
		t.Fatalf("link target hash: %s", got)
	}
	if TreeStamp(root, "missing") != "deleted" || TreeStamp(root, "file/child") != Unreadable {
		t.Fatal("ENOENT is deleted; ENOTDIR is unreadable")
	}
	if SameStamp(Unreadable, Unreadable) || !SameStamp("deleted", "deleted") {
		t.Fatal("unreadable stamps must never compare equal")
	}
	if got, err := ContentHash(root, "file"); err != nil || got != hash([]byte("changed")) {
		t.Fatalf("content hash: %s %v", got, err)
	}
	if got, err := ContentHash(root, "missing"); err != nil || got != DeletedHash {
		t.Fatalf("deleted hash: %s %v", got, err)
	}
	for _, file := range []string{"link", "../escape", ".git/config"} {
		if _, err := ContentHash(root, file); err == nil {
			t.Errorf("unsafe hash accepted: %s", file)
		}
	}
	if _, err := ChangedFiles(root, "HEAD"); err == nil || err.Error() != "the block base must be a commit id" {
		t.Fatalf("bad-ref: %v", err)
	}
	output := filepath.Join(t.TempDir(), "injected-diff")
	for _, invalid := range []string{"", "HEAD", "-x", "--output=" + output, "abcdef", strings.Repeat("a", 65), strings.ToUpper(base)} {
		t.Run("bad-base/"+invalid, func(t *testing.T) {
			files, err := TreeAtOpen(root, invalid)
			badRef, ok := err.(*Error)
			if files != nil || !ok || badRef.Code != "bad-ref" || badRef.Message != "the block base must be a commit id" {
				t.Errorf("TreeAtOpen(%q) = %v, %v; want nil, bad-ref", invalid, files, err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Errorf("base option created output file: %v", err)
			}
		})
	}
	files = []string{"\ue000", "😀", "a"}
	SortStrings(files)
	if !reflect.DeepEqual(files, []string{"a", "😀", "\ue000"}) {
		t.Fatalf("UTF-16 order: %v", files)
	}
}
