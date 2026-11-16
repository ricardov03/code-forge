package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// B726: the Unicode data and the expected values of the init wizard's text
// width (fast-string-width), wrap (fast-wrap-ansi) and NFC (String
// normalize) all come from running Node. TestB726RecordNode (B726_RECORD=1)
// runs the libraries clack uses and writes testdata/b726 and the Go tables
// in init_wizard_unicode.go; the other B726 tests replay them in Go.

// b726NodeScript reads {widths, wraps, nfc} on stdin and prints the Node
// answers and the Unicode tables Node's regular expressions and normalize
// use.
const b726NodeScript = `
import fs from 'node:fs';
import width from 'fast-string-width';
import { wrapAnsi } from 'fast-wrap-ansi';
const input = JSON.parse(fs.readFileSync(0, 'utf8'));
const out = { node: process.version, unicode: process.versions.unicode };
out.widths = input.widths.map((s) => width(s));
out.wraps = input.wraps.map(([s, c]) => wrapAnsi(s, c, { hard: true, trim: false }));
out.nfc = input.nfc.map((s) => s.normalize('NFC'));
const props = {
  emoji: /^\p{Emoji}$/u, presentation: /^\p{Emoji_Presentation}$/u, modifierBase: /^\p{Emoji_Modifier_Base}$/u,
  modifier: /^\p{Emoji_Modifier}$/u, mark: /^\p{M}$/u,
  cjkt: /^[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}\p{Script=Tangut}]$/u,
};
const ranges = {};
for (const k of Object.keys(props)) ranges[k] = [];
const cpWidth = [];
const nfcSingle = [];
const decomp = [];
const nonStarters = [];
const nfd = (s) => s.normalize('NFD');
const surrogate = (cp) => cp >= 0xD800 && cp <= 0xDFFF;
for (let cp = 0; cp <= 0x10FFFF; cp++) {
  if (surrogate(cp)) continue;
  const c = String.fromCodePoint(cp);
  for (const [k, re] of Object.entries(props)) {
    if (!re.test(c)) continue;
    const r = ranges[k], last = r[r.length - 1];
    if (last && last[1] === cp - 1) last[1] = cp; else r.push([cp, cp]);
  }
  const w = width(c), lw = cpWidth[cpWidth.length - 1];
  if (lw && lw[1] === cp - 1 && lw[2] === w) lw[1] = cp; else cpWidth.push([cp, cp, w]);
  const n = c.normalize('NFC');
  if (n !== c) nfcSingle.push([cp, [...n].map((x) => x.codePointAt(0))]);
  const d = nfd(c);
  if (d !== c) { decomp.push([cp, [...d].map((x) => x.codePointAt(0))]); continue; }
  if (cp === 0x345 || nfd('aͅ' + c) === 'a' + c + 'ͅ' || nfd('a' + c + '́') === 'á' + c) nonStarters.push(c);
}
const cmp = (x, y) => nfd('a' + x + y) === 'a' + y + x ? 1 : nfd('a' + y + x) === 'a' + x + y ? -1 : 0;
nonStarters.sort(cmp);
const ranks = [];
let rank = 1;
nonStarters.forEach((c, i) => {
  if (i > 0 && cmp(nonStarters[i - 1], c) !== 0) rank++;
  ranks.push([c.codePointAt(0), rank]);
});
const pairs = [];
const unresolved = [];
for (const [cp, d] of decomp) {
  if (cp >= 0xAC00 && cp <= 0xD7A3) continue;
  const c = String.fromCodePoint(cp);
  if (c.normalize('NFC') !== c || d.length < 2) continue;
  let found = null;
  for (let i = d.length - 1; i >= 1 && !found; i--) {
    const m = String.fromCodePoint(d[i]);
    const l = d.filter((_, j) => j !== i).map((x) => String.fromCodePoint(x)).join('').normalize('NFC');
    if ([...l].length === 1 && (l + m).normalize('NFC') === c) found = [l.codePointAt(0), d[i], cp];
  }
  if (found) pairs.push(found); else unresolved.push(cp);
}
out.tables = { ranges, ranks, pairs, unresolved, decomp: decomp.filter(([cp]) => cp < 0xAC00 || cp > 0xD7A3) };
out.cpWidth = cpWidth;
out.nfcSingle = nfcSingle;
process.stdout.write(JSON.stringify(out));
`

type b726Tables struct {
	Ranges     map[string][][2]int `json:"ranges"`
	Ranks      [][2]int            `json:"ranks"`
	Pairs      [][3]int            `json:"pairs"`
	Unresolved []int               `json:"unresolved"`
	Decomp     []json.RawMessage   `json:"decomp"`
}

