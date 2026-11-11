package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
)

const landMergeHint = "the gate ran in a temporary tree with tracked files only; git-ignored files the suite needs go in gates.merge_gate_links"

func landPrepareMergeGate(main, temporary, workspace string, settings config.MergeGateConfig, copyLinks bool) error {
	seen := map[string]bool{}
	for _, name := range settings.Links {
		name = filepath.Clean(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		source, err := config.MergeGatePath(main, name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(source); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "warning: gates.merge_gate_links source missing: %s\n", name)
			continue
		} else if err != nil {
			return fmt.Errorf("gates.merge_gate_links cannot read source metadata: %s", name)
		}
		// Check the destination before creating parents. A tracked symlink in
		// the trial tree must never redirect writes back into another checkout.
		destination, err := config.MergeGatePath(temporary, name)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			return fmt.Errorf("gates.merge_gate_links destination already exists: %s", name)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		if !copyLinks {
			err = os.Symlink(source, destination)
			if err == nil {
				continue
			}
			if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.ENOSYS) {
				return fmt.Errorf("gates.merge_gate_links cannot link: %s", name)
			}
		}
		if err := landCopyLink(main, source, destination); err != nil {
			return fmt.Errorf("gates.merge_gate_links cannot copy: %s", name)
		}
	}
	if len(settings.Setup) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, settings.Setup[0], settings.Setup[1:]...)
		cmd.Dir, cmd.Env = workspace, append(gitx.ChildEnv(), "CI=1", "npm_config_yes=false")
		// Setup may handle local secrets. Its output is never included in errors.
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("gates.merge_gate_setup failed")
		}
	}
	return nil
}

func landCopyLink(root, source, destination string) error {
	rel, err := filepath.Rel(root, source)
	if err != nil {
		return err
	}
	source, err = config.MergeGatePath(root, rel)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := landCopyLink(root, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("only regular files and directories can be copied")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closed := out.Close()
	if err != nil {
		return err
	}
	return closed
}
