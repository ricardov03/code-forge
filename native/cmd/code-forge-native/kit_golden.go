package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

// kitGolden is an internal JSON fixture driver. Lock holders announce a JSON
// result after acquisition and retain ownership until stdin closes.
func kitGolden(in io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(in)
	var data json.RawMessage
	if err := decoder.Decode(&data); err != nil {
		return err
	}
	input, err := ledger.DecodeObject(data)
	if err != nil {
		return err
	}
	text := func(key string) string { s, _ := input.Get(key).(string); return s }
	encode := func(value any) error { return json.NewEncoder(out).Encode(value) }
	var value any
	switch text("op") {
	case "owned":
		entries, _ := input.Get("entries").([]any)
		files, _ := input.Get("files").([]any)
		kinds := owned.Kinds(text("root"), entries)
		matches := []bool{}
		for _, file := range files {
			s, _ := file.(string)
			matches = append(matches, owned.OwnsFile(entries, kinds, s))
		}
		value = map[string]any{"kinds": kinds.Map(), "matches": matches}
	case "hash":
		var hash string
		hash, err = gitx.ContentHash(text("root"), text("file"))
		value = map[string]any{"hash": hash}
	case "changed":
		var files []string
		files, err = gitx.ChangedFiles(text("root"), text("base"))
		value = map[string]any{"files": files}
	case "git":
		var stdout, top string
		var changed []string
		var atOpen ledger.Object
		args := []string{}
		raw, _ := input.Get("args").([]any)
		for _, a := range raw {
			s, _ := a.(string)
			args = append(args, s)
		}
		stdout, err = gitx.Run(text("root"), args...)
		if err == nil {
			top, err = gitx.RepoTop(text("root"))
		}
		if err == nil {
			changed, err = gitx.ChangedFiles(text("root"), text("base"))
		}
		if err == nil {
			atOpen, err = gitx.TreeAtOpen(text("root"), text("base"))
		}
		stamps := map[string]string{}
		paths, _ := input.Get("paths").([]any)
		for _, p := range paths {
			s, _ := p.(string)
			stamps[s] = gitx.TreeStamp(text("root"), s)
		}
		value = map[string]any{"stdout": stdout, "top": top, "changed": changed, "stamps": stamps, "atOpen": atOpen.Map()}
	case "bad-locks":
		var paths []string
		paths, err = filelock.ListBadLocks()
		value = map[string]any{"paths": paths}
	case "try-lock":
		opts := filelock.Options{}
		if replacement, ok := input.Get("replaceAfterRead").(string); ok {
			opts.AfterRead = func() error {
				file, err := filelock.Path(text("key"))
				if err != nil {
					return err
				}
				return os.WriteFile(file, []byte(replacement), 0600)
			}
		}
		var lock filelock.Result
		lock, err = filelock.TryLock(text("key"), opts)
		value = map[string]any{"ok": lock.OK}
		if lock.OK {
			lock.Release()
		} else {
			value.(map[string]any)["result"] = "held"
		}
	case "with-lock":
		return filelock.WithLock(text("key"), func() error {
			if err := encode(map[string]bool{"ok": true}); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), in))
			return err
		}, filelock.Options{})
	case "update":
		err = state.UpdateBlock(text("home"), text("run"), text("id"), func(record, block *ledger.Object) ([]ledger.Object, error) {
			if block == nil {
				return nil, fmt.Errorf("update fixture requires id")
			}
			patch, _ := input.Get("patch").(ledger.Object)
			for _, f := range patch {
				block.Set(f.Key, f.Value)
			}
			rows := []ledger.Object{}
			raw, _ := input.Get("rows").([]any)
			for _, row := range raw {
				o, ok := row.(ledger.Object)
				if !ok {
					return nil, fmt.Errorf("row must be an object")
				}
				rows = append(rows, o)
			}
			return rows, nil
		})
		value = map[string]bool{"ok": true}
	default:
		return fmt.Errorf("unknown kit-golden operation %q", text("op"))
	}
	if err != nil {
		problem := map[string]string{"message": err.Error()}
		var se *state.Error
		var ge *gitx.Error
		var fe *filelock.Error
		if errors.As(err, &se) {
			problem["code"] = se.Code
		}
		if errors.As(err, &ge) {
			problem["code"] = ge.Code
		}
		if errors.As(err, &fe) {
			problem["code"] = fe.Code
		}
		value = map[string]any{"error": problem}
	}
	return encode(value)
}
