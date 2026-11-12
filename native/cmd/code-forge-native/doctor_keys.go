package main

// B483: the doctor keys row's refresh cases in Go (src/keys/store.mjs,
// src/keys/onepassword.mjs, src/keys/backends/file.mjs): an expired cached
// entry is deleted and its index row removed, a cached entry that outlives
// the cache hours is cut to now + hours (entry and index row rewritten), and
// an op:// reference is read with `op read` (retried once after a first-call
// timeout) and cached with its index row.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// opTimeout is OP_TIMEOUT_MS; tests shorten it.
var opTimeout = 60 * time.Second

// opMessages are OP_MESSAGES, the fixed texts a doctor reason may show.
var opMessages = map[string]string{
	"op_missing":    "1Password CLI not found; install it with: brew install 1password-cli",
	"op_locked":     "1Password is locked or not signed in; unlock the app (or run `op signin`) and try again",
	"op_timeout":    "1Password did not answer in time; unlock the app and try again",
	"op_not_found":  "1Password item or field not found, or no access",
	"op_bad_output": "unexpected answer from op",
	"op_failed":     "1Password CLI failed",
}

var (
	opLocked   = regexp.MustCompile(`(?i)not (currently )?signed in|signin|sign in|locked|unlock|session (has )?expired|desktop app|biometric`)
	opNotFound = regexp.MustCompile(`(?i)isn't an item|isn't a vault|isn't a field|no item found|(item|vault|field) not found|could not find (the )?(item|vault|field)|not authori[sz]ed|unauthori[sz]ed|no access|permission`)
	// a \uD800-\uDFFF escape: JSON.parse keeps a lone surrogate, Go does not
	jsonSurrogate = regexp.MustCompile(`\\u[dD][89a-fA-F]`)
)

// keyIndexReadable says whether Go rewrites index.json as Node's readIndex +
// JSON.stringify(index, null, 2) would. rowMerge is the too-long case, which
// spreads the existing row: only an object (or a falsy value) spreads alike.
func keyIndexReadable(home, name string, rowMerge bool) bool {
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "store", "index.json"))
	if err != nil {
		return true // missing reads as {}, any other error is a failed update
	}
	if !utf8.Valid(data) || jsonSurrogate.Match(data) {
		return false
	}
	value, err := ledger.DecodeValue(data)
	if err != nil {
		return !json.Valid(data) // a SyntaxError reads as {}; a number Go cannot hold hands over
	}
	index, ok := value.(ledger.Object)
	if !ok || !rowMerge {
		return true
	}
	switch row := index.Get(name).(type) {
	case nil, ledger.Object:
		return true
	case bool:
		return !row
	case float64:
		return row == 0
	case string:
		return row == ""
	}
	return false
}