type b726NodeOut struct {
	Node      string            `json:"node"`
	Unicode   string            `json:"unicode"`
	Widths    []int             `json:"widths"`
	Wraps     []string          `json:"wraps"`
	NFC       []string          `json:"nfc"`
	Tables    b726Tables        `json:"tables"`
	CPWidth   [][3]int          `json:"cpWidth"`
	NFCSingle []json.RawMessage `json:"nfcSingle"`
}

// b726Pieces are the shapes the corpus mixes: plain, wide, emoji
// sequences, marks, escapes and controls.
var b726Pieces = []string{
	"a", "Z", "~", " ", "é", "ÿ", " ", "­", "日", "本", "語", "한", "国", "ア", "ｱ", "ﾞ", "Ａ", "　", "〜", "ㄅ", "𠀀", "𝐀", "\U0001B000",
	"👩", "💻", "👍", "🏽", "👩‍💻", "👨‍👩‍👧", "🇯", "🇵", "🇯🇵", "#️⃣", "1️⃣", "©", "©️", "☀", "☀️", "⌚", "⌛", "❤️",
	"🏴\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F", "🏴", "\U000E0067", "‍", "​", "️", "︎", "⃣",
	"é", "ą́", "ộ", "́", "ͅ", "क्षि", "กั", "각", "가", "각", "Å", "Å", "̈́",
	"\x1b[31m", "\x1b[39m", "\x1b[2m", "\x1b[22m", "\u009b1m", "\x1b]8;;https://x.y\x07", "\x1b]8;;\x07", "\x1b", "\t", "\x07", "\x00", "\u0085", " ", "\n",
}

func b726Corpus() (widths []string, wraps [][2]any, nfc []string) {
	rnd := rand.New(rand.NewSource(726))
	pick := func(n int, skipNL bool) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			p := b726Pieces[rnd.Intn(len(b726Pieces))]
			if skipNL && p == "\n" {
				p = " "
			}
			b.WriteString(p)
		}
		return b.String()
	}
	widths = append(widths, "", "abc", "日本語", "한국어", "ｱｲｳ", "Ａ", "👩‍💻 dev", "👍🏽", "🇯🇵🇺", "#️⃣", "©", "©️", "é", "\x1b[31mred\x1b[39m",
		"\x1b]8;;http://x\x07link\x1b]8;;\x07", "a\tb", "\x00\x07", "​‍", "a️", "1️", "🏴\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F", "\x1b[1234m", "\x1b[12345m")
	for _, p := range b726Pieces {
		widths = append(widths, p)
	}
	for i := 0; i < 3000; i++ {
		widths = append(widths, pick(1+rnd.Intn(8), false))
	}
	for i := 0; i < 1500; i++ {
		words := make([]string, 1+rnd.Intn(6))
		for j := range words {
			words[j] = pick(1+rnd.Intn(6), true)
		}
		wraps = append(wraps, [2]any{strings.Join(words, " "), []int{1, 2, 3, 4, 5, 7, 10, 20, 40, 76}[rnd.Intn(10)]})
	}
	// clack's frames: styled words, a prefix, a long line at 80 columns
	wraps = append(wraps, [2]any{"\x1b[90m│\x1b[39m\n\x1b[32m◆\x1b[39m  Gate test: echo 日本語 👩‍💻 café \x1b[2m(from --gate)\x1b[22m " + strings.Repeat("数据 ", 30), 80})
	nfcPool := []string{"a", "e", "o", "u", "A", "C", "S", "̀", "́", "̂", "̃", "̄", "̇", "̈", "̣", "̧", "̨", "ͅ", "̈́", "̀",
		"α", "ω", "ᄀ", "ᅡ", "ᆨ", "가", "क", "़", "्", "ั", "ཱ", "ི", "ྀ", "ְ", "ּ", "֑", "Å", "Å", "ü",
		"ẛ", "ཱི", "ཱུ", "େ", "ା", "ୗ", "ෙ", "ා", "්", "\U000110a5", "\U000110ba", "\U00011099", "゙", "か", "゚", "x", "Ω", " "}
	for i := 0; i < 4000; i++ {
		var b strings.Builder
		for j, n := 0, 1+rnd.Intn(7); j < n; j++ {
			b.WriteString(nfcPool[rnd.Intn(len(nfcPool))])
		}
		nfc = append(nfc, b.String())
	}
	nfc = append(nfc, widths...)
	return widths, wraps, nfc
}

