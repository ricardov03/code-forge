package review

// B537: util/exec.mjs exec for the packet's extra git reads (moved code, the
// deletion note, related files): optional stdin, accepted exit codes and a
// per-stream byte cap. Past the cap the rest of the stream is discarded (the
// child is never signalled) and the call fails, as Node's maxBufferBytes does.

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// capDefaultBytes is exec.mjs DEFAULT_MAX_BUFFER_BYTES (10 MiB).
const capDefaultBytes = 10 * 1024 * 1024

type capBuffer struct {
	buf  bytes.Buffer
	cap  int
	over bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.cap - c.buf.Len(); len(p) > room {
		c.over = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// gitCapped runs git like gitx.WithEnv (30 s, the given env) with stdin
// `input` (nil: none), the accepted exit codes (0 always) and maxBytes per
// stream. ok is Node's `result === 'ok'`; stdout is decoded as Node does.
func gitCapped(root string, env []string, input []byte, okCodes []int, maxBytes int, args ...string) (stdout string, code int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = env
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out := &capBuffer{cap: maxBytes}
	errOut := &capBuffer{cap: maxBytes}
	cmd.Stdout, cmd.Stderr = out, errOut
	err := cmd.Run()
	code = 0
	if err != nil {
		code = -1
		if e, isExit := err.(*exec.ExitError); isExit {
			code = e.ExitCode()
		}
	}
	stdout = decodeUTF8(out.buf.Bytes())
	if out.over || errOut.over || ctx.Err() != nil || code < 0 {
		return stdout, code, false
	}
	if code == 0 {
		return stdout, 0, true
	}
	for _, c := range okCodes {
		if c == code {
			return stdout, code, true
		}
	}
	return stdout, code, false
}

// gitListFiles is readFileDiff's listFiles: `git ls-files -z`, [] on failure.
func gitListFiles(root string) []string {
	out, _, ok := gitCapped(root, reviewGitEnv(), nil, nil, capDefaultBytes, "ls-files", "-z")
	if !ok {
		return []string{}
	}
	files := []string{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}
