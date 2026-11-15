package gatecheck

import (
	"math"
	"slices"
	"time"

	"github.com/ricardov03/code-forge/native/jsonx"
)

// B720: V8DateParse is V8's Date.parse of a string, ported from V8's
// DateParser (src/date/dateparser*.{h,cc}) and ParseDateTimeString: the ES5
// date time reader first, then the legacy reader for whatever it leaves, then
// MakeDay/MakeTime/MakeDate, local time to UTC and TimeClip. It reads the
// string as UTF-16 code units as V8 does. ok is false for NaN. A local time
// (no zone in the text) takes dateLocal's offset by ICU's rule (see
// v8LocalOffset); the zone rules are Go's tz data, which can differ from the
// ICU data Node carries for a zone whose history changed between versions.
func V8DateParse(s string) (ms float64, ok bool) {
	return v8DateValue(s)
}

// V8DateFinite is Number.isFinite(Date.parse(s)).
func V8DateFinite(s string) bool {
	_, finite := v8DateValue(s)
	return finite
}

func v8DateValue(s string) (ms float64, finite bool) {
	nan := math.NaN()
	r, parsed := v8ParseDate(jsonx.Units(s))
	if !parsed {
		return nan, false
	}
	day := v8MakeDay(float64(r.year), float64(r.month), float64(r.day))
	if math.IsNaN(day) {
		return nan, false
	}
	t := float64(r.hour)*3600000 + float64(r.minute)*60000 + float64(r.second)*1000 + float64(r.milli)
	date := t + day*86400000
	if r.hasOffset {
		date -= float64(r.offset) * 1000
	} else {
		const maxBeforeUTC = maxDateMs + 864000000
		if date >= maxBeforeUTC || date < -maxDateMs-2*86400000 {
			return nan, false
		}
		date -= v8LocalOffset(date)
	}
	if math.Abs(date) > maxDateMs {
		return nan, false
	}
	return math.Trunc(date) + 0, true
}

// v8LocalOffset is the offset in ms of dateLocal at the local wall time
// date (ms since the epoch read as UTC fields), as V8's ICU time zone cache
// takes it: getOffsetFromLocal with UCAL_TZ_LOCAL_FORMER for both a wall
// time that happens twice (the earlier instant, the larger offset) and one
// that never happens (the offset in force before the change). Measured
// against Node v24 in ten zones around every offset change from 1900 to
// 2050 and near the TimeClip bounds (testdata/b720_zones.json).
func v8LocalOffset(date float64) float64 {
	wall := int64(math.Floor(date))
	at := func(u int64) int64 {
		_, off := time.UnixMilli(u).In(dateLocal).Zone()
		return int64(off) * 1000
	}
	// Every offset in force within 27 hours of the wall time (no zone is
	// more than 26 hours from UTC), sampled each half hour.
	seen := []int64{}
	for u := wall - 27*3600000; u <= wall+27*3600000; u += 1800000 {
		if off := at(u); !slices.Contains(seen, off) {
			seen = append(seen, off)
		}
	}
	best, found := int64(0), false
	for _, off := range seen {
		if at(wall-off) == off && (!found || off > best) {
			best, found = off, true
		}
	}
	if found {
		return float64(best)
	}
	// A skipped wall time: the offset before the change is the one in force
	// at the earliest instant the wall time could name.
	return float64(at(wall - slices.Max(seen)))
}

// v8MakeDay is V8's MakeDay for the parser's integer fields.
func v8MakeDay(year, month, date float64) float64 {
	if !(year >= -1000000 && year <= 1000000) || !(month >= -10000000 && month <= 10000000) {
		return math.NaN()
	}
	y, m := int64(year), int64(month)
	y += m / 12
	m %= 12
	if m < 0 {
		m += 12
		y--
	}
	const delta = 399999
	base := int64(365*(1970+delta) + (1970+delta)/4 - (1970+delta)/100 + (1970+delta)/400)
	fromYear := 365*(y+delta) + (y+delta)/4 - (y+delta)/100 + (y+delta)/400 - base
	common := [12]int64{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334}
	leap := [12]int64{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335}
	if y%4 != 0 || (y%100 == 0 && y%400 != 0) {
		fromYear += common[m]
	} else {
		fromYear += leap[m]
	}
	return float64(fromYear-1) + date
}

const v8None = math.MaxInt32

type v8DateResult struct {
	year, month, day            int
	hour, minute, second, milli int
	offset                      int
	hasOffset                   bool
}

