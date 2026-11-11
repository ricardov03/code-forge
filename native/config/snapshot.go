package config

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Snapshot is the project config as Node's loadProjectConfig returns it: parsed,
// version-migrated, keys in YAML order (JS object order), with nothing validated
// past that. Ordered is what a run record stores; Map is the same data for lookups.
type Snapshot struct {
	Path    string
	Ordered ledger.Object
	Map     map[string]any
}

// Hash is configHash: sha256 hex of the canonical JSON (keys sorted at every level).
func (s Snapshot) Hash() string { return Hash(s.Map) }

// Hash returns configHash for a config already in its JSON form.
func Hash(cfg any) string {
	data, _ := canon.Marshal(cfg)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// LoadSnapshot reads the project root's config the way loadProjectConfig does
// (not-found, read-error, parse-error and migrate-error refusals, same messages).
// Unlike Load it runs no merge-gate checks, which Node's loader does not have.
func LoadSnapshot(cwd string) (Snapshot, error) {
	return LoadSnapshotFile(filepath.Join(ProjectRootFor(cwd), Filename))
}

// LoadSnapshotFile is LoadSnapshot for a particular file: LoadFileOrdered's
// parse and migrate, then every mapping as a ledger.Object in YAML key order
// (migrateConfig's {...mapping, version: 1} keeps version's place, else appends it).
func LoadSnapshotFile(path string) (Snapshot, error) {
	mapping, order, err := LoadFileOrdered(path)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Path: path, Ordered: order.Object(mapping).(ledger.Object), Map: mapping}, nil
}
