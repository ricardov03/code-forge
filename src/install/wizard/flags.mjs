/**
 * `code-forge init` flags (plan §2 table, block B13a). Every answer flag overrides exactly one
 * wizard answer (`ANSWER_FLAGS` maps flag -> answer key); `--no-interaction` is a mode switch and
 * overrides no answer.
 *
 * **Q16 = cut (Ricardo, 2026-09-25):** step 8 asks no mutation-tool or nightly question, so the
 * v1.2 `--no-nightly` flag is gone and `--proof` takes only the keys the schema still has
 * (`isolation`, `high`, `link_dirs`, `copy_untracked`). With `-g` and `-p` counted as two flags
 * (two tokens that each set the scope answer) there are 19 answer flags.
 *
 * Parsing is strict: an unknown flag, a missing value, a repeated single-value flag or a bad value
 * is a usage error. Error messages name the flag, never the value given (a user can paste a key
 * into any flag by mistake).
 */

import { isOpInput } from '../../keys/onepassword.mjs';
import { HARNESSES } from '../harnesses.mjs';

export const PROVIDERS = Object.freeze(['anthropic', 'openai', 'xai']);
/** The engines the wizard offers. `subprocess` is never proposed (R2): a power user types it. */
export const ENGINE_CHOICES = Object.freeze(['auto', 'solo', 'harness']);
export const GATE_NAMES = Object.freeze(['test', 'lint', 'types', 'format']);
export const PROOF_KEYS = Object.freeze(['isolation', 'high', 'link_dirs', 'copy_untracked']);
/** Tools step 1 knows how to detect (and, for some, install after a per-tool yes). */
export const TOOL_IDS = Object.freeze(['solo', 'codex', 'grok', 'gemini', 'op']);

/** flag (as typed) -> the one answer key it overrides. */
export const ANSWER_FLAGS = Object.freeze({
  '--tools': 'tools',
  '--yes-tool': 'yes_tools',
  '--harness': 'harnesses',
  '-g': 'scope',
  '-p': 'scope',
  '--copy': 'method',
  '--provider': 'provider',
  '--level': 'levels',
  '--refresh-models': 'refresh_models',
  '--multimodel': 'multimodel',
  '--second-provider': 'second_provider',
  '--jev-ref': 'jev',
  '--jev-env': 'jev',
  '--no-jev': 'jev',
  '--engine': 'engine',
  '--solo-project': 'solo_project',
  '--gate': 'gates',
  '--proof': 'proof',
  '--skip-doctor': 'doctor',
});

const BOOLEAN_FLAGS = new Set(['-g', '-p', '--copy', '--refresh-models', '--no-jev', '--skip-doctor', '--no-interaction']);
const REPEATABLE = new Set(['--yes-tool', '--level', '--gate', '--proof']);
const LEVEL_NAMES = new Set(['L0', 'L1', 'L2', 'L3']);
const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

export class UsageError extends Error {}

/**
 * Split a gate command into argv the way a shell would split words (single/double quotes,
 * backslash escapes) — without ever running a shell.
 * @param {string} text
 * @returns {string[]}
 */
export function shellWords(text) {
  const words = [];
  let current = '';
  let has = false;
  /** @type {string|null} */
  let quote = null;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (quote === "'") {
      if (ch === "'") quote = null;
      else current += ch;
    } else if (ch === '\\' && i + 1 < text.length && quote !== "'") {
      i += 1;
      current += text[i];
      has = true;
    } else if (quote === '"') {
      if (ch === '"') quote = null;
      else current += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has) words.push(current);
      current = '';
      has = false;
    } else {
      current += ch;
      has = true;
    }
  }
  if (quote !== null) throw new UsageError('--gate: unterminated quote');
  if (has) words.push(current);
  return words;
}

/** @param {string} flag @param {string} value @returns {[string, string]} */
function keyValue(flag, value) {
  const eq = value.indexOf('=');
  if (eq <= 0) throw new UsageError(`${flag} expects <key>=<value>`);
  return [value.slice(0, eq), value.slice(eq + 1)];
}

/** @param {string} value @returns {string[]} */
function csv(value) {
  return value.split(',').map((s) => s.trim()).filter((s) => s.length > 0);
}

/**
 * `L1=model[:effort][@provider]`
 * @param {string} value
 * @returns {{level: string, spec: {model: string, effort?: string, provider?: string}}}
 */
export function parseLevel(value) {
  const [level, rest] = keyValue('--level', value);
  if (!LEVEL_NAMES.has(level)) throw new UsageError('--level: level must be L0, L1, L2 or L3');
  let body = rest;
  /** @type {{model: string, effort?: string, provider?: string}} */
  const spec = { model: '' };
  const at = body.lastIndexOf('@');
  if (at >= 0) {
    const provider = body.slice(at + 1);
    if (!PROVIDERS.includes(provider)) throw new UsageError('--level: unknown provider');
    spec.provider = provider;
    body = body.slice(0, at);
  }
  const colon = body.lastIndexOf(':');
  if (colon >= 0) {
    const effort = body.slice(colon + 1);
    if (effort.length === 0) throw new UsageError('--level: empty effort');
    spec.effort = effort;
    body = body.slice(0, colon);
  }
  if (body.length === 0) throw new UsageError('--level: empty model');
  spec.model = body;
  // key order model, effort, provider — the order the schema documents
  return { level, spec: { model: spec.model, ...(spec.effort ? { effort: spec.effort } : {}), ...(spec.provider ? { provider: spec.provider } : {}) } };
}

