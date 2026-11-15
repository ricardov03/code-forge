package main

import "testing"

// B729 removed six hand-over reasons no product code could return. They must
// not come back, and no prefix and condition handoverFlagReason builds may
// resolve to one of them.
var b729RemovedReasons = []string{
	"close-human",
	"close-external-review",
	"tools-confirm",
	"help-verbs",
	"doctor-full-live-worker",
	"init-doctor",
}

func TestB729RemovedReasonsStayRemoved(t *testing.T) {
	live := map[string]bool{}
	for _, code := range handoverReasons {
		live[code] = true
	}
	for _, code := range b729RemovedReasons {
		if live[code] {
			t.Errorf("reason %q is back in handoverReasons; B729 removed it", code)
		}
	}
	for _, code := range b729RemovedReasons {
		for _, scope := range []string{"close", "tools", "help", "doctor", "init"} {
			if len(code) <= len(scope)+1 || code[:len(scope)+1] != scope+"-" {
				continue
			}
			flag := code[len(scope)+1:]
			if got := handoverFlagReason(scope, flag, hUnrouted); got != hUnrouted {
				t.Errorf("handoverFlagReason(%q, %q) = %q, want the fallback", scope, flag, handoverReasons[got])
			}
		}
	}
}
