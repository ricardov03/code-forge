package main

import (
	"fmt"
	"strings"
	"time"
)

// B806: a short gh network error (timeout, connection reset or refused, EOF,
// TLS or DNS failure, HTTP 5xx) while land reads CI is retried with a backoff
// on the CI clock (landCISleep), up to landCIGhTries tries in all; the last
// error names the try count. Other errors (auth, a rate limit, a bad flag)
// return at once. Only read calls go through it (run list, pr list, run view).

const landCIGhTries = 3

var landCIGhBackoff = 2 * time.Second

// landCIGhNetworkMarks are lower-case parts of gh's text for a network error.
var landCIGhNetworkMarks = []string{
	"timeout", "timed out", "connection reset", "connection refused", "broken pipe",
	"eof", "error connecting", "tls handshake", "no such host", "temporary failure",
	"network is unreachable", "http 500", "http 502", "http 503", "http 504",
	"500 internal server error", "502 bad gateway", "503 service unavailable", "504 gateway",
}

// landCIGhNetwork says whether a gh error is a short network error.
func landCIGhNetwork(err error) bool {
	if err == nil || ciRateLimited(err) {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, mark := range landCIGhNetworkMarks {
		if strings.Contains(text, mark) {
			return true
		}
	}
	return false
}

// landCIGh is ciGh with the B806 retry of short network errors.
func landCIGh(cwd string, args ...string) (string, error) {
	pause := landCIGhBackoff
	for try := 1; ; try++ {
		out, err := ciGh(cwd, args...)
		if !landCIGhNetwork(err) {
			return out, err
		}
		if try >= landCIGhTries {
			return out, fmt.Errorf("%s (after %d tries)", strings.SplitN(err.Error(), "\n", 2)[0], try)
		}
		landCISleep(pause)
		pause *= 2
	}
}
