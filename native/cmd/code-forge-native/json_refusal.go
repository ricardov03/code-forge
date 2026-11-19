package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ricardov03/code-forge/native/gosafe"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B872 (B801 audit P04): a command asked for --json that refuses (exit not 0)
// and prints nothing on stdout leaves a caller that parses stdout with
// nothing to read. Every such refusal now also prints one compact line on
// stdout, {"ok":false,"error":<the refusal text>,"next":<command or null>},
// with the same exit code. The refusal text still goes to stderr as before.
// A command that already printed on stdout (its own JSON answer, ok:false
// included) is left as it is.

// jsonRefusalWanted reports whether args ask a public verb for --json (before
// any `--`, which starts a child command's own arguments). Help never is.
func jsonRefusalWanted(args []string) bool {
	if len(args) < 2 || args[0] == "internal" || args[0] == "help" || strings.HasPrefix(args[0], "-") {
		return false
	}
	want := false
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" {
			return false
		}
		if a == "--json" {
			want = true
		}
	}
	return want
}

// jsonRefusalRun runs fn; for a --json call it copies stdout and stderr
// through (as written, so nothing streams later) and adds the refusal line
// when fn fails having printed nothing on stdout.
func jsonRefusalRun(args []string, fn func([]string) (int, error)) (code int, err error) {
	if !jsonRefusalWanted(args) {
		return fn(args)
	}
	realOut := os.Stdout
	stopOut := jsonTee(&os.Stdout)
	stopErr := jsonTee(&os.Stderr)
	finished := false
	defer func() {
		outText := stopOut()
		errText := stopErr()
		if !finished {
			return // a panic: the guard answers it
		}
		if code == 0 || strings.TrimSpace(outText) != "" {
			return
		}
		if err != nil {
			errText += "code-forge-native: " + err.Error() + "\n"
		}
		fmt.Fprint(realOut, jsonRefusalLine(args, code, errText))
	}()
	code, err = fn(args)
	finished = true
	return code, err
}

var (
	jsonNextCommand = regexp.MustCompile("code-forge [a-z][a-z0-9-]*(?: [^;\n()`'\"]*)?")
	jsonUsageLine   = regexp.MustCompile(`(?m)^\s*usage:`)
	jsonSubWord     = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// jsonRefusalLine is the one stdout line of a refused --json call: error is
// the refusal text (registered secrets masked), next the command it names
// (for a usage error, the verb's --help), or null.
func jsonRefusalLine(args []string, code int, errText string) string {
	text := strings.TrimSpace(autopilotRedact(errText))
	if text == "" {
		text = fmt.Sprintf("code-forge %s failed (exit %d)", strings.Join(jsonRefusalCommand(args), " "), code)
	}
	var next any
	if code == 2 && jsonUsageLine.MatchString(text) {
		next = "code-forge " + args[0] + " --help" // the verb's help names every subcommand
	} else if m := jsonNextCommand.FindString(text); m != "" {
		next = strings.TrimRight(strings.TrimSpace(m), ".,:")
	}
	line, _ := ledger.Stringify(ledger.Object{{Key: "ok", Value: false}, {Key: "error", Value: text}, {Key: "next", Value: next}}, false)
	return string(line) + "\n"
}

// jsonRefusalCommand is the verb and, when the second word is not a flag or
// an id, its subcommand.
func jsonRefusalCommand(args []string) []string {
	if len(args) > 1 && jsonSubWord.MatchString(args[1]) {
		return args[:2]
	}
	return args[:1]
}

// jsonTee points *stream at a pipe whose bytes are copied to the stream's
// file as they come and kept; stop restores the stream and returns what was
// written. A child that still holds the pipe is not waited for long.
func jsonTee(stream **os.File) (stop func() string) {
	real := *stream
	r, w, err := os.Pipe()
	if err != nil {
		return func() string { return "" }
	}
	*stream = w
	kept := &healKept{} // the last 64 KiB
	done := make(chan struct{})
	gosafe.Go("json-refusal-tee", func() {
		defer close(done)
		_, _ = io.Copy(io.MultiWriter(real, kept), r)
	})
	var once sync.Once
	return func() string {
		once.Do(func() {
			*stream = real
			_ = w.Close()
			select {
			case <-done:
				_ = r.Close()
			case <-time.After(2 * time.Second):
			}
		})
		return kept.String()
	}
}
