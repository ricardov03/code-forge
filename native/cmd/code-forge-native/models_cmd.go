package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/config"
)

const modelsUsage = "usage: code-forge models [--refresh --from-cli-caches]\n"

// knownIDs is KNOWN_IDS in src/config/known-ids.mjs, in declaration order.
var knownIDs = []struct {
	provider string
	ids      []string
}{
	{"anthropic", []string{"claude-haiku-4-5-20251001", "claude-sonnet-5", "claude-opus-5-5", "claude-fable-5-1", "claude-fable-5-2"}},
	{"openai", []string{"gpt-6-luna", "gpt-6-sol", "gpt-6-astra"}},
	{"xai", []string{"grok-4.7", "grok-4.7-build-fast", "grok-4.6", "grok-4.5"}},
}

func knownIDsFor(provider string) []string {
	for _, entry := range knownIDs {
		if entry.provider == provider {
			return entry.ids
		}
	}
	return nil
}

func init() {
	routes["models"] = smallVerb{name: "models", usage: modelsUsage, prepare: prepareModels}.run
}

// jsLess is Array.prototype.sort's default order: UTF-16 code units.
func jsLess(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// prepareModels is src/cli/models.mjs.
func prepareModels(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	refresh, fromCaches := false, false
	usageError := ""
	for _, arg := range args {
		if arg == "--refresh" {
			refresh = true
		} else if arg == "--from-cli-caches" {
			fromCaches = true
		} else {
			usageError = "unknown argument: " + arg
			break
		}
	}
	if usageError == "" && fromCaches && !refresh {
		usageError = "--from-cli-caches requires --refresh"
	}
	if usageError == "" && refresh && !fromCaches {
		usageError = "models --refresh requires --from-cli-caches (the only refresh source in 0.1)"
	}
	if usageError != "" {
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens(usageError))
			return 2
		}, 0, true
	}
	cwd, _ := os.Getwd()
	cfg, _, err := config.LoadOrdered(cwd)
	if yamlSyntaxError(err) {
		return nil, hConfigParseError, false
	}
	home := os.Getenv("HOME")
	if refresh && !modelsCacheWritable(home) {
		// Node's fs error text (code, syscall, quoted path) is left to Node.
		return nil, hModelsRefreshUnwritable, false
	}
	if refresh {
		for _, source := range []string{".codex", ".grok"} {
			if data, err := os.ReadFile(filepath.Join(home, source, "models_cache.json")); err == nil && !json.Valid(data) {
				// V8's JSON.parse wording decides the skip reason; Node prints it.
				return nil, hModelsRefreshInvalidJSON, false
			}
		}
	}
	return func(stdout, stderr io.Writer) int {
		if refresh {
			result, err := config.RefreshFromCLICaches(home)
			if err != nil {
				nodeLog(stderr, "error", maskTokens("refresh failed: "+err.Error()))
				return 1
			}
			if len(result.SourcesRead) == 0 {
				nodeLog(stderr, "warn", "no CLI caches were found to refresh from (checked ~/.codex and ~/.grok)")
			} else {
				nodeLog(stdout, "info", "refreshed from "+strconv.Itoa(len(result.SourcesRead))+" CLI cache(s) -> "+result.CachePath+" (status: seen-in-cache)")
			}
			for _, skipped := range result.SourcesSkipped {
				nodeLog(stderr, "warn", maskTokens("skipped "+skipped.Path+": "+skipped.Reason))
			}
		}
		seen := config.LoadSeenInCache(home)
		if err != nil {
			if e, ok := err.(*config.Error); ok && e.Kind != "not-found" {
				nodeLog(stderr, "warn", maskTokens("could not read known_extra from "+e.Path+": "+e.Kind+": "+e.Message))
			}
		}
		knownExtra, _ := cfg["known_extra"].(map[string]any)
		providers := map[string]bool{}
		for _, entry := range knownIDs {
			providers[entry.provider] = true
		}
		for provider := range knownExtra {
			providers[provider] = true
		}
		for provider := range seen {
			providers[provider] = true
		}
		sorted := []string{}
		for provider := range providers {
			sorted = append(sorted, provider)
		}
		sort.Slice(sorted, func(i, j int) bool { return jsLess(sorted[i], sorted[j]) })
		for _, provider := range sorted {
			nodeLog(stdout, "info", maskTokens(provider+":"))
			printed := map[string]bool{}
			for _, id := range knownIDsFor(provider) {
				nodeLog(stdout, "info", "  "+id)
				printed[id] = true
			}
			extra, _ := knownExtra[provider].([]any)
			for _, item := range extra {
				id, ok := item.(string)
				if !ok || printed[id] {
					continue
				}
				nodeLog(stdout, "info", maskTokens("  "+id+" (known_extra)"))
				printed[id] = true
			}
			for _, id := range seen[provider] {
				if printed[id] {
					continue
				}
				nodeLog(stdout, "info", maskTokens("  "+id+" (seen-in-cache)"))
				printed[id] = true
			}
		}
		return 0
	}, 0, true
}

// modelsCacheWritable reports whether the cache directory exists as a
// writable directory, or can be made under a writable existing ancestor,
// and the cache path is absent or a regular file the rename can replace.
func modelsCacheWritable(home string) bool {
	return cacheFileWritable(config.UserModelsCachePath(home))
}

// cacheFileWritable reports that a temp-file-and-rename write of file can
// finish: file is absent or a regular file, and its nearest existing
// directory can take new entries.
func cacheFileWritable(file string) bool {
	if info, err := os.Lstat(file); err == nil && !info.Mode().IsRegular() {
		return false
	}
	dir := filepath.Dir(file)
	for {
		info, err := os.Stat(dir)
		if err == nil {
			return info.IsDir() && unixWritable(dir)
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func unixWritable(dir string) bool {
	return syscall.Access(dir, 2|1) == nil
}
