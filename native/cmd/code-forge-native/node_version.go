package main

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// B585: Node writes process.version (the RUNNING node) into the gate-tree
// stamp, the error-log `node` field and the `logs report` system line. Go asks
// the node a hand-over would run (contextNode: CODE_FORGE_NODE, else node on
// PATH) for its version once per process. The build-time -X value
// (reviewWaitNodeVersion) is only the fallback when that ask fails.

var nodeRunVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// nodeRunVersionTimeout bounds the one `node --version` ask.
var nodeRunVersionTimeout = 10 * time.Second

// nodeRunVersionAsk is the seam tests replace; it returns "" when no usable
// node answers.
var nodeRunVersionAsk = nodeRunVersionAskNode

var (
	nodeRunVersionOnce   sync.Once
	nodeRunVersionCached string
)

// nodeRunVersion returns the version of the node that runs at this moment
// (vX.Y.Z), else the build-time value, else "unknown".
func nodeRunVersion() string {
	nodeRunVersionOnce.Do(func() { nodeRunVersionCached = nodeRunVersionAsk() })
	if nodeRunVersionCached != "" {
		return nodeRunVersionCached
	}
	if reviewWaitNodeVersion == "" {
		return "unknown"
	}
	return reviewWaitNodeVersion
}

func nodeRunVersionAskNode() string {
	node, err := contextNode()
	if err != nil || node == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeRunVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--version")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return ""
	}
	version := string(bytes.TrimSpace(out.Bytes()))
	if !nodeRunVersionPattern.MatchString(version) {
		return ""
	}
	return version
}
