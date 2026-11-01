//go:build !unix

package ledger

import (
	"fmt"
	"os"
	"runtime"
)

// Release binaries target Unix; other platforms must not append without a lock.
func lockAppend(file *os.File, slug string) error {
	return fmt.Errorf("ledger append locking is not supported on %s", runtime.GOOS)
}
