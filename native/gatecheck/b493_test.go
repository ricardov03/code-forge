package gatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B493: a late finding row with no file is read as Node reads late.file
// (undefined): the refusal names "undefined", a nit ruling or human waiver
// with no file clears it, and a ruling for a named file does not.
func TestB493LateFindingWithNoFile(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	late := Row{"event": "review.late_finding", "finding": "L1", "severity": "warning"}
	approved := Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"}
	if !Supported(b380Rows(t, key, approved, late)) {
		t.Fatal("a late row with no file is supported")
	}
	for _, bad := range []any{"", nil, 7} {
		if Supported(b380Rows(t, key, Row{"event": "review.late_finding", "file": bad, "finding": "L1"})) {
			t.Fatalf("a late row with file %#v stays unsupported", bad)
		}
	}
	res := b380Eval(b380Rows(t, key, approved, late), key, nil, "")
	if res.Unknown || len(res.Blockers) != 1 || res.Blockers[0] != (Blocker{"late_unruled", "", "L1", "late finding L1 in undefined has no ruling"}) {
		t.Fatalf("late finding with no file: %+v", res)
	}
	named := Row{"event": "review.late_ruling", "file": "a.mjs", "finding": "L1", "ruling": "nit"}
	if got := b380Codes(b380Eval(b380Rows(t, key, approved, late, named), key, nil, "")); len(got) != 1 {
		t.Fatalf("a ruling on a named file does not clear a late finding with no file: %v", got)
	}
	ruling := Row{"event": "review.late_ruling", "finding": "L1", "ruling": "nit"}
	if got := b380Codes(b380Eval(b380Rows(t, key, approved, late, ruling), key, nil, "")); len(got) != 0 {
		t.Fatalf("a nit ruling with no file clears it: %v", got)
	}
	critical := Row{"event": "review.late_finding", "finding": "L1", "severity": "critical"}
	waiver := Row{"event": "review.waived", "finding": "L1", "by": "human", "reason": "ok"}
	if got := b380Codes(b380Eval(b380Rows(t, key, approved, critical, waiver), key, nil, "")); len(got) != 0 {
		t.Fatalf("a human waiver with no file clears it: %v", got)
	}
}

// B493: the toUTCString and toString forms V8's legacy parser reads, with the
// values `node -e 'Date.parse(s)'` printed under TZ=UTC, America/New_York and
// Asia/Kathmandu (the same in each: every form names its zone).
func TestB493JSDateParseLegacyForms(t *testing.T) {
	for _, c := range []struct {
		in string
		ms float64
	}{
		{"Tue, 11 Nov 2026 10:00:00 GMT", 1794391200000}, {"11 Nov 2026 10:00:00 GMT", 1794391200000},
		{"Wed, 01 Jan 2025 00:00:00 UTC", 1735689600000}, {"Tue, 1 Nov 2026 23:59:59 GMT", 1793577599000},
		{"Thu, 29 Feb 2024 12:00:00 GMT", 1709208000000}, {"Mon, 31 Dec 9999 23:59:59 GMT", 253402300799000},
		{"Tue, 11 Nov 0100 10:00:00 GMT", -58984293600000},
		{"Wed Nov 11 2026 10:00:00 GMT+0100 (Central European Standard Time)", 1794387600000},
		{"Nov 11 2026 10:00:00 GMT-0530", 1794411000000}, {"Wed Nov 11 2026 10:00:00 GMT+0000 (Coordinated Universal Time)", 1794391200000},
		{"Sun Dec 31 1899 23:00:00 GMT-0100", -2208988800000}, {"Wed Mar 05 2025 07:08:09 GMT+0545 (Nepal Time)", 1741137789000},
	} {
		ms, ok, known := jsDateParse(c.in, true)
		if !ok || !known || ms != c.ms {
			t.Errorf("jsDateParse(%q) = %v %v %v; Node says %v", c.in, ms, ok, known, c.ms)
		}
	}
	// V8 reads other spellings; Go does not guess them. (B498 reads the
	// roll-overs, short years, 24:00 and no-seconds forms; see b498_test.go.)
	for _, in := range []string{"tue, 11 nov 2026 10:00:00 gmt", "Nov 11 2026 10:00:00 GMT+2400", "Wed Nov 11 2026 10:00:00 GMT+0100 (CET) extra", "Nov 10 2026"} {
		if _, _, known := jsDateParse(in, true); known {
			t.Errorf("jsDateParse(%q) must be unknown", in)
		}
	}
}

