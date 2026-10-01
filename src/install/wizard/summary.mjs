/**
 * The "Project settings (detected)" block `code-forge init` prints instead of asking for gates and
 * proof settings (B24). Every gate and proof value is listed with where it came from; a blank value
 * says so and why, and the block ends with the one line that says how to fill the blanks later.
 * Agent/JSON mode gets the same facts as `settings` + `blank`. Pure apart from `existsSync` reads
 * that name the file a detected gate came from.
 */

import { existsSync } from 'node:fs';
import path from 'node:path';
import { LINK_DIR_MANIFESTS, copyCandidates, linkDirsFromManifests } from './answers.mjs';
import { GATE_NAMES } from './flags.mjs';

export const SUMMARY_TITLE = 'Project settings (detected)';

/** The config key each summarised value is written to. */
export const SETTING_KEYS = Object.freeze({
  test: 'gates.test',
  lint: 'gates.lint',
  types: 'gates.types',
  format: 'gates.format',
  high: 'proof.tiers.high.paths',
  isolation: 'proof.isolation',
  link_dirs: 'proof.export.link_dirs',
  copy_untracked: 'proof.export.copy_untracked',
});

/** What a blank gate means, in the words the summary prints. */
const GATE_MISSING = Object.freeze({
  test: 'no test command found',
  lint: 'no linter found',
  types: 'no type checker found',
  format: 'no formatter found',
});

/** The summary's label for each proof value. */
const PROOF_LABELS = Object.freeze({
  high: 'high-risk paths',
  isolation: 'isolation',
  link_dirs: 'link dirs',
  copy_untracked: 'copy untracked',
});

/** The marker file that decides each non-Node stack in `detectGates`. */
const STACK_MARKER = Object.freeze({ rust: 'Cargo.toml', python: 'pyproject.toml', go: 'go.mod' });

/**
 * @typedef {object} Setting
 * @property {unknown} value - what is written to the config.
 * @property {'flag'|'existing'|'detected'|'default'} source
 * @property {string} note - where it came from, or why it is blank, in the summary's words.
 */

/**
 * @typedef {object} SettingsSummary
 * @property {Record<string, Setting>} settings - keyed by config path (`gates.test`, …).
 * @property {string[]} blank - the config paths left blank, in summary order.
 * @property {string[]} lines - the printed block, title first, the "set later" line last.
 */

