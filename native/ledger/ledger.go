// Package ledger reads code-forge JSONL ledgers without modifying them.
package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Row is a schema-free JSON value, normally an object. Like Node's reader,
// this package accepts any valid JSON value rather than enforcing a row schema.
type Row = any

// ReadRows reads only the current file in dir. Missing files return an empty slice.
func ReadRows(dir, slug string) ([]Row, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("invalid ledger slug %q", slug)
	}
	return readFile(filepath.Join(dir, slug+".jsonl"))
}

// ReadAllRows reads rotations in numeric order, followed by the current file.
func ReadAllRows(dir, slug string) ([]Row, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("invalid ledger slug %q", slug)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Row{}, nil
	}
	if err != nil {
		return nil, err
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `\.(\d+)\.jsonl$`)
	type rotation struct {
		name string
		n    float64
	}
	rotated := []rotation{}
	for _, entry := range entries {
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		// Node converts the suffix with Number(), so a huge suffix may be +Inf.
		n, _ := strconv.ParseFloat(match[1], 64)
		rotated = append(rotated, rotation{entry.Name(), n})
	}
	sort.SliceStable(rotated, func(i, j int) bool { return rotated[i].n < rotated[j].n })
	rotated = append(rotated, rotation{name: slug + ".jsonl"})
	rows := []Row{}
	for _, file := range rotated {
		part, err := readFile(filepath.Join(dir, file.name))
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

func readFile(file string) ([]Row, error) {
	text, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return []Row{}, nil
	}
	if err != nil {
		return nil, err
	}
	lines := []string{}
	for _, line := range strings.Split(string(text), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	rows := []Row{}
	for i, line := range lines {
		var row Row
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			if i == len(lines)-1 {
				continue
			}
			return nil, fmt.Errorf("%s: line %d: %w", file, i+1, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