/**
 * @param {string} flag @param {string} value
 * @returns {unknown} the typed value for that flag
 */
function convert(flag, value) {
  switch (flag) {
    case '--tools':
      if (value !== 'recommended' && value !== 'current') throw new UsageError('--tools must be recommended or current');
      return value;
    case '--yes-tool':
      if (!TOOL_IDS.includes(value)) throw new UsageError(`--yes-tool must be one of ${TOOL_IDS.join(', ')}`);
      return value;
    case '--harness': {
      const ids = csv(value);
      const known = HARNESSES.map((h) => h.id);
      if (ids.length === 0 || ids.some((id) => !known.includes(id))) throw new UsageError(`--harness takes a comma list of ${known.join(', ')}`);
      return [...new Set(ids)];
    }
    case '--provider':
    case '--second-provider':
      if (!PROVIDERS.includes(value)) throw new UsageError(`${flag} must be one of ${PROVIDERS.join(', ')}`);
      return value;
    case '--level':
      return parseLevel(value);
    case '--multimodel':
      if (value !== 'on' && value !== 'off') throw new UsageError('--multimodel must be on or off');
      return value === 'on';
    case '--jev-ref':
      // shape only here (parsing is sync); an item ID or link is resolved to op:// in run.mjs (B25)
      if (!isOpInput(value)) throw new UsageError('--jev-ref must be a 1Password item ID, item link or op://vault/item/field reference');
      return value.trim();
    case '--jev-env':
      if (!ENV_NAME.test(value)) throw new UsageError('--jev-env must be an environment variable name');
      return value;
    case '--engine':
      if (value === 'subprocess') {
        throw new UsageError('--engine: init never selects subprocess (R2); type "engine: subprocess" into .code-forge.yml yourself');
      }
      if (!ENGINE_CHOICES.includes(value)) throw new UsageError(`--engine must be one of ${ENGINE_CHOICES.join(', ')}`);
      return value;
    case '--solo-project':
      if (!/^[1-9]\d{0,9}$/.test(value)) throw new UsageError('--solo-project must be a positive integer');
      return Number(value);
    case '--gate': {
      const [name, cmd] = keyValue('--gate', value);
      if (!GATE_NAMES.includes(name)) throw new UsageError(`--gate: gate must be one of ${GATE_NAMES.join(', ')}`);
      if (cmd === 'none' || cmd === 'null') return { name, argv: null };
      const argv = shellWords(cmd);
      if (argv.length === 0) throw new UsageError('--gate: empty command');
      return { name, argv };
    }
    case '--proof': {
      const [key, raw] = keyValue('--proof', value);
      if (!PROOF_KEYS.includes(key)) throw new UsageError(`--proof: key must be one of ${PROOF_KEYS.join(', ')}`);
      if (key === 'isolation') {
        if (raw !== 'export' && raw !== 'lock') throw new UsageError('--proof isolation must be export or lock');
        return { key, value: raw };
      }
      return { key, value: csv(raw) };
    }
    default:
      throw new UsageError(`unknown flag ${flag}`);
  }
}

/**
 * @typedef {object} ParsedFlags
 * @property {boolean} noInteraction
 * @property {Array<{flag: string, value: unknown}>} given - answer flags in the order typed.
 */

/**
 * @param {string[]} args
 * @returns {ParsedFlags}
 * @throws {UsageError}
 */
export function parseInitArgs(args) {
  /** @type {ParsedFlags} */
  const out = { noInteraction: false, given: [] };
  const seen = new Set();
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    const eq = arg.startsWith('--') ? arg.indexOf('=') : -1;
    const flag = eq > 0 ? arg.slice(0, eq) : arg;
    if (flag !== '--no-interaction' && !Object.hasOwn(ANSWER_FLAGS, flag)) {
      throw new UsageError(arg.startsWith('-') ? `unknown flag ${flag}` : 'init takes no positional argument');
    }
    if (seen.has(flag) && !REPEATABLE.has(flag)) throw new UsageError(`${flag} given more than once`);
    seen.add(flag);
    if (BOOLEAN_FLAGS.has(flag)) {
      if (eq > 0) throw new UsageError(`${flag} takes no value`);
      if (flag === '--no-interaction') out.noInteraction = true;
      else out.given.push({ flag, value: flag === '--copy' ? 'copy' : flag === '-g' ? 'global' : flag === '-p' ? 'project' : true });
      continue;
    }
    let value;
    if (eq > 0) value = arg.slice(eq + 1);
    else {
      value = args[i + 1];
      i += 1;
    }
    if (typeof value !== 'string' || value.length === 0 || (eq < 0 && value.startsWith('-'))) throw new UsageError(`${flag} needs a value`);
    out.given.push({ flag, value: convert(flag, value) });
  }
  if (seen.has('-g') && seen.has('-p')) throw new UsageError('-g and -p are exclusive');
  const jev = ['--jev-ref', '--jev-env', '--no-jev'].filter((f) => seen.has(f));
  if (jev.length > 1) throw new UsageError(`${jev.join(' and ')} are exclusive`);
  return out;
}
