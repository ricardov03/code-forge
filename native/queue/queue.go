// Package queue maintains a stat-invalidated index of the workspace review queue.
package queue

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

type Object = map[string]any

type cached struct {
	info  os.FileInfo
	value Object
	err   error
	data  []byte
}

// Index caches parsed tickets by filename and indexes valid entries by block/file.
// Done markers share the stat cache but are not counted as ticket parses.
type Index struct {
	Dir           string
	TicketsParsed int64
	entries       map[string]cached
	byBlock       map[string]map[string][]Object
}

var ticketName = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)
var ticketID = regexp.MustCompile(`^[0-9a-f]{24}$`)
var answerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// Refresh stats every ticket, but reads JSON only when the inode, size or mtime
// changes. Unreadable tickets fail closed, including tickets outside this block.
func (idx *Index) Refresh() error {
	if idx.entries == nil {
		idx.entries = make(map[string]cached)
	}
	// Node's blockTickets and reviewIdentity throw WorkerError
	// queue-unreadable for a queue they cannot list; only block close reads
	// the raw scandir error, through TicketNames itself (B718, B735).
	names, err := TicketNames(idx.Dir)
	if err != nil {
		return fmt.Errorf("queue-unreadable")
	}
	seen := make(map[string]bool)
	for _, name := range names {
		seen[name] = true
		if _, err := idx.read(name, true); err != nil {
			return err
		}
	}
	for name := range idx.entries {
		if ticketName.MatchString(name) && !seen[name] {
			delete(idx.entries, name)
		}
	}
	idx.byBlock = make(map[string]map[string][]Object)
	for name, item := range idx.entries {
		if !ticketName.MatchString(name) {
			continue
		}
		t := item.value
		key := text(t, "run") + "\x00" + text(t, "block")
		if idx.byBlock[key] == nil {
			idx.byBlock[key] = make(map[string][]Object)
		}
		file := text(t, "file")
		idx.byBlock[key][file] = append(idx.byBlock[key][file], t)
	}
	return nil
}

func (idx *Index) read(name string, isTicket bool) (Object, error) {
	file := filepath.Join(idx.Dir, name)
	info, err := os.Stat(file)
	if os.IsNotExist(err) {
		delete(idx.entries, name)
		// A dangling link is still listed: readTicket cannot read it.
		if _, lerr := os.Lstat(file); isTicket && lerr == nil {
			return nil, notReadable(name[:24])
		}
		return nil, nil
	}
	if err != nil {
		if isTicket {
			return nil, notReadable(name[:24])
		}
		return nil, fmt.Errorf("queue-unreadable")
	}
	if prev, ok := idx.entries[name]; ok && os.SameFile(prev.info, info) && prev.info.Size() == info.Size() && prev.info.ModTime().Equal(info.ModTime()) {
		return prev.value, prev.err
	}
	if !info.Mode().IsRegular() {
		if isTicket {
			return nil, notReadable(name[:24])
		}
		return nil, fmt.Errorf("bad-ticket")
	}
	data, err := os.ReadFile(file)
	var value Object
	if isTicket {
		idx.TicketsParsed++
		if err != nil {
			data = nil
		}
		// readTicket: readFileSync + JSON.parse, then its checks in order.
		value, err = checkTicket(name[:24], parseTicketJSON(data))
		if err != nil {
			value = nil
		}
	} else {
		if err == nil {
			err = json.Unmarshal(data, &value)
		}
		if err != nil || value == nil {
			err = fmt.Errorf("result-unreadable")
		}
	}
	idx.entries[name] = cached{info: info, value: value, err: err, data: data}
	return value, err
}