/** @param {string[]} argv @returns {string} */
function shown(argv) {
  return argv.map((w) => (/[\s"'\\]/.test(w) || w === '' ? JSON.stringify(w) : w)).join(' ');
}

/** @param {string} cwd @param {string[]} names @returns {string|undefined} */
function firstPresent(cwd, names) {
  return names.find((n) => existsSync(path.join(cwd, n)));
}

/**
 * The file a detected gate came from (the evidence `detectGates` read).
 * @param {string} stack @param {string} name @param {string[]} argv @param {string} cwd
 * @returns {string}
 */
export function gateEvidence(stack, name, argv, cwd) {
  if (stack.startsWith('php')) {
    const tool = path.basename(argv[0] ?? '');
    if (tool === 'phpstan') return firstPresent(cwd, ['phpstan.neon', 'phpstan.neon.dist']) ?? 'vendor/bin/phpstan';
    if (tool === 'pint') return firstPresent(cwd, ['pint.json']) ?? 'vendor/bin/pint';
    if (tool === 'php' && argv[1] === 'artisan') return 'artisan';
    if (tool === 'pest') return 'vendor/bin/pest';
    return 'composer.json';
  }
  if (stack === 'node') {
    if (argv[0] === 'npx' && argv[1] === 'tsc') return 'tsconfig.json';
    if (argv[0] === 'npx' && argv[1] === 'prettier') return firstPresent(cwd, ['.prettierrc', '.prettierrc.json', '.prettierrc.js']) ?? 'package.json';
    return 'package.json';
  }
  return STACK_MARKER[/** @type {keyof typeof STACK_MARKER} */ (stack)] ?? 'the project';
}

/** @param {unknown} value @returns {boolean} an empty proof value: null, undefined, '' or [] */
function isBlankValue(value) {
  return value === null || value === undefined || value === '' || (Array.isArray(value) && value.length === 0);
}

/** @param {'flag'|'existing'|'detected'|'default'} source @param {string} flag @returns {string} */
function keptFrom(source, flag) {
  return source === 'flag' ? flag : source === 'existing' ? '.code-forge.yml' : 'default';
}

/**
 * @param {import('./answers.mjs').Answers} values
 * @param {import('./answers.mjs').Origins} origins
 * @param {{cwd: string, profile: string, detected: {stack: string}}} ctx
 * @returns {SettingsSummary}
 */
export function summarizeSettings(values, origins, ctx) {
  /** @type {Record<string, Setting>} */
  const settings = {};
  /** @type {string[]} */
  const blank = [];
  const lines = [SUMMARY_TITLE];

  for (const name of GATE_NAMES) {
    const key = SETTING_KEYS[/** @type {keyof typeof SETTING_KEYS} */ (name)];
    const argv = values.gates[/** @type {keyof typeof values.gates} */ (name)];
    const source = origins.gates[name] ?? 'detected';
    let note;
    if (argv === null || argv === undefined || argv.length === 0) {
      if (source === 'flag') note = 'set blank by --gate';
      else if (source === 'existing') note = 'empty in .code-forge.yml';
      else note = GATE_MISSING[/** @type {keyof typeof GATE_MISSING} */ (name)];
      blank.push(key);
      lines.push(`  ${name}: blank — ${note}`);
    } else {
      note = source === 'detected' ? gateEvidence(ctx.detected.stack, name, argv, ctx.cwd) : keptFrom(source, '--gate');
      lines.push(`  ${name}: ${shown(argv)} (from ${note})`);
    }
    settings[key] = { value: argv, source, note };
  }

  const found = new Map(linkDirsFromManifests(ctx.cwd).map((e) => [e.dir, e.manifests]));
  for (const pk of /** @type {Array<keyof typeof PROOF_LABELS>} */ (Object.keys(PROOF_LABELS))) {
    const key = SETTING_KEYS[pk];
    const value = values.proof[pk];
    const source = origins.proof[pk] ?? 'default';
    const label = PROOF_LABELS[pk];
    let note;
    if (isBlankValue(value)) {
      if (source === 'flag') note = 'set blank by --proof';
      else if (source === 'existing') note = 'empty in .code-forge.yml';
      else if (pk === 'high') note = 'none set (all files light tier by path)';
      else if (pk === 'link_dirs') note = 'no dependency manifest found';
      else {
        const looked = copyCandidates(ctx.profile);
        note = looked.length > 0 ? `none found (looked for ${looked.join(', ')})` : 'none for this stack';
      }
      blank.push(key);
      lines.push(`  ${label}: blank — ${note}`);
    } else {
      const list = Array.isArray(value) ? value : [String(value)];
      // copied files are evidence themselves; everything else names where it came from
      const foundOnDisk = source === 'detected' && pk === 'copy_untracked';
      if (foundOnDisk) note = 'found in the project';
      else if (source !== 'detected') note = keptFrom(source, '--proof');
      else note = [...new Set(list.flatMap((d) => found.get(d) ?? LINK_DIR_MANIFESTS[/** @type {keyof typeof LINK_DIR_MANIFESTS} */ (d)] ?? []))].join(', ');
      lines.push(`  ${label}: ${list.join(', ')} (${foundOnDisk ? note : `from ${note}`})`);
    }
    settings[key] = { value, source, note };
  }

  lines.push(blank.length > 0
    ? `Set the blanks later: edit .code-forge.yml (${blank.join(', ')}), then run \`code-forge validate\`.`
    : 'Change any of these later: edit .code-forge.yml, then run `code-forge validate`.');
  return { settings, blank, lines };
}
