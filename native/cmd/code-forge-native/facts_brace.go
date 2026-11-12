package main

import (
	"math"
	"strconv"
	"strings"
)

// B577: minimatch's braceExpand (Node 24's bundled minimatch with the
// brace-expansion and balanced-match packages), ported line by line. Lengths
// are UTF-16 lengths, as in JS.

const (
	fcBraceMax       = 100000
	fcBraceMaxLength = 4000000
	fcBraceEscSlash  = "\x00SLASHfc577\x00"
	fcBraceEscOpen   = "\x00OPENfc577\x00"
	fcBraceEscClose  = "\x00CLOSEfc577\x00"
	fcBraceEscComma  = "\x00COMMAfc577\x00"
	fcBraceEscPeriod = "\x00PERIODfc577\x00"
)

// fcIndexFrom is String#indexOf(sub, from).
func fcIndexFrom(s, sub string, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		return -1
	}
	i := strings.Index(s[from:], sub)
	if i < 0 {
		return -1
	}
	return from + i
}

// fcBalancedRange is balanced-match's range("{", "}", str).
func fcBalancedRange(str string) (int, int, bool) {
	ai := strings.Index(str, "{")
	bi := fcIndexFrom(str, "}", ai+1)
	i := ai
	if ai < 0 || bi <= 0 {
		return 0, 0, false
	}
	var begs []int
	left, right, haveRight := len(str), 0, false
	resA, resB, have := 0, 0, false
	for i >= 0 && !have {
		if i == ai {
			begs = append(begs, i)
			ai = fcIndexFrom(str, "{", i+1)
		} else if len(begs) == 1 {
			r := begs[0]
			begs = begs[:0]
			resA, resB, have = r, bi, true
		} else {
			beg := begs[len(begs)-1]
			begs = begs[:len(begs)-1]
			if beg < left {
				left, right, haveRight = beg, bi, true
			}
			bi = fcIndexFrom(str, "}", i+1)
		}
		if ai < bi && ai >= 0 {
			i = ai
		} else {
			i = bi
		}
	}
	if len(begs) > 0 && haveRight {
		resA, resB, have = left, right, true
	}
	return resA, resB, have
}

type fcBalanced struct{ pre, body, post string }

func fcBalance(str string) (fcBalanced, bool) {
	a, b, ok := fcBalancedRange(str)
	if !ok {
		return fcBalanced{}, false
	}
	return fcBalanced{pre: str[:a], body: str[a+1 : b], post: str[b+1:]}, true
}

func fcBraceParseCommaParts(str string) []string {
	if str == "" {
		return []string{""}
	}
	m, ok := fcBalance(str)
	if !ok {
		return strings.Split(str, ",")
	}
	p := strings.Split(m.pre, ",")
	p[len(p)-1] += "{" + m.body + "}"
	postParts := fcBraceParseCommaParts(m.post)
	if m.post != "" {
		p[len(p)-1] += postParts[0]
		p = append(p, postParts[1:]...)
	}
	return p
}

// fcJSLineTerminator: what a JS regex dot does not match.
func fcJSLineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

// fcBraceCommaThenClose is /,(?!,).*\}/.test(s).
func fcBraceCommaThenClose(s string) bool {
	rs := []rune(s)
	for k, r := range rs {
		if r != ',' || (k+1 < len(rs) && rs[k+1] == ',') {
			continue
		}
		for _, q := range rs[k+1:] {
			if q == '}' {
				return true
			}
			if fcJSLineTerminator(q) {
				break
			}
		}
	}
	return false
}

// fcBraceHasGroup is /\{(?:(?!\{).)*\}/.test(s).
func fcBraceHasGroup(s string) bool {
	rs := []rune(s)
	for k, r := range rs {
		if r != '{' {
			continue
		}
		for _, q := range rs[k+1:] {
			if q == '}' {
				return true
			}
			if q == '{' || fcJSLineTerminator(q) {
				break
			}
		}
	}
	return false
}

