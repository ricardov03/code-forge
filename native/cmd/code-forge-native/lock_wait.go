package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/state"
)

// defaultLockWait is how long gates run and block rebase wait for a run lock
// another verb holds (block land holds it through its whole merge gate).
const defaultLockWait = 15 * time.Minute

// lockWaitFlag removes --lock-wait <d> / --lock-wait=<d> from args and returns
// the wait: a Go duration such as 90s, 15m or 500ms (B393).
func lockWaitFlag(args []string) ([]string, time.Duration, string) {
	rest, wait := []string{}, defaultLockWait
	given := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := ""
		if arg == "--lock-wait" {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return args, 0, "--lock-wait requires a value"
			}
			i++
			value = args[i]
		} else if v, ok := strings.CutPrefix(arg, "--lock-wait="); ok {
			value = v
		} else {
			rest = append(rest, arg)
			continue
		}
		if given {
			return args, 0, "--lock-wait given more than once"
		}
		given = true
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return args, 0, "--lock-wait must be a positive duration like 90s, 15m or 500ms"
		}
		wait = d
	}
	return rest, wait, ""
}

// useLockWait makes this process wait up to wait for a held run lock and
// print one line naming its holder the first time it waits.
func useLockWait(verb string, wait time.Duration, stderr io.Writer) {
	state.SetLockWait(wait, func(holder string) {
		fmt.Fprintf(stderr, "%s: waiting for run lock held by %s\n", verb, holder)
	})
}
