package worker

// B496 step 1: the fix-loop state load (src/worker/review-state.mjs
// loadState): the run's review.state anchors checked first (one that does
// not verify, or has no integer seq or string state_sha, makes the whole
// block tampered), then the state file at the new path, else at the legacy
// path (B69), checked against the latest anchor of the file; a file that is
// gone is rebuilt from the anchor's signed state_body (B68: restored) or is
// lost when the anchor has none. A legacy file that checks out moves to the
// new path (never over a file already there) when migrate is set.
//
// Text Node would decode with replacement characters (a state file, a blob
// or the current file that is not UTF-8) is ErrUnsupported: Node hashes the
// decoded text, Go would hash the bytes.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// StateEvent and RestoredEvent are review-state.mjs STATE_EVENT and RESTORED_EVENT.
const (
	StateEvent    = "review.state"
	RestoredEvent = "review.state_restored"
)

var (
	blockIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	runIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// StateWhere is review-state.mjs Where plus the content root: Rows are the
// run's ledger rows in order (insertion-ordered objects, so a restored state
// keeps Node's key order). RunRootDir "" is Node's absent runRootDir (no
// legacy path).
type StateWhere struct {
	Home, RunRootDir, RunID, Block, File, ContentRoot string
	Key                                               []byte
	Rows                                              []ledger.Object
}

// Loaded is loadState's {status, state, seq}.
type Loaded struct {
	Status string
	State  ledger.Object
	Seq    float64
}

// Object is the result as Node returns it ({status, state, seq}).
func (l Loaded) Object() ledger.Object {
	var state any
	if l.State != nil {
		state = l.State
	}
	return ledger.Object{{Key: "status", Value: l.Status}, {Key: "state", Value: state}, {Key: "seq", Value: l.Seq}}
}

// LegacyStatePath is review-state.mjs legacyStatePath.
func LegacyStatePath(runRootDir, block, file string) string {
	return filepath.Join(runRootDir, "review-state", block, ticket.StateName(file))
}

func verified(row ledger.Object, key []byte) bool {
	res, err := sign.VerifyRow(row.Map(), key)
	return err == nil && res.OK
}

func isJSInteger(v any) bool { return isInteger(v) }

// blockAnchors is review-state.mjs blockAnchors: nil, false when any of the
// run's anchors is bad.
func blockAnchors(runID, block string, key []byte, rows []ledger.Object) ([]ledger.Object, bool) {
	out := []ledger.Object{}
	for _, r := range rows {
		if r.Get("event") != StateEvent || r.Get("run") != runID {
			continue
		}
		if _, ok := r.Get("state_sha").(string); !ok || !isJSInteger(r.Get("seq")) || !verified(r, key) {
			return nil, false
		}
		if r.Get("block") == block {
			out = append(out, r)
		}
	}
	return out, true
}

func latestAnchor(anchors []ledger.Object, file string) ledger.Object {
	var best ledger.Object
	for _, a := range anchors {
		if a.Get("file") == file && (best == nil || a.Get("seq").(float64) > best.Get("seq").(float64)) {
			best = a
		}
	}
	return best
}

func bodyMatches(body any, w StateWhere, seq float64) bool {
	b, ok := body.(ledger.Object)
	if !ok || !verified(b, w.Key) || b.Get("run") != w.RunID || b.Get("block") != w.Block || b.Get("file") != w.File || b.Get("seq") != seq {
		return false
	}
	state, ok := b.Get("state").(ledger.Object)
	return ok && state.Get("file") == w.File
}

// readBlob is review-state.mjs readBlob: the blob's text when its sha is
// wanted, else nil.
func readBlob(contentRoot string, id any, wanted string) (*string, error) {
	s, ok := id.(string)
	if !ok || !objectID.MatchString(s) {
		return nil, nil
	}
	abs, err := filepath.Abs(contentRoot)
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), blobTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", s)
	cmd.Dir = contentRoot
	cmd.Env = append(gitx.ChildEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(abs))
	var out bytes.Buffer
	cmd.Stdout = &out
	if cmd.Run() != nil || out.Len() > 64*1024*1024 {
		return nil, nil
	}
	if !utf8.Valid(out.Bytes()) {
		return nil, unsupported("a state blob that is not UTF-8")
	}
	text := out.String()
	if sha256Hex(text) != wanted {
		return nil, nil
	}
	return &text, nil
}

