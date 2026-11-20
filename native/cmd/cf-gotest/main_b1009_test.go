package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestB1009CfGotestRunsByName(t *testing.T) {
	log := fakeGo(t, "0")
	t.Setenv("CODE_FORGE_RUN_ID", "")
	t.Setenv("CODE_FORGE_TEST_RUN", "^(TestB1009One|TestB1009Two)$")
	var out, errb bytes.Buffer
	if code := run([]string{"native/testslot/slot_test.go"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := readLog(t, log)
	if !strings.Contains(got, "-run ^(TestB1009One|TestB1009Two)$") || !strings.Contains(got, "-count=1") || !strings.Contains(got, "./testslot") {
		t.Fatalf("go args: %s", got)
	}
}
