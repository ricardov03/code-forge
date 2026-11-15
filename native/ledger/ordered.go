package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadAllOrdered reads the same rows as ReadAllRows, in the same order, but
// keeps each object's property order (objects are Object values), the order
// JSON.parse gives Node. Callers that print a value with JSON.stringify use it.
// A lone surrogate escape stays a lone surrogate (WTF-8), as JSON.parse keeps
// it (B704).
func ReadAllOrdered(dir, slug string) ([]any, error) {
	names, err := ledgerNames(dir, slug)
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for _, name := range names {
		text, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
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
		for i, line := range lines {
			value, err := ParseOrdered([]byte(line))
			if err == nil && !json.Valid([]byte(line)) {
				err = fmt.Errorf("invalid JSON")
			}
			if err != nil {
				if i == len(lines)-1 {
					continue
				}
				return nil, fmt.Errorf("%s: line %d: %w", name, i+1, err)
			}
			rows = append(rows, value)
		}
	}
	return rows, nil
}
