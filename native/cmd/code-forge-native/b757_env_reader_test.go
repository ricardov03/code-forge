package main

import (
	"os"
	"testing"
)

// B757: the run sweep reads the markers from the same real `ps -E` bytes the
// worker test reads (native/worker/testdata, recorded on darwin) as Node's
// readProcessEnv does: a value stops at a line end, so the batched row's last
// marker (whose value reaches the row end) is not read.
func TestB757RunSweepEnvFromPsMatchesNode(t *testing.T) {
	p := runProc{uid: 501, pid: 34871, ppid: 1, state: "SN", start: "Sun Nov 15 17:39:12 2026", command: "./helper/b757-sleeper"}
	cases := []struct {
		file string
		want map[string]string
	}{
		{"b757-ps-E-row.txt", map[string]string{"HOME": "/h/b757", "CODE_FORGE_OWNER_PID": "34838", "CODE_FORGE_RUN_ID": "r-b757", "CODE_FORGE_OWNER_START_TIME": "Sun Nov 15 17:39:11 2026"}},
		{"b757-ps-E-batch.txt", map[string]string{"HOME": "/h/b757", "CODE_FORGE_OWNER_PID": "34838", "CODE_FORGE_RUN_ID": "r-b757"}},
	}
	for _, c := range cases {
		data, err := os.ReadFile("../../worker/testdata/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		got := runEnvFromPs(p, string(data))
		if len(got) != len(c.want) {
			t.Fatalf("%s: markers %q, want %q", c.file, got, c.want)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: %s = %q, want %q", c.file, k, got[k], v)
			}
		}
	}
}
