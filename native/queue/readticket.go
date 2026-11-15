package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/jsonx"
	"github.com/ricardov03/code-forge/native/ticket"
)

// TicketError is the WorkerError Node's readTicket throws: Code is its code
// (bad-ticket or bad-path) and Error() its message, byte for byte.
type TicketError struct {
	Code, Message string
}

func (e *TicketError) Error() string { return e.Message }

// ErrUnsupported marks a ticket Go cannot judge as Node does (a reopen stamp
// whose Date.parse result Go cannot prove, or an attempt order that needs
// localeCompare); callers hand those over instead of guessing.
var ErrUnsupported = errors.New("unsupported ticket")

type unsupported string

func (u unsupported) Error() string        { return string(u) }
func (u unsupported) Is(target error) bool { return target == ErrUnsupported }

// parseTicketJSON is Node's readJSON over bytes already read: readFileSync's
// UTF-8 decoding (each invalid sequence one U+FFFD) and JSON.parse, which
// keeps a lone surrogate escape. nil when JSON.parse throws.
func parseTicketJSON(data []byte) any {
	v, err := jsonx.Parse(jsonx.DecodeUTF8(data))
	if err != nil {
		return nil
	}
	return v
}

// ReadTicket is Node's readTicket(repoRoot, id) over dir (the queue directory):
// any read or parse failure is "not readable JSON", then the id must match the
// content. The error is a *TicketError with Node's message, or ErrUnsupported.
func ReadTicket(dir, id string) (Object, error) {
	file := filepath.Join(dir, id+".json")
	// A FIFO would block readFileSync forever; Go refuses it as unreadable.
	if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
		return nil, notReadable(id)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, notReadable(id)
	}
	return checkTicket(id, parseTicketJSON(data))
}

// TicketNames is readdirSync of the queue filtered to ticket files, in its
// (byte) order. A missing directory has none; any other error is libuv's,
// which blockIOMessage prints as Node does ("ENOTDIR: not a directory,
// scandir '<dir>'").
func TicketNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			if errno == syscall.ENOENT {
				return nil, nil
			}
			return nil, &os.PathError{Op: "scandir", Path: dir, Err: errno}
		}
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if ticketName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func notReadable(id string) error {
	return &TicketError{Code: "bad-ticket", Message: "ticket " + id + " is not readable JSON"}
}

func badTicket(id, why string) error {
	return &TicketError{Code: "bad-ticket", Message: "ticket " + id + " " + why}
}

// rowPathError is assertRowPath's refusal of value, or nil.
func rowPathError(value any) error {
	bad := func(message string) error { return &TicketError{Code: "bad-path", Message: message} }
	s, ok := value.(string)
	if !ok || s == "" || len(jsonx.Units(s)) > 4096 {
		return bad("file must be a non-empty relative path")
	}
	if strings.ContainsAny(s, "\x00\\") {
		return bad("file must be a POSIX path")
	}
	if path.IsAbs(s) {
		return bad("file must be relative to the repository root, not absolute")
	}
	segments := strings.Split(s, "/")
	if slices.ContainsFunc(segments, func(seg string) bool { return seg == "" || seg == "." || seg == ".." }) {
		return bad(`file must not contain "", "." or ".." segments`)
	}
	// toLowerCase can make these two names only from their ASCII letters.
	if top := asciiLower(segments[0]); top == ".git" || top == ".code-forge" {
		return bad("file may not be under " + segments[0] + "/")
	}
	return nil
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// truthy is JavaScript's ToBoolean for a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && x == x
	case string:
		return x != ""
	}
	return true
}

func digest(parts []string) string {
	for i, p := range parts {
		// createHash().update(string) encodes a lone surrogate as U+FFFD.
		parts[i] = jsonx.WellFormed(p)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:24]
}

// checkTicket is readTicket after readJSON: v is JSON.parse's value (nil
// for null or a read/parse failure), checked in Node's order and messages.
func checkTicket(id string, v any) (Object, error) {
	var t Object
	switch x := v.(type) {
	case map[string]any:
		t = x
	case []any:
		// An array is an object to typeof; it has none of the fields.
		t = Object{}
	default:
		// null, a number, string or boolean: typeof is not 'object'.
		return nil, notReadable(id)
	}
	if err := rowPathError(t["file"]); err != nil {
		return nil, err
	}
	var tree *string
	if raw := t["tree"]; raw != nil {
		s, ok := raw.(string)
		if !ok || !path.IsAbs(s) {
			return nil, badTicket(id, "names a tree that is not an absolute path")
		}
		tree = &s
	}
	mismatch := badTicket(id, "does not match its id")
	run, ok1 := t["run"].(string)
	block, ok2 := t["block"].(string)
	hash, ok3 := t["content_hash"].(string)
	if !ok1 || !ok2 || !ok3 {
		return nil, mismatch
	}
	file := t["file"].(string)
	if kind, exists := t["kind"]; exists {
		if kind != "answers" {
			return nil, badTicket(id, "has an unknown kind")
		}
		if err := rowPathError(t["by_file"]); err != nil {
			return nil, err
		}
		ids, ok := answerIDs(t["answers"])
		if !ok {
			return nil, badTicket(id, "names no valid finding ids")
		}
		byHash, ok := t["by_hash"].(string)
		given := []string{}
		for _, a := range t["answers"].([]any) {
			given = append(given, a.(string))
		}
		if !ok || strings.Join(ids, ",") != strings.Join(given, ",") {
			return nil, mismatch
		}
		sorted := slices.Clone(given)
		slices.SortFunc(sorted, jsonx.CompareUTF16)
		parts := []string{"answers", run, block, file, hash, t["by_file"].(string), byHash, strings.Join(sorted, ",")}
		if tree != nil {
			parts = append(parts, *tree)
		}
		if digest(parts) != id {
			return nil, mismatch
		}
		return t, nil
	}
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash, Tree: tree}
	if raw, exists := t["acceptance_revision"]; exists {
		s, ok := raw.(string)
		if !ok {
			return nil, badTicket(id, "has an invalid acceptance revision")
		}
		p.AcceptanceRevision = &s
	}
	if raw, exists := t["reopened_at"]; exists {
		s, ok := raw.(string)
		if !ok {
			return nil, badTicket(id, "has an invalid reopen timestamp")
		}
		_, finite, known := gatecheck.JSDateParse(s, true)
		if !known {
			return nil, unsupported("unsupported reopen time")
		}
		if !finite {
			return nil, badTicket(id, "has an invalid reopen timestamp")
		}
		p.ReopenedAt = &s
	}
	parts := []string{p.Run, p.Block, p.File, p.ContentHash}
	if p.Tree != nil {
		parts = append(parts, *p.Tree)
	}
	sum := digest(parts)
	if p.AcceptanceRevision != nil {
		sum = digest([]string{sum, *p.AcceptanceRevision})
	}
	if p.ReopenedAt != nil {
		sum = digest([]string{sum, "reopen", *p.ReopenedAt})
	}
	if sum != id {
		return nil, mismatch
	}
	return t, nil
}

// answerIDs is assertAnswers: 1..10 finding ids after dropping repeats,
// returned sorted. ok false where it throws.
func answerIDs(v any) ([]string, bool) {
	values, ok := v.([]any)
	if !ok || len(values) == 0 {
		return nil, false
	}
	ids := []string{}
	for _, value := range values {
		s, ok := value.(string)
		if !ok || !answerID.MatchString(s) {
			return nil, false
		}
		if !slices.Contains(ids, s) {
			ids = append(ids, s)
		}
	}
	slices.SortFunc(ids, jsonx.CompareUTF16)
	return ids, len(ids) <= 10
}
