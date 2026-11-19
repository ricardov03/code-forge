package main

import "testing"

// B947: the recorded reference examples must not hold today's date. The
// autopilot report byline {"date":"YYYY-MM-DD"} (compact and pretty) is
// normalized like a timestamp, so TestB801DocExamplesRun stays green on any
// day. The binary takes its clock from the machine (no env seam), so the
// rule is checked on outputs dated on other days.
func TestB947NormalizeBylineDate(t *testing.T) {
	s := &helpRefSandbox{tmp: "/x/tmp", pkg: "/x/pkg", home: "/x/home", repo: "/x/repo", outside: "/x/outside"}
	for in, want := range map[string]string{
		`{"byline":{"date":"2027-03-04","owner":"T"}}`:  `{"byline":{"date":"2026-01-01","owner":"T"}}`,
		`{"byline":{"date": "2031-12-31","owner":"T"}}`: `{"byline":{"date": "2026-01-01","owner":"T"}}`,
		`{"time":"2027-03-04T10:00:00Z"}`:               `{"time":"2026-01-01T00:00:00Z"}`,
	} {
		if got := s.normalize(in); got != want {
			t.Errorf("normalize(%s) = %s, want %s", in, got, want)
		}
	}
}
