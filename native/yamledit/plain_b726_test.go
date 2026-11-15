package yamledit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B726: StringifyPlain writes strings beyond printable ASCII (CJK, emoji
// sequences, combining marks, full-width forms) as the yaml package does,
// so init writes a config with wide text instead of handing it to Node.
// testdata/plain_wide_golden.json holds Node's bytes for each value
// (TestB726RecordPlainWide, B726_RECORD=1).

const b726PlainWideFile = "testdata/plain_wide_golden.json"

// b726PlainWideCases builds the values: wide pieces mixed with the ASCII
// pieces that make yaml quote a string, and the characters Go refuses.
func b726PlainWideCases() []any {
	wide := []string{"日本語", "👩\u200d💻", "cafe\u0301", "café", "🇯🇵", "#\ufe0f\u20e3", "👍🏽", "한국어", "ｱｲｳ", "ＡＢＣ", "x\u200dy",
		"\u00a0", "é\u0302", "📁", "長い", "\u3000", "€", "ñ", "Ω", "\u0085", "\u2028", "\u2029", "\ufeff", "\x7f", "\x9b", "\t", "\n"}
	ascii := []string{"", " ", "a", "-", "- ", ": ", ":", "#", " #", "'", "\"", "\\", ",", "[", "{", "&", "*", "!", "|", ">", "%", "@", "`", "?",
		"true", "null", "1", "0x1F", "---", "...", "npm test"}
	seed := uint32(726)
	rand := func(n int) int {
		seed = seed*1103515245 + 12345
		return int((seed >> 8) % uint32(n))
	}
	var out []any
	for _, w := range wide {
		out = append(out, ledger.Object{{Key: "v", Value: w}}, []any{w})
		for _, a := range ascii {
			out = append(out, ledger.Object{{Key: "v", Value: a + w}}, ledger.Object{{Key: "v", Value: w + a}})
		}
	}
	for i := 0; i < 400; i++ {
		var s string
		for n := rand(6); n >= 0; n-- {
			if rand(2) == 0 {
				s += wide[rand(len(wide))]
			} else {
				s += ascii[rand(len(ascii))]
			}
		}
		out = append(out, ledger.Object{{Key: "gates", Value: ledger.Object{{Key: "test", Value: []any{"echo", s}}}}, {Key: "model", Value: s}})
	}
	return out
}

func b726YAMLJS(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		file := filepath.Join(dir, "node_modules", "yaml", "dist", "index.js")
		if _, err := os.Stat(file); err == nil {
			return file
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Skip("node_modules/yaml not found")
		}
		dir = up
	}
}

// TestB726RecordPlainWide writes Node's stringify(value, {lineWidth: 0})
// for each value. It runs only with B726_RECORD=1.
func TestB726RecordPlainWide(t *testing.T) {
	if os.Getenv("B726_RECORD") != "1" {
		t.Skip("B726_RECORD is not 1")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	values, err := json.Marshal(b726PlainWideCases())
	if err != nil {
		t.Fatal(err)
	}
	script := `import { pathToFileURL } from 'node:url';
const { stringify } = await import(pathToFileURL(process.env.YAML_JS).href);
let input = '';
for await (const chunk of process.stdin) input += chunk;
const values = JSON.parse(input);
process.stdout.write(JSON.stringify({ node: process.version, out: values.map((v) => stringify(v, { lineWidth: 0 })) }));`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Env = append(os.Environ(), "YAML_JS="+b726YAMLJS(t))
	cmd.Stdin = strings.NewReader(string(values))
	cmd.Stderr = os.Stderr
	got, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Node string   `json:"node"`
		Out  []string `json:"out"`
	}
	if err := json.Unmarshal(got, &rec); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(map[string]any{"node": rec.Node, "values": json.RawMessage(values), "out": rec.Out}, "", " ")
	if err := os.WriteFile(b726PlainWideFile, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

var b726DocMark = regexp.MustCompile(`(?m)^(%|---|\.\.\.)`)

// TestB726StringifyPlainWideMatchesNode: every value with wide text is
// written byte-equal to Node; Go refuses only a string with a control
// character, a separator, a byte order mark, or a document marker (as
// before B726).
func TestB726StringifyPlainWideMatchesNode(t *testing.T) {
	data, err := os.ReadFile(b726PlainWideFile)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Node   string            `json:"node"`
		Values []json.RawMessage `json:"values"`
		Out    []string          `json:"out"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Values) != len(rec.Out) || len(rec.Values) < 1000 {
		t.Fatalf("%d values, %d outputs", len(rec.Values), len(rec.Out))
	}
	servedWide := 0
	for i, raw := range rec.Values {
		value, err := ledger.DecodeValue(raw)
		if err != nil {
			t.Fatal(err)
		}
		texts := []string{}
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case string:
				texts = append(texts, x)
			case []any:
				for _, item := range x {
					walk(item)
				}
			case ledger.Object:
				for _, f := range x {
					walk(f.Value)
				}
			}
		}
		walk(value)
		mayRefuse := false
		wide := false
		for _, s := range texts {
			mayRefuse = mayRefuse || !wideText(s) || b726DocMark.MatchString(s)
			wide = wide || !safeText(s)
		}
		out, err := StringifyPlain(value)
		if err != nil {
			if !IsUnsupported(err) || !mayRefuse {
				t.Fatalf("%s: refused: %v", raw, err)
			}
			continue
		}
		if out != rec.Out[i] {
			t.Fatalf("%s:\n go: %q\nnode: %q", raw, out, rec.Out[i])
		}
		if wide {
			servedWide++
		}
	}
	if servedWide < 500 {
		t.Fatalf("served %d values with wide text", servedWide)
	}
	t.Logf("served %d values with wide text (Node %s)", servedWide, rec.Node)
}
