package rebase

import (
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

func ownsFile(entries []any, kinds ledger.Object, file string) bool {
	return owned.OwnsFile(entries, kinds, file)
}
