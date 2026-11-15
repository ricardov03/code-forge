package canon

import (
	"encoding/json"
	"testing"
)

// TestB722NilContainersAreNull: a nil map or slice is JSON null, as
// encoding/json writes it and as Node's canonicalJSON reads a null config.
// B696's direct walk wrote {} and [] for them.
func TestB722NilContainersAreNull(t *testing.T) {
	var m map[string]any
	var a []any
	var s []string
	for _, v := range []any{m, a, s, map[string]any{"k": m, "l": a, "s": s}, []any{m, a, s}} {
		want, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var norm any
		_ = json.Unmarshal(want, &norm)
		wantCanon, _ := Marshal(norm)
		if string(got) != string(wantCanon) {
			t.Fatalf("%#v: got %s want %s", v, got, wantCanon)
		}
	}
}
