package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/jev"
)

// B550: `code-forge jev ask` runs in Go: the --rules answer and the Jev
// answer with the key from CODE_FORGE_KEY_JEV or the 0600 file store. A fake
// Jev (httptest) serves the bodies Node's fetch was given for the goldens
// (CODE_FORGE_NATIVE=0 ./bin/code-forge jev ask ..., globalThis.fetch replaced
// by the same bodies through NODE_OPTIONS=--import); the request body below is
// the one Node sent. Masks: ts and the random decision_id (fixed here).

const b550JevState = `{"filesChanged":2,"linesAdded":10,"touchesMigration":false,"touchesPolicyOrMiddleware":false,"pathFloorHit":false,"keywordsFound":["Auth"]}`

const b550LaneRequest = `{"state":` + b550JevState + `,"model":"jev-latest","questions":{"lane":{"type":"choice","instructions":"Which is the lowest model level that can reliably complete ` + "`block.task`" + ` within ` + "`block.owned_files`" + `?","criteria":{"L0":"trivial","L1":"plain feature, following an existing pattern","L2":"hard (money, dates, tenancy, migrations, concurrency, unknown failure)","split":"the block is too large or mixes two concerns — re-decompose before dispatch"}}}}`

const b550JevOK = `{"answers":{"lane":{"type":"choice","probabilities":{"L1":0.95,"L0":0.03,"L2":0.02}},"risk":{"type":"score","score":2,"probabilities":{"2":0.7,"0":0.1,"1":0.2}},"security_sensitive":{"type":"noul","noul":0.3}},"usage":{"input_tokens":50,"output_tokens":4}}`

type b550JevServer struct {
	status int
	body   string
	calls  []string // "<Authorization> <body>"
}

// newB550Jev: a temp HOME, a repo named "repo" holding config and the state
// files, the Jev client pointed at a fake server with no back-off wait, and a
// fixed decision id. down=true closes the server (a network failure).
func newB550Jev(t *testing.T, config string, status int, body string, down bool) (runFixture, *b550JevServer) {
	t.Helper()
	f := newRunFixture(t)
	repo := filepath.Join(f.workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	if config != "" {
		b460Write(t, filepath.Join(repo, ".code-forge.yml"), config)
	}
	b460Write(t, filepath.Join(repo, "state.json"), b550JevState)
	b460Write(t, filepath.Join(repo, "badstate.json"), `{"filesChanged":"2"}`)
	b460Write(t, filepath.Join(repo, "liststate.json"), `[1,2]`)
	fake := &b550JevServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		fake.calls = append(fake.calls, r.Header.Get("Authorization")+" "+string(data))
		w.WriteHeader(fake.status)
		_, _ = w.Write([]byte(fake.body))
	}))
	if down {
		srv.Close()
	} else {
		t.Cleanup(srv.Close)
	}
	saved, savedID, savedNow := b550JevOpts, b550JevID, b550JevNow
	b550JevOpts = jev.Opts{URL: srv.URL, Sleep: func(float64) {}}
	b550JevID = func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil }
	b550JevNow = func() float64 { return 1_800_000_000_000 }
	t.Cleanup(func() { b550JevOpts, b550JevID, b550JevNow = saved, savedID, savedNow })
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "sk-test-123")
	real, _ := filepath.EvalSymlinks(repo)
	b460Chdir(t, real)
	return f, fake
}

const b550ID = `"decision_id":"00000000-0000-4000-8000-000000000001"`

const b550PrettyID = `"decision_id": "00000000-0000-4000-8000-000000000001"`

func b550NoHandover(t *testing.T, f runFixture) {
	t.Helper()
	if got := b460Handovers(t, f.home); len(got) != 0 {
		t.Fatalf("handed over: %v", got)
	}
}