// updateKeyIndex is updateIndex: read (missing or corrupt ⇒ {}), change,
// write private with two-space JSON.
func updateKeyIndex(home string, change func(index *ledger.Object)) error {
	file := filepath.Join(home, ".code-forge", "store", "index.json")
	index := ledger.Object{}
	data, err := os.ReadFile(file)
	switch {
	case err == nil:
		if value, err := ledger.DecodeValue(data); err == nil {
			if obj, ok := value.(ledger.Object); ok {
				index = obj
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	change(&index)
	out, err := ledger.Stringify(index, true)
	if err != nil {
		return err
	}
	return writePrivateFile(file, string(out))
}

// writePrivateFile is backends/file.mjs writePrivateFile: a 0700 directory,
// a new 0600 temp file created exclusively, synced, renamed over the target.
func writePrivateFile(file, content string) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	tmp := filepath.Join(dir, "."+filepath.Base(file)+"."+strconv.Itoa(os.Getpid())+"."+hex.EncodeToString(random)+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err == nil {
		if _, err = f.WriteString(content); err == nil {
			err = f.Sync()
		}
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	} else {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, file)
}

// encodeKeyEntry is encodeEntry: JSON.stringify({v, exp}).
func encodeKeyEntry(value string, exp float64) string {
	data, _ := ledger.Stringify(ledger.Object{{Key: "v", Value: value}, {Key: "exp", Value: exp}}, false)
	return string(data)
}

// opOnPath is spawn's PATH search: false means ENOENT (op_missing).
func opOnPath() bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		if _, err := os.Stat(filepath.Join(dir, "op")); err == nil {
			return true
		}
	}
	return false
}

// opRead is opRead: the value, or "" with the failure kind (reported for the
// error log as reportErrorKind does).
func opRead(ref string) (string, string) {
	if !opOnPath() {
		nativeReportedKind = "op_missing"
		return "", "op_missing"
	}
	// exec: each op child is recorded in the pid registry of this
	// process's run root while it runs (B567).
	res := toolsExec([]string{"op", "read", ref}, opTimeout)
	if !res.ok && res.timedOut {
		res = toolsExec([]string{"op", "read", ref}, opTimeout)
		logNativeWarning("doctor", "op_retry", "1Password did not answer in time; the call was retried once")
	}
	if !res.ok {
		kind := "op_failed"
		stderr := nodeUTF8(res.stderr)
		switch {
		case res.timedOut:
			kind = "op_timeout"
		case res.code != nil && *res.code == 127:
			kind = "op_missing"
		case opLocked.MatchString(stderr):
			kind = "op_locked"
		case opNotFound.MatchString(stderr):
			kind = "op_not_found"
		}
		nativeReportedKind = kind
		return "", kind
	}
	value := nodeUTF8(res.stdout)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	if value == "" {
		nativeReportedKind = "op_bad_output"
		return "", "op_bad_output"
	}
	return value, ""
}

// logNativeWarning is logWarning in src/util/error-log.mjs: one kind
// "warning" line with exit 0, no flags and the breadcrumbs before.
func logNativeWarning(verb, warning, message string) {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return
	}
	slug := ""
	if cwd, err := os.Getwd(); err == nil {
		if loaded, err := config.LoadFile(filepath.Join(cwd, ".code-forge.yml")); err == nil {
			if project, ok := loaded["project"].(map[string]any); ok {
				slug, _ = project["slug"].(string)
			}
		}
	}
	clean, counts := gatesScrubber(slug)
	message = strings.TrimSpace(clean(message))
	normalized := reviewWaitLogQuotes.ReplaceAllString(message, " ")
	normalized = reviewWaitLogPlaceholders.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPaths.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(reviewWaitLogWords.ReplaceAllString(normalized, " "))
	fp := sha256.Sum256([]byte(verb + "\n\nwarning\n" + normalized + "\n" + warning))
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	entry := ledger.Object{
		{Key: "ts", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "version", Value: version}, {Key: "node", Value: reviewWaitNodeVersion},
		{Key: "platform", Value: runtime.GOOS}, {Key: "arch", Value: arch},
		{Key: "verb", Value: clean(verb)}, {Key: "sub", Value: nil}, {Key: "flags", Value: []any{}},
		{Key: "exit", Value: 0}, {Key: "kind", Value: "warning"}, {Key: "warning", Value: warning},
		{Key: "message", Value: message}, {Key: "stack", Value: nil}, {Key: "cleaned", Value: counts()},
		{Key: "fp", Value: fmt.Sprintf("%x", fp[:6])}, {Key: "before", Value: smallVerbBefore},
	}
	if data, err := ledger.Stringify(entry, false); err == nil {
		appendErrorLogLine(data)
	}
}

// keyReasons is the doctor's reason list: a 1Password failure by its fixed
// message, anything else as "key store error", in order, deduplicated.
func keyReasons(reasons []string) string {
	seen, out := map[string]bool{}, []string{}
	for _, r := range reasons {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return " (" + strings.Join(out, "; ") + ")"
}
