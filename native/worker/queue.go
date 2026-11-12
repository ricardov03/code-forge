package worker

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/state"
)

var (
	ticketName = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)
	ticketID   = regexp.MustCompile(`^[0-9a-f]{24}$`)
	// B83: a dropDoneIf claim of a done marker.
	claimName = regexp.MustCompile(`^([0-9a-f]{24})\.done\.\d+\.[0-9a-f]{8}\.fresh$`)
)

// DefaultParallelTickets is review.parallel_tickets when unset or invalid (B42).
const DefaultParallelTickets = 6

// MaxParallelTickets bounds review.parallel_tickets and review.provider_parallel.
const MaxParallelTickets = 16

// DefaultProviderParallel is review.provider_parallel when unset or invalid.
const DefaultProviderParallel = 4

// DefaultPollMs is the worker's queue poll interval.
const DefaultPollMs = 250

func intIn(v any, lo, hi int) (int, bool) {
	n, ok := v.(float64)
	if !ok || n < float64(lo) || n > float64(hi) || n != float64(int(n)) {
		return 0, false
	}
	return int(n), true
}

func reviewSection(cfg map[string]any) map[string]any {
	r, _ := cfg["review"].(map[string]any)
	return r
}

// ParallelTickets is loop.mjs parallelTickets.
func ParallelTickets(cfg map[string]any) int {
	if n, ok := intIn(reviewSection(cfg)["parallel_tickets"], 1, MaxParallelTickets); ok {
		return n
	}
	return DefaultParallelTickets
}

// ProviderParallel is the pool's review.provider_parallel (loop.mjs poolSettings).
func ProviderParallel(cfg map[string]any) int {
	if n, ok := intIn(reviewSection(cfg)["provider_parallel"], 1, MaxParallelTickets); ok {
		return n
	}
	return DefaultProviderParallel
}

// FairShare is review.fair_share === true.
func FairShare(cfg map[string]any) bool { return reviewSection(cfg)["fair_share"] == true }

// IsDone is queue.mjs isDone: the ticket's done marker exists.
func IsDone(root, id string) bool {
	if !ticketID.MatchString(id) {
		return false
	}
	_, err := os.Stat(filepath.Join(QueueDir(root), id+".done"))
	return err == nil
}

// Pending is queue.mjs pendingTickets: tickets with no done marker and no
// B83 claim, oldest first (enqueued_at, then id). An unreadable queue is empty.
func Pending(root string) []string {
	list, err := os.ReadDir(QueueDir(root))
	if err != nil {
		return nil
	}
	present := map[string]bool{}
	claimed := map[string]bool{}
	for _, e := range list {
		present[e.Name()] = true
		if m := claimName.FindStringSubmatch(e.Name()); m != nil {
			claimed[m[1]] = true
		}
	}
	type item struct{ id, at string }
	var items []item
	for _, e := range list {
		name := e.Name()
		if !ticketName.MatchString(name) {
			continue
		}
		id := name[:24]
		if present[id+".done"] || claimed[id] {
			continue
		}
		at := ""
		if t, err := readJSON(filepath.Join(QueueDir(root), name)); err == nil && t != nil {
			at, _ = t.Get("enqueued_at").(string)
		}
		items = append(items, item{id, at})
	}
	// JS string compare is UTF-16 order; enqueued_at and ids are ASCII.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].at == items[j].at {
			return items[i].id < items[j].id
		}
		return items[i].at < items[j].at
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.id
	}
	return out
}

// ClaimTicket is the pool's cross-process ticket claim: Node's
// tryLock(`ticket\0<run>\0<id>`), the same lock file.
func ClaimTicket(run, id string) (release func(), ok bool, err error) {
	res, err := filelock.TryLock("ticket\x00"+run+"\x00"+id, filelock.Options{})
	if err != nil || !res.OK {
		return nil, false, err
	}
	return res.Release, true, nil
}

var errnoNames = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOTDIR: "ENOTDIR",
	syscall.EISDIR: "EISDIR", syscall.EEXIST: "EEXIST", syscall.EBUSY: "EBUSY", syscall.EMFILE: "EMFILE",
	syscall.ENOSPC: "ENOSPC", syscall.EROFS: "EROFS", syscall.EIO: "EIO", syscall.ENFILE: "ENFILE",
	syscall.ELOOP: "ELOOP", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.EAGAIN: "EAGAIN",
}

// Kinded lets an error name its kind for ErrorKind (Node: `name:code`).
type Kinded interface{ Kind() string }

var kindPattern = regexp.MustCompile(`^[A-Za-z0-9_:.-]{1,64}$`)

// ErrorKind is loop.mjs errorKind: an error's code or class only, never its
// message: an errno name (EACCES), a Kinded kind, else "Error".
func ErrorKind(err error) string {
	var kinded Kinded
	if errors.As(err, &kinded) {
		if k := kinded.Kind(); kindPattern.MatchString(k) {
			return k
		}
		return "Error"
	}
	var stateErr *state.Error
	if errors.As(err, &stateErr) {
		if k := "StateError:" + stateErr.Code; kindPattern.MatchString(k) {
			return k
		}
		return "Error"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name, ok := errnoNames[errno]; ok {
			return name
		}
	}
	return "Error"
}
