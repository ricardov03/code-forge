package main

// B694: opItemIdFromInput (src/keys/onepassword.mjs) without Go's URL parser:
// the WHATWG URL steps Node's `new URL()` and URLSearchParams take for an
// https link (tab and newline dropped, leading and trailing C0 controls and
// spaces dropped, any run of / or \ before the authority, userinfo up to the
// last @, a port of digits up to 65535, the query up to #, the pairs split on
// &, + as space, lenient percent-decoding). Only a link that needs IDNA (a
// non-ASCII character, or an xn-- label) is left unsure: Node runs it.

import "strings"

// keysOpLinkHosts is LINK_HOSTS.
var keysOpLinkHosts = []string{"1password.com", "1password.ca", "1password.eu"}

// keysOpItem is opItemIdFromInput: the item ID (or "") and whether Go is
// sure Node finds the same.
func keysOpItem(s string) (string, bool) {
	text := jsTrim(s)
	if keysOpItemID.MatchString(text) {
		return text, true
	}
	if !keysHTTPS.MatchString(text) {
		return "", true
	}
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return "", false
		}
	}
	host, query, ok := keysURLParts(text)
	if !ok {
		// new URL() throws: null
		return "", true
	}
	host = strings.ToLower(host)
	for _, label := range strings.Split(host, ".") {
		if strings.HasPrefix(label, "xn--") {
			return "", false
		}
	}
	hostOK := false
	for _, h := range keysOpLinkHosts {
		hostOK = hostOK || host == h || strings.HasSuffix(host, "."+h)
	}
	if !hostOK {
		return "", true
	}
	if id, found := keysSearchParam(query, "i"); found && keysOpItemID.MatchString(id) {
		return id, true
	}
	return "", true
}

// keysURLParts parses an ASCII "https://" text as the WHATWG URL parser
// does: the host (percent-decoded) and the query; ok false is a failure.
func keysURLParts(text string) (string, string, bool) {
	text = strings.TrimFunc(text, func(r rune) bool { return r <= 0x20 })
	text = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(text)
	rest := text[len("https:"):]
	rest = strings.TrimLeft(rest, "/\\")
	end := strings.IndexAny(rest, "/\\?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	host, port, hasPort := strings.Cut(authority, ":")
	if strings.HasPrefix(authority, "[") {
		// an IPv6 host is never a 1Password host (valid or not, null)
		return "", "", false
	}
	if hasPort {
		if strings.Trim(port, "0123456789") != "" {
			return "", "", false
		}
		if n := strings.TrimLeft(port, "0"); len(n) > 5 || len(n) == 5 && n > "65535" {
			return "", "", false
		}
	}
	if host == "" {
		return "", "", false
	}
	host = keysPercentDecode(host, false)
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c >= 0x80 {
			// a percent-encoded non-ASCII host needs IDNA: never a sure 1Password host
			return "", "", false
		}
		if c <= 0x20 || c == 0x7f || strings.IndexByte("#%/:<>?@[\\]^|", c) >= 0 {
			return "", "", false
		}
	}
	query := ""
	if q := strings.IndexByte(tail, '?'); q >= 0 && (strings.IndexByte(tail, '#') < 0 || q < strings.IndexByte(tail, '#')) {
		query = tail[q+1:]
		if h := strings.IndexByte(query, '#'); h >= 0 {
			query = query[:h]
		}
	}
	return host, query, true
}

// keysSearchParam is URLSearchParams(query).get(name): the first pair's value.
func keysSearchParam(query, name string) (string, bool) {
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if keysPercentDecode(key, true) == name {
			return keysPercentDecode(value, true), true
		}
	}
	return "", false
}

// keysPercentDecode is the URL standard's percent-decode (a % not followed
// by two hex digits stays), with + as space for form data.
func keysPercentDecode(s string, plus bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if plus && c == '+' {
			b.WriteByte(' ')
			continue
		}
		if c == '%' && i+2 < len(s) && keysIsHex(s[i+1]) && keysIsHex(s[i+2]) {
			b.WriteByte(keysUnhex(s[i+1])<<4 | keysUnhex(s[i+2]))
			i += 2
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func keysIsHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func keysUnhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}
