package main

import (
	"io"
	"os"
)

// B712: Node writes every verb's stdout and stderr through writeSafe
// (src/util/redact.mjs): each write call masks every registered secret
// (redactString) before it reaches the stream. Go verbs write through
// safeStdout and safeStderr, which do the same with the one registered set
// (autopilotSecrets: the run's signer key once a loader read it, B710).
//
// Each Write call is redacted on its own, as Node redacts each writeSafe
// call: a secret split across two calls is not caught, in Node or here.
// The stream is looked up at write time, so a command (healTee) or a test
// that moves os.Stdout or os.Stderr still gets the output. A child process
// that inherits the standard streams (exec.Cmd) is not routed through this,
// as Node's stdio 'inherit' children are not.
type safeStream struct{ stderr bool }

var (
	safeStdout io.Writer = safeStream{}
	safeStderr io.Writer = safeStream{stderr: true}
)

func (s safeStream) Write(p []byte) (int, error) {
	file := os.Stdout
	if s.stderr {
		file = os.Stderr
	}
	if !safeHasSecrets() {
		return file.Write(p)
	}
	if _, err := io.WriteString(file, autopilotRedact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// safeHasSecrets reports whether any secret is registered (no copy of the
// bytes is needed when none is).
func safeHasSecrets() bool {
	autopilotSecretsMu.Lock()
	defer autopilotSecretsMu.Unlock()
	return len(autopilotSecrets) > 0
}