// Token tags (dateparser.h DateToken) and keyword types.
const (
	v8TagInvalid = -6
	v8TagUnknown = -5
	v8TagSpace   = -4
	v8TagNumber  = -3
	v8TagSymbol  = -2
	v8TagEnd     = -1
	// keyword types (tag >= 0)
	v8KwInvalid   = 0
	v8KwMonth     = 1
	v8KwZone      = 2
	v8KwSeparator = 3
	v8KwAmPm      = 4
)

type v8Token struct{ tag, length, value int }

func (t v8Token) isNumber() bool           { return t.tag == v8TagNumber }
func (t v8Token) isSymbol(c int) bool      { return t.tag == v8TagSymbol && t.value == c }
func (t v8Token) isEnd() bool              { return t.tag == v8TagEnd }
func (t v8Token) isSpace() bool            { return t.tag == v8TagSpace }
func (t v8Token) isKeyword() bool          { return t.tag >= 0 }
func (t v8Token) isFixedNumber(n int) bool { return t.isNumber() && t.length == n }
func (t v8Token) isSign() bool             { return t.tag == v8TagSymbol && (t.value == '-' || t.value == '+') }
func (t v8Token) sign() int                { return 44 - t.value }
func (t v8Token) isKeywordZ() bool         { return t.tag == v8KwZone && t.length == 1 && t.value == 0 }
func (t v8Token) isKeywordType(k int) bool { return t.tag == k }

var v8Keywords = []struct {
	prefix [3]uint32
	kind   int
	value  int
}{
	{[3]uint32{'j', 'a', 'n'}, v8KwMonth, 1}, {[3]uint32{'f', 'e', 'b'}, v8KwMonth, 2},
	{[3]uint32{'m', 'a', 'r'}, v8KwMonth, 3}, {[3]uint32{'a', 'p', 'r'}, v8KwMonth, 4},
	{[3]uint32{'m', 'a', 'y'}, v8KwMonth, 5}, {[3]uint32{'j', 'u', 'n'}, v8KwMonth, 6},
	{[3]uint32{'j', 'u', 'l'}, v8KwMonth, 7}, {[3]uint32{'a', 'u', 'g'}, v8KwMonth, 8},
	{[3]uint32{'s', 'e', 'p'}, v8KwMonth, 9}, {[3]uint32{'o', 'c', 't'}, v8KwMonth, 10},
	{[3]uint32{'n', 'o', 'v'}, v8KwMonth, 11}, {[3]uint32{'d', 'e', 'c'}, v8KwMonth, 12},
	{[3]uint32{'a', 'm', 0}, v8KwAmPm, 0}, {[3]uint32{'p', 'm', 0}, v8KwAmPm, 12},
	{[3]uint32{'u', 't', 0}, v8KwZone, 0}, {[3]uint32{'u', 't', 'c'}, v8KwZone, 0},
	{[3]uint32{'z', 0, 0}, v8KwZone, 0}, {[3]uint32{'g', 'm', 't'}, v8KwZone, 0},
	{[3]uint32{'c', 'd', 't'}, v8KwZone, -5}, {[3]uint32{'c', 's', 't'}, v8KwZone, -6},
	{[3]uint32{'e', 'd', 't'}, v8KwZone, -4}, {[3]uint32{'e', 's', 't'}, v8KwZone, -5},
	{[3]uint32{'m', 'd', 't'}, v8KwZone, -6}, {[3]uint32{'m', 's', 't'}, v8KwZone, -7},
	{[3]uint32{'p', 'd', 't'}, v8KwZone, -7}, {[3]uint32{'p', 's', 't'}, v8KwZone, -8},
	{[3]uint32{'t', 0, 0}, v8KwSeparator, 0},
}

// v8IsWhiteSpace is V8's IsWhiteSpace (ECMAScript WhiteSpace, no line terminators).
func v8IsWhiteSpace(c uint32) bool {
	switch c {
	case 0x09, 0x0B, 0x0C, 0x20, 0xA0, 0xFEFF, 0x1680, 0x202F, 0x205F, 0x3000:
		return true
	}
	return c >= 0x2000 && c <= 0x200A
}

func v8IsLineTerminator(c uint32) bool {
	return c == 0x0A || c == 0x0D || c == 0x2028 || c == 0x2029
}

// v8Reader is DateParser::InputReader.
type v8Reader struct {
	units []uint16
	index int
	ch    uint32
}

