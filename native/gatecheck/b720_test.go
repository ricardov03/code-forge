package gatecheck

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// B720: every string of testdata/b720_dates.json (made by b720_dates.js under
// TZ=UTC, America/New_York and Asia/Kathmandu) parses in Go to V8's Date.parse
// value.
func TestB720V8DateParseCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/b720_dates.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs struct {
		Inputs []string `json:"inputs"`
	}
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kathmandu"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip("no zone data:", err)
		}
		var want []*float64
		if err := json.Unmarshal(raw[zone], &want); err != nil || len(want) != len(inputs.Inputs) {
			t.Fatalf("%s: bad table: %v", zone, err)
		}
		saved := dateLocal
		dateLocal = loc
		bad := 0
		for i, in := range inputs.Inputs {
			if finite := V8DateFinite(in); finite != (want[i] != nil) && bad < 40 {
				bad++
				t.Errorf("%s: V8DateFinite(%q) = %v; Node says %v", zone, in, finite, fmtWant(want[i]))
			}
			ms, ok := V8DateParse(in)
			if (ok != (want[i] != nil) || ok && ms != *want[i]) && bad < 40 {
				bad++
				t.Errorf("%s: V8DateParse(%q) = %v %v; Node says %v", zone, in, ms, ok, fmtWant(want[i]))
			}
		}
		dateLocal = saved
	}
}

// B720: a local wall time around every offset change of ten zones (1900 to
// 2050, and near the TimeClip bounds) parses to Node's Date.parse value:
// a repeated wall time takes the earlier instant, a skipped one the offset
// before the change (testdata/b720_zones.json, made by b720_zones.js). A row
// whose probe instants have other offsets in Go's tz data than in Node's ICU
// data is a data version difference, not a rule one; it is counted, not
// judged.
func TestB720LocalTimeOffsetMatchesNode(t *testing.T) {
	data, err := os.ReadFile("testdata/b720_zones.json")
	if err != nil {
		t.Fatal(err)
	}
	type zoneRow struct {
		In     string
		Want   *float64
		Probes [][2]float64
	}
	var raw map[string][][3]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	all := map[string][]zoneRow{}
	for zone, rows := range raw {
		for _, r := range rows {
			var row zoneRow
			if json.Unmarshal(r[0], &row.In) != nil || json.Unmarshal(r[1], &row.Want) != nil || json.Unmarshal(r[2], &row.Probes) != nil {
				t.Fatalf("%s: bad row %s", zone, r)
			}
			all[zone] = append(all[zone], row)
		}
	}
	for zone, rows := range all {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip("no zone data:", err)
		}
		saved := dateLocal
		dateLocal = loc
		bad, skipped := 0, 0
	rows:
		for _, r := range rows {
			for _, p := range r.Probes {
				if _, off := time.UnixMilli(int64(p[0])).In(loc).Zone(); float64(off/60) != p[1] {
					skipped++
					continue rows
				}
			}
			ms, ok := V8DateParse(r.In)
			if (ok != (r.Want != nil) || ok && ms != *r.Want || V8DateFinite(r.In) != ok) && bad < 20 {
				bad++
				t.Errorf("%s: V8DateParse(%q) = %v %v; Node says %v", zone, r.In, ms, ok, fmtWant(r.Want))
			}
		}
		dateLocal = saved
		if skipped*100 > len(rows) {
			t.Errorf("%s: %d of %d rows differ in tz data", zone, skipped, len(rows))
		}
	}
}

func fmtWant(v *float64) any {
	if v == nil {
		return math.NaN()
	}
	return *v
}

// B720: LocaleCompare gives Node's a.localeCompare(b) for every row of
// testdata/b720_collate.json (made by b720_collate.js under an empty
// environment) that it claims to know, and knows every ASCII row.
func TestB720LocaleCompareCorpus(t *testing.T) {
	saved := collationEnv
	collationEnv = func(string) (string, bool) { return "", false }
	defer func() { collationEnv = saved }()
	data, err := os.ReadFile("testdata/b720_collate.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][3]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	known, bad := 0, 0
	for _, row := range rows {
		a, b, want := row[0].(string), row[1].(string), int(row[2].(float64))
		got, ok := LocaleCompare(a, b)
		if !ok {
			if isASCII(a) && isASCII(b) {
				t.Errorf("LocaleCompare(%q, %q) is unknown", a, b)
			}
			continue
		}
		known++
		if got != want && bad < 40 {
			bad++
			t.Errorf("LocaleCompare(%q, %q) = %d; Node says %d", a, b, got, want)
		}
	}
	t.Logf("%d of %d rows known", known, len(rows))
}

// B720: the locale comes from the first of LC_ALL, LC_MESSAGES and LANG that
// is set, as Node's ICU reads it; a language not measured to order ASCII as
// root does (Danish here) or a modifier leaves the order unknown, and only
// English or root reads the precomposed Latin letters.
func TestB720LocaleCompareEnvironment(t *testing.T) {
	saved := collationEnv
	defer func() { collationEnv = saved }()
	cases := []struct {
		env          map[string]string
		ascii, latin bool
	}{
		{map[string]string{}, true, true},
		{map[string]string{"LANG": "C"}, true, true},
		{map[string]string{"LANG": "POSIX"}, true, true},
		{map[string]string{"LANG": "C.UTF-8"}, true, true},
		{map[string]string{"LANG": "en_US.UTF-8"}, true, true},
		{map[string]string{"LANG": "es_ES.UTF-8"}, true, false},
		{map[string]string{"LANG": "da_DK.UTF-8"}, false, false},
		{map[string]string{"LANG": "da_DK.UTF-8", "LC_ALL": "en_US.UTF-8"}, true, true},
		{map[string]string{"LANG": "en_US.UTF-8", "LC_ALL": "da_DK.UTF-8"}, false, false},
		{map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "da_DK.UTF-8"}, false, false},
		{map[string]string{"LANG": "en_US.UTF-8", "LC_COLLATE": "da_DK.UTF-8"}, true, true},
		{map[string]string{"LANG": "en_US.UTF-8", "LANGUAGE": "da"}, true, true},
		{map[string]string{"LANG": "da_DK.UTF-8", "LC_ALL": ""}, true, true},
		{map[string]string{"LANG": "de_DE@phonebook"}, false, false},
		{map[string]string{"LANG": "nb_NO.UTF-8"}, false, false},
		{map[string]string{"LANG": "tr_TR.UTF-8"}, false, false},
	}
	for _, c := range cases {
		env := c.env
		collationEnv = func(name string) (string, bool) { v, ok := env[name]; return v, ok }
		if _, ok := LocaleCompare("aa", "z"); ok != c.ascii {
			t.Errorf("%v: ASCII known = %v, want %v", env, ok, c.ascii)
		}
		if _, ok := LocaleCompare("café", "cafe"); ok != c.latin {
			t.Errorf("%v: Latin known = %v, want %v", env, ok, c.latin)
		}
		if got, ok := LocaleCompare("xé", "xé"); !ok || got != 0 {
			t.Errorf("%v: equal strings = %d %v", env, got, ok)
		}
	}
}
