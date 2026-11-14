package ledger

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// DefaultRotateAtBytes matches the Node ledger writer's 10 MiB default.
const DefaultRotateAtBytes int64 = 10 * 1024 * 1024

// AppendRow appends one normalized map. Maps have no insertion order; callers
// needing input byte parity should use AppendObject with an ordered Object.
func AppendRow(dir, slug string, row map[string]any, rotateAtBytes int64) (map[string]any, error) {
	if row == nil {
		return nil, fmt.Errorf("appendRow: row must be a plain object")
	}
	result, err := AppendObject(dir, slug, ObjectFromMap(row), rotateAtBytes)
	if err != nil {
		return nil, err
	}
	return result.Map(), nil
}

// AppendObject is AppendRow's ordered variant. Normalization, rotation, recovery
// and encoding follow src/ledger/write.mjs. One O_APPEND Write publishes the
// entire line, including its newline, to Node and Go readers/writers alike.
func AppendObject(dir, slug string, row Object, rotateAtBytes int64) (Object, error) {
	event, ok := row.Get("event").(string)
	if !ok || event == "" {
		return nil, fmt.Errorf("appendRow: row.event must be a non-empty string")
	}
	if !slugPattern.MatchString(slug) {
		return nil, fmt.Errorf("invalid ledger slug %q", slug)
	}
	if rotateAtBytes <= 0 {
		return nil, fmt.Errorf("appendRow: rotateAtBytes must be a finite number > 0, got %d", rotateAtBytes)
	}
	normalized := row.Clone()
	if normalized.Get("ts") == nil {
		normalized.Set("ts", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	if normalized.Get("tokens_source") == nil {
		var source any
		if isNumber(row.Get("tokens_in")) || isNumber(row.Get("tokens_out")) {
			source = "estimated"
		}
		normalized.Set("tokens_source", source)
	}
	if normalized.Get("cost_source") == nil {
		var source any
		if isNumber(row.Get("cost_usd")) {
			source = "estimated"
		}
		normalized.Set("cost_source", source)
	}
	data, err := Stringify(normalized, false)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// Node and Go hold the same link-published identity lock across rotation, torn-tail
	// recovery and the append, so recovery cannot truncate another writer's row.
	lock := filepath.Join(dir, slug+".append.lock")
	release, err := lockAppend(lock, slug, time.Now().Add(5*time.Second))
	if err != nil {
		return nil, err
	}
	defer release()
	file := filepath.Join(dir, slug+".jsonl")
	info, err := os.Stat(file)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && info.Size() >= rotateAtBytes {
		if err := claimRotatedName(file, dir, slug); err != nil {
			return nil, err
		}
	}
	if beforeTornTailRecovery != nil {
		beforeTornTailRecovery()
	}
	if err := truncateTornTail(file); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}
	n, err := f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if n != len(data) {
		return nil, io.ErrShortWrite
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return normalized, nil
}

// beforeTornTailRecovery is nil in production. Tests set it to hold the append
// lock with the torn tail still present until a foreign writer signals that it
// has seen the lock, so contention with recovery waits on a signal, not a clock.
var beforeTornTailRecovery func()

func isNumber(value any) bool {
	switch value.(type) {
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func claimRotatedName(file, dir, base string) error {
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s.%d.jsonl", base, n))
		err := os.Link(file, candidate)
		if os.IsExist(err) {
			continue
		}
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := os.Remove(file); err != nil {
			if os.IsNotExist(err) {
				_ = os.Remove(candidate)
				return nil
			}
			return err
		}
		return nil
	}
}

func truncateTornTail(file string) error {
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := []byte{0}
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	data := make([]byte, info.Size())
	n, err := f.ReadAt(data, 0)
	if err != nil && err != io.EOF {
		return err
	}
	return f.Truncate(int64(bytes.LastIndexByte(data[:n], '\n') + 1))
}
