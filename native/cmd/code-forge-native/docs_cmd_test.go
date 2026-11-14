package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDocsProcess runs the public router in a child process whose executable
// sits inside a fake package, so the command finds that package's version.
func TestDocsProcess(t *testing.T) {
	if os.Getenv("CF_DOCS_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_DOCS_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	code, _ := run(args)
	os.Exit(code)
}

type docsFixture struct {
	t              *testing.T
	exe, pkg, home string
	synced, packed string
}

func newDocsFixture(t *testing.T, packed string) docsFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg, home := filepath.Join(root, "pkg"), filepath.Join(root, "home")
	binDir := filepath.Join(pkg, "native", "bin", "host")
	for _, dir := range []string{binDir, home, filepath.Join(pkg, "docs", "reference")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"x","version":"9.8.7"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "code-forge-native")
	if os.Link(self, exe) != nil {
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := docsFixture{t: t, exe: exe, pkg: pkg, home: home,
		synced: filepath.Join(home, ".code-forge", "docs", "llms-full.txt"),
		packed: filepath.Join(pkg, "docs", "reference", "llms-full.txt")}
	if packed != "" {
		if err := os.WriteFile(f.packed, []byte(packed), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f docsFixture) call(url string, args ...string) (int, string, string) {
	f.t.Helper()
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(f.exe, "-test.run=^TestDocsProcess$")
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_DOCS_URL="+url, "CODE_FORGE_HANDOVER_LOG=0",
		"CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_DOCS_PROCESS=1", "CF_DOCS_ARGS="+string(encoded))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func docsServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/docs/llms-full.txt"
}

func TestDocsSyncKeepsMatchingVersion(t *testing.T) {
	f := newDocsFixture(t, "version: 9.8.7\npacked\n")
	body := "# code-forge docs\n\n> generated\nversion: 9.8.7\n\nfresh body\n"
	url := docsServer(t, http.StatusOK, body)
	start := time.Now()
	code, out, errOut := f.call(url, "docs", "sync")
	elapsed := time.Since(start)
	if code != 0 || out != "docs: synced 9.8.7\n" {
		t.Fatalf("sync = %d %q %q; want 0 and one synced line", code, out, errOut)
	}
	// B654: no wall-clock bound; a fetch that waited out docsync.Timeout
	// reports a failure, not "synced", so the status line above proves it.
	t.Logf("perf: docs sync against local server %v", elapsed)
	data, err := os.ReadFile(f.synced)
	if err != nil || string(data) != body {
		t.Fatalf("synced file = %q, %v; want the fetched body", data, err)
	}
	if info, _ := os.Stat(f.synced); info.Mode().Perm() != 0o600 {
		t.Fatalf("synced mode = %v; want 0600", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(f.synced)); len(entries) != 1 {
		t.Fatalf("docs dir holds %d entries; want only llms-full.txt", len(entries))
	}
	code, out, errOut = f.call(url, "docs", "path")
	if code != 0 || out != f.synced+"\n" {
		t.Fatalf("path = %d %q %q; want the synced path %s", code, out, errOut, f.synced)
	}
	code, out, _ = f.call(url, "docs", "path", "--json")
	var got map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || len(got) != 2 || got["path"] != f.synced || got["source"] != "synced" {
		t.Fatalf("path --json = %d %q; want {path: synced, source: synced}", code, out)
	}
}

func TestDocsSyncKeepsPackedOrNone(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL + "/docs/llms-full.txt"
	down.Close()
	for _, tc := range []struct {
		name, url string
	}{
		{"older version", docsServer(t, http.StatusOK, "version: 9.8.6\nold body\n")},
		{"http error", docsServer(t, http.StatusInternalServerError, "version: 9.8.7\n")},
		{"no server", downURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDocsFixture(t, "intro\nversion: 9.8.7\npacked\n")
			code, out, errOut := f.call(tc.url, "docs", "sync")
			if code != 0 || out != "docs: kept packed 9.8.7\n" {
				t.Fatalf("sync = %d %q %q; want 0 and kept packed", code, out, errOut)
			}
			if _, err := os.Stat(f.synced); !os.IsNotExist(err) {
				t.Fatalf("synced file must not be written: %v", err)
			}
			code, out, _ = f.call(tc.url, "docs", "path")
			if code != 0 || out != f.packed+"\n" {
				t.Fatalf("path = %d %q; want packed path %s", code, out, f.packed)
			}

			bare := newDocsFixture(t, "")
			code, out, errOut = bare.call(tc.url, "docs", "sync")
			if code != 0 || !strings.HasPrefix(out, "docs: none (") || strings.Count(out, "\n") != 1 {
				t.Fatalf("sync without packed copy = %d %q %q; want 0 and one none line", code, out, errOut)
			}
			code, out, errOut = bare.call(tc.url, "docs", "path")
			if code != 1 || out != "" || errOut != "docs: none; run code-forge docs sync\n" {
				t.Fatalf("path with no copy = %d %q %q; want exit 1 and the none line", code, out, errOut)
			}
		})
	}
}

func TestDocsBadFlags(t *testing.T) {
	f := newDocsFixture(t, "")
	for _, args := range [][]string{{"docs"}, {"docs", "pull"}, {"docs", "sync", "--force"}, {"docs", "path", "--json", "--json"}} {
		code, out, errOut := f.call("http://127.0.0.1:1/", args...)
		if code != 2 || out != "" || !strings.HasPrefix(errOut, "usage: code-forge docs") {
			t.Fatalf("%q = %d %q %q; want exit 2 with usage", args, code, out, errOut)
		}
	}
}
