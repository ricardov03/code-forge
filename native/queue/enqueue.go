package queue

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/jsonx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

// Identity is Node's reviewIdentity without a reopen boundary: the ticket of
// p's current content, reusing the latest reopened attempt. No files have
// been changed when this returns an error. An error that Is ErrScript is a
// JavaScript TypeError Node's reviewIdentity throws (it also Is
// ErrUnsupported, so callers that do not print Node's message hand over).
func Identity(dir string, p ticket.Parts) (string, error) {
	_, id, err := Resolve(dir, p, nil)
	return id, err
}

// ErrScript marks the TypeError Node's reviewIdentity throws for a ticket
// whose enqueued_at or ticket field its sort or Date.parse cannot read.
var ErrScript = errors.New("review identity throws")

type scriptError string

func (e scriptError) Error() string { return string(e) }
func (e scriptError) Is(target error) bool {
	return target == ErrScript || target == ErrUnsupported
}

// errIdentityUnknown stops the sort when Go cannot prove localeCompare.
var errIdentityUnknown = unsupported("unsupported ticket order")

// Resolve is Node's reviewIdentity. It returns p with the attempt's
// reopened_at (absent for an ordinary review) and that attempt's ticket id.
// With after (a reopen row's ts), the latest matching ticket enqueued later is
// reused, else after names a new attempt; without it the latest reopened
// attempt is reused. It never writes; Publish retires the earlier attempts.
func Resolve(dir string, p ticket.Parts, after *string) (ticket.Parts, string, error) {
	p.ReopenedAt = nil
	matching, err := scan(dir, p, after != nil)
	if err != nil {
		return p, "", err
	}
	afterMs, afterOK := math.NaN(), false
	if after != nil {
		afterMs, afterOK = gatecheck.V8DateParse(*after)
	}
	var latest, fresh Object
	for _, t := range matching {
		if _, reopened := t["reopened_at"]; reopened {
			latest = t
		}
		// Date.parse(t.enqueued_at) reads every ticket, even with no boundary.
		stamp, err := identityString(t, "enqueued_at")
		if err != nil {
			return p, "", err
		}
		if afterOK {
			if ms, ok := gatecheck.V8DateParse(stamp); ok && ms > afterMs {
				fresh = t
			}
		}
	}
	switch {
	case fresh != nil:
		if at, ok := fresh["reopened_at"].(string); ok {
			p.ReopenedAt = &at
		}
	case afterOK:
		at := *after
		p.ReopenedAt = &at
	case latest != nil:
		at := latest["reopened_at"].(string)
		p.ReopenedAt = &at
	}
	return p, ticket.ReviewID(p), nil
}

