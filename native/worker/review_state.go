package worker

// B495: the fix-loop state save (src/worker/review-state.mjs saveState):
// the signed state file under <home>/.code-forge/runs/<run>/review-state/
// <block>/<StateName(file)>, its reviewed text kept as a git blob in the
// content root, and the review.state anchor row written first.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// StateBodyCap is review-state.mjs STATE_BODY_CAP.
const StateBodyCap = 64 * 1024

const blobTimeout = 10 * time.Second

// writeBlob is review-state.mjs writeBlob: the text's bytes as a blob in
// contentRoot's object store, or nil on any failure.
func writeBlob(contentRoot, text string) any {
	abs, err := filepath.Abs(contentRoot)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), blobTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "hash-object", "-w", "--no-filters", "--stdin")
	cmd.Dir = contentRoot
	cmd.Env = append(gitx.ChildEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(abs))
	cmd.Stdin = strings.NewReader(text)
	var out bytes.Buffer
	cmd.Stdout = &out
	if childreg.Run(cmd) != nil {
		return nil
	}
	id := strings.TrimSpace(out.String())
	if !objectID.MatchString(id) {
		return nil
	}
	return id
}

func sha256Hex(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// StatePath is review-state.mjs statePath under home.
func StatePath(home, runID, block, file string) string {
	return filepath.Join(home, ".code-forge", "runs", runID, "review-state", block, ticket.StateName(file))
}

// SaveRequest is one saveState call; Now is signRow's clock.
type SaveRequest struct {
	Home, RunID, Block, File, ContentRoot string
	Key                                   []byte
	State                                 ledger.Object
	Seq                                   int
	WriteRow                              func(ledger.Object) error
	Now                                   func() time.Time
}

func signAt(row ledger.Object, key []byte, now time.Time) (ledger.Object, error) {
	out := row.Clone()
	if out.Get("ts") == nil {
		out.Set("ts", ISO(now))
	}
	out.Delete("mac")
	signed, err := sign.SignRow(out.Map(), key)
	if err != nil {
		return nil, err
	}
	out.Set("mac", signed["mac"])
	return out, nil
}

// SaveState is review-state.mjs saveState: anchor row first, then the
// atomic 0600 write (directory 0700).
func SaveState(r SaveRequest) error {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	next := float64(r.Seq + 1)
	head := ledger.Object{{Key: "run", Value: r.RunID}, {Key: "block", Value: r.Block}, {Key: "file", Value: r.File}, {Key: "seq", Value: next}}
	signed, err := signAt(append(head.Clone(), ledger.Field{Key: "state", Value: r.State}), r.Key, now())
	if err != nil {
		return err
	}
	data, err := ledger.Stringify(signed, false)
	if err != nil {
		return err
	}
	text := string(data) + "\n"
	reviewed, isText := r.State.Get("reviewed_content").(string)
	var blob, reviewedSha any
	if isText {
		reviewedSha = sha256Hex(reviewed)
		if r.ContentRoot != "" {
			blob = writeBlob(r.ContentRoot, reviewed)
		}
	}
	slimState := r.State.Clone()
	slimState.Set("reviewed_content", nil)
	body, err := signAt(append(head.Clone(), ledger.Field{Key: "state", Value: slimState},
		ledger.Field{Key: "reviewed_content_sha", Value: reviewedSha}, ledger.Field{Key: "reviewed_content_blob", Value: blob}), r.Key, now())
	if err != nil {
		return err
	}
	bodyJSON, err := ledger.Stringify(body, false)
	if err != nil {
		return err
	}
	row := ledger.Object{{Key: "event", Value: "review.state"}, {Key: "block", Value: r.Block}, {Key: "file", Value: r.File},
		{Key: "seq", Value: next}, {Key: "state_sha", Value: sha256Hex(text)}, {Key: "l3_rung_used", Value: r.State.Get("l3_rung_used") == true}}
	if len(bodyJSON) > StateBodyCap {
		row.Set("state_omitted", "too_large")
	} else {
		row.Set("state_body", body)
	}
	if err := r.WriteRow(row); err != nil {
		return err
	}
	target := StatePath(r.Home, r.RunID, r.Block, r.File)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp := tmpName(target)
	defer func() { _ = os.Remove(tmp) }()
	if err := writeNew(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}
