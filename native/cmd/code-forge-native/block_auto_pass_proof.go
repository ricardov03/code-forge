package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

type autoPassHeader struct {
	Block     string `json:"block"`
	Run       string `json:"run"`
	Prover    string `json:"prover"`
	CreatedAt string `json:"created_at"`
	AllGreen  bool   `json:"all_green"`
	Checks    []struct {
		Clause   string   `json:"clause"`
		Result   string   `json:"result"`
		Evidence []string `json:"evidence"`
	} `json:"checks"`
}
type autoPassProof struct {
	Path, Hash, Prover string
	Evidence           []any
}

func autoPassHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Physical ancestry also handles case aliases on case-insensitive filesystems.
func autoPassWithin(root, path string) bool {
	info, e := os.Stat(root)
	if e != nil || !info.IsDir() {
		return false
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		st, e := os.Stat(current)
		if e == nil && os.SameFile(info, st) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}
func autoPassResolve(path, workspace string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return filepath.EvalSymlinks(path)
}
func autoPassExport(path string) bool {
	s := "/" + filepath.ToSlash(filepath.Clean(path)) + "/"
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		s = strings.ToLower(s)
	}
	return strings.Contains(s, "/.code-forge/export/")
}

// Open nonblocking and do not follow a replaced final symlink. Check the
// descriptor before reading, so a FIFO is never read as evidence.
func autoPassReadRegular(path string) ([]byte, error) {
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, fmt.Errorf("must be a non-empty regular file")
	}
	b, e := io.ReadAll(f)
	if e != nil {
		return nil, e
	}
	real, resolveErr := filepath.EvalSymlinks(path)
	now, e := os.Stat(path)
	if e != nil || resolveErr != nil || real != path || !os.SameFile(st, now) || len(b) == 0 {
		return nil, fmt.Errorf("file changed while reading")
	}
	return b, nil
}

var autoPassScripts = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
var autoPassAttributes = regexp.MustCompile(`(?i)(?:^|\s)(id|type)\s*=\s*["']([^"']*)["']`)

func autoPassReadProof(file, workspace, tree, run, id string, dirs []any, targets []humancheck.Entry) (autoPassProof, error) {
	out := autoPassProof{Evidence: []any{}}
	path, e := autoPassResolve(file, workspace)
	if e != nil {
		return out, fmt.Errorf("cannot resolve proof file")
	}
	allowed := false
	for _, v := range dirs {
		name, valid := v.(string)
		if !valid || strings.TrimSpace(name) == "" {
			return out, fmt.Errorf("proof_dirs must contain non-blank directory paths")
		}
		if root, e := autoPassResolve(name, workspace); e == nil && autoPassWithin(root, path) {
			allowed = true
		}
	}
	if !allowed {
		return out, fmt.Errorf("proof is outside proof_dirs")
	}
	for _, root := range []string{workspace, tree} {
		real, e := gitx.RepoTop(root)
		if e != nil {
			return out, fmt.Errorf("cannot resolve project worktree")
		}
		if autoPassWithin(proofRealAutoPass(real), path) {
			return out, fmt.Errorf("proof is inside the project worktree")
		}
	}
	if autoPassExport(path) {
		return out, fmt.Errorf("proof is inside .code-forge/export")
	}
	b, e := autoPassReadRegular(path)
	if e != nil {
		return out, fmt.Errorf("proof must be a non-empty regular file")
	}
	data := b
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
	case ".html":
		found := false
		for _, m := range autoPassScripts.FindAllSubmatch(b, -1) {
			attrs := map[string]string{}
			for _, a := range autoPassAttributes.FindAllSubmatch(m[1], -1) {
				attrs[strings.ToLower(string(a[1]))] = string(a[2])
			}
			if attrs["id"] == "code-forge-proof" && attrs["type"] == "application/json" {
				if found {
					return out, fmt.Errorf("duplicate code-forge-proof header")
				}
				data = m[2]
				found = true
			}
		}
		if !found {
			return out, fmt.Errorf("missing code-forge-proof JSON script")
		}
	default:
		return out, fmt.Errorf("proof must be .html or .json")
	}
	var header autoPassHeader
	if e := json.Unmarshal(data, &header); e != nil {
		return out, fmt.Errorf("invalid proof JSON header")
	}
	if header.Block != id || header.Run != run || !header.AllGreen {
		return out, fmt.Errorf("header block/run must match and all_green must be true")
	}
	if strings.TrimSpace(header.Prover) == "" || !closeTextSafe(header.Prover) || autoPassControl.MatchString(header.Prover) {
		return out, fmt.Errorf("header prover must name a session")
	}
	if _, e := time.Parse(time.RFC3339Nano, header.CreatedAt); e != nil {
		return out, fmt.Errorf("header created_at must be ISO-8601")
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	for _, c := range header.Checks {
		if !autoPassClauseID.MatchString(c.Clause) {
			return out, fmt.Errorf("invalid header clause id")
		}
		if seen[c.Clause] {
			return out, fmt.Errorf("duplicate header clause %s", c.Clause)
		}
		seen[c.Clause] = true
		target := false
		for _, t := range targets {
			target = target || t.Clause == c.Clause
		}
		if !target {
			continue
		}
		if c.Result != "pass" || len(c.Evidence) == 0 {
			return out, fmt.Errorf("check %s must pass and name evidence", c.Clause)
		}
		for _, name := range c.Evidence {
			if name == "" || filepath.IsAbs(name) {
				return out, fmt.Errorf("check %s evidence must be relative to proof", c.Clause)
			}
			full, e := filepath.EvalSymlinks(filepath.Join(filepath.Dir(path), name))
			if e != nil || !autoPassWithin(filepath.Dir(path), full) || autoPassExport(full) {
				return out, fmt.Errorf("check %s evidence is missing or escapes the proof directory", c.Clause)
			}
			for _, root := range []string{workspace, tree} {
				if real, e := gitx.RepoTop(root); e == nil && autoPassWithin(proofRealAutoPass(real), full) {
					return out, fmt.Errorf("check %s evidence is inside the worktree", c.Clause)
				}
			}
			bytes, e := autoPassReadRegular(full)
			if e != nil {
				return out, fmt.Errorf("check %s evidence must be a non-empty regular file", c.Clause)
			}
			if !files[full] {
				out.Evidence = append(out.Evidence, ledger.Object{{Key: "path", Value: full}, {Key: "sha256", Value: autoPassHash(bytes)}})
				files[full] = true
			}
		}
	}
	for _, t := range targets {
		if !seen[t.Clause] {
			return out, fmt.Errorf("missing passing check %s", t.Clause)
		}
	}
	out.Path, out.Hash, out.Prover = path, autoPassHash(b), header.Prover
	return out, nil
}

func proofRealAutoPass(path string) string {
	real, e := filepath.EvalSymlinks(path)
	if e == nil {
		return real
	}
	return path
}
