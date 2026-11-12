package main

import (
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B478: Node's registered secrets (src/util/redact.mjs) for the autopilot
// verb. In an autopilot process the one secret Node registers is the run's
// signer key (hex), and only once loadKey has read it: a signed write
// (writeSigned) or the decision check of round and level. From then on
// every stdout and stderr write (writeSafe) and every binnacle field
// (makeScrub) masks it, so Go registers it at the same points.

var autopilotSecrets []string

func autopilotRegisterSecret(value string) {
	if value != "" && !slices.Contains(autopilotSecrets, value) {
		autopilotSecrets = append(autopilotSecrets, value)
	}
}

// autopilotRegisterKey is loadKey's registerSecret: the key, when it reads.
func autopilotRegisterKey(home, runID string) {
	if key, err := rebaseKey(home, runID); err == nil {
		autopilotRegisterSecret(hex.EncodeToString(key))
	}
}

// autopilotRedact is redactString: every occurrence of every registered
// secret, ranges merged, each merged range one [REDACTED].
func autopilotRedact(text string) string {
	ranges := [][2]int{}
	for _, secret := range autopilotSecrets {
		for at := strings.Index(text, secret); at != -1; {
			ranges = append(ranges, [2]int{at, at + len(secret)})
			next := strings.Index(text[at+1:], secret)
			if next == -1 {
				break
			}
			at += 1 + next
		}
	}
	if len(ranges) == 0 {
		return text
	}
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	var b strings.Builder
	cursor := 0
	start, end := ranges[0][0], ranges[0][1]
	for _, r := range ranges[1:] {
		if r[0] <= end {
			end = max(end, r[1])
			continue
		}
		b.WriteString(text[cursor:start] + "[REDACTED]")
		cursor = end
		start, end = r[0], r[1]
	}
	b.WriteString(text[cursor:start] + "[REDACTED]" + text[end:])
	return b.String()
}

// autopilotCheckExpiry is checkExpiry: the expire row's writeSigned loads
// the key. A failed check registers it too when the write was due (an
// active grant past its window).
func autopilotCheckExpiry(home, runID string, now time.Time) (autopilot.Result, error) {
	result, err := autopilot.CheckGrantExpiry(home, runID, now)
	switch {
	case result.ExpiredNow:
		autopilotRegisterKey(home, runID)
	case err != nil:
		if record, readErr := state.ReadRun(home, runID); readErr == nil {
			if due, dueErr := autopilot.ReadGrant(record, now); dueErr == nil && due.State == "expired" && due.Grant.Get("status") == "active" {
				autopilotRegisterKey(home, runID)
			}
		}
	}
	return result, err
}

// autopilotWriteSigned is writeSigned: loadKey registers the key even when
// the append fails after it.
func autopilotWriteSigned(home, runID string, row ledger.Object) (ledger.Object, error) {
	written, err := state.WriteSigned(home, runID, row)
	autopilotRegisterKey(home, runID)
	return written, err
}
