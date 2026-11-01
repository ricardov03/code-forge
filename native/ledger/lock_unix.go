//go:build unix

package ledger

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockAppend(file *os.File, slug string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ledger %s is locked (%s)", slug, file.Name())
		}
		time.Sleep(25 * time.Millisecond)
	}
}
