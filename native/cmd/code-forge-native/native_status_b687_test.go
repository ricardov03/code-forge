package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// b687Key is the run key Node's signer used for the signed rows below.
var b687Key = bytes.Repeat([]byte{0x11}, 32)

// b687Input holds the lines B501 handed to Node: lone surrogate escapes
// (strings, block ids, awaiting ids, a path), escaped pairs and U+2028 that
// stay raw, bytes that are not UTF-8, numbers JSON.parse reads as Infinity,
// lines split at U+2028 and U+2029 as node:readline splits them, and rows
// signed by Node with a lone surrogate or an Infinity (valid, then with a
// broken waiver mac).
const b687Input = "" +
	"{\"record\":{\"run_id\":\"\\ud83d\",\"status\":\"\\udc00x\",\"blocks\":{\"\\ud800\":{\"level\":\"L\\udfff\",\"status\":\"awaiting_human_check\"},\"\\udbff\":{\"status\":\"closed\"},\"B2\":{\"status\":\"stopped\"}}},\"rows\":[{\"run\":\"\\ud83d\",\"block\":\"\\ud800\",\"event\":\"session\",\"role\":\"coder\",\"model\":\"m\\ud83d\",\"provider\":\"p\\ude00\",\"usd\":1},{\"run\":\"\\ud83d\",\"block\":\"\\ud800\",\"event\":\"gate.red\",\"gate\":\"\\udead\"}]}" + "\n" +
	"{\"record\":{\"run_id\":\"\\ud83d\\ude00\\u00e9\\u2028\",\"workspace\":\"/nonexistent\\ud800\"},\"rows\":[]}" + "\n" +
	"{\"record\":{\"run_id\":\"\xff\xe2\x82\",\"status\":\"a\xed\xa0\x80b\xf0\x9f\x98\"},\"rows\":[]}" + "\n" +
	"{\"record\":{\"run_id\":1e400,\"blocks\":{\"B1\":{\"attempt\":1e400},\"B2\":{\"attempt\":-1e400},\"B3\":{\"attempt\":1e21}}},\"rows\":[{\"run\":\"Infinity\",\"event\":\"session\",\"usd\":1e400},{\"run\":\"Infinity\",\"event\":\"session\",\"tokens_in\":1e400,\"tokens_out\":1,\"provider\":\"anthropic\",\"level\":\"L1\"}]}" + "\n" +
	"{\"record\":{\"run_id\":\"a\"},\"rows\":[]}\xe2\x80\xa8{\"record\":{\"run_id\":\"b\"}}\xe2\x80\xa9{\"record\":{\"run_id\":\"c\xe2\x80\xa8d\"},\"rows\":[]}" + "\n" +
	"{\"record\":{\"run_id\":\"r1\",\"blocks\":{\"B1\":{\"status\":\"open\"}}},\"rows\":[{\"run\":\"r1\",\"block\":\"B1\",\"event\":\"review.result\",\"ticket\":\"t\\ud800\",\"file\":\"a.mjs\",\"content_hash\":\"h1\",\"status\":\"reviewed\",\"approved\":false,\"open\":[{\"id\":\"F1\",\"severity\":\"warning\"}],\"score\":1e400,\"mac\":\"8d44b4e507f2cbf6fb157a15730dd0c5c5a536c87481889bbcdda2efff2eb439\"}]}" + "\n" +
	"{\"record\":{\"run_id\":\"r1\",\"blocks\":{\"B1\":{\"status\":\"open\"}}},\"rows\":[{\"run\":\"r1\",\"block\":\"B1\",\"event\":\"review.result\",\"ticket\":\"t\\ud800\",\"file\":\"a.mjs\",\"content_hash\":\"h1\",\"status\":\"reviewed\",\"approved\":false,\"open\":[{\"id\":\"F1\",\"severity\":\"warning\"}],\"score\":1e400,\"mac\":\"8d44b4e507f2cbf6fb157a15730dd0c5c5a536c87481889bbcdda2efff2eb439\"},{\"run\":\"r1\",\"block\":\"B1\",\"event\":\"review.waived\",\"file\":\"a.mjs\",\"finding\":\"F1\",\"severity\":\"warning\",\"verdict\":\"agree\",\"by\":\"agent\",\"reason\":\"r\\ud83d\",\"mac\":\"5982ace762ac9f917825e02d3ccaab7fe1990e4ffb339a47c345e2c2503efd80\"}]}" + "\n" +
	"{\"record\":{\"run_id\":\"r1\",\"blocks\":{\"B1\":{\"status\":\"open\"}}},\"rows\":[{\"run\":\"r1\",\"block\":\"B1\",\"event\":\"review.result\",\"ticket\":\"t\\ud800\",\"file\":\"a.mjs\",\"content_hash\":\"h1\",\"status\":\"reviewed\",\"approved\":false,\"open\":[{\"id\":\"F1\",\"severity\":\"warning\"}],\"score\":1e400,\"mac\":\"8d44b4e507f2cbf6fb157a15730dd0c5c5a536c87481889bbcdda2efff2eb439\"},{\"run\":\"r1\",\"block\":\"B1\",\"event\":\"review.waived\",\"file\":\"a.mjs\",\"finding\":\"F1\",\"severity\":\"warning\",\"verdict\":\"agree\",\"by\":\"agent\",\"reason\":\"r\\ud83d\",\"mac\":\"6982ace762ac9f917825e02d3ccaab7fe1990e4ffb339a47c345e2c2503efd80\"}]}" + "\n"

