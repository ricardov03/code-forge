package gatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

func Object(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func Owns(prefix string, block ledger.Object, file string) bool {
	files, _ := block.Get("owned_files").([]any)
	kinds := Object(block.Get("owned_kinds"))
	return owned.OwnsFile(files, kinds, file) || prefix != "" && prefix != "." && strings.HasPrefix(file, prefix+"/") && owned.OwnsFile(files, kinds, strings.TrimPrefix(file, prefix+"/"))
}
func reviewEvent(v any) bool {
	return slices.Contains([]string{"review.result", "review.round", "review.approved", "review.triage", "review.cap", "review.late_finding"}, Text(v))
}
func ReviewPath(file string) string {
	if file == "" || strings.HasPrefix(file, "/") || strings.ContainsAny(file, "\\\x00") {
		return ""
	}
	f := path.Clean(file)
	if f == "." || f == ".." || strings.HasPrefix(f, "../") {
		return ""
	}
	return f
}
func ReviewedFiles(rows []Row, id string) []string {
	files := []string{}
	for _, r := range rows {
		if r["block"] == id && reviewEvent(r["event"]) {
			f := ReviewPath(Text(r["file"]))
			if f != "" && !slices.Contains(files, f) {
				files = append(files, f)
			}
		}
	}
	gitx.SortStrings(files)
	return files
}

// noChangeOnly (B365, twin of Node's noChangeOnly) is a review.result that only
// says the reviewer found these bytes equal to the base: status no_change, the
// exact hash, and no open finding. It neither approves nor refuses the content.
func noChangeOnly(r Row, hash string) bool {
	if r["event"] != "review.result" || r["status"] != "no_change" || r["content_hash"] != hash {
		return false
	}
	open, has := r["open"]
	if !has {
		return true
	}
	list, ok := open.([]any)
	return ok && len(list) == 0
}

// ReviewedWork (B365, twin of Node's reviewedWorkFiles) is ReviewedFiles less
// each file whose review rows of its current bytes (that hash, or none) exist
// and are all no_change results: main changed it, not the block. It is the
// evidence the close's no_changes check counts. A path that cannot be hashed stays.
func ReviewedWork(top string, rows []Row, id string) []string {
	work := []string{}
	for _, f := range ReviewedFiles(rows, id) {
		current, err := gitx.ContentHash(top, f)
		if err != nil {
			work = append(work, f)
			continue
		}
		matching, onlyNoChange := 0, true
		for _, r := range rows {
			if r["block"] == id && reviewEvent(r["event"]) && ReviewPath(Text(r["file"])) == f && (r["content_hash"] == nil || r["content_hash"] == current) {
				matching++
				onlyNoChange = onlyNoChange && noChangeOnly(r, current)
			}
		}
		if matching == 0 || !onlyNoChange {
			work = append(work, f)
		}
	}
	return work
}

// FileSet reuses one diff and retains reviewed paths that are absent from it,
// except paths back at their base bytes with no review of those current bytes
// (B365: or whose every review of those bytes is a no_change result).
func FileSet(top, base, prefix, id string, block ledger.Object, rows []Row, changed []string) ([]File, []string, error) {
	reviewed := []string{}
	for _, f := range ReviewedFiles(rows, id) {
		if !Owns(prefix, block, f) {
			continue
		}
		keep := true
		if !slices.Contains(changed, f) {
			data, err := gitx.ReadRegularFileNoFollow(top, f)
			if err == nil && data != nil {
				current, _ := gitx.ContentHash(top, f)
				matching := false
				for _, r := range rows {
					if r["block"] == id && reviewEvent(r["event"]) && ReviewPath(Text(r["file"])) == f && (r["content_hash"] == nil || r["content_hash"] == current) && !noChangeOnly(r, current) {
						matching = true
					}
				}
				if !matching {
					if shown, err := gitx.Run(top, "show", base+":"+f); err == nil {
						sum := sha256.Sum256([]byte(shown))
						if hex.EncodeToString(sum[:]) == current {
							keep = false
						}
					}
				}
			}
		}
		if keep {
			reviewed = append(reviewed, f)
		}
	}
	all := slices.Clone(changed)
	for _, f := range reviewed {
		if !slices.Contains(all, f) {
			all = append(all, f)
		}
	}
	gitx.SortStrings(all)
	files := []File{}
	for _, f := range all {
		if Owns(prefix, block, f) {
			hash, err := gitx.ContentHash(top, f)
			if err != nil {
				return nil, nil, err
			}
			if hash == gitx.DeletedHash {
				if listed, err := gitx.Run(top, "ls-tree", "--name-only", "-z", base, "--", f); err == nil && listed == "" {
					if history, err := gitx.Run(top, "log", "--format=%H", base+"..HEAD", "--", f); err == nil && strings.TrimSpace(history) != "" {
						continue
					}
				}
			}
			files = append(files, File{f, hash})
		}
	}
	return files, reviewed, nil
}

func Scope(top, prefix, id, run string, blocks ledger.Object, changed []string, rows []Row, key []byte, ownTree bool) (unowned, warnings []string, checked bool) {
	block := Object(blocks.Get(id))
	atOpen := Object(block.Get("tree_at_open"))
	if !ownTree && atOpen == nil {
		return []string{}, []string{}, false
	}
	unowned = []string{}
	warnings = []string{}
	for _, f := range changed {
		if f == ".code-forge" || strings.HasPrefix(f, ".code-forge/") || Owns(prefix, block, f) {
			continue
		}
		if f == path.Join(prefix, ".code-forge/runs", run+".json") || f == path.Join(prefix, ".code-forge/runs", run, id+".log") {
			continue
		}
		if !ownTree && gitx.SameStamp(Text(atOpen.Get(f)), gitx.TreeStamp(top, f)) {
			continue
		}
		accounted := false
		if !ownTree {
			for _, other := range blocks {
				b := Object(other.Value)
				if other.Key == id || b.Get("status") != "open" || !Owns(prefix, b, f) {
					continue
				}
				warnings = append(warnings, "WARN block "+id+": "+f+" changed and is owned by open block "+other.Key+"; not checked here (its own gate reviews it)\n")
				accounted = true
				break
			}
			if !accounted {
				hash, err := gitx.ContentHash(top, f)
				if err == nil {
					for _, other := range blocks {
						if other.Key == id {
							continue
						}
						b := Object(other.Value)
						if b.Get("status") == "closed" && Owns(prefix, b, f) {
							for _, r := range rows {
								if r["event"] == "review.approved" && r["block"] == other.Key && r["file"] == f && r["content_hash"] == hash && Verified(r, key) {
									accounted = true
								}
							}
						}
					}
				}
			}
		}
		if !accounted {
			unowned = append(unowned, f)
		}
	}
	return unowned, warnings, true
}

func Prefix(top, workspace string) string {
	if real, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = real
	}
	rel, _ := filepath.Rel(top, workspace)
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}