type b726Recorded struct {
	Node   string   `json:"node"`
	Inputs []string `json:"inputs"`
	Want   []any    `json:"want"`
}

func b726TestdataFile(name string) string { return filepath.Join("testdata", "b726", name) }

func TestB726RecordNode(t *testing.T) {
	b760NoNodeRecorder(t, "B726_RECORD")
	if os.Getenv("B726_RECORD") != "1" {
		t.Skip("B726_RECORD is not 1")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	only := os.Getenv("B726_CASE")
	if only == "" || only == "unicode" {
		b726RecordUnicode(t, node)
	}
	if only == "" || only != "unicode" {
		b726RecordWizard(t, only)
	}
}

func b726RecordUnicode(t *testing.T, node string) {
	repo := b458Repo(t)
	widths, wraps, nfc := b726Corpus()
	in, _ := json.Marshal(map[string]any{"widths": widths, "wraps": wraps, "nfc": nfc})
	cmd := exec.Command(node, "--input-type=module", "-e", b726NodeScript)
	cmd.Dir = repo
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v %s", err, stderr.String())
	}
	var o b726NodeOut
	if err := json.Unmarshal(data, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Tables.Unresolved) > 0 {
		t.Fatalf("compositions Node data does not resolve: %v", o.Tables.Unresolved)
	}
	_ = os.MkdirAll(b726TestdataFile(""), 0o755)
	write := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b726TestdataFile(name), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wrapIn := make([]any, len(wraps))
	for i, w := range wraps {
		wrapIn[i] = w
	}
	write("width.json", map[string]any{"node": o.Node, "inputs": widths, "want": o.Widths, "codepoints": o.CPWidth})
	write("wrap.json", map[string]any{"node": o.Node, "inputs": wrapIn, "want": o.Wraps})
	write("nfc.json", map[string]any{"node": o.Node, "inputs": nfc, "want": o.NFC, "codepoints": o.NFCSingle})
	write("props.json", map[string]any{"node": o.Node, "unicode": o.Unicode, "ranges": o.Tables.Ranges})
	b726WriteTables(t, o)
}

// b726WriteTables writes init_wizard_unicode.go from Node's data.
func b726WriteTables(t *testing.T, o b726NodeOut) {
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by TestB726RecordNode (B726_RECORD=1) from Node %s (Unicode %s); DO NOT EDIT.\n\npackage main\n\n", o.Node, o.Unicode)
	names := make([]string, 0, len(o.Tables.Ranges))
	for k := range o.Tables.Ranges {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("// initWizProps are the code point ranges of the Unicode properties fast-string-truncated-width's regular expressions use.\nvar initWizProps = map[string][][2]rune{\n")
	for _, k := range names {
		fmt.Fprintf(&b, "\t%q: {", k)
		for i, r := range o.Tables.Ranges[k] {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{0x%X, 0x%X}", r[0], r[1])
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// initWizCCCRank orders the code points of a nonzero canonical combining class like Node's normalize: a higher rank is a higher class.\nvar initWizCCCRank = map[rune]uint8{")
	for i, r := range o.Tables.Ranks {
		if i%8 == 0 {
			b.WriteString("\n\t")
		}
		fmt.Fprintf(&b, "0x%X: %d, ", r[0], r[1])
	}
	b.WriteString("\n}\n\n")
	b.WriteString("// initWizDecomp is each code point's full canonical decomposition (Node's NFD), Hangul syllables aside.\nvar initWizDecomp = map[rune][]rune{")
	for i, raw := range o.Tables.Decomp {
		var row []any
		_ = json.Unmarshal(raw, &row)
		if i%4 == 0 {
			b.WriteString("\n\t")
		}
		fmt.Fprintf(&b, "0x%X: {", int(row[0].(float64)))
		for j, x := range row[1].([]any) {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "0x%X", int(x.(float64)))
		}
		b.WriteString("}, ")
	}
	b.WriteString("\n}\n\n")
	b.WriteString("// initWizCompose maps a starter and the next code point to their primary composite (Node's NFC), Hangul aside.\nvar initWizCompose = map[[2]rune]rune{")
	for i, p := range o.Tables.Pairs {
		if i%4 == 0 {
			b.WriteString("\n\t")
		}
		fmt.Fprintf(&b, "{0x%X, 0x%X}: 0x%X, ", p[0], p[1], p[2])
	}
	b.WriteString("\n}\n")
	src, err := format.Source([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("init_wizard_unicode.go", src, 0o644); err != nil {
		t.Fatal(err)
	}
}