func TestB550JevAskAnswersFromJev(t *testing.T) {
	cfg := "version: 1\n"
	for _, c := range []struct {
		name, config string
		args         []string
		out, err     string
		row          string
		request      string
	}{
		{"lane", cfg, []string{"jev", "ask", "lane", "--state", "state.json", "--block", "B1"},
			"{\n  " + b550PrettyID + `,
  "question": "lane",
  "answer": {
    "type": "choice",
    "probabilities": {
      "L1": 0.95,
      "L0": 0.03,
      "L2": 0.02
    }
  },
  "decision": {
    "decided": true,
    "stage": "act",
    "value": "L1",
    "confidence": 0.95,
    "margin": 0.9199999999999999
  }
}
`, "",
			`{"event":"decision",` + b550ID + `,"question":"lane","answer":"L1","stage":"act","confidence":0.95,"margin":0.9199999999999999,"source":"jev","block":"B1","tokens_in":50,"tokens_out":4,"tokens_source":"estimated","cost_source":null}`,
			b550LaneRequest},
		{"risk", cfg, []string{"jev", "ask", "risk", "--state", "state.json"},
			"{\n  " + b550PrettyID + `,
  "question": "risk",
  "answer": {
    "type": "score",
    "score": 2,
    "probabilities": {
      "0": 0.1,
      "1": 0.2,
      "2": 0.7
    }
  },
  "decision": {
    "decided": false,
    "stage": "check",
    "value": 2,
    "confidence": 0.7,
    "margin": 0.49999999999999994
  }
}
`, "",
			`{"event":"decision",` + b550ID + `,"question":"risk","answer":2,"stage":"check","confidence":0.7,"margin":0.49999999999999994,"source":"jev","tokens_in":50,"tokens_out":4,"tokens_source":"estimated","cost_source":null}`,
			`{"state":` + b550JevState + `,"model":"jev-latest","questions":{"risk":{"type":"score","instructions":"How much damage could a subtle mistake here cause in production?","criteria":["cosmetic","wrong behaviour, easy to notice and revert","silent wrong data for some users","silent wrong money or cross-tenant leak"]}}}`},
		{"security_sensitive", cfg, []string{"jev", "ask", "security_sensitive", "--state", "state.json"},
			"{\n  " + b550PrettyID + `,
  "question": "security_sensitive",
  "answer": {
    "type": "noul",
    "noul": 0.3
  },
  "decision": {
    "decided": false,
    "stage": "check",
    "value": false,
    "confidence": 0.7,
    "margin": 0.4
  }
}
`, "",
			`{"event":"decision",` + b550ID + `,"question":"security_sensitive","answer":false,"stage":"check","confidence":0.7,"margin":0.4,"source":"jev","tokens_in":50,"tokens_out":4,"tokens_source":"estimated","cost_source":null}`,
			`{"state":` + b550JevState + `,"model":"jev-latest","questions":{"security_sensitive":{"type":"noul","instructions":"Does this change touch authentication, authorization, tenancy boundaries, secrets, money movement or webhook verification?","criteria":{"true":"yes","false":"no"}}}}`},
		{"thresholds and a cache_hours warning", "version: 1\nkeys:\n  cache_hours: 99\nthresholds:\n  lane:\n    act: 0.99\n", []string{"jev", "ask", "lane", "--state", "state.json"},
			"{\n  " + b550PrettyID + `,
  "question": "lane",
  "answer": {
    "type": "choice",
    "probabilities": {
      "L1": 0.95,
      "L0": 0.03,
      "L2": 0.02
    }
  },
  "decision": {
    "decided": false,
    "stage": "check",
    "value": "L1",
    "confidence": 0.95,
    "margin": 0.9199999999999999
  }
}
`, "WARN jev ask: keys.cache_hours must be a whole number of hours from 1 to 24; caching for the default 8 h\n",
			`{"event":"decision",` + b550ID + `,"question":"lane","answer":"L1","stage":"check","confidence":0.95,"margin":0.9199999999999999,"source":"jev","tokens_in":50,"tokens_out":4,"tokens_source":"estimated","cost_source":null}`,
			b550LaneRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, fake := newB550Jev(t, c.config, 200, b550JevOK, false)
			code, out, errText := b460Call(t, c.args...)
			if code != 0 || out != c.out || errText != c.err {
				t.Fatalf("= %d\n%s\n%q", code, out, errText)
			}
			if rows := b477Ledger(t, f.home); len(rows) != 1 || rows[0] != c.row {
				t.Fatalf("ledger rows %v", rows)
			}
			if len(fake.calls) != 1 || fake.calls[0] != "Bearer sk-test-123 "+c.request {
				t.Fatalf("Jev calls %q", fake.calls)
			}
			b550NoHandover(t, f)
		})
	}
}

