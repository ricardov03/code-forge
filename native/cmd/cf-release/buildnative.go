package main

// The release's native build in Go (B580): scripts/build-native.mjs without --host, run with no
// Node. Same four targets, environment, flags, temp name, mode, rename and printed lines.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// bnTargets are the script's release targets: Node's platform and arch, and Go's GOARCH.
var bnTargets = [][3]string{
	{"darwin", "arm64", "arm64"}, {"darwin", "x64", "amd64"}, {"linux", "x64", "amd64"}, {"linux", "arm64", "arm64"},
}

var bnNodeVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*$`)

// bnNodeVersion is the version the script stamped into main.reviewWaitNodeVersion (its own
// process.version), with B575's rule: the Node a hand-over uses (CODE_FORGE_NODE, else node on
// PATH), asked with --version. With no usable node it is "", and the build leaves the variable at
// its "unknown" default: the gate stamp is then not written (gates_stamp.go) and logs report hands
// over (logs_report.go), so nothing reuses a stamp made for another Node.
func bnNodeVersion() string {
	node := ""
	if override := os.Getenv("CODE_FORGE_NODE"); override != "" {
		if p, err := exec.LookPath(override); err == nil {
			if p, err = filepath.EvalSymlinks(p); err == nil {
				node = p
			}
		}
	}
	if node == "" {
		p, err := exec.LookPath("node")
		if err != nil {
			return ""
		}
		if node, err = filepath.EvalSymlinks(p); err != nil {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--version").Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if !bnNodeVersionPattern.MatchString(v) {
		return ""
	}
	return v
}

// bnBuild builds the four release targets of root. It returns the lines the script printed, or
// the script's error message (without its "build-native: " prefix).
func (a *app) bnBuild(root string) (string, string) {
	check := spawn(root, []string{"go", "version"}, 0, nil)
	switch {
	case check.errMsg == "spawnSync go ENOENT":
		return "", "Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go)."
	case check.failed():
		return "", "go version failed: " + check.errMsg
	case check.code != 0:
		return "", "go version failed: " + check.stderr
	}
	pkg, _, err := readPackage(root)
	if err != nil {
		return "", err.Error()
	}
	versionV, versionOK := pkg.get("version")
	version := jsTemplate(versionV, versionOK)
	ldflags := "-s -w -X main.version=" + version
	if node := bnNodeVersion(); node != "" {
		ldflags += " -X main.reviewWaitNodeVersion=" + node
	}
	var printed strings.Builder
	for _, t := range bnTargets {
		target := t[0] + "-" + t[1]
		dir := filepath.Join(root, "native", "bin", target)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return "", err.Error()
		}
		binary := filepath.Join(dir, "code-forge-native")
		pending := fmt.Sprintf("%s.%d.tmp", binary, a.pid)
		failure := func() string {
			defer os.Remove(pending)
			env := envWith(nil, [2]string{"CGO_ENABLED", "0"}, [2]string{"GOOS", t[0]}, [2]string{"GOARCH", t[2]},
				[2]string{"GOTOOLCHAIN", "local"}, [2]string{"GOPROXY", "off"})
			built := spawn(filepath.Join(root, "native"), []string{"go", "build", "-trimpath", "-ldflags", ldflags, "-o", pending,
				"./cmd/code-forge-native"}, 0, env)
			switch {
			case built.failed():
				return target + " build failed: " + built.errMsg
			case built.code != 0:
				return target + " build failed: " + built.stderr
			}
			if err := os.Chmod(pending, 0o755); err != nil {
				return err.Error()
			}
			if err := os.Rename(pending, binary); err != nil {
				return err.Error()
			}
			st, err := os.Stat(binary)
			if err != nil {
				return err.Error()
			}
			printed.WriteString(fmt.Sprintf("native %s: %d bytes (v%s)\n", target, st.Size(), version))
			return ""
		}()
		if failure != "" {
			return "", failure
		}
	}
	return printed.String(), ""
}
