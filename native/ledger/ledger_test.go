package ledger_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile("../../test/compat/golden/ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Slug   string
		Files        map[string]string
		Current, All []ledger.Row
		Error        bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 8 {
		t.Fatalf("ledger cases: got %d, want 8", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range c.Files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, read := range []struct {
				name string
				fn   func(string, string) ([]ledger.Row, error)
				want []ledger.Row
			}{{"current", ledger.ReadRows, c.Current}, {"all", ledger.ReadAllRows, c.All}} {
				got, err := read.fn(dir, c.Slug)
				if (err != nil) != c.Error {
					t.Fatalf("%s error: got %v, want error=%t", read.name, err, c.Error)
				}
				if !c.Error && !reflect.DeepEqual(got, read.want) {
					t.Errorf("%s rows: got %d %#v, want %d %#v", read.name, len(got), got, len(read.want), read.want)
				}
			}
			for name, text := range c.Files {
				after, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(after) != text {
					t.Errorf("read changed %s: err=%v", name, err)
				}
			}
		})
	}
}