// b687Want is what Node's native-status printed for b687Input over the same
// HOME, key and working folder.
const b687Want = "" +
	"{\"runs\":[{\"run_id\":\"\\ud83d\",\"status\":\"\\udc00x\",\"mode\":\"plan\",\"spent_usd\":1,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[{\"id\":\"\\ud800\",\"state\":\"awaiting_human_check\",\"level\":\"L\\udfff\",\"model\":\"p\\ude00/m\\ud83d\",\"gate\":{\"result\":\"fail\",\"name\":\"\\udead\"},\"findings\":0}],\"more\":0,\"closed\":1,\"stopped\":1,\"awaiting_human_check\":[\"\\ud800\"]}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"\xf0\x9f\x98\x80\xc3\xa9\xe2\x80\xa8\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":null,\"blocks\":[],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"\xef\xbf\xbd\xef\xbf\xbd\",\"status\":\"a\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbdb\xef\xbf\xbd\",\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"Infinity\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":2,\"budget_usd\":5,\"blocks\":[{\"id\":\"B1\",\"state\":\"open\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":0},{\"id\":\"B2\",\"state\":\"open\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":0},{\"id\":\"B3\",\"state\":\"attempt-1e+21\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":0}],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"a\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"b\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":null,\"unknown_usd_sessions\":null,\"budget_usd\":5,\"blocks\":[],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"ok\":false,\"error\":\"status-unreadable\"}" + "\n" +
	"{\"ok\":false,\"error\":\"status-unreadable\"}" + "\n" +
	"{\"runs\":[{\"run_id\":\"r1\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[{\"id\":\"B1\",\"state\":\"open\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":1}],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"r1\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[{\"id\":\"B1\",\"state\":\"open\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":0}],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n" +
	"{\"runs\":[{\"run_id\":\"r1\",\"status\":null,\"mode\":\"plan\",\"spent_usd\":0,\"unknown_usd_sessions\":0,\"budget_usd\":5,\"blocks\":[{\"id\":\"B1\",\"state\":\"open\",\"level\":null,\"model\":null,\"gate\":{\"result\":\"none\",\"name\":null},\"findings\":1}],\"more\":0,\"closed\":0,\"stopped\":0}]}" + "\n"

func b687Signed(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".code-forge", "runs", "r1.key")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b687Key, 0600); err != nil {
		t.Fatal(err)
	}
}

// B687: the lines B501 handed to Node's companion are answered in Go, byte
// for byte as Node answered them, with Node out of reach and no hand-over.
func TestB687NativeStatusServesNodeOnlyLinesInGo(t *testing.T) {
	home := b501NativeStatusFixture(t, false)
	b687Signed(t, home)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	b501Stdin(t, b687Input)
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != b687Want || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%q\nwant  =%q\nstderr=%q", code, out, b687Want, errText)
	}
	if data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("handovers.jsonl must not exist: err=%v data=%q", err, data)
	}
}

