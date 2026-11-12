package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// B568: --sources outside the project (src/session/facts.mjs sourceRoots,
// mapClaims and the outside half of buildSnapshot): each outside directory or
// file is a root mounted at _sources/<label> in the snapshot.

// fcSourceRoots is sourceRoots(project, sources); project is the realpath.
func fcSourceRoots(project string, sources []string) ([]*fcRoot, error) {
	roots := []*fcRoot{}
	add := func(dir, file string) {
		for _, r := range roots {
			if r.dir == dir {
				if r.file == "" || r.file == file {
					return
				}
				break
			}
		}
		base := filepath.Base(dir)
		label := base
		for n := 2; fcLabelTaken(roots, label); n++ {
			label = base + "-" + strconv.Itoa(n)
		}
		prefix := ""
		if dir != project {
			prefix = "_sources/" + label
		}
		roots = append(roots, &fcRoot{dir: dir, label: label, prefix: prefix, file: file})
	}
	add(project, "")
	for _, source := range sources {
		real, err := filepath.EvalSymlinks(source)
		if err != nil {
			return nil, err
		}
		if fcBelowRoot(project, real) {
			continue
		}
		st, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			add(real, "")
		} else {
			add(filepath.Dir(real), real)
		}
	}
	return roots, nil
}

func fcLabelTaken(roots []*fcRoot, label string) bool {
	for _, r := range roots {
		if r.label == label {
			return true
		}
	}
	return false
}

// fcPosixJoin is path.posix.join.
func fcPosixJoin(parts ...string) string {
	kept := []string{}
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return "."
	}
	return fcPosixNormalize(strings.Join(kept, "/"))
}

// fcMapClaims is mapClaims(claims, roots).
func fcMapClaims(claims []fcClaim, roots []*fcRoot) []fcClaim {
	directories := []*fcRoot{}
	for _, r := range roots {
		if r.file == "" {
			directories = append(directories, r)
		}
	}
	byLength := append([]*fcRoot{}, roots...)
	sort.SliceStable(byLength, func(i, j int) bool { return len(byLength[i].dir) > len(byLength[j].dir) })
	out := []fcClaim{}
	for _, claim := range claims {
		if claim.kind == "path" && filepath.IsAbs(claim.token) {
			if claim.token == "/dev" || strings.HasPrefix(claim.token, "/dev/") {
				continue
			}
			var root *fcRoot
			for _, r := range byLength {
				if fcBelowRoot(r.dir, claim.token) && (r.file == "" || r.file == claim.token) {
					root = r
					break
				}
			}
			if root != nil {
				claim.root = root
				if len(roots) > 1 || root.prefix != "" {
					claim.source, claim.hasSource = root.label, true
				}
				claim.checkPath, claim.hasCheckPath = fcPosixJoin(root.prefix, filepath.ToSlash(fcRelative(root.dir, claim.token))), true
				out = append(out, claim)
				continue
			}
			if fcBelowAny(fcToolRoots, claim.token) {
				out = append(out, claim)
				continue
			}
			if !fcIsDir("/" + strings.Split(claim.token, "/")[1]) {
				continue
			}
			claim.unavailable = "outside every --sources root and the snapshot"
			out = append(out, claim)
			continue
		}
		if len(directories) < 2 || (claim.kind != "path" && claim.kind != "symbol" && claim.kind != "flag") {
			out = append(out, claim)
			continue
		}
		for _, root := range directories {
			c := claim
			c.source, c.hasSource, c.root, c.hasCheckPath = root.label, true, root, true
			switch {
			case claim.kind == "path" && root.prefix != "":
				c.checkPath = root.prefix + "/" + claim.token
			case claim.kind == "path":
				c.checkPath = claim.token
			case root.prefix != "":
				c.checkPath = root.prefix
			default:
				c.checkPath = "."
			}
			out = append(out, c)
		}
	}
	return out
}

// fcSnapshotSources is the outside-source loop of buildSnapshot.
func fcSnapshotSources(roots []*fcRoot, sessionCwd, snapDir, workDir string) error {
	env := fcGitEnv()
	for _, source := range roots {
		if source.prefix == "" {
			continue
		}
		dest := filepath.Join(sessionCwd, source.prefix)
		if err := fcAssertNoLinkSegments(snapDir, dest); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return err
		}
		if source.file != "" {
			if err := fcCopyNoFollow(source.file, filepath.Join(dest, filepath.Base(source.file)), snapDir); err != nil {
				return err
			}
			continue
		}
		top := fcExec([]string{"git", "rev-parse", "--show-toplevel"}, source.dir, env, "")
		if !top.ok {
			if err := fcCopyNoFollow(source.dir, dest, snapDir); err != nil {
				return err
			}
			continue
		}
		repo, err := filepath.EvalSymlinks(jsTrim(top.stdout))
		if err != nil {
			return err
		}
		if head := fcExec([]string{"git", "rev-parse", "--verify", "-q", "HEAD"}, repo, env, ""); !head.ok {
			continue
		}
		tree := "HEAD"
		if sub := filepath.ToSlash(fcRelative(repo, source.dir)); sub != "" {
			tree = "HEAD:" + sub
		}
		tarPath := filepath.Join(workDir, "source.tar")
		err = func() error {
			defer os.Remove(tarPath)
			if archived := fcExec([]string{"git", "archive", "--format=tar", "-o", tarPath, tree}, repo, env, ""); !archived.ok {
				return &fcRefused{"source " + source.label + " could not be archived"}
			}
			if untar := fcExec([]string{"tar", "-xf", tarPath, "-C", dest}, "", os.Environ(), ""); !untar.ok {
				return &fcRefused{"source " + source.label + " could not be extracted"}
			}
			return fcDropLinks(dest)
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
