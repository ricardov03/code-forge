# Changelog

All notable changes to `@codedology/code-forge` are recorded here. Format loosely follows
[Keep a Changelog](https://keepachangelog.com/); versions follow semver ahead of a 1.0.0 that
waits on a second real consumer (plan §10.4, Q17).

## [Unreleased]

## [0.25.1] — 2026-11-19

### Added

- `code-forge config fix [--dry-run] [--file <path>]` rewrites the `.code-forge.yml` shapes an older release accepted and this one refuses, one named rule each with a one-line reason (today `gates-extra-invariants`: `gates.extra.invariants`, refused since 0.22.0, is removed and an emptied `gates.extra` with it, with a hint to move the script to `gates.custom`). Only the old key's lines change (comments, blank lines and key order stay); `--dry-run` prints a diff and writes nothing; unknown keys are listed, never deleted; nothing is added, so no secret is written; a file with YAML anchors, aliases or tags, or one that would not read back as the same config minus the old key, is left unchanged with the step to do by hand. `code-forge upgrade` runs the fixer on the cwd's `.code-forge.yml` after a successful upgrade and prints what it changed or `config ok`; it never fails the upgrade (B791)
