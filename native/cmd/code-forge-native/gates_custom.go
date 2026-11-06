package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// The shared signed writer owns key validation and signing for the ordinary
// project slug. An explicit ledger slug changes only the append destination.
func gatesSignedRow(home, run, slug string, row ledger.Object) error {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return err
	}
	if record.Get("project") == slug {
		_, err = state.WriteSigned(home, run, row)
		return err
	}
	file := filepath.Join(home, ".code-forge", "runs", run+".key")
	info, err := os.Stat(file)
	if os.IsNotExist(err) {
		return fmt.Errorf("run %s has no signer key", run)
	}
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("run %s signer key is accessible to other users (mode %o); expected 600", run, info.Mode().Perm())
	}
	key, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if len(key) != 32 {
		return fmt.Errorf("run %s signer key is not 32 bytes", run)
	}
	signed, err := sign.SignRow(row.Map(), key)
	if err != nil {
		return err
	}
	row.Set("ts", signed["ts"])
	row.Set("mac", signed["mac"])
	_, err = ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, row, ledger.DefaultRotateAtBytes)
	return err
}
