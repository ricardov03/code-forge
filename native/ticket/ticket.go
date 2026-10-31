// Package ticket computes ticket identities and review-state filenames.
package ticket

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Parts uses pointers to distinguish absent strings from present empty strings.
type Parts struct {
	Run                string  `json:"run"`
	Block              string  `json:"block"`
	File               string  `json:"file"`
	ContentHash        string  `json:"content_hash"`
	Tree               *string `json:"tree,omitempty"`
	AcceptanceRevision *string `json:"acceptance_revision,omitempty"`
	ReopenedAt         *string `json:"reopened_at,omitempty"`
}

func hash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])[:24]
}

// ID is the content ticket identity, including tree when present.
func ID(p Parts) string {
	parts := []string{p.Run, p.Block, p.File, p.ContentHash}
	if p.Tree != nil {
		parts = append(parts, *p.Tree)
	}
	return hash(strings.Join(parts, "\x00"))
}

// ReviewID includes the acceptance revision and reopen attempt in its identity.
func ReviewID(p Parts) string {
	id := ID(p)
	if p.AcceptanceRevision != nil {
		id = hash(id + "\x00" + *p.AcceptanceRevision)
	}
	if p.ReopenedAt != nil {
		id = hash(id + "\x00reopen\x00" + *p.ReopenedAt)
	}
	return id
}

// StateName returns the review-state basename for a repository-relative file.
func StateName(file string) string { return hash(file) + ".json" }
