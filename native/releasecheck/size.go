package releasecheck

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
)

// B951: smaller releases. Owner rule 2026-11-19: release-ready every 5–8
// blocks. The size is the number of distinct blocks (B<id> commit subjects)
// since the last v* tag; release.max_blocks (default 8) is the limit.

// DefaultMaxBlocks is release.max_blocks when the config does not set it.
const DefaultMaxBlocks = 8

// Size levels.
const (
	SizeNone = "none" // fewer than max-3 blocks
	SizeInfo = "info" // max-3 to max-1 blocks: a release is near
	SizeNow  = "now"  // max or more blocks: release now
)

// Size is the release size: the blocks since the last v* tag.
type Size struct {
	LastTag string   `json:"last_tag"`
	Blocks  int      `json:"blocks"`
	IDs     []string `json:"ids"`
	Max     int      `json:"max_blocks"`
	Level   string   `json:"level"`
}

// sizeSubject is the block-id head of a commit subject: "B950: …",
// "B877+B878: …", "B828fix: …".
var (
	sizeSubject = regexp.MustCompile(`^(B[0-9]+[A-Za-z0-9]*(?:\s*[+,/&]\s*B[0-9]+[A-Za-z0-9]*)*)\s*[:(\s]`)
	sizeID      = regexp.MustCompile(`B[0-9]+`)
)

// BlockIDs are the distinct block ids the commit subjects name, in first-seen order.
func BlockIDs(subjects []string) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, s := range subjects {
		m := sizeSubject.FindStringSubmatch(strings.TrimSpace(s) + " ")
		if m == nil {
			continue
		}
		for _, id := range sizeID.FindAllString(m[1], -1) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// SizeLevel is the level of n blocks against max.
func SizeLevel(n, max int) string {
	switch {
	case max < 1:
		return SizeNone
	case n >= max:
		return SizeNow
	case n > 0 && n >= max-3:
		return SizeInfo
	}
	return SizeNone
}

// MaxBlocks reads release.max_blocks from root's .code-forge.yml: a whole
// number from 1 to 1000, else DefaultMaxBlocks.
func MaxBlocks(root string) int {
	cfg, _, err := config.LoadFileOrdered(root + "/" + config.Filename)
	if err != nil {
		return DefaultMaxBlocks
	}
	section, _ := cfg["release"].(map[string]any)
	switch v := section["max_blocks"].(type) {
	case float64:
		if v >= 1 && v <= 1000 && v == math.Trunc(v) {
			return int(v)
		}
	case int:
		if v >= 1 && v <= 1000 {
			return v
		}
	}
	return DefaultMaxBlocks
}

// SizeOf counts the blocks since the last v* tag reachable from HEAD (every
// commit when there is none). A root that is not a git work tree has size 0.
func SizeOf(root string, run Runner) Size {
	if run == nil {
		run = DefaultRunner
	}
	git := func(args ...string) Exec { return run(root, 60*time.Second, append([]string{"git"}, args...)...) }
	s := Size{IDs: []string{}, Max: MaxBlocks(root), Level: SizeNone}
	tag := git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD")
	rangeArg := "HEAD"
	if t := strings.TrimSpace(tag.Stdout); tag.Code == 0 && t != "" {
		s.LastTag, rangeArg = t, t+"..HEAD"
	}
	log := git("log", "--no-merges", "--format=%s", rangeArg)
	if log.Code != 0 {
		return s
	}
	s.IDs = BlockIDs(nonEmpty(log.Stdout))
	s.Blocks = len(s.IDs)
	s.Level = SizeLevel(s.Blocks, s.Max)
	return s
}

// Line is the release size for a person: "" for SizeNone.
func (s Size) Line() string {
	since := "since " + s.LastTag
	if s.LastTag == "" {
		since = "and no v* tag yet"
	}
	switch s.Level {
	case SizeNow:
		return fmt.Sprintf("RELEASE NOW: %d blocks %s (release.max_blocks %d): make a release before more blocks land", s.Blocks, since, s.Max)
	case SizeInfo:
		return fmt.Sprintf("release size: %d blocks %s (release.max_blocks %d): a release is near", s.Blocks, since, s.Max)
	}
	return ""
}
