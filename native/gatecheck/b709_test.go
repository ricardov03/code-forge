package gatecheck

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

// b709ByDesign: the legacy rows Go leaves unknown on purpose (they keep the
// hand-over): a year below 1000, a minute or second of 60, a zone word glued
// to the time, or a local time near a zone change.
var b709ByDesign = regexp.MustCompile(`^(?:.*\b0[0-9]{3}\b.*|.*:60.*|.*\d(?:GMT|UTC)$)$`)

// B709: every row of testdata/b709_dates.json (made by b709_dates.js under
// TZ=UTC, America/New_York and Asia/Kathmandu) reads in Go as V8's
// Date.parse reads it, or stays unknown only for a by-design form.
func TestB709JSDateParseLocalForms(t *testing.T) {
	data, err := os.ReadFile("testdata/b709_dates.json")
	if err != nil {
		t.Fatal(err)
	}
	var table map[string][][3]any
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kathmandu"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip("no zone data:", err)
		}
		rows := table[zone]
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", zone)
		}
		saved := dateLocal
		dateLocal = loc
		read, numeric := 0, 0
		for _, row := range rows {
			in := row[0].(string)
			want, isNum := row[1].(float64)
			if isNum {
				numeric++
			}
			ms, ok, known := jsDateParse(in, true)
			if !known {
				if isNum && !b709ByDesign.MatchString(in) && !(row[2] == true && zone == "America/New_York") {
					t.Errorf("%s: jsDateParse(%q) is unknown; Node says %v", zone, in, row[1])
				}
				continue
			}
			if isNum {
				read++
			}
			if ok != isNum || ok && ms != want {
				t.Errorf("%s: jsDateParse(%q) = %v %v; Node says %v", zone, in, ms, ok, row[1])
			}
		}
		t.Logf("%s: %d of %d dated rows read", zone, read, numeric)
		dateLocal = saved
		if read*10 < numeric*6 {
			t.Fatalf("%s: only %d of %d dated rows read", zone, read, numeric)
		}
	}
}

// The forms a grant until is written in, one by one, in UTC.
func TestB709JSDateParseGrantForms(t *testing.T) {
	saved := dateLocal
	dateLocal = time.UTC
	defer func() { dateLocal = saved }()
	for in, want := range map[string]float64{
		"2020-01-01": 1577836800000, "2020-01-01 10:20:30": 1577874030000, "2020/01/02 10:20:30": 1577960430000,
		"1/2/2020 10:20": 1577960400000, "Jan 2 2020": 1577923200000, "January 2, 2099": 4070995200000,
		"Thursday, January 2, 2020": 1577923200000, "2 Jan 2020": 1577923200000, "2020-01-01 10:20 Z": 1577874000000,
		// B709: B380/B466/B493 kept these two unknown; Go reads them now.
		"Nov 10 2026": 1794268800000, "2026-11-11 10:00Z": 1794391200000,
	} {
		ms, ok, known := jsDateParse(in, true)
		if !known || !ok || ms != want {
			t.Errorf("jsDateParse(%q) = %v ok=%v known=%v; want %v", in, ms, ok, known, want)
		}
	}
	for _, in := range []string{"soon", "2020-01-01 10:20 PM", "20200101", "0099/01/01", "Jan 2 2020 10:20GMT"} {
		if _, _, known := jsDateParse(in, true); known {
			t.Errorf("jsDateParse(%q) is known; it must stay with Node", in)
		}
	}
}
