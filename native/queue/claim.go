package queue

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ricardov03/code-forge/native/ledger"
)

// ClaimError carries Node's refusal without handing over after a queue mutation.
type ClaimError struct{ Code, Message string }

func (e *ClaimError) Error() string { return e.Message }
func claimCode(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EACCES:
			return "EACCES"
		case syscall.EPERM:
			return "EPERM"
		case syscall.ENOENT:
			return "ENOENT"
		case syscall.EEXIST:
			return "EEXIST"
		case syscall.EISDIR:
			return "EISDIR"
		case syscall.ENOTDIR:
			return "ENOTDIR"
		case syscall.ENOSPC:
			return "ENOSPC"
		case syscall.EROFS:
			return "EROFS"
		case syscall.EIO:
			return "EIO"
		case syscall.EMFILE:
			return "EMFILE"
		case syscall.ENFILE:
			return "ENFILE"
		case syscall.ENAMETOOLONG:
			return "ENAMETOOLONG"
		case syscall.EBUSY:
			return "EBUSY"
		case syscall.EXDEV:
			return "EXDEV"
		}
	}
	return "unknown"
}

// DropDone mirrors dropDoneIf without a retry marker: classification happens
// before this call, exclusion is rename, and restoration is no-overwrite link.
//
// B918: the ticket's finish timings are cleared before its marker is
// claimed, so the rerun is never taken for a finished ticket whose done
// marker was lost (the worker closes those, recoverFinished).
func DropDone(dir, id string, seen []byte) (string, error) {
	marker := filepath.Join(dir, id+".done")
	if err := ResetTimings(dir, id); err != nil {
		return "", &ClaimError{"fresh-claim-failed", fmt.Sprintf("the finish time of ticket %s could not be cleared (%s); nothing was queued", id, claimCode(err))}
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	claim := fmt.Sprintf("%s.%d.%x.fresh", marker, os.Getpid(), random)
	if err := os.Rename(marker, claim); err != nil {
		if os.IsNotExist(err) {
			return "gone", nil
		}
		return "", &ClaimError{"fresh-claim-failed", fmt.Sprintf("the done marker of ticket %s could not be claimed (%s); nothing was queued", id, claimCode(err))}
	}
	claimed, readErr := os.ReadFile(claim)
	if readErr == nil && bytes.Equal(seen, claimed) {
		if err := os.Remove(claim); err != nil {
			return "", &ClaimError{"fresh-claim-failed", fmt.Sprintf("the done marker of ticket %s was dropped but its claim could not be removed (%s); block reopen clears it; nothing was queued", id, claimCode(err))}
		}
		return "dropped", nil
	}
	linkErr := os.Link(claim, marker)
	unread := fmt.Sprintf("the claimed done marker of ticket %s could not be read (%s)", id, claimCode(readErr))
	if linkErr != nil && !os.IsExist(linkErr) {
		cause := fmt.Sprintf("the done marker of ticket %s changed and could not be restored (%s)", id, claimCode(linkErr))
		if readErr != nil {
			cause = unread + " and could not be restored (" + claimCode(linkErr) + ")"
		}
		body := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: "refused"}, {Key: "reason", Value: "fresh-restore-failed"}}
		data, _ := ledger.Stringify(body, false)
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(append(data, '\n'))
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
		left := "a refused fresh-restore-failed marker was written in its place, so the worker does not run the ticket"
		if os.IsExist(err) {
			left = "a newer marker is in place"
		}
		if err == nil || os.IsExist(err) {
			_ = os.Remove(claim)
		} else {
			left = fmt.Sprintf("the refused marker could not be written (%s) and no claim exists", claimCode(err))
			if _, e := os.Stat(claim); e == nil {
				left = fmt.Sprintf("the refused marker could not be written (%s); its claim was kept, so the worker does not run the ticket; block reopen clears it", claimCode(err))
			}
		}
		return "", &ClaimError{"fresh-restore-failed", cause + "; " + left + "; nothing was queued"}
	}
	_ = os.Remove(claim)
	if readErr != nil {
		left := "it was put back"
		if os.IsExist(linkErr) {
			left = "a newer marker is in place"
		}
		return "", &ClaimError{"fresh-restore-failed", unread + "; " + left + "; nothing was queued"}
	}
	return "changed", nil
}

// ResetTimings clears the finish timings (finished_at, duration_ms) of
// ticket id before a re-queue drops its done marker (B918): a ticket with
// finished_at and no done marker is a finished one the worker closes. A
// missing or unreadable ticket, or one with no finish time, is left as is.
func ResetTimings(dir, id string) error {
	if !ticketID.MatchString(id) {
		return nil
	}
	target := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	t, err := ledger.ParseObject(data)
	if err != nil || t == nil || t.Get("finished_at") == nil {
		return nil
	}
	t.Set("finished_at", nil)
	t.Set("duration_ms", nil)
	return publishFile(dir, target, t, true)
}
