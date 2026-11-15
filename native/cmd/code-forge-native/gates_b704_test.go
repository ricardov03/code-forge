package main

import "testing"

// TestB704GatesAcceptanceLoneSurrogateInGo: clauses with lone surrogate
// escapes are read as JSON.parse reads them and the result is Node's JSON
// (captured from CODE_FORGE_NATIVE=0 code-forge gates acceptance on the same
// files), instead of the gates-acceptance-json hand-over.
func TestB704GatesAcceptanceLoneSurrogateInGo(t *testing.T) {
	const tap = "TAP version 13\nok 1 - t1\n"
	for _, c := range []struct {
		clauses string
		code    int
		out     string
	}{
		{`[{"clause":"ok \ud83d","tests":["t1"]},{"clause":"c\udc00","tests":["t\ud800",["\ud83d","x"]]}]`, 1, `{"ok":false,"reason":"clause \"c\udc00\" names test \"t\ud800\", which did not exist or did not pass in this gate run","clause":"c\udc00","test":"t\ud800"}`},
		{`[{"clause":"ok \ud83d","tests":["t1"]},{"kind":"human_check","clause":"h\udfff"}]`, 0, `{"ok":true}`},
		{`[{"clause":"c","tests":[["\ud83d","x"]]}]`, 1, `{"ok":false,"reason":"clause \"c\" names test \"\ud83d,x\", which did not exist or did not pass in this gate run","clause":"c","test":["\ud83d","x"]}`},
	} {
		t.Run(c.clauses, func(t *testing.T) {
			f := b459Setup(t)
			f.write("c.json", c.clauses, 0644)
			f.write("t.tap", tap, 0644)
			code, out, errOut := f.call(nil, "gates", "acceptance", "--clauses", "c.json", "--tap", "t.tap")
			b459Native(t, f, code, out, errOut, c.code, c.out+"\n", "")
		})
	}
}
