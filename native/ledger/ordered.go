package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ReadAllOrdered reads the same rows as ReadAllRows, in the same order, but
// keeps each object's property order (objects are Object values), the order
// JSON.parse gives Node. Callers that print a value with JSON.stringify use it.
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
			dec := json.NewDecoder(bytes.NewReader([]byte(line)))
			value, err := decodeValue(dec)
			if err == nil {
				if _, end := dec.Token(); end != io.EOF {
					err = fmt.Errorf("trailing data")
				}
			}
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
