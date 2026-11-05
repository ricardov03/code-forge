package queue

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/ticket"
)

var surrogate = regexp.MustCompile(`(?i)\\ud[89a-f][0-9a-f]{2}`)

// Identity scopes before validating stored tickets, like reviewIdentity. Rare
// reopened attempts stay with Node, which also retires their prior attempts.
// No files have been changed when this returns an error.
func Identity(dir string, p ticket.Parts) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, entry := range entries {
		if !ticketName.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || !json.Valid(data) {
			continue
		}
		if !utf8.Valid(data) || surrogate.Match(data) {
			return "", fmt.Errorf("unsupported ticket encoding")
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
			return "", fmt.Errorf("unsupported ticket kind")
		}
		if err := validate(entry.Name()[:24], t); err != nil {
			return "", err
		}
		if _, ok := t["enqueued_at"].(string); !ok {
			return "", fmt.Errorf("unsupported ticket time")
		}
		if _, reopened := t["reopened_at"]; reopened {
			return "", fmt.Errorf("reopened ticket requires Node")
		}
	}
	return ticket.ReviewID(p), nil
}

// Publish uses Node's no-overwrite hard-link publication and 0644 temp mode.
// There is deliberately no queue lock. Concurrent writers leave the first
// complete ticket's original bytes in place.
func Publish(dir string, p ticket.Parts, id string, now time.Time) error {
	target := filepath.Join(dir, id+".json")
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	stamp := now.UTC().Format("2006-01-02T15:04:05.000Z")
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
	if err := os.Link(tmp, target); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}