func restoreFromAnchor(anchor ledger.Object, w StateWhere) (Loaded, error) {
	seq := anchor.Get("seq").(float64)
	if !hasKey(anchor, "state_body") {
		return Loaded{Status: "lost", Seq: seq}, nil
	}
	body := anchor.Get("state_body")
	if !bodyMatches(body, w, seq) {
		return Loaded{Status: "tampered", Seq: seq}, nil
	}
	b := body.(ledger.Object)
	var reviewed any
	if wanted, ok := b.Get("reviewed_content_sha").(string); ok {
		text, err := readBlob(w.ContentRoot, b.Get("reviewed_content_blob"), wanted)
		if err != nil {
			return Loaded{}, err
		}
		if text == nil {
			if data, err := os.ReadFile(filepath.Join(w.ContentRoot, w.File)); err == nil {
				if !utf8.Valid(data) {
					return Loaded{}, unsupported("a reviewed file that is not UTF-8")
				}
				if sha256Hex(string(data)) == wanted {
					text = new(string)
					*text = string(data)
				}
			}
		}
		if text != nil {
			reviewed = *text
		}
	}
	state := b.Get("state").(ledger.Object).Clone()
	state.Set("reviewed_content", reviewed)
	return Loaded{Status: "restored", State: state, Seq: seq}, nil
}

// readIfThere: nil on ENOENT; any other read error is Node's throw.
func readIfThere(file string) (*string, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, syscall.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, unsupported("a review state file that is not UTF-8")
	}
	text := string(data)
	return &text, nil
}

func checkText(text string, anchor ledger.Object, w StateWhere) Loaded {
	seq := anchor.Get("seq").(float64)
	if sha256Hex(text) != anchor.Get("state_sha") {
		return Loaded{Status: "tampered", Seq: seq}
	}
	body, err := ledger.DecodeObject([]byte(text))
	if err != nil || !bodyMatches(body, w, seq) {
		return Loaded{Status: "tampered", Seq: seq}
	}
	return Loaded{Status: "ok", State: body.Get("state").(ledger.Object), Seq: seq}
}

// LoadState is review-state.mjs loadState.
func LoadState(w StateWhere, migrate bool) (Loaded, error) {
	anchors, ok := blockAnchors(w.RunID, w.Block, w.Key, w.Rows)
	if !ok {
		return Loaded{Status: "tampered"}, nil
	}
	anchor := latestAnchor(anchors, w.File)
	// statePath's asserts throw inside loadState's try: tampered
	if !runIDPattern.MatchString(w.RunID) || !blockIDPattern.MatchString(w.Block) || rowPathError(w.File) {
		return Loaded{Status: "tampered"}, nil
	}
	target := StatePath(w.Home, w.RunID, w.Block, w.File)
	legacy := ""
	text, err := readIfThere(target)
	if err == nil && text == nil && w.RunRootDir != "" {
		legacy = LegacyStatePath(w.RunRootDir, w.Block, w.File)
		text, err = readIfThere(legacy)
		if err == nil && text == nil {
			legacy = ""
			// another ticket may have moved the old file between the two reads
			text, err = readIfThere(target)
		}
	}
	if IsUnsupported(err) {
		return Loaded{}, err
	}
	if err != nil {
		return Loaded{Status: "tampered"}, nil
	}
	if anchor == nil {
		if text == nil {
			return Loaded{Status: "none"}, nil
		}
		return Loaded{Status: "tampered"}, nil
	}
	if text == nil {
		return restoreFromAnchor(anchor, w)
	}
	checked := checkText(*text, anchor, w)
	if checked.Status != "ok" || legacy == "" || !migrate {
		return checked, nil
	}
	switch moveIn(target, *text) {
	case "exists":
		again := w
		again.RunRootDir = ""
		now, err := LoadState(again, migrate)
		if err != nil || now.Status != "ok" {
			return now, err
		}
		_ = os.Remove(legacy)
		return now, nil
	case "moved":
		_ = os.Remove(legacy)
	}
	return checked, nil
}

// moveIn is review-state.mjs moveIn: link a 0600 copy to target, never over
// a file already there.
func moveIn(target, text string) string {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "failed"
	}
	tmp := tmpName(target)
	defer func() { _ = os.Remove(tmp) }()
	if err := writeNew(tmp, []byte(text), 0o600); err != nil {
		return "failed"
	}
	err := os.Link(tmp, target)
	if err == nil {
		return "moved"
	}
	if errors.Is(err, fs.ErrExist) {
		return "exists"
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "exists"
	}
	if err != nil {
		return "failed"
	}
	_, werr := f.Write([]byte(text))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "failed"
	}
	return "moved"
}
