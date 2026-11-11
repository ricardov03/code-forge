//go:build unix

package daemon

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// JoinOptions describe one block join without a daemon socket, as Node's
// waitBlock runs it when the daemon is disabled, unreachable or answers with
// an invalid envelope. Repo is the main working tree (it holds the queue),
// Scope the workspace mirror of the run record the caller already validated.
type JoinOptions struct {
	Home, Repo, Scope, Run, Block string
	Deadline                      time.Time
	// Live is Node's liveWorker(repo)?.run === run. supported=false means the
	// worker files have a shape only Node reads; the join then stops.
	Live func() (live, supported bool)
	// Sleep pauses between polls; false stops the join (an interrupt).
	Sleep func(time.Duration) bool
}

// Join polls the block like the daemon's wait-block method and returns its
// answer decoded as a client would read it from the socket. On failure the
// answer is nil and code names it: block-scope-unreadable, stopped (a live
// worker and a stopped review whose restart needs a signed row),
// worker-unsupported, interrupted, or a daemon failure code.
func Join(o JoinOptions) (answer map[string]any, code string) {
	s := &server{home: o.Home, run: o.Run, repo: o.Repo, scope: o.Scope, local: true, live: o.Live, trees: make(map[string]*treeState)}
	s.queue.Dir = filepath.Join(o.Repo, ".code-forge", "queue")
	down := 0
	delivered := make(map[string]bool)
	for {
		result, finished := s.step(request{Method: "wait-block", Block: o.Block}, o.Deadline, &down, delivered)
		if finished {
			if result["ok"] == false {
				return nil, text(result, "error")
			}
			data, err := json.Marshal(result)
			if err != nil {
				return nil, "bad-answer"
			}
			if err := json.Unmarshal(data, &answer); err != nil {
				return nil, "bad-answer"
			}
			return answer, ""
		}
		delay := 200 * time.Millisecond
		if down == 0 && time.Until(o.Deadline) < delay {
			delay = max(0, time.Until(o.Deadline))
		}
		if !o.Sleep(delay) {
			return nil, "interrupted"
		}
	}
}

// stopped reports Node's requeue candidates: an owned changed file whose
// newest block ticket is marked stopped with findings in its result.
func (s *server) stopped(files []string, owned []any, kinds object, byFile map[string][]object) (bool, error) {
	for _, file := range files {
		tickets := byFile[file]
		if !owns(owned, kinds, file) || len(tickets) == 0 {
			continue
		}
		marker, err := s.queue.Marker(text(tickets[len(tickets)-1], "ticket"))
		if err != nil {
			return false, err
		}
		if marker == nil || marker["status"] != "stopped" {
			continue
		}
		// resultOfMarker: only a path under .code-forge/reviews, never "..".
		rel := text(marker, "result")
		prefix := ".code-forge" + string(filepath.Separator) + "reviews" + string(filepath.Separator)
		if !strings.HasPrefix(rel, prefix) || slices.Contains(strings.Split(rel, string(filepath.Separator)), "..") {
			continue
		}
		result, err := readObject(filepath.Join(s.repo, rel))
		if findings, ok := result["findings"].([]any); err == nil && ok && len(findings) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// automaticReopen is isAutomaticReopen in src/review/reopen.mjs.
func automaticReopen(row object) bool {
	from, a := row["from_hash"].(string)
	to, b := row["to_hash"].(string)
	return row["by"] == "auto" && row["reason"] == "auto-requeue: content changed after stopped review" && row["from"] == "stopped" && a && b && from != to
}
