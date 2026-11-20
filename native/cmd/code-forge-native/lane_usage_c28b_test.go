package main

import (
	"strings"
	"testing"
)

func TestC28bLaneIntegrationUsageErrors(t *testing.T) {
	for _, sub := range []string{"ready", "combine"} {
		for _, args := range [][]string{{"--bogus-flag"}, {"--run"}, {"--run", "r", "--run", "s"}, {"--json", "--json"}, {}} {
			t.Run(sub+"/"+strings.Join(args, " "), func(t *testing.T) {
				code, out, errs, _ := b690aUsageRun(t, append([]string{"lane", sub}, args...))
				usage := "usage: code-forge lane " + sub + " --run "
				wantOut := ""
				argv := append([]string{"lane", sub}, args...)
				if jsonRefusalWanted(argv) {
					wantOut = jsonRefusalLine(argv, 2, errs)
				}
				if code != 2 || out != wantOut || strings.Count(errs, "\n"+usage) != 1 || !strings.HasPrefix(errs, "lane "+sub+": ") {
					t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 2 and one command usage", code, out, errs)
				}
			})
		}
	}
}
