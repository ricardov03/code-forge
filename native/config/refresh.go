package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

type SkippedSource struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type RefreshResult struct {
	SourcesRead    []string            `json:"sourcesRead"`
	SourcesSkipped []SkippedSource     `json:"sourcesSkipped"`
	SeenThisRun    map[string][]string `json:"seenThisRun"`
	CachePath      string              `json:"cachePath"`
}

func UserModelsCachePath(home string) string {
	return filepath.Join(home, ".code-forge", "cache", "models.json")
}

func forbiddenProvider(key string) bool {
	return key == "__proto__" || key == "constructor" || key == "prototype"
}

// LoadSeenInCache tolerates unreadable, corrupt and wrongly shaped caches. Only
// own provider entries whose values are arrays of strings survive.
func LoadSeenInCache(home string) map[string][]string {
	out := map[string][]string{}
	data, err := os.ReadFile(UserModelsCachePath(home))
	if err != nil {
		return out
	}
	if !json.Valid(data) {
		return out
	}
	parsed, _ := schemaValue(readCacheJSON(data)).(map[string]any)
	seen, _ := parsed["seen"].(map[string]any)
	for key, value := range seen {
		if forbiddenProvider(key) {
			continue
		}
		items, ok := value.([]any)
		if !ok {
			continue
		}
		ids := []string{}
		valid := true
		for _, item := range items {
			str, ok := item.(string)
			if !ok {
				valid = false
				break
			}
			ids = append(ids, str)
		}
		if valid {
			out[key] = ids
		}
	}
	return out
}

func readCacheJSON(data []byte) any {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	return readSchema(decoder)
}

func jsonSkipReason(data []byte) string {
	var parsed any
	err := json.Unmarshal(data, &parsed)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		// V8 includes positions for punctuation, property-name and string-escape
		// errors, but omits them for unexpected tokens and truncated documents.
		for _, detail := range []string{"after top-level value", "object key string", "after object key", "after array element", "in string escape code", "in string literal", "in \\u hexadecimal character escape"} {
			if strings.Contains(syntax.Error(), detail) {
				prefix := string(data[:syntax.Offset-1])
				position := len(utf16.Encode([]rune(prefix)))
				return "not valid JSON (at position " + strconv.Itoa(position) + ")"
			}
		}
	}
	return "not valid JSON"
}

func extractIDs(parsed any, codex bool) []string {
	models := parsed
	if mapping, ok := parsed.(schemaObject); ok {
		models = mapping.get("models")
	}
	out := []string{}
	if items, ok := models.([]any); ok {
		for _, item := range items {
			if codex {
				if mapping, ok := item.(schemaObject); ok {
					item = mapping.get("slug")
				}
			}
			if id, ok := item.(string); ok && (!codex || id != "") {
				out = append(out, id)
			}
		}
	} else if mapping, ok := models.(schemaObject); ok && !codex {
		for _, field := range mapping {
			id := text(object(object(field.value).get("info")).get("id"))
			if id == "" {
				id = field.key
			}
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func utf16Less(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// RefreshFromCLICaches explicitly refreshes the user cache. It never reads or
// writes a project config; malformed sources are skipped independently.
func RefreshFromCLICaches(home string) (RefreshResult, error) {
	result := RefreshResult{SourcesRead: []string{}, SourcesSkipped: []SkippedSource{}, SeenThisRun: map[string][]string{}, CachePath: UserModelsCachePath(home)}
	for _, source := range []struct{ provider, relative string }{{"openai", ".codex"}, {"xai", ".grok"}} {
		path := filepath.Join(home, source.relative, "models_cache.json")
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				result.SourcesSkipped = append(result.SourcesSkipped, SkippedSource{path, "unreadable (" + errno(err) + ")"})
			}
			continue
		}
		if !json.Valid(data) {
			result.SourcesSkipped = append(result.SourcesSkipped, SkippedSource{path, jsonSkipReason(data)})
			continue
		}
		ids := extractIDs(readCacheJSON(data), source.provider == "openai")
		if len(ids) == 0 {
			result.SourcesSkipped = append(result.SourcesSkipped, SkippedSource{path, "parsed but no model ids could be read from it"})
			continue
		}
		result.SeenThisRun[source.provider] = ids
		result.SourcesRead = append(result.SourcesRead, path)
	}
	merged := LoadSeenInCache(home)
	for provider, ids := range result.SeenThisRun {
		unique := map[string]bool{}
		for _, id := range append(merged[provider], ids...) {
			unique[id] = true
		}
		values := []string{}
		for id := range unique {
			values = append(values, id)
		}
		sort.Slice(values, func(i, j int) bool { return utf16Less(values[i], values[j]) })
		merged[provider] = values
	}
	if err := os.MkdirAll(filepath.Dir(result.CachePath), 0755); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(filepath.Dir(result.CachePath), ".models.json.*.tmp")
	if err != nil {
		return result, err
	}
	defer os.Remove(file.Name())
	cache := struct {
		Status    string              `json:"status"`
		UpdatedAt string              `json:"updated_at"`
		Seen      map[string][]string `json:"seen"`
	}{"seen-in-cache", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), merged}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	writeErr := encoder.Encode(cache)
	closeErr := file.Close()
	if writeErr != nil {
		return result, writeErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if err := os.Rename(file.Name(), result.CachePath); err != nil {
		return result, err
	}
	return result, nil
}
