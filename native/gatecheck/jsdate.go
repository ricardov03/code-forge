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
		return jsLegacyDate(value)
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
	// B498: V8 takes any day 1-31 and rolls it over (2026-02-30 is 2 March);
	// a month or day outside that range is NaN (checked against Node's table).
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return math.NaN(), false, true
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

// utcDate is the Date.prototype.toUTCString form (RFC 1123) and toString's
// form with a GMT offset and an optional zone name. V8's legacy parser skips
// the leading weekday word and the parenthesised name. Each form names its
// zone, so local time plays no part. Other legacy forms stay unknown.
// B498: one- or two-digit day and time fields and two- to four-digit years
// follow V8's legacy reader (checked against a table Node generated).
var (
	utcDate    = regexp.MustCompile(`^(?:(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), )?(\d{1,2}) (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) (\d{2,4}) (\d{1,2}):(\d{1,2})(?::(\d{1,2}))? (?:GMT|UTC)$`)
	stringDate = regexp.MustCompile(`^(?:(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun) )?(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) (\d{1,2}) (\d{2,4}) (\d{1,2}):(\d{1,2})(?::(\d{1,2}))? GMT([+-])(\d{2})(\d{2})(?: \([A-Za-z ]*\))?$`)
	monthNames = "JanFebMarAprMayJunJulAugSepOctNovDec"
)

func jsLegacyDate(value string) (ms float64, ok, known bool) {
	var day, mon, year, hour, minute, second string
	offset := 0
	if m := utcDate.FindStringSubmatch(value); m != nil {
		day, mon, year, hour, minute, second = m[1], m[2], m[3], m[4], m[5], m[6]
	} else if m := stringDate.FindStringSubmatch(value); m != nil {
		mon, day, year, hour, minute, second = m[1], m[2], m[3], m[4], m[5], m[6]
		oh, _ := strconv.Atoi(m[8])
		om, _ := strconv.Atoi(m[9])
		if oh > 23 || om > 59 {
			return math.NaN(), false, false
		}
		offset = oh*60 + om
		if m[7] == "-" {
			offset = -offset
		}
	} else {
		return math.NaN(), false, false
	}
	y, _ := strconv.Atoi(year)
	d, _ := strconv.Atoi(day)
	h, _ := strconv.Atoi(hour)
	mi, _ := strconv.Atoi(minute)
	sec, _ := strconv.Atoi(second)
	month := 1
	for i := 0; i < 12; i++ {
		if monthNames[i*3:i*3+3] == mon {
			month = i + 1
		}
	}
	// V8 reads a minute or second of 60 or more in ways Go does not model,
	// and a day outside 1-31 can make it read the numbers in another order.
	if sec > 59 || mi > 59 || d < 1 || d > 31 {
		return math.NaN(), false, false
	}
	// V8's legacy reader takes any day 1-31 and rolls it over (31 Nov is
	// 1 Dec), allows 24:00 for the next midnight, and refuses other hours.
	if h > 24 || h == 24 && (mi != 0 || sec != 0) {
		return math.NaN(), false, true
	}
	// A year below 100 is 20xx up to 49 and 19xx from 50, however it is written.
	if y <= 49 {
		y += 2000
	} else if y <= 99 {
		y += 1900
	}
	t := time.Date(y, time.Month(month), d, h, mi, sec, 0, time.FixedZone("", offset*60))
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