// Jev down (a closed server, 529 four times) and a 4xx: one stderr line,
// exit 1, no ledger row.
func TestB550JevAskFailures(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		down   bool
		err    string
		calls  int
	}{
		{"jev down", 0, "", true, "jev ask: request failed (network)\n", 0},
		{"529 every attempt", 529, "busy", false, "jev ask: request failed (unavailable, status 529)\n", 4},
		{"401", 401, `{"error":"no"}`, false, "jev ask: request failed (unauthorized, status 401)\n", 1},
		{"422", 422, `{"error":"bad"}`, false, "jev ask: request failed (invalid_request, status 422)\n", 1},
		{"500", 500, "x", false, "jev ask: request failed (unexpected_status, status 500)\n", 1},
		{"no answer for the question", 200, `{"answers":{}}`, false, "jev ask: Jev did not answer question \"lane\"\n", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, fake := newB550Jev(t, "version: 1\n", c.status, c.body, c.down)
			code, out, errText := b460Call(t, "jev", "ask", "lane", "--state", "state.json")
			if code != 1 || out != "" || errText != c.err {
				t.Fatalf("= %d %q %q", code, out, errText)
			}
			if len(fake.calls) != c.calls {
				t.Fatalf("Jev calls %d; want %d", len(fake.calls), c.calls)
			}
			if rows := b477Ledger(t, f.home); len(rows) != 0 {
				t.Fatalf("ledger rows %v", rows)
			}
			b550NoHandover(t, f)
		})
	}
}

