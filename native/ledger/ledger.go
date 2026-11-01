// Package ledger reads and appends Node-compatible code-forge JSONL ledgers.
package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Index retains each inode's parsed lines and physical read offset. An unfinished
// line is kept in memory, including valid JSON without a trailing newline: Node
// exposes that value now, but a later append can still change its meaning.
type Index struct {
	files     map[string]*tail
	BytesRead int64
}

type parsedLine struct {
	row Row
	err error
}

type tail struct {
	info    os.FileInfo
	offset  int64
	lines   []parsedLine
	partial []byte
}

// Refresh reads only appended bytes. Renames reuse the inode's cached rows;
// replacements and truncations start a new tail. Its ordering and tolerated last
// malformed nonempty line are the same as ReadAllRows.
func (idx *Index) Refresh(dir, slug string) ([]Row, error) {
	names, err := ledgerNames(dir, slug)
	if err != nil {
		return nil, err
	}
	next := make(map[string]*tail)
	used := make(map[*tail]bool)
	rows := []Row{}
	for _, name := range names {
		file := filepath.Join(dir, name)
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("ledger is not a regular file")
		}
		var state *tail
		for _, old := range idx.files {
			if !used[old] && os.SameFile(old.info, info) {
				state = old
				break
			}
		}
		if state == nil || info.Size() < state.offset || (info.Size() == state.offset && !info.ModTime().Equal(state.info.ModTime())) {
			state = &tail{info: info}
		}
		used[state] = true
		next[file] = state
		if info.Size() > state.offset {
			f, err := os.Open(file)
			if err != nil {
				return nil, err
			}
			_, err = f.Seek(state.offset, io.SeekStart)
			var data []byte
			if err == nil {
				data, err = io.ReadAll(io.LimitReader(f, info.Size()-state.offset))
			}
			f.Close()
			if err != nil {
				return nil, err
			}
			idx.BytesRead += int64(len(data))
			state.offset += int64(len(data))
			data = append(state.partial, data...)
			parts := bytes.Split(data, []byte{'\n'})
			for _, line := range parts[:len(parts)-1] {
				if len(line) != 0 {
					state.lines = append(state.lines, parseLine(line))
				}
			}
			state.partial = bytes.Clone(parts[len(parts)-1])
		}
		state.info = info
		count := len(state.lines)
		if len(state.partial) > 0 {
			count++
		}
		for i, line := range state.lines {
			if line.err != nil {
				if i != count-1 {
					return nil, fmt.Errorf("%s: line %d: %w", file, i+1, line.err)
				}
			} else {
				rows = append(rows, line.row)
			}
		}
		if len(state.partial) > 0 {
			line := parseLine(state.partial)
			if line.err == nil {
				rows = append(rows, line.row)
			}
		}
	}
	idx.files = next
	return rows, nil
}

func parseLine(data []byte) parsedLine {
	var row Row
	err := json.Unmarshal(data, &row)
	return parsedLine{row: row, err: err}
}

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
	names, err := ledgerNames(dir, slug)
	if err != nil {
		return nil, err
	}
	rows := []Row{}
	for _, name := range names {
		part, err := readFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

func ledgerNames(dir, slug string) ([]string, error) {
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("invalid ledger slug %q", slug)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
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
	names := []string{}
	for _, file := range rotated {
		names = append(names, file.name)
	}
	return names, nil
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
