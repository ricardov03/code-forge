package queue

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

var surrogate = regexp.MustCompile(`(?i)\\ud[89a-f][0-9a-f]{2}`)

// Identity is Node's reviewIdentity without a reopen boundary: the ticket of
// p's current content, reusing the latest reopened attempt. No files have
// been changed when this returns an error.
func Identity(dir string, p ticket.Parts) (string, error) {
	_, id, err := Resolve(dir, p, nil)
	return id, err
}

// Resolve is Node's reviewIdentity. It returns p with the attempt's
// reopened_at (absent for an ordinary review) and that attempt's ticket id.
// With after (a reopen row's ts), the latest matching ticket enqueued later is
// reused, else after names a new attempt; without it the latest reopened
// attempt is reused. It never writes; Publish retires the earlier attempts.
func Resolve(dir string, p ticket.Parts, after *string) (ticket.Parts, string, error) {
	p.ReopenedAt = nil
	matching, err := scan(dir, p)
	if err != nil {
		return p, "", err
	}
	afterMs, afterOK := 0.0, false
	if after != nil {
		ms, ok, known := gatecheck.JSDateParse(*after, true)
		if !known {
			return p, "", fmt.Errorf("unsupported reopen time")
		}
		afterMs, afterOK = ms, ok
	}
	var latest, fresh Object
	for _, t := range matching {
		if _, reopened := t["reopened_at"]; reopened {
			latest = t
		}
		if afterOK {
			ms, ok, known := gatecheck.JSDateParse(t["enqueued_at"], true)
			if !known {
				return p, "", fmt.Errorf("unsupported ticket time")
			}
			if ok && ms > afterMs {
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
// localeCompare order, which reviewIdentity sorts attempts by.
var canonicalStamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$`)

// scan returns the valid tickets of p's scope (reopen attempts included) in
// Node's order: enqueued_at, then ticket id. Scope is checked before
// validation, like reviewIdentity.
func scan(dir string, p ticket.Parts) ([]Object, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	matching := []Object{}
	ordered := false
	for _, entry := range entries {
		if !ticketName.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || !json.Valid(data) {
			continue
		}
		if !utf8.Valid(data) || surrogate.Match(data) {
			return nil, fmt.Errorf("unsupported ticket encoding")
		}
		var t Object
		if json.Unmarshal(data, &t) != nil || t == nil {
			continue
		}
		if t["run"] != p.Run || t["block"] != p.Block || t["file"] != p.File || t["content_hash"] != p.ContentHash || !same(t, "tree", p.Tree, true) || !same(t, "acceptance_revision", p.AcceptanceRevision, false) {
			continue
		}
		// Any present kind would fail readTicket or select an answers ticket.
		if kind, exists := t["kind"]; exists {
			if kind == "answers" {
				continue
			}
			return nil, fmt.Errorf("unsupported ticket kind")
		}
		if err := validate(entry.Name()[:24], t); err != nil {
			return nil, err
		}
		if _, ok := t["enqueued_at"].(string); !ok {
			return nil, fmt.Errorf("unsupported ticket time")
		}
		if _, reopened := t["reopened_at"]; reopened {
			ordered = true
		}
		matching = append(matching, t)
	}
	// The order decides only between reopen attempts; Go's byte order equals
	// localeCompare for toISOString stamps and hand over anything else.
	if ordered && len(matching) > 1 {
		for _, t := range matching {
			if !canonicalStamp.MatchString(text(t, "enqueued_at")) {
				return nil, fmt.Errorf("unsupported ticket time")
			}
		}
	}
	sort.SliceStable(matching, func(i, j int) bool {
		a, b := text(matching[i], "enqueued_at"), text(matching[j], "enqueued_at")
		if a != b {
			return a < b
		}
		return text(matching[i], "ticket") < text(matching[j], "ticket")
	})
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
			matching, err := scan(dir, p)
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
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
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
