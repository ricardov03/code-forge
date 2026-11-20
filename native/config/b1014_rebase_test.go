package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1014RebaseConfig(t *testing.T) {
	base := "version: 1\nprovider: anthropic\nlevels:\n  L0: {model: claude-haiku-4-5}\n  L1: {model: claude-sonnet-4-5}\n  L2: {model: claude-opus-4-1}\n  L3: {model: claude-opus-4-1}\n"
	for _, value := range []string{"[]", "[design/approved/**, public/images/**]"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, Filename), []byte(base+"rebase:\n  approved_binary_sources: "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadValidated(dir, "0.0.0"); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"[../outside/**]", "[/absolute]", "[design/../outside]", "['']", "[.git/**]", "[.code-forge/**]", "[42]", "true"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, Filename), []byte(base+"rebase:\n  approved_binary_sources: "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadValidated(dir, "0.0.0"); err == nil || !strings.Contains(err.Error(), "approved_binary_sources") {
			t.Errorf("must refuse %s: %v", value, err)
		}
	}
}
