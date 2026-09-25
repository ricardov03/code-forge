/**
 * Flag parsing shared by the `run` and `block` verbs. Strict: every flag must be declared in the
 * spec — `values` (`--flag value` / `--flag=value`, once), `booleans` (no value), or `multi`
 * (every following token up to the next `--flag`, seeded by an inline `--flag=value`). An unknown
 * flag, a bare `--`, a repeated single-value flag, or a value on a boolean is a usage error, so a
 * typo never swallows the next token silently. Positionals are what is left.
 */

import { StateError } from './paths.mjs';

/**
 * @param {string[]} args
 * @param {{values?: string[], booleans?: string[], multi?: string[]}} [spec]
 * @returns {{flags: Record<string, string | string[] | true>, positionals: string[]}}
 * @throws {StateError} `usage`
 */
export function parseFlags(args, { values = [], booleans = [], multi = [] } = {}) {
  /** @type {Record<string, string | string[] | true>} */
  const flags = {};
  const positionals = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (!arg.startsWith('--')) {
      positionals.push(arg);
      continue;
    }
    const eq = arg.indexOf('=');
    const name = arg.slice(2, eq < 0 ? undefined : eq);
    const inline = eq < 0 ? undefined : arg.slice(eq + 1);
    if (!booleans.includes(name) && !multi.includes(name) && !values.includes(name)) {
      throw new StateError('usage', `unknown flag ${JSON.stringify(arg)}`);
    }
    if (Object.hasOwn(flags, name) && !multi.includes(name)) throw new StateError('usage', `--${name} given more than once`);
    if (booleans.includes(name)) {
      if (inline !== undefined) throw new StateError('usage', `--${name} takes no value`);
      flags[name] = true;
    } else if (multi.includes(name)) {
      const collected = Array.isArray(flags[name]) ? /** @type {string[]} */ (flags[name]) : [];
      if (inline !== undefined && inline.length > 0) collected.push(inline);
      while (i + 1 < args.length && !args[i + 1].startsWith('--')) collected.push(args[(i += 1)]);
      if (collected.length === 0) throw new StateError('usage', `--${name} needs at least one value`);
      flags[name] = collected;
    } else if (inline !== undefined) {
      if (inline.length === 0) throw new StateError('usage', `--${name} needs a value`);
      flags[name] = inline;
    } else {
      const next = args[i + 1];
      if (next === undefined || next.startsWith('--')) throw new StateError('usage', `--${name} needs a value`);
      flags[name] = next;
      i += 1;
    }
  }
  return { flags, positionals };
}

/**
 * A strictly positive safe integer written without sign or leading zeros.
 * @param {string | string[] | true | undefined} raw @param {string} name
 * @returns {number | undefined}
 * @throws {StateError} `usage`
 */
export function intFlag(raw, name) {
  if (raw === undefined) return undefined;
  const value = typeof raw === 'string' && /^[1-9]\d*$/.test(raw) ? Number(raw) : Number.NaN;
  if (!Number.isSafeInteger(value)) throw new StateError('usage', `--${name} must be a positive integer`);
  return value;
}