func (r *v8Reader) next() {
	if r.index < len(r.units) {
		r.ch = uint32(r.units[r.index])
	} else {
		r.ch = 0
	}
	r.index++
}

func (r *v8Reader) isDigit() bool { return r.ch-'0' < 10 }

func (r *v8Reader) skip(c uint32) bool {
	if r.ch == c {
		r.next()
		return true
	}
	return false
}

func (r *v8Reader) readNumber() int {
	n, i := 0, 0
	for r.ch == '0' {
		r.next()
	}
	for r.isDigit() {
		if i < 9 {
			n = n*10 + int(r.ch-'0')
		}
		i++
		r.next()
	}
	return n
}

func (r *v8Reader) wordChar() bool { return r.ch >= 'A' && !v8IsWhiteSpace(r.ch) }

func (r *v8Reader) scan() v8Token {
	pre := r.index
	if r.ch == 0 {
		return v8Token{v8TagEnd, 0, -1}
	}
	if r.isDigit() {
		n := r.readNumber()
		return v8Token{v8TagNumber, r.index - pre, n}
	}
	for _, c := range []uint32{':', '-', '+', '.', ')'} {
		if r.skip(c) {
			return v8Token{v8TagSymbol, 1, int(c)}
		}
	}
	if r.wordChar() {
		var prefix [3]uint32
		length := 0
		for ; r.wordChar(); r.next() {
			if length < 3 {
				prefix[length] = r.ch | 0x20
			}
			length++
		}
		for _, k := range v8Keywords {
			if k.prefix == prefix && (length <= 3 || k.kind == v8KwMonth) {
				return v8Token{k.kind, length, k.value}
			}
		}
		return v8Token{v8KwInvalid, length, 0}
	}
	if v8IsWhiteSpace(r.ch) || v8IsLineTerminator(r.ch) {
		r.next()
		return v8Token{v8TagSpace, r.index - pre, 0}
	}
	if r.ch == '(' {
		balance := 0
		for {
			if r.ch == ')' {
				balance--
			} else if r.ch == '(' {
				balance++
			}
			r.next()
			if !(balance > 0 && r.ch != 0) {
				break
			}
		}
		return v8Token{v8TagUnknown, 1, -1}
	}
	r.next()
	return v8Token{v8TagUnknown, 1, -1}
}

// v8Scanner is DateParser::DateStringTokenizer.
type v8Scanner struct {
	in   *v8Reader
	peek v8Token
}

func (s *v8Scanner) nextToken() v8Token {
	t := s.peek
	s.peek = s.in.scan()
	return t
}

func (s *v8Scanner) skipSymbol(c int) bool {
	if s.peek.isSymbol(c) {
		s.peek = s.in.scan()
		return true
	}
	return false
}

func v8Between(x, lo, hi int) bool { return x >= lo && x <= hi }

type v8Day struct {
	comp       [3]int
	index      int
	namedMonth int
	iso        bool
}

func (d *v8Day) add(n int) bool {
	if d.index == 3 {
		return false
	}
	d.comp[d.index] = n
	d.index++
	return true
}

func (d *v8Day) write(r *v8DateResult) bool {
	if d.index < 1 {
		return false
	}
	for d.index < 3 {
		d.comp[d.index] = 1
		d.index++
	}
	year, month, day := 0, v8None, v8None
	if d.namedMonth == v8None {
		if d.iso || (d.index == 3 && !v8Between(d.comp[0], 1, 31)) {
			year, month, day = d.comp[0], d.comp[1], d.comp[2]
		} else {
			month, day = d.comp[0], d.comp[1]
			if d.index == 3 {
				year = d.comp[2]
			}
		}
	} else {
		month = d.namedMonth
		if d.index == 1 {
			day = d.comp[0]
		} else if !v8Between(d.comp[0], 1, 31) {
			year, day = d.comp[0], d.comp[1]
		} else {
			day, year = d.comp[0], d.comp[1]
		}
	}
	if !d.iso {
		if v8Between(year, 0, 49) {
			year += 2000
		} else if v8Between(year, 50, 99) {
			year += 1900
		}
	}
	if year < -(1<<31) || year > 1<<31-1 || !v8Between(month, 1, 12) || !v8Between(day, 1, 31) {
		return false
	}
	r.year, r.month, r.day = year, month-1, day
	return true
}

type v8Time struct {
	comp       [4]int
	index      int
	hourOffset int
}

