// Package docsync keeps the newest published llms-full.txt for the installed
// package version, falling back to the copy packed with the package.
package docsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/pkgroot"
)

// DefaultURL is the published docs file; CODE_FORGE_DOCS_URL overrides it.
const DefaultURL = "https://code-forge-ai.pages.dev/docs/llms-full.txt"

// Timeout bounds the whole fetch so an upgrade never waits long offline.
const Timeout = 5 * time.Second

// maxBody caps the download; the real file is far smaller.
const maxBody = 32 << 20

// Result is what one sync did. Status is synced, kept-packed or none.
type Result struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Line is the one-line human summary of a sync.
func (r Result) Line() string {
	switch r.Status {
	case "synced":
		return "docs: synced " + r.Version
	case "kept-packed":
		if r.Version == "" {
			return "docs: kept packed unknown"
		}
		return "docs: kept packed " + r.Version
	}
	return "docs: none (" + r.Reason + ")"
}

// URL returns the docs URL, honouring CODE_FORGE_DOCS_URL.
func URL() string {
	if u := os.Getenv("CODE_FORGE_DOCS_URL"); u != "" {
		return u
	}
	return DefaultURL
}

// PackageRoot is the installed package root: the main package of the
// platform package that holds this binary (B606), or the root above
// <root>/native/bin/<platform>-<arch>/code-forge-native.
func PackageRoot() (string, error) { return pkgroot.Root() }

// InstalledVersion reads the version field of <root>/package.json.
func InstalledVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", err
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", err
	}
	if info.Version == "" {
		return "", errors.New("package.json has no version")
	}
	return info.Version, nil
}

// SyncedPath is where a fetched copy is kept.
func SyncedPath(home string) string {
	return filepath.Join(home, ".code-forge", "docs", "llms-full.txt")
}

// PackedPath is the copy shipped inside the package.
func PackedPath(root string) string {
	return filepath.Join(root, "docs", "reference", "llms-full.txt")
}

// ParseVersion returns the value of the first `version: <x>` line within the
// first 20 lines, or "" when there is none.
func ParseVersion(body []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for i := 0; i < 20 && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "version:"); ok {
			if fields := strings.Fields(rest); len(fields) == 1 {
				return fields[0]
			}
		}
	}
	return ""
}

func fileVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, head)
	return ParseVersion(head[:n])
}

// Best returns the synced copy when it matches want, else the packed copy when
// it exists. ok is false when neither is usable.
func Best(home, root, want string) (path, source string, ok bool) {
	if synced := SyncedPath(home); want != "" && fileVersion(synced) == want {
		return synced, "synced", true
	}
	if root == "" {
		return "", "", false
	}
	if info, err := os.Stat(PackedPath(root)); err == nil && info.Mode().IsRegular() {
		return PackedPath(root), "packed", true
	}
	return "", "", false
}

// Sync fetches url and keeps the body only when its version equals want.
// It never fails: every problem becomes a kept-packed or none result.
func Sync(ctx context.Context, url, home, root, want string) Result {
	reason := ""
	if want == "" {
		reason = "no installed version"
	} else if body, err := fetch(ctx, url); err != nil {
		reason = err.Error()
	} else if got := ParseVersion(body); got != want {
		if got == "" {
			got = "missing"
		}
		reason = fmt.Sprintf("remote version %s, installed %s", got, want)
	} else if err := writeAtomic(SyncedPath(home), body); err != nil {
		reason = "write: " + err.Error()
	} else {
		return Result{Status: "synced", Version: want, Path: SyncedPath(home)}
	}
	packed := PackedPath(root)
	if info, err := os.Stat(packed); root != "" && err == nil && info.Mode().IsRegular() {
		ver := fileVersion(packed)
		if ver == "" {
			ver = want
		}
		return Result{Status: "kept-packed", Version: ver, Path: packed, Reason: reason}
	}
	return Result{Status: "none", Reason: reason + "; no packed copy"}
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("offline or unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, errors.New("read failed")
	}
	if len(body) > maxBody {
		return nil, errors.New("body too large")
	}
	return body, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".llms-full-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
