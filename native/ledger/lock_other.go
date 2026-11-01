//go:build !unix

package ledger

import (
	"fmt"
	"runtime"
)

// Release binaries target Unix; other platforms must not append without a lock.
func appendOwnerAlive(pid int) (bool, error) {
	return false, fmt.Errorf("ledger append locking is not supported on %s", runtime.GOOS)
}
