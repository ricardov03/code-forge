// Package queue maintains a stat-invalidated index of the workspace review queue.
package queue

import (
	"bytes"
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
	names, err := os.ReadDir(idx.Dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("queue-unreadable")
	}
	seen := make(map[string]bool)
	for _, entry := range names {
		name := entry.Name()
		if !ticketName.MatchString(name) {
			continue
		}
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
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue-unreadable")
	}
	if prev, ok := idx.entries[name]; ok && os.SameFile(prev.info, info) && prev.info.Size() == info.Size() && prev.info.ModTime().Equal(info.ModTime()) {
		return prev.value, prev.err
	}
	var value Object
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("bad-ticket")
	}
	data, err := os.ReadFile(file)
	if isTicket {
		idx.TicketsParsed++
	}
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	if err == nil && isTicket {
		err = validate(name[:24], value)
	}
	if err != nil || value == nil {
		code := "result-unreadable"
		if isTicket {
			code = "bad-ticket"
		}
		err = fmt.Errorf("%s", code)
	}
	idx.entries[name] = cached{info: info, value: value, err: err, data: data}
	return value, err
}

// RetryOnce mirrors the join's one automatic retry. A marker alone cannot
// authorize a retry: its result must be readable and have the same status.
// The byte-checked rename claim preserves results replaced by a concurrent worker.
func (idx *Index) RetryOnce(repo, id string, marker Object) (Object, error) {
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
	same := readErr == nil && bytes.Equal(seen, claimed)
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
	t := Object{"ticket": id, "run": p.Run, "block": p.Block, "file": p.File, "content_hash": p.ContentHash, "enqueued_at": stamp, "queued_at": stamp, "started_at": nil, "finished_at": nil, "duration_ms": nil}
	if p.Tree != nil {
		t["tree"] = *p.Tree
	}
	if p.AcceptanceRevision != nil {
		t["acceptance_revision"] = *p.AcceptanceRevision
	}
	if p.ReopenedAt != nil {
		t["reopened_at"] = *p.ReopenedAt
	}
	if err := os.MkdirAll(idx.Dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(idx.Dir, ".enqueue-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = f.Chmod(0644)
	if err == nil {
		err = json.NewEncoder(f).Encode(t)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(f.Name(), filepath.Join(idx.Dir, id+".json")); err != nil && !os.IsExist(err) {
		return err
	}
	return idx.Refresh()
}

// Prune removes only entries indexed to this run/block, across all revisions,
// trees and ticket kinds. Callers must authorize it with a signed close row.
func (idx *Index) Prune(run, block string) (tickets, markers int, err error) {
	for name, item := range idx.entries {
		if !ticketName.MatchString(name) || item.value["run"] != run || item.value["block"] != block {
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

func validPath(s string) bool {
	if s == "" || filepath.IsAbs(s) || strings.Contains(s, "\\") {
		return false
	}
	parts := strings.Split(s, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return parts[0] != ".git" && parts[0] != ".code-forge"
}

func optional(t Object, key string) (*string, bool) {
	v, exists := t[key]
	if !exists {
		return nil, true
	}
	s, ok := v.(string)
	return &s, ok
}

func validate(id string, t Object) error {
	bad := fmt.Errorf("bad-ticket")
	if !validPath(text(t, "file")) {
		return bad
	}
	for _, key := range []string{"run", "block", "content_hash"} {
		if _, ok := t[key].(string); !ok {
			return bad
		}
	}
	tree, ok := optional(t, "tree")
	if !ok || tree != nil && !filepath.IsAbs(*tree) {
		return bad
	}
	p := ticket.Parts{Run: text(t, "run"), Block: text(t, "block"), File: text(t, "file"), ContentHash: text(t, "content_hash"), Tree: tree}
	if kind, exists := t["kind"]; exists {
		if kind != "answers" || !validPath(text(t, "by_file")) {
			return bad
		}
		if _, ok := t["by_hash"].(string); !ok {
			return bad
		}
		values, ok := t["answers"].([]any)
		if !ok || len(values) == 0 || len(values) > 10 {
			return bad
		}
		ids := []string{}
		for _, v := range values {
			s, ok := v.(string)
			if !ok || !answerID.MatchString(s) {
				return bad
			}
			ids = append(ids, s)
		}
		if !sort.StringsAreSorted(ids) {
			return bad
		}
		for i := 1; i < len(ids); i++ {
			if ids[i] == ids[i-1] {
				return bad
			}
		}
		parts := []string{"answers", p.Run, p.Block, p.File, p.ContentHash, text(t, "by_file"), text(t, "by_hash"), strings.Join(ids, ",")}
		if tree != nil {
			parts = append(parts, *tree)
		}
		hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		if hex.EncodeToString(hash[:])[:24] != id {
			return bad
		}
		return nil
	}
	p.AcceptanceRevision, ok = optional(t, "acceptance_revision")
	if !ok {
		return bad
	}
	p.ReopenedAt, ok = optional(t, "reopened_at")
	if !ok {
		return bad
	}
	if p.ReopenedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *p.ReopenedAt); err != nil {
			return bad
		}
	}
	if ticket.ReviewID(p) != id {
		return bad
	}
	return nil
}
