package jev

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// The exact body Node's askJev sends for doctor's risk call (checked against
// buildQuestionPayload(['risk']) + JSON.stringify under Node).
const doctorBody = `{"state":{"paths":["README.md"],"diff_stat":"+1 -0"},"model":"jev-latest","questions":{"risk":{"type":"score","instructions":"How much damage could a subtle mistake here cause in production?","criteria":["cosmetic","wrong behaviour, easy to notice and revert","silent wrong data for some users","silent wrong money or cross-tenant leak"]}}}`

type reply struct {
	status int
	header map[string]string
	body   string
	delay  time.Duration
}

type fake struct {
	mu      sync.Mutex
	replies []reply
	seen    []*http.Request
	bodies  []string
}

func (f *fake) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		data, _ := io.ReadAll(r.Body)
		f.seen = append(f.seen, r)
		f.bodies = append(f.bodies, string(data))
		rep := f.replies[0]
		if len(f.replies) > 1 {
			f.replies = f.replies[1:]
		}
		f.mu.Unlock()
		if rep.delay > 0 {
			time.Sleep(rep.delay)
		}
		for k, v := range rep.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func doctorOpts(t *testing.T, url string, slept *[]float64) Opts {
	q, ok := RiskPayload(nil)
	if !ok {
		t.Fatal("risk payload refused")
	}
	return Opts{
		State:     ledger.Object{{Key: "paths", Value: []any{"README.md"}}, {Key: "diff_stat", Value: "+1 -0"}},
		Questions: q,
		Key:       "test-key-not-real",
		URL:       url,
		Sleep:     func(ms float64) { *slept = append(*slept, ms) },
	}
}

func TestB505AskSendsNodesRequest(t *testing.T) {
	f := &fake{replies: []reply{{200, nil, `{"answers":{"risk":{"type":"score","score":1}},"usage":{"input_tokens":3}}`, 0}}}
	srv := f.serve(t)
	var slept []float64
	res, err := Ask(doctorOpts(t, srv.URL, &slept))
	if err != nil || !res.OK || res.Attempts != 1 || len(slept) != 0 {
		t.Fatalf("res=%+v err=%v slept=%v", res, err, slept)
	}
	r := f.seen[0]
	if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-key-not-real" || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %v", r.Method, r.Header)
	}
	if f.bodies[0] != doctorBody {
		t.Fatalf("body\n%s\nwant\n%s", f.bodies[0], doctorBody)
	}
	if strings.Contains(f.bodies[0], "test-key-not-real") {
		t.Fatal("the key is in the body")
	}
}

func TestB505AskFailureKinds(t *testing.T) {
	for _, c := range []struct {
		name     string
		replies  []reply
		kind     string
		status   int
		attempts int
		slept    []float64
		reqID    string
	}{
		{"429 then ok", []reply{{429, nil, "", 0}, {200, nil, `{"answers":{}}`, 0}}, "", 0, 2, []float64{500}, ""},
		{"529 four times", []reply{{529, nil, "", 0}}, "unavailable", 529, 4, []float64{500, 2000, 8000}, ""},
		{"429 four times", []reply{{429, nil, "", 0}}, "rate_limited", 429, 4, []float64{500, 2000, 8000}, ""},
		{"401 header id", []reply{{401, map[string]string{"x-request-id": "req-1"}, `{"request_id":"body"}`, 0}}, "unauthorized", 401, 1, nil, "req-1"},
		{"422 body id", []reply{{422, nil, `{"requestId":"req-2"}`, 0}}, "invalid_request", 422, 1, nil, "req-2"},
		{"401 no id", []reply{{401, nil, `not json`, 0}}, "unauthorized", 401, 1, nil, ""},
		{"500", []reply{{500, nil, "", 0}}, "unexpected_status", 500, 1, nil, ""},
		{"bad json", []reply{{200, nil, `{`, 0}}, "invalid_response", 200, 1, nil, ""},
		{"no answers", []reply{{200, nil, `{"answers":null}`, 0}}, "invalid_response", 200, 1, nil, ""},
		{"array body", []reply{{200, nil, `[1]`, 0}}, "invalid_response", 200, 1, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{replies: c.replies}
			srv := f.serve(t)
			var slept []float64
			res, err := Ask(doctorOpts(t, srv.URL, &slept))
			if err != nil {
				t.Fatal(err)
			}
			if res.OK != (c.kind == "") || res.Kind != c.kind || res.Status != c.status || res.Attempts != c.attempts || len(slept) != len(c.slept) {
				t.Fatalf("res=%+v slept=%v", res, slept)
			}
			for i := range slept {
				if slept[i] != c.slept[i] {
					t.Fatalf("slept %v", slept)
				}
			}
			got := ""
			if res.RequestID != nil {
				got = *res.RequestID
			}
			if got != c.reqID {
				t.Fatalf("request id %q", got)
			}
		})
	}
}

func TestB505AskNetworkAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens: a refused connection
	var slept []float64
	res, _ := Ask(doctorOpts(t, url, &slept))
	if res.OK || res.Kind != "network" || res.Status != 0 || res.Attempts != 4 || len(slept) != 3 {
		t.Fatalf("network: %+v %v", res, slept)
	}

	f := &fake{replies: []reply{{200, nil, `{"answers":{}}`, 300 * time.Millisecond}}}
	slow := f.serve(t)
	slept = nil
	o := doctorOpts(t, slow.URL, &slept)
	o.Timeout = 30 * time.Millisecond
	res, _ = Ask(o)
	if res.OK || res.Kind != "timeout" || res.Attempts != 4 || len(slept) != 3 {
		t.Fatalf("timeout: %+v %v", res, slept)
	}
}

func TestB505AskIgnoresProxyVariables(t *testing.T) {
	f := &fake{replies: []reply{{200, nil, `{"answers":{}}`, 0}}}
	srv := f.serve(t)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	var slept []float64
	res, _ := Ask(doctorOpts(t, srv.URL, &slept))
	if !res.OK {
		t.Fatalf("a proxy variable changed the call: %+v", res)
	}
}

func TestB505RiskPayload(t *testing.T) {
	text := func(o ledger.Object) string { b, _ := ledger.Stringify(o, false); return string(b) }
	q, ok := RiskPayload(map[string]any{"system1": map[string]any{"criteria_extra": map[string]any{"risk": map[string]any{
		"instructions": "Custom?", "criteria": map[string]any{"1": "minor", "4": "fatal"}}}}})
	if !ok || text(q) != `{"risk":{"type":"score","instructions":"Custom?","criteria":["cosmetic","minor","silent wrong data for some users","silent wrong money or cross-tenant leak","fatal"]}}` {
		t.Fatalf("%v %s", ok, text(q))
	}
	for name, cfg := range map[string]map[string]any{
		"disabled": {"system1": map[string]any{"disable": []any{"lane", "risk"}}},
		"gap":      {"system1": map[string]any{"criteria_extra": map[string]any{"risk": map[string]any{"criteria": map[string]any{"5": "x"}}}}},
		"word key": {"system1": map[string]any{"criteria_extra": map[string]any{"risk": map[string]any{"criteria": map[string]any{"high": "x"}}}}},
		"string":   {"system1": map[string]any{"criteria_extra": map[string]any{"risk": map[string]any{"criteria": "ab"}}}},
	} {
		if _, ok := RiskPayload(cfg); ok {
			t.Fatalf("%s: Node throws, Go served it", name)
		}
	}
}
