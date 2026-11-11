package gatecheck

import (
	"math"
	"regexp"
	"strconv"
	"time"
)

// isoDate is ECMAScript's date time string format as V8 reads it before its
// legacy fallback: YYYY, YYYY-MM or YYYY-MM-DD (or a ±YYYYYY year), then an
// optional THH:mm[:ss[.s…]] with an optional Z or ±HH:mm offset.
var isoDate = regexp.MustCompile(`^([+-]\d{6}|\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?(Z|[+-]\d{2}:\d{2})?)?$`)

// dateLocal is the zone of a date-time with no offset (V8 reads it as local
// time). Tests replace it; the process zone is what Node uses too.
var dateLocal = time.Local

// maxDateMs is ECMAScript's TimeClip bound: a time farther from the epoch is NaN.
const maxDateMs = 8.64e15

// jsDateParse is Date.parse(String(v)) in milliseconds. ok is false for NaN.
// known is false for a value whose result Go cannot prove equal to V8's (a
// number, an array, a string outside the format above, a calendar overflow
// V8 rolls over, or a local time near a zone change); callers hand those over
// instead of guessing.
func jsDateParse(v any, present bool) (ms float64, ok, known bool) {
	if !present {
		return math.NaN(), false, true
	}
	switch value := v.(type) {
	case nil, bool, map[string]any:
		// "null", "true", "false" and "[object Object]" are NaN in V8.
		return math.NaN(), false, true
	case string:
		return jsDateString(value)
	}
	return math.NaN(), false, false
}

func jsDateString(value string) (ms float64, ok, known bool) {
	m := isoDate.FindStringSubmatch(value)
	if m == nil {
		return math.NaN(), false, false
	}
	if m[1] == "-000000" {
		return math.NaN(), false, true
	}
	year, _ := strconv.Atoi(m[1])
	month, day := 1, 1
	if m[2] != "" {
		month, _ = strconv.Atoi(m[2])
	}
	if m[3] != "" {
		day, _ = strconv.Atoi(m[3])
	}
	if month < 1 || month > 12 || day < 1 || day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return math.NaN(), false, false
	}
	hour, minute, second, milli := 0, 0, 0, 0
	if m[4] != "" {
		hour, _ = strconv.Atoi(m[4])
		minute, _ = strconv.Atoi(m[5])
		if m[6] != "" {
			second, _ = strconv.Atoi(m[6])
		}
		if m[7] != "" {
			frac := m[7]
			if len(frac) > 3 {
				frac = frac[:3] // V8 keeps milliseconds and drops the rest
			}
			for len(frac) < 3 {
				frac += "0"
			}
			milli, _ = strconv.Atoi(frac)
		}
		if hour == 24 && (minute != 0 || second != 0 || milli != 0) || hour > 24 || minute > 59 || second > 59 {
			return math.NaN(), false, false
		}
	}
	var t time.Time
	switch {
	case m[4] == "" || m[8] == "Z":
		t = time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.UTC)
	case m[8] == "":
		// A date-time without an offset is local time; near a zone change
		// Go and V8 may pick different instants.
		t = time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), dateLocal)
		_, offset := t.Zone()
		for _, h := range []time.Duration{-26, 26} {
			if _, near := t.Add(h * time.Hour).Zone(); near != offset {
				return math.NaN(), false, false
			}
		}
	default:
		oh, _ := strconv.Atoi(m[8][1:3])
		om, _ := strconv.Atoi(m[8][4:6])
		if oh > 23 || om > 59 {
			return math.NaN(), false, false
		}
		offset := oh*60 + om
		if m[8][0] == '-' {
			offset = -offset
		}
		t = time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.FixedZone("", offset*60))
	}
	ms = float64(t.UnixMilli())
	if math.Abs(ms) > maxDateMs {
		return math.NaN(), false, true
	}
	return ms, true, true
}

// JSDateParse exposes jsDateParse to callers outside the gate checker.
func JSDateParse(v any, present bool) (ms float64, ok, known bool) {
	return jsDateParse(v, present)
}
