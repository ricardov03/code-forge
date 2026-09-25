/**
 * The per-harness install path table (plan §2.1, unchanged from v1; research/installer-patterns.md
 * §"Agent Skills standard + `npx skills`"). Pure data — no filesystem access here (that is
 * `./detect.mjs`) and no writes (that is `./link.mjs`).
 *
 * Claude Code and Codex CLI's paths are backed by assumption `[A7]` (both skill directories were
 * observed on this machine, 2026-09-24: `~/.claude/skills` and `~/.codex/skills`, the latter
 * empty). Grok's GLOBAL path is also backed by `[A7]` (`~/.grok/skills` observed with 5 entries,
 * so Grok reads that directory) but its PROJECT path is not — a harness CLI exists to probe it
 * (`doctor`, a later block, asks Grok to list its skills). Gemini is not installed on this machine
 * at all: `[A6]`, its whole row is `verified: false`, matching the plan's "Gemini row UNVERIFIED"
 * (§10.3 acceptance) — the only row that flag applies to. Cursor and Copilot have no CLI and no
 * adapter; detection is directory-presence only, and their paths (shared with Codex/Gemini under
 * `.agents/skills/`) come from research/installer-patterns.md, not from an observation on this
 * machine, but the plan does not ask `doctor` to mark them unverified (there is no CLI to probe).
 */

import path from 'node:path';

/**
 * @typedef {object} HarnessRow
 * @property {string} id - stable identifier, also the config `harnesses[]` entry value.
 * @property {string} label - human-readable name.
 * @property {string|null} command - executable name to look for on PATH, or `null` when the
 *   harness has no CLI (Cursor, Copilot) — detection then relies on `homeMarker` alone.
 * @property {string} homeMarker - directory name directly under `$HOME` (e.g. `.claude`) used
 *   both as a detection fallback and as the base of the global install path.
 * @property {string} projectSkillsDir - project-relative directory that holds installed skills.
 * @property {boolean} verified - whether this row's paths are confirmed on a real machine (§10.3:
 *   exactly the Gemini row is `false`).
 * @property {string} notes - the plan's free-text note for this row, unchanged wording.
 */

/**
 * Six rows, Claude Code first (plan §2.1 table order). Frozen so a caller cannot mutate the
 * shared table in place.
 * @type {ReadonlyArray<HarnessRow>}
 */
export const HARNESSES = Object.freeze(
  [
    {
      id: 'claude',
      label: 'Claude Code',
      command: 'claude',
      homeMarker: '.claude',
      projectSkillsDir: '.claude/skills',
      verified: true,
      notes: '[A7] global skills dir exists here.',
    },
    {
      id: 'codex',
      label: 'Codex CLI',
      command: 'codex',
      homeMarker: '.codex',
      projectSkillsDir: '.agents/skills',
      verified: true,
      notes: '[A7] ~/.codex/skills exists (empty). Paths from research/installer-patterns.md.',
    },
    {
      id: 'grok',
      label: 'Grok CLI',
      command: 'grok',
      homeMarker: '.grok',
      projectSkillsDir: '.agents/skills',
      verified: true,
      notes:
        '[A7] ~/.grok/skills exists with 5 entries, so Grok reads it; the PROJECT path is unverified — doctor asks Grok to list its skills.',
    },
    {
      id: 'gemini',
      label: 'Gemini CLI',
      command: 'gemini',
      homeMarker: '.gemini',
      projectSkillsDir: '.agents/skills',
      verified: false,
      notes: '[A6] not installed on this machine; path from research, UNVERIFIED.',
    },
    {
      id: 'cursor',
      label: 'Cursor',
      command: null,
      homeMarker: '.cursor',
      projectSkillsDir: '.agents/skills',
      verified: true,
      notes: 'detection only; no adapter (no CLI).',
    },
    {
      id: 'copilot',
      label: 'Copilot',
      command: null,
      homeMarker: '.copilot',
      projectSkillsDir: '.agents/skills',
      verified: true,
      notes: 'detection only; no adapter (no CLI).',
    },
  ].map((row) => Object.freeze(row)),
);

/**
 * @param {string} id
 * @returns {HarnessRow}
 * @throws {RangeError} when `id` names no row in {@link HARNESSES}.
 */
export function getHarness(id) {
  const row = HARNESSES.find((h) => h.id === id);
  if (!row) {
    throw new RangeError(`install: unknown harness "${id}"`);
  }
  return row;
}

/**
 * @param {HarnessRow} harness
 * @param {string} projectRoot - absolute path to the project.
 * @param {string} [skillName]
 * @returns {string} absolute path where the skill would be installed inside the project.
 */
export function projectSkillPath(harness, projectRoot, skillName = 'code-forge') {
  return path.join(projectRoot, harness.projectSkillsDir, skillName);
}

/**
 * @param {HarnessRow} harness
 * @param {string} home - absolute path to `$HOME`.
 * @param {string} [skillName]
 * @returns {string} absolute path where the skill would be installed globally, under
 *   `<home>/<homeMarker>/skills/<skillName>`.
 */
export function globalSkillPath(harness, home, skillName = 'code-forge') {
  return path.join(home, harness.homeMarker, 'skills', skillName);
}
