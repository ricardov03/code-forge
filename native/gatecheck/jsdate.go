package gatecheck

import (
	"math"
	"regexp"
	"strconv"
	"time"
)

// isoDate is the part of ECMAScript's date time string format that Go can read
// exactly like Date.parse: a UTC date, or a date and time with Z or an offset.
var isoDate = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?(Z|[+-]\d{2}:\d{2}))?$`)

// jsDateParse is Date.parse(String(v)) in milliseconds. ok is false for NaN.
// known is false for a value whose result Go cannot prove equal to V8's (a
// number, an array, or a string outside the exact ISO subset above); callers
// hand those over instead of guessing.
func jsDateParse(v any, present bool) (ms float64, ok, known bool) {
	if !present {
		return math.NaN(), false, true
	}
	switch value := v.(type) {
	case nil, bool, map[string]any:
		// "null", "true", "false" and "[object Object]" are NaN in V8.
		return math.NaN(), false, true
	case string:
		m := isoDate.FindStringSubmatch(value)
		if m == nil {
			return math.NaN(), false, false
		}
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		if month < 1 || month > 12 || day < 1 || day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			return math.NaN(), false, false
		}
		if m[4] == "" {
			return float64(time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()), true, true
		}
		hour, _ := strconv.Atoi(m[4])
		minute, _ := strconv.Atoi(m[5])
		second := 0
		if m[6] != "" {
			second, _ = strconv.Atoi(m[6])
		}
		if hour > 23 || minute > 59 || second > 59 {
			return math.NaN(), false, false
		}
		milli := 0
		if m[7] != "" {
			frac := m[7]
			for len(frac) < 3 {
				frac += "0"
			}
			milli, _ = strconv.Atoi(frac)
		}
		offset := 0
		if m[8] != "Z" {
			oh, _ := strconv.Atoi(m[8][1:3])
			om, _ := strconv.Atoi(m[8][4:6])
			if oh > 23 || om > 59 {
				return math.NaN(), false, false
			}
			offset = oh*60 + om
			if m[8][0] == '-' {
				offset = -offset
			}
		}
		t := time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.UTC)
		return float64(t.UnixMilli()) - float64(offset)*60000, true, true
	}
	return math.NaN(), false, false
}
