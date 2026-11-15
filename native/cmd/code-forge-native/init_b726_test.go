package main

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// B726: Go's text width, wrap and NFC against the values Node gave
// (testdata/b726, recorded by TestB726RecordNode).

type b726Data struct {
	Node       string            `json:"node"`
	Inputs     []json.RawMessage `json:"inputs"`
	Want       []json.RawMessage `json:"want"`
	Codepoints []json.RawMessage `json:"codepoints"`
}

func b726Load(t *testing.T, name string) b726Data {
	t.Helper()
	data, err := os.ReadFile(b726TestdataFile(name))
	if err != nil {
		t.Fatal(err)
	}
	var d b726Data
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Inputs) != len(d.Want) || len(d.Inputs) == 0 {
		t.Fatalf("%s: %d inputs, %d answers", name, len(d.Inputs), len(d.Want))
	}
	return d
}

func b726Surrogate(r rune) bool { return r >= 0xD800 && r <= 0xDFFF }

// TestB726WidthMatchesNode: every code point alone and a corpus of mixed
// strings (emoji sequences, CJK, marks, ZWJ, escapes, controls) have the
// width fast-string-width gives in Node.
func TestB726WidthMatchesNode(t *testing.T) {
	d := b726Load(t, "width.json")
	bad := 0
	for i, raw := range d.Inputs {
		var s string
		var want int
		_ = json.Unmarshal(raw, &s)
		_ = json.Unmarshal(d.Want[i], &want)
		if got := initWizWidth(s); got != want {
			if bad++; bad <= 20 {
				t.Errorf("width(%q) = %d, Node %d", s, got, want)
			}
		}
	}
	seen := 0
	for _, raw := range d.Codepoints {
		var run [3]int
		_ = json.Unmarshal(raw, &run)
		for cp := rune(run[0]); cp <= rune(run[1]); cp++ {
			if b726Surrogate(cp) {
				continue
			}
			seen++
			if got := initWizWidth(string(cp)); got != run[2] {
				if bad++; bad <= 40 {
					t.Errorf("width(U+%04X) = %d, Node %d", cp, got, run[2])
				}
			}
		}
	}
	if seen != 0x110000-0x800 {
		t.Fatalf("Node data covers %d code points", seen)
	}
	if bad > 0 {
		t.Fatalf("%d widths differ from Node %s", bad, d.Node)
	}
}

// TestB726WrapMatchesNode: wrapAnsi(text, cols, {hard: true, trim:
// false}) of wide and styled text, byte-equal to Node.
func TestB726WrapMatchesNode(t *testing.T) {
	d := b726Load(t, "wrap.json")
	bad := 0
	for i, raw := range d.Inputs {
		var in [2]json.RawMessage
		var s, want string
		var cols int
		_ = json.Unmarshal(raw, &in)
		_ = json.Unmarshal(in[0], &s)
		_ = json.Unmarshal(in[1], &cols)
		_ = json.Unmarshal(d.Want[i], &want)
		if got := initWizWrap(s, cols); got != want {
			if bad++; bad <= 20 {
				t.Errorf("wrap(%q, %d) =\n%q\nNode\n%q", s, cols, got, want)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d wraps differ from Node %s", bad, d.Node)
	}
}

// TestB726NFCMatchesNode: every code point alone and a corpus of marks,
// Hangul jamo, composites and singletons normalize to Node's NFC.
func TestB726NFCMatchesNode(t *testing.T) {
	d := b726Load(t, "nfc.json")
	bad := 0
	for i, raw := range d.Inputs {
		var s, want string
		_ = json.Unmarshal(raw, &s)
		_ = json.Unmarshal(d.Want[i], &want)
		if got := initWizNFC(s); got != want {
			if bad++; bad <= 20 {
				t.Errorf("nfc(%+q) = %+q, Node %+q", s, got, want)
			}
		}
	}
	changed := map[rune]string{}
	for _, raw := range d.Codepoints {
		var row struct {
			cp  int
			out []int
		}
		var pair [2]json.RawMessage
		_ = json.Unmarshal(raw, &pair)
		_ = json.Unmarshal(pair[0], &row.cp)
		_ = json.Unmarshal(pair[1], &row.out)
		rs := make([]rune, len(row.out))
		for i, x := range row.out {
			rs[i] = rune(x)
		}
		changed[rune(row.cp)] = string(rs)
	}
	for cp := rune(0); cp <= 0x10FFFF; cp++ {
		if b726Surrogate(cp) {
			continue
		}
		want, ok := changed[cp]
		if !ok {
			want = string(cp)
		}
		if got := initWizNFC(string(cp)); got != want {
			if bad++; bad <= 40 {
				t.Errorf("nfc(U+%04X) = %+q, Node %+q", cp, got, want)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d normalizations differ from Node %s", bad, d.Node)
	}
}

// TestB726PropertiesMatchNode: the Unicode property ranges Go measures with
// are the ones Node's regular expressions match.
func TestB726PropertiesMatchNode(t *testing.T) {
	data, err := os.ReadFile(b726TestdataFile("props.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Ranges map[string][][2]rune `json:"ranges"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Ranges) != 6 {
		t.Fatalf("Node gave %d properties", len(d.Ranges))
	}
	for name, ranges := range d.Ranges {
		in := map[rune]bool{}
		for _, r := range ranges {
			for cp := r[0]; cp <= r[1]; cp++ {
				in[cp] = true
			}
		}
		for cp := rune(0); cp <= 0x10FFFF; cp++ {
			if !b726Surrogate(cp) && initWizIn(initWizProps[name], cp) != in[cp] {
				t.Fatalf("%s: U+%s is %v in Go, %v in Node", name, strconv.FormatInt(int64(cp), 16), !in[cp], in[cp])
			}
		}
	}
}