func (t *v8Time) isExpecting(n int) bool {
	return (t.index == 1 && v8Between(n, 0, 59)) || (t.index == 2 && v8Between(n, 0, 59)) || (t.index == 3 && v8Between(n, 0, 999))
}

func (t *v8Time) add(n int) bool {
	if t.index < 4 {
		t.comp[t.index] = n
		t.index++
		return true
	}
	return false
}

func (t *v8Time) addFinal(n int) bool {
	if !t.add(n) {
		return false
	}
	for t.index < 4 {
		t.comp[t.index] = 0
		t.index++
	}
	return true
}

func (t *v8Time) write(r *v8DateResult) bool {
	for t.index < 4 {
		t.comp[t.index] = 0
		t.index++
	}
	hour, minute, second, milli := t.comp[0], t.comp[1], t.comp[2], t.comp[3]
	if t.hourOffset != v8None {
		if !v8Between(hour, 0, 12) {
			return false
		}
		hour %= 12
		hour += t.hourOffset
	}
	if !v8Between(hour, 0, 23) || !v8Between(minute, 0, 59) || !v8Between(second, 0, 59) || !v8Between(milli, 0, 999) {
		if hour != 24 || minute != 0 || second != 0 || milli != 0 {
			return false
		}
	}
	r.hour, r.minute, r.second, r.milli = hour, minute, second, milli
	return true
}

type v8Zone struct{ sign, hour, minute int }

func (z *v8Zone) set(hours int) {
	z.sign = 1
	if hours < 0 {
		z.sign = -1
	}
	z.hour = hours * z.sign
	z.minute = 0
}

func (z *v8Zone) setSign(s int) {
	z.sign = 1
	if s < 0 {
		z.sign = -1
	}
}

func (z *v8Zone) isExpecting(n int) bool {
	return z.hour != v8None && z.minute == v8None && v8Between(n, 0, 59)
}

func (z *v8Zone) isUTC() bool { return z.hour == 0 && z.minute == 0 }

// v8SmiMax is Smi::kMaxValue of Node's 64-bit build (no pointer compression).
const v8SmiMax = 1<<31 - 1

func (z *v8Zone) write(r *v8DateResult) bool {
	if z.sign == v8None {
		return true
	}
	if z.hour == v8None {
		z.hour = 0
	}
	if z.minute == v8None {
		z.minute = 0
	}
	total := uint32(z.hour)*3600 + uint32(z.minute)*60
	if total > v8SmiMax {
		return false
	}
	seconds := int(total)
	if z.sign < 0 {
		seconds = -seconds
	}
	r.offset, r.hasOffset = seconds, true
	return true
}

func v8ReadMilliseconds(t v8Token) int {
	number, length := t.value, t.length
	if length < 3 {
		if length == 1 {
			number *= 100
		} else if length == 2 {
			number *= 10
		}
	} else if length > 3 {
		if length > 9 {
			length = 9
		}
		factor := 1
		for {
			factor *= 10
			length--
			if length <= 3 {
				break
			}
		}
		number /= factor
	}
	return number
}

func v8ParseES5(s *v8Scanner, day *v8Day, tm *v8Time, tz *v8Zone) v8Token {
	if s.peek.isSign() {
		signToken := s.nextToken()
		if !s.peek.isFixedNumber(6) {
			return signToken
		}
		sign := signToken.sign()
		year := s.nextToken().value
		if sign < 0 && year == 0 {
			return signToken
		}
		day.add(sign * year)
	} else if s.peek.isFixedNumber(4) {
		day.add(s.nextToken().value)
	} else {
		return s.nextToken()
	}
	if s.skipSymbol('-') {
		if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 1, 12) {
			return s.nextToken()
		}
		day.add(s.nextToken().value)
		if s.skipSymbol('-') {
			if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 1, 31) {
				return s.nextToken()
			}
			day.add(s.nextToken().value)
		}
	}
	invalid := v8Token{v8TagInvalid, 0, -1}
	if !s.peek.isKeywordType(v8KwSeparator) {
		if !s.peek.isEnd() {
			return s.nextToken()
		}
	} else {
		s.nextToken()
		if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 0, 24) {
			return invalid
		}
		hourIs24 := s.peek.value == 24
		tm.add(s.nextToken().value)
		if !s.skipSymbol(':') {
			return invalid
		}
		if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 0, 59) || (hourIs24 && s.peek.value > 0) {
			return invalid
		}
		tm.add(s.nextToken().value)
		if s.skipSymbol(':') {
			if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 0, 59) || (hourIs24 && s.peek.value > 0) {
				return invalid
			}
			tm.add(s.nextToken().value)
			if s.skipSymbol('.') {
				if !s.peek.isNumber() || (hourIs24 && s.peek.value > 0) {
					return invalid
				}
				tm.add(v8ReadMilliseconds(s.nextToken()))
			}
		}
		if s.peek.isKeywordZ() {
			s.nextToken()
			tz.set(0)
		} else if s.peek.isSymbol('+') || s.peek.isSymbol('-') {
			if s.nextToken().value == '+' {
				tz.setSign(1)
			} else {
				tz.setSign(-1)
			}
			if s.peek.isFixedNumber(4) {
				hourmin := s.nextToken().value
				hour, min := hourmin/100, hourmin%100
				if !v8Between(hour, 0, 23) || !v8Between(min, 0, 59) {
					return invalid
				}
				tz.hour, tz.minute = hour, min
			} else {
				if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 0, 23) {
					return invalid
				}
				tz.hour = s.nextToken().value
				if !s.skipSymbol(':') {
					return invalid
				}
				if !s.peek.isFixedNumber(2) || !v8Between(s.peek.value, 0, 59) {
					return invalid
				}
				tz.minute = s.nextToken().value
			}
		}
		if !s.peek.isEnd() {
			return invalid
		}
	}
	if tz.hour == v8None && tm.index == 0 {
		tz.set(0)
	}
	day.iso = true
	return v8Token{v8TagEnd, 0, -1}
}

