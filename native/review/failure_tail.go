package review

import (
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B876 (B801 audit P09): a review session that ends unavailable on an exit
// or a timeout keeps why in its session meta: the verdict detail and the
// last lines the provider CLI wrote on stderr. Lines that repeat the packet
// (16 characters or more, as session.StderrTail) and PEM blocks are dropped
// and tokens are masked, so the result file never holds the packet or a key.

const (
	failureTailMaxLines  = 5
	failureTailMaxRunes  = 300
	failureTailPacketMin = 16
)

var (
	failureTailPemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*-----`)
	failureTailPemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*-----`)
)

// failureTail is the safe tail of a session's stderr ("" when it printed
// nothing): its stderr_tail when the session layer made one, else stderr.
func failureTail(res ledger.Object, prompt string) string {
	text, _ := res.Get("stderr_tail").(string)
	if text == "" {
		text, _ = res.Get("stderr").(string)
	}
	if text == "" {
		return ""
	}
	var packet []string
	for _, line := range strings.Split(prompt, "\n") {
		if t := strings.TrimSpace(line); len([]rune(t)) >= failureTailPacketMin {
			packet = append(packet, t)
		}
	}
	var kept []string
	inPem := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if inPem {
			inPem = !failureTailPemEnd.MatchString(t)
			continue
		}
		if failureTailPemBegin.MatchString(t) {
			inPem = !failureTailPemEnd.MatchString(t)
			continue
		}
		if t == "" || failureTailInPacket(t, packet) {
			continue
		}
		if r := []rune(t); len(r) > failureTailMaxRunes {
			t = string(r[:failureTailMaxRunes]) + "…"
		}
		kept = append(kept, config.MaskSecrets(t))
	}
	if len(kept) > failureTailMaxLines {
		kept = kept[len(kept)-failureTailMaxLines:]
	}
	return strings.Join(kept, "\n")
}

func failureTailInPacket(line string, packet []string) bool {
	if len([]rune(line)) < failureTailPacketMin {
		return false
	}
	for _, p := range packet {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}