const b687AwaitingIn = `{"record":{"run_id":"r","project":"p","blocks":{"B1":{"status":"awaiting_human_check"},"B10":{},"a":{"status":"stopped"}}}}` + "\n"

// Node printed this for b687AwaitingIn with the fixture's ledger and budget.
const b687AwaitingWant = `{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":0.5,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"B1","state":"awaiting_human_check","level":null,"model":null,"gate":{"result":"none","name":null},"findings":1},{"id":"B10","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":0,"stopped":1,"awaiting_human_check":["B1"]}]}` + "\n"

// Without HOME, Node's os.homedir() is the password file's home: Go reads the
// ledger there, serves every line, keeps HOME unset and writes no log (the
// router's logs follow process.env.HOME).
func TestB687NativeStatusWithoutHomeUsesThePasswordHome(t *testing.T) {
	home := b501NativeStatusFixture(t, true)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	saved := nsUserHome
	nsUserHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { nsUserHome = saved })
	b501Stdin(t, b687AwaitingIn)
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != b687AwaitingWant || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errText)
	}
	if value, set := os.LookupEnv("HOME"); set {
		t.Fatalf("HOME left set to %q", value)
	}
	for _, name := range []string{"logs/errors.jsonl", "logs/breadcrumbs.json", "native/handovers.jsonl"} {
		if _, err := os.Stat(filepath.Join(home, ".code-forge", name)); !os.IsNotExist(err) {
			t.Fatalf("%s written without HOME: %v", name, err)
		}
	}
}

// When the password file has no home, os.homedir() throws: Node's statusOf
// then reads no ledger (spent unknown), loads no budget and checks findings
// with the zero key. Go answers the same, still without Node.
func TestB687NativeStatusWithoutAnyHome(t *testing.T) {
	b501NativeStatusFixture(t, false)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	saved := nsUserHome
	nsUserHome = func() (string, error) { return "", errors.New("no passwd entry") }
	t.Cleanup(func() { nsUserHome = saved })
	b501Stdin(t, `{"record":{"run_id":"r","project":"p"}}`+"\n"+`{"record":{"run_id":"r"},"rows":[{"run":"r","event":"session","usd":2}]}`+"\n")
	want := `{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":null,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n" +
		`{"runs":[{"run_id":"r","status":null,"mode":"plan","spent_usd":2,"unknown_usd_sessions":0,"budget_usd":null,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != want || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errText)
	}
}

// An empty HOME is os.homedir()'s "": Node reads .code-forge in the working
// folder. Go does the same instead of handing over.
func TestB687NativeStatusEmptyHomeReadsTheWorkingFolder(t *testing.T) {
	b501NativeStatusFixture(t, false)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("HOME", "")
	b452Write(t, filepath.Join(".code-forge", "ledger", "p.jsonl"),
		`{"run":"r","event":"session","usd":0.5}`+"\n"+
			`{"run":"r","block":"B1","event":"review.result","file":"a.js","approved":false,"open":[{"id":"F1","severity":"warning"}]}`+"\n")
	b501Stdin(t, b687AwaitingIn)
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != b687AwaitingWant || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errText)
	}
}

// On a terminal the router's maybeHint prints its hint after the router line
// and remembers the fingerprint; Go does it instead of handing the verb over.
func TestB687NativeStatusTerminalHintInGo(t *testing.T) {
	home := b501NativeStatusFixture(t, true)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	saved := routerLogStderrTTY
	routerLogStderrTTY = func() bool { return true }
	t.Cleanup(func() { routerLogStderrTTY = saved })
	b501Stdin(t, b687AwaitingIn)
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != b687AwaitingWant || errText != b501RouterLine+routerHintLine {
		t.Fatalf("code=%d\nstdout=%q\nstderr=%q", code, out, errText)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs", "hints.json")); err != nil {
		t.Fatalf("hints.json: %v", err)
	}
	// The same fingerprint within a day prints no second hint.
	b501Stdin(t, b687AwaitingIn)
	code, out, errText = runCaptured(t, "native-status")
	if code != 1 || out != b687AwaitingWant || errText != b501RouterLine {
		t.Fatalf("second run: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