// B493: a signed digest that matches no order means Node does not ack.
// B498: Go now sorts Latin letters with marks like localeCompare, so the
// digest of Node's order acks and the digest of another order does not.
func TestB493AckDigestNonASCII(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	base := []Row{}
	for _, file := range []string{"é.txt", "f.txt", "ä.txt"} {
		base = append(base, Row{"event": "review.triage", "file": file, "finding": "F1", "severity": "warning"},
			Row{"event": "review.waived", "file": file, "finding": "F1", "severity": "warning", "verdict": "agree", "by": "agent", "reason": "minor"})
	}
	rows := b380Rows(t, key, base...)
	nomatch := append(append([]Row{}, rows...), b380Rows(t, key, Row{"event": "agent_waivers.acked", "digest": strings.Repeat("0", 64), "by": "human"})...)
	if acked, known := AgentWaiversAcked(true, "r1", "B1", nomatch, key); acked || !known {
		t.Fatalf("no order matches: not acked and known, got %v %v", acked, known)
	}
	set := []any{}
	// Node: ["ä.txt",…] < ["é.txt",…] < ["f.txt",…] whatever the macs.
	for _, i := range []int{5, 1, 3} {
		set = append(set, []any{rows[i]["file"], rows[i]["finding"], rows[i]["mac"]})
	}
	data, err := ledger.Stringify(set, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	match := append(append([]Row{}, rows...), b380Rows(t, key, Row{"event": "agent_waivers.acked", "digest": hex.EncodeToString(sum[:]), "by": "human"})...)
	if acked, known := AgentWaiversAcked(true, "r1", "B1", match, key); !acked || !known {
		t.Fatalf("the digest of Node's order acks: %v %v", acked, known)
	}
	reverse := []any{set[2], set[1], set[0]}
	data, err = ledger.Stringify(reverse, false)
	if err != nil {
		t.Fatal(err)
	}
	reversed := sha256.Sum256(data)
	other := append(append([]Row{}, rows...), b380Rows(t, key, Row{"event": "agent_waivers.acked", "digest": hex.EncodeToString(reversed[:]), "by": "human"})...)
	if acked, known := AgentWaiversAcked(true, "r1", "B1", other, key); acked || !known {
		t.Fatalf("the digest of another order does not ack: %v %v", acked, known)
	}
	unsigned := append(append([]Row{}, rows...), Row{"event": "agent_waivers.acked", "block": "B1", "run": "r1", "digest": hex.EncodeToString(sum[:]), "by": "human"})
	if acked, known := AgentWaiversAcked(true, "r1", "B1", unsigned, key); acked || !known {
		t.Fatalf("an unsigned matching digest does not ack: %v %v", acked, known)
	}
}

// B493: Node's scanTranscript(text, hookForbidden(argv)) — the hook command
// (basename, no .exe) and its trimmed first argument as a prefix at any
// command start; git reads its global options.
func TestB493HookTranscript(t *testing.T) {
	hook := "code-forge-on-block-end-hook"
	cases := []struct {
		argv  []string
		text  string
		lines []string
	}{
		{[]string{"/usr/local/bin/notify-hook"}, "$ ls\n$ notify-hook --x\n$ y && notify-hook\n", []string{"2", "3"}},
		{[]string{"/usr/local/bin/notify-hook"}, "$ echo notify-hook is mentioned\n", []string{"1"}},
		{[]string{"/usr/local/bin/notify-hook"}, "$ npx -y notify-hook.EXE\n", []string{"1"}},
		{[]string{"/usr/local/bin/notify-hook"}, "$ notify\\x2dhook\n$ notify-hooks\n", nil},
		{[]string{"notify-hook", " --done "}, "$ notify-hook --done\n$ notify-hook other --done\n", []string{"1"}},
		{[]string{"git", "push"}, "$ git status\n$ git -C /x --no-pager push origin\n", []string{"2"}},
		{[]string{"dir/"}, "$ dir/\n", nil},
		{[]string{".."}, "$ ..\n", nil},
		{[]string{"notify-hook", ""}, "$ notify-hook\n", nil},
		{[]string{"notify-hook"}, `{"type":"thread.started"}` + "\n" + `{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc 'notify-hook now'"}}` + "\n", []string{"2"}},
	}
	for i, c := range cases {
		hits, ok := HookTranscript(c.argv, c.text)
		got := []string{}
		for _, h := range hits {
			if h.Code != "rule_break" || !strings.HasSuffix(h.Detail, ": "+hook) {
				t.Fatalf("case %d: blocker %+v", i, h)
			}
			got = append(got, strings.TrimSuffix(strings.TrimPrefix(h.Detail, "transcript line "), ": "+hook))
		}
		if !ok || strings.Join(got, ",") != strings.Join(c.lines, ",") {
			t.Fatalf("case %d: ok=%v lines=%v want %v", i, ok, got, c.lines)
		}
	}
}
