//go:build !unix

package daemon

import (
	"fmt"
	"runtime"
	"time"
)

// Serve requires Unix sockets and process APIs, as used by the release targets.
func Serve(home, run, version string, idle time.Duration) error {
	return fmt.Errorf("native daemon is not supported on %s", runtime.GOOS)
}
