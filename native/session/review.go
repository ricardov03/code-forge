package session

import (
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// ReviewSpawn is review.Deps.Spawn backed by Spawn: the worker's pinned
// options (base: run, block, slug, timeout, run root) with the call's level,
// role, packet and row fields, the call's config winning over the base's
// (loop.mjs spawn), and finding.schema.json as the schema (reviewFile).
// A refusal (*Error, or a case IsUnsupported names) comes back as the error,
// which review.File counts as a session that threw: a caller hands a ticket
// to this only after CheckReview passed for every level it may use.
func ReviewSpawn(base Opts, deps Deps) func(review.SpawnOpts) (ledger.Object, error) {
	return func(in review.SpawnOpts) (ledger.Object, error) {
		o, err := reviewOpts(base, in)
		if err != nil {
			return nil, err
		}
		return Spawn(o, deps)
	}
}

// CheckReview is Check for one level and role a review would spawn.
func CheckReview(base Opts, level, role string) error {
	o, err := reviewOpts(base, review.SpawnOpts{Level: level, Role: role, PromptPath: "/review-packet.md"})
	if err != nil {
		return err
	}
	return Check(o)
}

func reviewOpts(base Opts, in review.SpawnOpts) (Opts, error) {
	schema, err := LoadFindingSchema()
	if err != nil {
		return Opts{}, err
	}
	o := base
	if in.Cfg != nil {
		o.Cfg = in.Cfg
	}
	o.Level, o.Role, o.PromptPath, o.Schema = in.Level, in.Role, in.PromptPath, schema
	o.RowExtra = in.RowExtra
	return o, nil
}