func TestB550JevAskLocalAnswers(t *testing.T) {
	usage := "usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]\n"
	for _, c := range []struct {
		name, config string
		noKey        bool
		args         []string
		code         int
		out, err     string
		rows         []string
	}{
		{name: "rules lane", config: "version: 1\n", args: []string{"jev", "ask", "lane", "--state", "state.json", "--rules", "--block", "B1", "--plan", "plans/p.md"},
			out:  "{\n  " + b550PrettyID + ",\n  \"question\": \"lane\",\n  \"answer\": \"L0\",\n  \"source\": \"rules\",\n  \"block\": \"B1\",\n  \"plan\": \"p.md\"\n}\n",
			rows: []string{`{"event":"decision",` + b550ID + `,"question":"lane","answer":"L0","source":"rules","block":"B1","plan":"p.md","tokens_source":null,"cost_source":null}`}},
		{name: "rules security with --slug", config: "version: 1\n", args: []string{"jev", "ask", "security_sensitive", "--state", "state.json", "--rules", "--slug", "myslug"},
			out:  "{\n  " + b550PrettyID + ",\n  \"question\": \"security_sensitive\",\n  \"answer\": true,\n  \"source\": \"rules\"\n}\n",
			rows: []string{`{"event":"decision",` + b550ID + `,"question":"security_sensitive","answer":true,"source":"rules","tokens_source":null,"cost_source":null}`}},
		{name: "rules with bad facts", config: "version: 1\n", args: []string{"jev", "ask", "lane", "--state", "badstate.json", "--rules"}, code: 2,
			err: "jev ask: --rules needs the fallback facts in the --state object; missing or wrong type: filesChanged, linesAdded, touchesMigration, touchesPolicyOrMiddleware, pathFloorHit, keywordsFound\n"},
		{name: "usage", config: "version: 1\n", args: []string{"jev", "ask", "lane", "--state", "state.json", "--bogus"}, code: 2, err: usage},
		{name: "state missing", config: "version: 1\n", args: []string{"jev", "ask", "lane", "--state", "nope.json"}, code: 1, err: "jev ask: could not read --state file: ENOENT\n"},
		{name: "disabled", config: "version: 1\nsystem1:\n  disable: [lane]\n", args: []string{"jev", "ask", "lane", "--state", "state.json", "--rules"}, code: 1,
			err: "jev ask: question \"lane\" is disabled by system1.disable\n"},
		{name: "no key", config: "version: 1\n", noKey: true, args: []string{"jev", "ask", "lane", "--state", "state.json"}, code: 1,
			err: "jev ask: no Jev key resolved (set one with \"code-forge keys set jev\")\n"},
		{name: "duplicate config key", config: "version: 1\nversion: 2\n", args: []string{"jev", "ask", "lane", "--state", "state.json", "--rules"}, code: 1,
			err: "jev ask: DUPLICATE_KEY: Map keys must be unique (line 2, column 1)\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, fake := newB550Jev(t, c.config, 200, b550JevOK, false)
			if c.noKey {
				t.Setenv("CODE_FORGE_KEY_JEV", "")
			}
			code, out, errText := b460Call(t, c.args...)
			if code != c.code || out != c.out || errText != c.err {
				t.Fatalf("= %d\n%s\n%q", code, out, errText)
			}
			if rows := b477Ledger(t, f.home); strings.Join(rows, "\n") != strings.Join(c.rows, "\n") {
				t.Fatalf("ledger rows %v", rows)
			}
			if len(c.rows) == 1 {
				slug := "repo"
				if strings.Contains(strings.Join(c.args, " "), "--slug myslug") {
					slug = "myslug"
				}
				if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "ledger", slug+".jsonl")); err != nil {
					t.Fatalf("ledger file: %v", err)
				}
			}
			if len(fake.calls) != 0 {
				t.Fatalf("Jev called: %q", fake.calls)
			}
			b550NoHandover(t, f)
		})
	}
}

// A key from the 0600 file store (CODE_FORGE_KEY_BACKEND=file) answers like
// the env key; an expired entry hands over before any side effect (Node
// deletes it and may ask 1Password).
func TestB550JevAskFileStoreKey(t *testing.T) {
	f, fake := newB550Jev(t, "version: 1\n", 200, b550JevOK, false)
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	store := filepath.Join(f.home, ".code-forge", "store")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(store, "jev.key"), "sk-file-9\n")
	if err := os.Chmod(filepath.Join(store, "jev.key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errText := b460Call(t, "jev", "ask", "lane", "--state", "state.json")
	if code != 0 || errText != "" || len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "Bearer sk-file-9 ") {
		t.Fatalf("= %d %q %q", code, errText, fake.calls)
	}
	b550NoHandover(t, f)
}

// Cases Go leaves to Node hand over before anything is written: a state that
// is not an object (askJev throws), a YAML syntax error (the parser's own
// wording).
func TestB550JevAskHandsOverBeforeSideEffects(t *testing.T) {
	for _, c := range []struct {
		name, config, state, reason string
	}{
		{"list state", "version: 1\n", "liststate.json", "jev-ask-state"},
		{"yaml syntax", "version: 1\nlevels: [\n", "state.json", "jev-ask-config"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, fake := newB550Jev(t, c.config, 200, b550JevOK, false)
			code, out, _ := b460Call(t, "jev", "ask", "lane", "--state", c.state)
			if code != 127 || out != "" {
				t.Fatalf("= %d %q; want the Node hand-over", code, out)
			}
			if got := b460Handovers(t, f.home); len(got) != 1 || got[0] != "jev "+c.reason {
				t.Fatalf("handovers %v", got)
			}
			if rows := b477Ledger(t, f.home); len(rows) != 0 || len(fake.calls) != 0 {
				t.Fatalf("rows %v calls %q", rows, fake.calls)
			}
		})
	}
}
