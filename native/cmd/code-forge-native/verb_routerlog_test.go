package main

import "testing"

// Expected values come from Node's normalizeMessage (src/util/error-log.mjs).
func TestB500ErrorLogNormalizeMatchesNode(t *testing.T) {
	cases := [][2]string{
		{`ledger tail: --n/-n must be a non-negative integer, got "abc"`, `ledger tail: must be a non-negative integer, got`},
		{"it's 'quoted' and `tick` and \"dq\"", `it's and and`},
		{`step "a": unknown need "b"`, `step : unknown need`},
		{`at 2026-11-11T10:00:00.000Z and 10:22 then 12:30:45.5`, `at and then`},
		{`hash deadbeef1 and abcdefabc and ABCDEF12345 and 1234567g`, `hash and abcdefabc and and 1234567g`},
		{`n 12345 v1.2345 x 2026 3.14159 12345.678 9999_ a.12345 12345abc`, `n v1.2345 x 3.14159 9999_ a.12345`},
		{`path a/b and /x/y.z and <thing> ok`, `path and and ok`},
		{"  spaced\t\tout  ", `spaced out`},
		{`x'y' z 'w'`, `x'y' z`},
		{`unterminated "quote and 'other`, `unterminated "quote and 'other`},
		{`logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log`, `logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log`},
		{"step \"a\": unknown need \"q\u00e9\" \u00fc12345 \u00e91234567 \u00f1/x 12345\u00e9", "step : unknown need \u00fc \u00e9 \u00e9"},
		{"a\u00a0\u2003b\u3000 \u2019quoted\u2019 x'y' \u00e9'z'", "a b \u2019quoted\u2019 x'y' \u00e9"},
	}
	for _, c := range cases {
		if got := errorLogNormalize(c[0]); got != c[1] {
			t.Errorf("errorLogNormalize(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}