// canonicalStamp is the toISOString form whose byte order equals Node's
// localeCompare order, which reviewIdentity sorts attempts by. B720: measured
// equal for 20000 stamp pairs in 112 ICU locales (Node v24.22.1, ICU 78.3).
var canonicalStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$`)

// identityString is JavaScript's String(t[key]) for a JSON value, or the
// TypeError an object with its own toString key throws (JSON values are never
// callable, so ToPrimitive finds no method that returns a primitive).
func identityString(t Object, key string) (string, error) {
	v, has := t[key]
	if !has {
		return "undefined", nil
	}
	return jsString(v)
}

func jsString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "null", nil
	case string:
		return x, nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		return jsonx.Number(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e == nil {
				continue
			}
			s, err := jsString(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return strings.Join(parts, ","), nil
	case map[string]any:
		if _, own := x["toString"]; own {
			return "", scriptError("Cannot convert object to primitive value")
		}
		return "[object Object]", nil
	}
	return "", unsupported("unsupported ticket value")
}

// identityCompare is a.localeCompare(b) for reviewIdentity's sort: Go's byte
// order for two toISOString stamps (every measured locale agrees), else
// gatecheck.LocaleCompare, else errIdentityUnknown.
func identityCompare(a, b string) (int, error) {
	if canonicalStamp.MatchString(a) && canonicalStamp.MatchString(b) {
		return strings.Compare(a, b), nil
	}
	c, ok := gatecheck.LocaleCompare(a, b)
	if !ok {
		return 0, errIdentityUnknown
	}
	return c, nil
}

// identityOrder is reviewIdentity's comparator,
// (a, b) => a.enqueued_at.localeCompare(b.enqueued_at) || a.ticket.localeCompare(b.ticket):
// a receiver that is not a string has no localeCompare (TypeError), and the
// argument is read with String().
func identityOrder(a, b Object) (int, error) {
	for _, key := range []string{"enqueued_at", "ticket"} {
		value, has := a[key]
		receiver, ok := value.(string)
		if !ok {
			return 0, identityReceiverError(key, value, has)
		}
		argument, err := identityString(b, key)
		if err != nil {
			return 0, err
		}
		c, err := identityCompare(receiver, argument)
		if err != nil || c != 0 {
			return c, err
		}
	}
	return 0, nil
}

// identityReceiverError is V8's TypeError message for a.<key>.localeCompare
// on a receiver that is not a string (B730: recorded from Node v24).
func identityReceiverError(key string, value any, has bool) error {
	switch {
	case !has:
		return scriptError("Cannot read properties of undefined (reading 'localeCompare')")
	case value == nil:
		return scriptError("Cannot read properties of null (reading 'localeCompare')")
	}
	return scriptError("a." + key + ".localeCompare is not a function")
}

// scan returns the valid tickets of p's scope (reopen attempts included) in
// Node's order: matching.sort with identityOrder, run as V8's TimSort runs
// it. Like reviewIdentity it reads each ticket as readJSON does (UTF-8 with
// replacement, lone surrogate escapes kept), checks scope on that raw value,
// then runs readTicket's checks, whose WorkerError it returns. A comparator
// TypeError is a scriptError.
func scan(dir string, p ticket.Parts, bounded bool) ([]Object, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, &TicketError{Code: "queue-unreadable", Message: "the review queue cannot be read"}
	}
	matching := []Object{}
	for _, entry := range entries {
		if !ticketName.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		t, ok := parseTicketJSON(data).(map[string]any)
		if !ok || truthy(t["kind"]) {
			continue
		}
		if t["run"] != p.Run || t["block"] != p.Block || t["file"] != p.File || t["content_hash"] != p.ContentHash || !same(t, "tree", p.Tree, true) || !same(t, "acceptance_revision", p.AcceptanceRevision, false) {
			continue
		}
		if _, err := checkTicket(entry.Name()[:24], t); err != nil {
			return nil, err
		}
		matching = append(matching, t)
	}
	if len(matching) < 2 {
		return matching, nil
	}
	// After the sort Date.parse reads every enqueued_at: a value String()
	// throws on makes reviewIdentity throw whatever the sort does.
	plain, attempts := true, map[string]bool{}
	for _, t := range matching {
		if _, err := identityString(t, "enqueued_at"); err != nil {
			return nil, err
		}
		_, stamp := t["enqueued_at"].(string)
		_, id := t["ticket"].(string)
		plain = plain && stamp && id
		if at, reopened := t["reopened_at"].(string); reopened {
			attempts["reopened "+at] = true
		} else if bounded {
			attempts["ordinary"] = true
		}
	}
	// With text fields the comparator never throws, and the order only
	// picks the latest reopened attempt (with a boundary, the latest fresh
	// ticket, which may be an ordinary one): one attempt needs no order.
	if plain && len(attempts) < 2 {
		return matching, nil
	}
	if err := v8Sort(matching, identityOrder); err != nil {
		return nil, err
	}
	return matching, nil
}

// Publish is enqueueHash's write. A reopened attempt first retires every
// other matching attempt enqueued at or before its reopen (supersedeTicket),
// then publishes with Node's no-overwrite hard link and 0644 temp mode. There
// is deliberately no queue lock. Concurrent writers leave the first complete
// ticket's original bytes in place.
func Publish(dir string, p ticket.Parts, id string, now time.Time) error {
	stampMs := now.UnixMilli()
	if p.ReopenedAt != nil {
		at, ok, known := gatecheck.JSDateParse(*p.ReopenedAt, true)
		if !known {
			return fmt.Errorf("unsupported reopen time")
		}
		if ok {
			matching, err := scan(dir, p, false)
			if err != nil {
				return err
			}
			retire := []string{}
			for _, previous := range matching {
				ms, parsed, known := gatecheck.JSDateParse(previous["enqueued_at"], true)
				if !known {
					return fmt.Errorf("unsupported ticket time")
				}
				if text(previous, "ticket") != id && parsed && ms <= at {
					retire = append(retire, text(previous, "ticket"))
				}
			}
			for _, previous := range retire {
				if err := publishObject(dir, filepath.Join(dir, previous+".done"), ledger.Object{{Key: "ticket", Value: previous}, {Key: "status", Value: "refused"}, {Key: "reason", Value: "superseded"}}); err != nil {
					return err
				}
			}
			stampMs = max(stampMs, int64(at)+1)
		}
	}
	target := filepath.Join(dir, id+".json")
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	stamp := time.UnixMilli(stampMs).UTC().Format("2006-01-02T15:04:05.000Z")
	body := ledger.Object{
		{Key: "ticket", Value: id}, {Key: "run", Value: p.Run}, {Key: "block", Value: p.Block}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash},
		{Key: "enqueued_at", Value: stamp}, {Key: "queued_at", Value: stamp}, {Key: "started_at", Value: nil}, {Key: "finished_at", Value: nil}, {Key: "duration_ms", Value: nil},
	}
	if p.Tree != nil {
		body.Set("tree", *p.Tree)
	}
	if p.AcceptanceRevision != nil {
		body.Set("acceptance_revision", *p.AcceptanceRevision)
	}
	if p.ReopenedAt != nil {
		body.Set("reopened_at", *p.ReopenedAt)
	}
	return publishObject(dir, target, body)
}

// ResolveAfter selects the latest attempt newer than a rebase checkpoint, or
// reserves a checkpoint-scoped identity and retires earlier matching attempts.
func (idx *Index) ResolveAfter(p ticket.Parts, after string) (ticket.Parts, error) {
	boundary, err := time.Parse(time.RFC3339Nano, after)
	if err != nil {
		return p, err
	}
	matching := []Object{}
	var fresh Object
	for _, t := range idx.Block(p.Run, p.Block, p.Tree, p.AcceptanceRevision) {
		if t["file"] != p.File || t["content_hash"] != p.ContentHash {
			continue
		}
		matching = append(matching, t)
		stamp, err := time.Parse(time.RFC3339Nano, text(t, "enqueued_at"))
		if err != nil {
			return p, err
		}
		if stamp.After(boundary) {
			fresh = t
		}
	}
	if fresh != nil {
		if at, ok := fresh["reopened_at"].(string); ok {
			p.ReopenedAt = &at
		}
	} else {
		p.ReopenedAt = &after
	}
	id := ticket.ReviewID(p)
	if p.ReopenedAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *p.ReopenedAt)
		if err != nil {
			return p, err
		}
		for _, t := range matching {
			stamp, _ := time.Parse(time.RFC3339Nano, text(t, "enqueued_at"))
			if text(t, "ticket") != id && !stamp.After(at) {
				previous := text(t, "ticket")
				if err := publishObject(idx.Dir, filepath.Join(idx.Dir, previous+".done"), ledger.Object{{Key: "ticket", Value: previous}, {Key: "status", Value: "refused"}, {Key: "reason", Value: "superseded"}}); err != nil {
					return p, err
				}
			}
		}
	}
	return p, nil
}

func AnswersID(p ticket.Parts, byFile, byHash string, answers []string) string {
	ids := append([]string{}, answers...)
	sort.Strings(ids)
	parts := []string{"answers", p.Run, p.Block, p.File, p.ContentHash, byFile, byHash, strings.Join(ids, ",")}
	if p.Tree != nil {
		parts = append(parts, *p.Tree)
	}
	// update(string): a lone surrogate is hashed as U+FFFD, as Node does (B704)
	hash := sha256.Sum256([]byte(jsonx.WellFormed(strings.Join(parts, "\x00"))))
	return hex.EncodeToString(hash[:])[:24]
}

func PublishAnswers(dir string, p ticket.Parts, id, byFile, byHash string, ids []string, now time.Time) error {
	target := filepath.Join(dir, id+".json")
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	stamp := now.UTC().Format("2006-01-02T15:04:05.000Z")
	body := ledger.Object{{Key: "ticket", Value: id}, {Key: "kind", Value: "answers"}, {Key: "run", Value: p.Run}, {Key: "block", Value: p.Block}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash}, {Key: "by_file", Value: byFile}, {Key: "by_hash", Value: byHash}, {Key: "answers", Value: ids}, {Key: "enqueued_at", Value: stamp}, {Key: "queued_at", Value: stamp}, {Key: "started_at", Value: nil}, {Key: "finished_at", Value: nil}, {Key: "duration_ms", Value: nil}}
	if p.Tree != nil {
		body.Set("tree", *p.Tree)
	}
	return publishFile(dir, target, body, true)
}

func publishObject(dir, target string, body ledger.Object) error {
	return publishFile(dir, target, body, false)
}

func publishFile(dir, target string, body ledger.Object, replace bool) error {
	data, err := ledger.Stringify(body, false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0777); err != nil {
		return err
	}
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%x.tmp", target, os.Getpid(), random)
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp, target)
	}
	if err := os.Link(tmp, target); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
