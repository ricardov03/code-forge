package landmerge

import (
	"fmt"
	"github.com/ricardov03/code-forge/native/releasecheck"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B990: B813 folds repeated CHANGELOG `### Kind` headings in the merges and
// replays code-forge runs itself (land, batch rebase, block rebase). A merge
// a person or a script makes with plain git (a wave combine's
// `git merge --no-ff`) kept both headings, because the file's
// `merge=union` attribute runs git's union driver. MergeDriver is a git
// merge driver for that attribute:
//
//	git config merge.union.driver 'code-forge land merge-driver %O %A %B %P'
//
// It merges like git's union driver (`git merge-file --union`: both sides'
// lines kept) and, for a CHANGELOG.md, then folds its repeated headings
// against ours (the branch merged into). Other merge=union files get the
// plain union result.

// MergeDriver merges theirs into ours over base (three file paths, as git's
// %O %A %B give them), writes the result into ours and returns nil. name is
// the path in the repository (%P); when empty, ours' own name decides
// whether the file is a CHANGELOG.md.
func MergeDriver(base, ours, theirs, name string) error {
	before, err := os.ReadFile(ours)
	if err != nil {
		return err
	}
	info, err := os.Stat(ours)
	if err != nil {
		return err
	}
	merged, err := gitx.Run("", "merge-file", "-p", "--union", "--", ours, base, theirs)
	if err != nil {
		return fmt.Errorf("git merge-file --union: %v", err)
	}
	if name == "" {
		name = ours
	}
	if isChangelog(name) {
		baseText, readErr := os.ReadFile(base)
		if readErr != nil {
			return readErr
		}
		theirsText, readErr := os.ReadFile(theirs)
		if readErr != nil {
			return readErr
		}
		if sectioned, handled, mergeErr := mergeReleaseBoundary(string(baseText), string(before), string(theirsText)); mergeErr != nil {
			return mergeErr
		} else if handled {
			merged = sectioned
		}
		merged = FoldChangelog(merged, string(before))
		if faults := releasecheck.ChangelogBlockDuplicates(merged); len(faults) > 0 {
			return fmt.Errorf("%s", strings.Join(faults, "; "))
		}
	}
	return os.WriteFile(ours, []byte(merged), info.Mode().Perm())
}
