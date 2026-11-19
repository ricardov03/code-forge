package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B829: the customer's case (Laravel 13 + Pest, 4 coders, 0.25.1). The repo
// had no .code-forge/ line in .gitignore, the proof export held a copied
// Composer vendor/ and the queue held tickets; every block open copied all
// of it into tree_at_open and the run JSON grew ~150,000 lines.

// b829Laravel commits the customer's Laravel shape (its .gitignore without
// the .code-forge/ line) into the fixture repository.
func b829Laravel(t *testing.T, top string) {
	t.Helper()
	files := map[string]string{
		".gitignore":          b828LaravelIgnore,
		"composer.json":       `{"name":"acme/app","require":{"php":"^8.3","laravel/framework":"^13.0"},"require-dev":{"pestphp/pest":"^4.0"}}` + "\n",
		"artisan":             "#!/usr/bin/env php\n<?php\n",
		"app/Models/User.php": "<?php\nnamespace App\\Models;\nclass User {}\n",
		"tests/Pest.php":      "<?php\n",
	}
	for rel, body := range files {
		closeFixtureWrite(t, filepath.Join(top, rel), []byte(body))
	}
	b828MustGit(t, top, "add", ".")
	b828MustGit(t, top, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "laravel")
}

// b829StateFolder writes what the customer's .code-forge/ held: a proof
// export of B1 with a copied vendor/ of 5,000 files and 20 queue tickets.
func b829StateFolder(t *testing.T, top string) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		file := filepath.Join(top, ".code-forge", "export", "B1", "vendor", fmt.Sprintf("pkg%02d", i/100), fmt.Sprintf("File%04d.php", i))
		closeFixtureWrite(t, file, []byte("<?php\n"))
	}
	closeFixtureWrite(t, filepath.Join(top, ".code-forge", "export", "B1", ".env"), []byte("APP_KEY=base64:secret\n"))
	for i := 0; i < 20; i++ {
		closeFixtureWrite(t, filepath.Join(top, ".code-forge", "queue", fmt.Sprintf("t-%02d.json", i)), []byte(`{"ticket":"t"}`+"\n"))
	}
}

func TestB829TreeAtOpenSkipsTheStateFolder(t *testing.T) {
	f, acceptance := b581Fixture(t)
	b829Laravel(t, f.top)
	b829StateFolder(t, f.top)
	// a real dirty file outside .code-forge/, present before any open
	closeFixtureWrite(t, filepath.Join(f.top, "app", "Models", "User.php"), []byte("<?php\nnamespace App\\Models;\nclass User { /* local */ }\n"))
	if !b828Visible(t, f.top) {
		t.Fatal("the sample must be the customer's: git sees .code-forge/")
	}
	for i := 2; i <= 9; i++ { // the customer's 8 blocks
		id := fmt.Sprintf("B%d", i)
		res := b451Run(t, f.home, "open", id, "--run", "r-close-fixture", "--level", "L1", "--owned", fmt.Sprintf("app/Card%d.php", i), "--acceptance", acceptance)
		if res.code != 0 {
			t.Fatalf("open %s: exit %d stdout %q stderr %q", id, res.code, res.stdout, res.stderr)
		}
	}
	code, stdout, stderr := runVerbCall(t, "status", "--run", "r-close-fixture")
	if code != 0 {
		t.Fatalf("run status: %d %q", code, stderr)
	}
	status, err := ledger.ParseObject([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := status.Get("blocks").(ledger.Object)
	for i := 2; i <= 9; i++ {
		id := fmt.Sprintf("B%d", i)
		atOpen, ok := blockObject(blocks.Get(id)).Get("tree_at_open").(ledger.Object)
		if !ok {
			t.Fatalf("%s has no tree_at_open in run status", id)
		}
		for _, field := range atOpen {
			if gitx.IsStatePath(field.Key) {
				t.Fatalf("%s tree_at_open lists %s (%d keys)", id, field.Key, len(atOpen))
			}
		}
		if atOpen.Get("app/Models/User.php") == nil {
			t.Fatalf("%s tree_at_open lost the real dirty file: %v", id, atOpen)
		}
	}
	mirror := readText(t, filepath.Join(f.top, ".code-forge", "runs", "r-close-fixture.json"))
	if lines := strings.Count(mirror, "\n"); lines >= 2000 {
		t.Fatalf("the run mirror has %d lines", lines)
	}
	// close's unowned-change check: the dirty file from before the open is
	// excused, the state folder is never a change, a file changed after the
	// open is still caught.
	closeFixtureWrite(t, filepath.Join(f.top, ".code-forge", "export", "B1", "vendor", "late.php"), []byte("<?php\n"))
	closeFixtureWrite(t, filepath.Join(f.top, "app", "Models", "Late.php"), []byte("<?php\n"))
	base := strings.TrimSpace(b828MustGit(t, f.top, "rev-parse", "HEAD~1"))
	if text, _ := blockObject(blocks.Get("B2")).Get("base_sha").(string); text != "" {
		base = text
	}
	changed, err := gitx.ChangedFiles(f.top, base)
	if err != nil {
		t.Fatal(err)
	}
	unowned, _, checked := gatecheck.Scope(f.top, "", "B2", "r-close-fixture", blocks, changed, nil, nil, false)
	if !checked || strings.Join(unowned, ",") != "app/Models/Late.php" {
		t.Fatalf("unowned at close = %v (checked %v); changed %d files", unowned, checked, len(changed))
	}
}

// b828Visible is git's own answer: a file in the export is not ignored.
func b828Visible(t *testing.T, dir string) bool {
	t.Helper()
	return !b828Ignored(t, dir, ".code-forge/export/B1/vendor/pkg00/File0000.php")
}
