package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ricardov03/code-forge/native/jev"
)

// B505: checkJev's row for every answer kind, against a local fake Jev
// (expected texts checked against Node's checkJev on the same replies).
func TestB505DoctorJevRow(t *testing.T) {
	saved := doctorJevOpts
	t.Cleanup(func() { doctorJevOpts = saved })
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{200, `{"answers":{"risk":{"score":1}}}`, "OK   jev: live call ok (1 attempt(s))"},
		{401, ``, "FAIL jev: live call failed (unauthorized, status 401)"},
		{422, ``, "FAIL jev: live call failed (invalid_request, status 422)"},
		{429, ``, "WARN jev: live call failed (rate_limited, status 429)"},
		{529, ``, "WARN jev: live call failed (unavailable, status 529)"},
		{500, ``, "FAIL jev: live call failed (unexpected_status, status 500)"},
		{200, `nope`, "FAIL jev: live call failed (invalid_response, status 200)"},
	} {
		t.Run(fmt.Sprint(c.status, c.body), func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			doctorJevOpts = jev.Opts{URL: srv.URL, Sleep: func(float64) {}}
			row, ok := doctorJevRow(map[string]any{}, "test-key-not-real", true)
			got := fmt.Sprintf("%-4s %s: %s", row.status, row.label, row.detail)
			if !ok || got != c.want || row.id != "jev" {
				t.Fatalf("got %q ok=%v", got, ok)
			}
		})
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	doctorJevOpts = jev.Opts{URL: url, Sleep: func(float64) {}}
	if row, _ := doctorJevRow(nil, "k", true); row.detail != "live call failed (network)" || row.status != "WARN" {
		t.Fatalf("network row %+v", row)
	}
	if row, ok := doctorJevRow(nil, "", false); !ok || row.status != "WARN" || row.detail != "live call skipped (no key)" {
		t.Fatalf("no key row %+v", row)
	}
	// risk disabled: Node's checkJev throws with a key, skips without one
	off := map[string]any{"system1": map[string]any{"disable": []any{"risk"}}}
	if _, ok := doctorJevRow(off, "k", true); ok {
		t.Fatal("Go served a config Node's checkJev throws on")
	}
	if _, ok := doctorJevRow(off, "", false); !ok {
		t.Fatal("no key: Node skips before building the payload")
	}
}
