package review

import (
	"bufio"
	"os"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB514DiagnoseAnswerMatchesNode replays Node's diagnoseAnswer over
// truncated, corrupted and schema-invalid answers (testdata made by a scratch
// script importing src/review/schema-fallback.mjs): the schema-miss row's
// diagnosis must be byte-equal.
func TestB514DiagnoseAnswerMatchesNode(t *testing.T) {
	f, err := os.Open("testdata/b514_diagnose.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	n, bad := 0, 0
	for sc.Scan() {
		line, err := ledger.DecodeObject(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		res, _ := get(line, "res").(ledger.Object)
		want, _ := ledger.Stringify(get(line, "want"), false)
		got, _ := ledger.Stringify(DiagnoseAnswer(res), false)
		n++
		if string(got) != string(want) {
			bad++
			if bad <= 10 {
				in, _ := ledger.Stringify(res, false)
				t.Errorf("res %s\n got %s\nwant %s", in, got, want)
			}
		}
	}
	if n < 1000 {
		t.Fatalf("only %d golden lines", n)
	}
	if bad > 0 {
		t.Fatalf("%d of %d diagnoses differ from Node", bad, n)
	}
}