// RetryOnce mirrors the join's one automatic retry. A marker alone cannot
// authorize a retry: its result must be readable and have the same status.
// The byte-checked rename claim preserves results replaced by a concurrent worker.
func (idx *Index) RetryOnce(repo, id string, marker Object) (Object, error) {
	if !ticketID.MatchString(id) {
		return nil, fmt.Errorf("bad-ticket")
	}
	status := text(marker, "status")
	if status != "stale" && status != "unavailable" && status != "split_required" {
		return marker, nil
	}
	if _, err := os.Stat(filepath.Join(idx.Dir, id+".retried")); err == nil {
		return marker, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	rel := text(marker, "result")
	if !strings.HasPrefix(rel, ".code-forge/reviews/") {
		return marker, nil
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return marker, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(repo, rel))
	var result Object
	if err != nil || json.Unmarshal(data, &result) != nil || result["status"] != status {
		return marker, nil
	}
	seen := idx.entries[id+".done"].data
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	file := filepath.Join(idx.Dir, id+".done")
	claim := fmt.Sprintf("%s.%d.%x.fresh", file, os.Getpid(), random)
	if err := os.Rename(file, claim); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("fresh-claim-failed")
	}
	claimed, readErr := os.ReadFile(claim)
	same := readErr == nil && seen != nil && bytes.Equal(seen, claimed)
	var retryErr error
	if same {
		var f *os.File
		f, retryErr = os.OpenFile(filepath.Join(idx.Dir, id+".retried"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if retryErr == nil {
			_, retryErr = f.WriteString(id + "\n")
			closeErr := f.Close()
			if retryErr == nil {
				retryErr = closeErr
			}
		}
		if retryErr == nil {
			if err := os.Remove(claim); err != nil {
				return nil, fmt.Errorf("fresh-claim-failed")
			}
			delete(idx.entries, id+".done")
			return idx.Marker(id)
		}
	}
	if err := os.Link(claim, file); err != nil && !os.IsExist(err) {
		// Preserve the claim if a refusal cannot be published: Node's pending
		// scanner treats this filename as done, preventing an unintended rerun.
		f, createErr := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr == nil {
			encodeErr := json.NewEncoder(f).Encode(Object{"ticket": id, "status": "refused", "reason": "fresh-restore-failed"})
			closeErr := f.Close()
			if encodeErr == nil && closeErr == nil {
				os.Remove(claim)
			}
		}
		return nil, fmt.Errorf("fresh-restore-failed")
	}
	os.Remove(claim)
	if readErr != nil {
		return nil, fmt.Errorf("fresh-restore-failed")
	}
	if retryErr != nil && !os.IsExist(retryErr) {
		return nil, fmt.Errorf("fresh-claim-failed")
	}
	delete(idx.entries, id+".done")
	return idx.Marker(id)
}

// Block returns the same review-ticket list as Node blockTickets. A nil tree or
// revision means that property must be absent (or null for tree).
func (idx *Index) Block(run, block string, tree, revision *string) []Object {
	out := []Object{}
	for _, group := range idx.byBlock[run+"\x00"+block] {
		for _, t := range group {
			if t["kind"] != nil || !same(t, "tree", tree, true) || !same(t, "acceptance_revision", revision, false) {
				continue
			}
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := text(out[i], "enqueued_at"), text(out[j], "enqueued_at")
		if a == b {
			return text(out[i], "ticket") < text(out[j], "ticket")
		}
		return a < b
	})
	return out
}

// BlockChecked is Block with blockTickets' sort run as Node runs it (B730):
// when a ticket's enqueued_at or ticket field is not a string, V8 sorts the
// tickets in readdir order with (a, b) => a.enqueued_at.localeCompare(...)
// || a.ticket.localeCompare(...), and the TypeError the comparator throws is
// returned as an ErrScript error. Text fields keep Block's order.
func (idx *Index) BlockChecked(run, block string, tree, revision *string) ([]Object, error) {
	out := idx.Block(run, block, tree, revision)
	plain := true
	for _, t := range out {
		_, stamp := t["enqueued_at"].(string)
		_, id := t["ticket"].(string)
		plain = plain && stamp && id
	}
	if plain || len(out) < 2 {
		return out, nil
	}
	names := make([]string, 0, len(idx.entries))
	for name := range idx.entries {
		if ticketName.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	ordered := make([]Object, 0, len(out))
	for _, name := range names {
		t := idx.entries[name].value
		if t == nil || t["kind"] != nil || t["run"] != run || t["block"] != block || !same(t, "tree", tree, true) || !same(t, "acceptance_revision", revision, false) {
			continue
		}
		ordered = append(ordered, t)
	}
	if err := v8Sort(ordered, identityOrder); err != nil {
		return nil, err
	}
	return ordered, nil
}

func same(t Object, key string, expected *string, nullAbsent bool) bool {
	v, exists := t[key]
	if expected == nil {
		return !exists || nullAbsent && v == nil
	}
	return v == *expected
}

// Marker validates just the unsigned progress marker, as Node's block wait does.
func (idx *Index) Marker(id string) (Object, error) {
	if !ticketID.MatchString(id) {
		return nil, fmt.Errorf("bad-ticket")
	}
	v, err := idx.read(id+".done", false)
	if err != nil || v == nil {
		return v, err
	}
	if v["ticket"] != id || text(v, "status") == "" {
		return nil, fmt.Errorf("result-unreadable")
	}
	return v, nil
}

// Deliver records the first findings handoff without reading a signed result.
func (idx *Index) Deliver(block, file, id string) (bool, error) {
	if !ticketID.MatchString(id) {
		return false, fmt.Errorf("bad-ticket")
	}
	name := filepath.Join(idx.Dir, id+".delivered")
	valid := func() (bool, error) {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			return false, nil
		}
		var value Object
		if err != nil || json.Unmarshal(data, &value) != nil || value["block"] != block || value["file"] != file || value["ticket"] != id {
			return false, fmt.Errorf("result-unreadable")
		}
		return true, nil
	}
	if exists, err := valid(); exists || err != nil {
		return false, err
	}
	f, err := os.CreateTemp(idx.Dir, ".delivery-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(Object{"block": block, "file": file, "ticket": id})
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Link(f.Name(), name); err != nil {
		if !os.IsExist(err) {
			return false, err
		}
		exists, err := valid()
		if !exists && err == nil {
			err = fmt.Errorf("result-unreadable")
		}
		return false, err
	}
	return true, nil
}

// Ensure publishes a missing current-content review without replacing a
// concurrent enqueue. The daemon never calls a separate Node process to queue it.
func (idx *Index) Ensure(p ticket.Parts) error {
	id := ticket.ReviewID(p)
	now := time.Now().UTC()
	if p.ReopenedAt != nil {
		if after, err := time.Parse(time.RFC3339Nano, *p.ReopenedAt); err == nil && !now.After(after) {
			now = after.Add(time.Millisecond)
		}
	}
	stamp := now.Format("2006-01-02T15:04:05.000Z")
	// enqueueHash's body: Node's key order and JSON.stringify bytes.
	body := ledger.Object{{Key: "ticket", Value: id}, {Key: "run", Value: p.Run}, {Key: "block", Value: p.Block}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash},
		{Key: "enqueued_at", Value: stamp}, {Key: "queued_at", Value: stamp}, {Key: "started_at", Value: nil}, {Key: "finished_at", Value: nil}, {Key: "duration_ms", Value: nil}}
	if p.Tree != nil {
		body.Set("tree", *p.Tree)
	}
	if p.AcceptanceRevision != nil {
		body.Set("acceptance_revision", *p.AcceptanceRevision)
	}
	if p.ReopenedAt != nil {
		body.Set("reopened_at", *p.ReopenedAt)
	}
	if err := publishObject(idx.Dir, filepath.Join(idx.Dir, id+".json"), body); err != nil {
		return err
	}
	return idx.Refresh()
}

// TicketRuns names the runs that hold at least one indexed ticket. A run not
// named has nothing PruneRun could remove (B755).
func (idx *Index) TicketRuns() map[string]bool {
	runs := make(map[string]bool)
	for name, item := range idx.entries {
		if run, ok := item.value["run"].(string); ok && ticketName.MatchString(name) {
			runs[run] = true
		}
	}
	return runs
}

// Prune removes only entries indexed to this run/block, across all revisions,
// trees and ticket kinds. Callers must authorize it with a signed close row.
func (idx *Index) Prune(run, block string) (tickets, markers int, err error) {
	return idx.prune(run, &block)
}

// PruneRun removes every ticket and marker belonging to an authorized ended run.
func (idx *Index) PruneRun(run string) (tickets, markers int, err error) {
	return idx.prune(run, nil)
}

func (idx *Index) prune(run string, block *string) (tickets, markers int, err error) {
	for name, item := range idx.entries {
		if !ticketName.MatchString(name) || item.value["run"] != run || (block != nil && item.value["block"] != *block) {
			continue
		}
		id := name[:24]
		for _, suffix := range []string{".done", ".delivered", ".retried", ".json"} {
			err = os.Remove(filepath.Join(idx.Dir, id+suffix))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return tickets, markers, err
			}
			delete(idx.entries, id+suffix)
			if suffix == ".json" {
				tickets++
			} else {
				markers++
			}
		}
	}
	err = idx.Refresh()
	return
}

func text(obj Object, key string) string { s, _ := obj[key].(string); return s }

// validate is readTicket's checks of an already parsed ticket.
func validate(id string, t Object) error {
	_, err := checkTicket(id, t)
	return err
}
