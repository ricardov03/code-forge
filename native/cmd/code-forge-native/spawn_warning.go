package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B482: error-log.mjs logWarning for `code-forge spawn` (the budget.usd 80 %
// warning of a --run session): one `kind: "warning"` line per fingerprint per
// process, appended to $HOME/.code-forge/logs/errors.jsonl with the 1 MB trim.

var (
	spawnWarnMu     sync.Mutex
	spawnWarned     = map[string]bool{}
	spawnWarnBefore = []any{}
	// spawnWarnVerb is the verb the warning line names (B489: the worker
	// logs its own warnings under "worker").
	spawnWarnVerb = "spawn"
)

func spawnLogWarning(warning, message string) {
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
	text := clean(strings.TrimSpace(message))
	if len(text) > 2048 {
		end := 2048
		for !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	verb := clean(spawnWarnVerb)
	normalized := reviewWaitLogQuotes.ReplaceAllString(text, " ")
	normalized = reviewWaitLogPlaceholders.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPaths.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(reviewWaitLogWords.ReplaceAllString(normalized, " "))
	sum := sha256.Sum256([]byte(verb + "\n\nwarning\n" + normalized + "\n" + warning))
	fp := fmt.Sprintf("%x", sum[:6])
	spawnWarnMu.Lock()
	defer spawnWarnMu.Unlock()
	if spawnWarned[fp] {
		return
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	entry := ledger.Object{
		{Key: "ts", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "version", Value: version}, {Key: "node", Value: reviewWaitNodeVersion},
		{Key: "platform", Value: runtime.GOOS}, {Key: "arch", Value: arch},
		{Key: "verb", Value: verb}, {Key: "sub", Value: nil}, {Key: "flags", Value: []any{}},
		{Key: "exit", Value: 0.0}, {Key: "kind", Value: "warning"}, {Key: "warning", Value: warning},
		{Key: "message", Value: text}, {Key: "stack", Value: nil}, {Key: "cleaned", Value: counts()},
		{Key: "fp", Value: fp}, {Key: "before", Value: spawnWarnBefore},
	}
	data, err := ledger.Stringify(entry, false)
	if err != nil {
		return
	}
	if spawnAppendLog(filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "errors.jsonl"), data) {
		spawnWarned[fp] = true // only after the line is written, as Node
	}
}

// spawnAppendLog is error-log.mjs appendLine: append, then keep the newest
// half when the file passes 1 MB.
func spawnAppendLog(file string, line []byte) bool {
	if os.MkdirAll(filepath.Dir(file), 0700) != nil {
		return false
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	_, err = f.Write(append(line, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false
	}
	info, err := os.Stat(file)
	if err != nil {
		return false
	}
	if info.Size() <= 1024*1024 {
		return true
	}
	all, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	lines := []string{}
	for _, l := range strings.Split(string(all), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	used, start := int64(0), len(lines)
	for start > 0 {
		size := int64(len(lines[start-1]) + 1)
		if start < len(lines) && used+size > info.Size()/2 {
			break
		}
		used += size
		start--
	}
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	if os.WriteFile(tmp, []byte(strings.Join(lines[start:], "\n")+"\n"), 0600) != nil {
		return false
	}
	return os.Rename(tmp, file) == nil
}