func fcBraceDigits(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// fcBraceSequence: /^-?\d+\.\.-?\d+(?:\.\.-?\d+)?$/ (numeric) or
// /^[a-zA-Z]\.\.[a-zA-Z](?:\.\.-?\d+)?$/ (alpha).
func fcBraceSequence(body string) (numeric, alpha bool) {
	n := strings.Split(body, "..")
	if len(n) != 2 && len(n) != 3 {
		return false, false
	}
	if len(n) == 3 && !fcBraceDigits(n[2]) {
		return false, false
	}
	letter := func(s string) bool {
		return len(s) == 1 && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
	}
	return fcBraceDigits(n[0]) && fcBraceDigits(n[1]), letter(n[0]) && letter(n[1])
}

// fcBraceNumeric is numeric(): parseInt for digits, else the first char code.
func fcBraceNumeric(s string) float64 {
	if fcBraceDigits(s) {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return float64(s[0])
}

// fcJSNumberString is String(n) for an integral number.
func fcJSNumberString(x float64) string {
	switch {
	case x == 0:
		return "0"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case math.Abs(x) < 1e21:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return strconv.FormatFloat(x, 'e', -1, 64)
}

func fcBraceIsPadded(s string) bool {
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	return len(s) >= 2 && s[0] == '0' && s[1] >= '0' && s[1] <= '9'
}

func fcBraceExpandSequence(body string, isAlpha bool, max, maxLength int) []string {
	n := strings.Split(body, "..")
	var out []string
	x, y := fcBraceNumeric(n[0]), fcBraceNumeric(n[1])
	width := len(n[0])
	if len(n[1]) > width {
		width = len(n[1])
	}
	incr := 1.0
	if len(n) == 3 {
		incr = math.Max(math.Abs(fcBraceNumeric(n[2])), 1)
	}
	reverse := y < x
	if reverse {
		incr = -incr
	}
	pad := false
	for _, s := range n {
		pad = pad || fcBraceIsPadded(s)
	}
	length := 0
	for i := x; (reverse && i >= y || !reverse && i <= y) && len(out) < max; i += incr {
		var c string
		if isAlpha {
			c = string(rune(uint16(int64(i))))
			if c == "\\" {
				c = ""
			}
		} else {
			c = fcJSNumberString(i)
			if pad {
				if need := width - len(c); need > 0 {
					z := strings.Repeat("0", need)
					if i < 0 {
						c = "-" + z + c[1:]
					} else {
						c = z + c
					}
				}
			}
		}
		if length+len(c) > maxLength {
			break
		}
		out = append(out, c)
		length += len(c)
	}
	return out
}

func fcBraceCombine(acc []string, pre string, values []string, max, maxLength int, dropEmpties bool) []string {
	var out []string
	length := 0
	for _, a := range acc {
		for _, v := range values {
			if len(out) >= max {
				return out
			}
			expansion := a + pre + v
			if dropEmpties && expansion == "" {
				continue
			}
			l := fcUTF16Len(expansion)
			if length+l > maxLength {
				return out
			}
			out = append(out, expansion)
			length += l
		}
	}
	return out
}

func fcBraceExpandInner(str string, max, maxLength int, isTop bool) []string {
	acc := []string{""}
	dropEmpties, firstGroup := false, true
	for {
		m, ok := fcBalance(str)
		if !ok {
			return fcBraceCombine(acc, str, []string{""}, max, maxLength, dropEmpties)
		}
		pre := m.pre
		if strings.HasSuffix(pre, "$") {
			acc = fcBraceCombine(acc, pre+"{"+m.body+"}", []string{""}, max, maxLength, dropEmpties && m.post == "")
			firstGroup = false
			if m.post == "" {
				break
			}
			str = m.post
			continue
		}
		isNumeric, isAlpha := fcBraceSequence(m.body)
		isSequence := isNumeric || isAlpha
		isOptions := strings.Contains(m.body, ",")
		if !isSequence && !isOptions {
			if fcBraceCommaThenClose(m.post) {
				str = m.pre + "{" + m.body + fcBraceEscClose + m.post
				isTop = true
				continue
			}
			return fcBraceCombine(acc, pre+"{"+m.body+"}"+m.post, []string{""}, max, maxLength, dropEmpties)
		}
		if firstGroup {
			dropEmpties = isTop && !isSequence
			firstGroup = false
		}
		var values []string
		if isSequence {
			values = fcBraceExpandSequence(m.body, isAlpha, max, maxLength)
		} else {
			n := fcBraceParseCommaParts(m.body)
			if len(n) == 1 {
				inner := fcBraceExpandInner(n[0], max, maxLength, false)
				n = make([]string, len(inner))
				for i, s := range inner {
					n[i] = "{" + s + "}"
				}
				if len(n) == 1 {
					acc = fcBraceCombine(acc, pre+n[0], []string{""}, max, maxLength, dropEmpties && m.post == "")
					if m.post == "" {
						break
					}
					str = m.post
					continue
				}
			}
			dropsEmpties := dropEmpties && m.post == "" && pre == ""
			for _, a := range acc {
				if a != "" {
					dropsEmpties = false
				}
			}
			valuesLength := 0
		outer:
			for _, part := range n {
				for _, v := range fcBraceExpandInner(part, max, maxLength, false) {
					if dropsEmpties && v == "" {
						continue
					}
					l := fcUTF16Len(v)
					if len(values) >= max || valuesLength+l > maxLength {
						break outer
					}
					values = append(values, v)
					valuesLength += l
				}
			}
		}
		acc = fcBraceCombine(acc, pre, values, max, maxLength, dropEmpties && m.post == "")
		if m.post == "" {
			break
		}
		str = m.post
	}
	return acc
}

// fcBraceReplaceSeq applies each (from, to) global replace in turn, as the
// chained String#replace calls of escapeBraces and unescapeBraces do.
func fcBraceReplaceSeq(s string, pairs ...string) string {
	for i := 0; i < len(pairs); i += 2 {
		s = strings.ReplaceAll(s, pairs[i], pairs[i+1])
	}
	return s
}

// fcBraceExpand is minimatch's braceExpand(pattern) with the default options.
func fcBraceExpand(pattern string) []string {
	if !fcBraceHasGroup(pattern) {
		return []string{pattern}
	}
	str := pattern
	if strings.HasPrefix(str, "{}") {
		str = `\{\}` + str[2:]
	}
	str = fcBraceReplaceSeq(str, `\\`, fcBraceEscSlash, `\{`, fcBraceEscOpen, `\}`, fcBraceEscClose, `\,`, fcBraceEscComma, `\.`, fcBraceEscPeriod)
	out := fcBraceExpandInner(str, fcBraceMax, fcBraceMaxLength, true)
	for i, s := range out {
		out[i] = fcBraceReplaceSeq(s, fcBraceEscSlash, `\`, fcBraceEscOpen, "{", fcBraceEscClose, "}", fcBraceEscComma, ",", fcBraceEscPeriod, ".")
	}
	return out
}