// v8ParseDate is DateParser::Parse.
func v8ParseDate(units []uint16) (v8DateResult, bool) {
	var r v8DateResult
	in := &v8Reader{units: units}
	in.next()
	s := &v8Scanner{in: in}
	s.peek = in.scan()
	tz := &v8Zone{sign: v8None, hour: v8None, minute: v8None}
	tm := &v8Time{hourOffset: v8None}
	day := &v8Day{namedMonth: v8None}
	token := v8ParseES5(s, day, tm, tz)
	if token.tag == v8TagInvalid {
		return r, false
	}
	hasNumber := day.index != 0
	for ; !token.isEnd(); token = s.nextToken() {
		switch {
		case token.isNumber():
			hasNumber = true
			n := token.value
			if s.skipSymbol(':') {
				if s.skipSymbol(':') {
					if tm.index != 0 {
						return r, false
					}
					tm.add(n)
					tm.add(0)
				} else {
					if !tm.add(n) {
						return r, false
					}
					if s.peek.isSymbol('.') {
						s.nextToken()
					}
				}
			} else if s.skipSymbol('.') && tm.isExpecting(n) {
				tm.add(n)
				if !s.peek.isNumber() {
					return r, false
				}
				ms := v8ReadMilliseconds(s.nextToken())
				if ms < 0 {
					return r, false
				}
				tm.addFinal(ms)
			} else if tz.isExpecting(n) {
				tz.minute = n
			} else if tm.isExpecting(n) {
				tm.addFinal(n)
				p := s.peek
				if !p.isEnd() && !p.isSpace() && !p.isKeywordZ() && !p.isSign() {
					return r, false
				}
			} else {
				if !day.add(n) {
					return r, false
				}
				s.skipSymbol('-')
			}
		case token.isKeyword():
			if token.tag == v8KwAmPm && tm.index != 0 {
				tm.hourOffset = token.value
			} else if token.tag == v8KwMonth {
				day.namedMonth = token.value
				s.skipSymbol('-')
			} else if token.tag == v8KwZone && hasNumber {
				tz.set(token.value)
			} else {
				if hasNumber {
					return r, false
				}
				if s.peek.isNumber() {
					return r, false
				}
			}
		case token.isSign() && (tz.isUTC() || tm.index != 0):
			tz.setSign(token.sign())
			n, length := 0, 0
			if s.peek.isNumber() {
				t := s.nextToken()
				length, n = t.length, t.value
			}
			hasNumber = true
			if s.peek.isSymbol(':') {
				tz.hour = n
				tz.minute = v8None
			} else if length == 2 || length == 1 {
				tz.hour, tz.minute = n, 0
			} else if length == 4 || length == 3 {
				tz.hour, tz.minute = n/100, n%100
			} else {
				return r, false
			}
		case (token.isSign() || token.isSymbol(')')) && hasNumber:
			return r, false
		}
	}
	if !day.write(&r) || !tm.write(&r) || !tz.write(&r) {
		return r, false
	}
	return r, true
}
