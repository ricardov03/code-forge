/**
 * The local error log (B27): one JSON line per failed verb in `~/.code-forge/logs/errors.jsonl`.
 *
 * `bin/code-forge.mjs` calls {@link logVerbFailure} after a verb exits non-zero or throws. Every
 * entry holds the verb, its subcommand word, its flag NAMES (never values, never positionals), the
 * exit code, a kind, the last stderr text the verb wrote and (for a crash) the stack — redacted
 * with `redact` and then scrubbed: the home dir, the working dir, the project slug, emails, op://
 * references and 1Password item IDs are replaced by placeholders. Each entry records how many
 * replacements each rule made (`cleaned`), so `logs report` can say what was cleaned.
 *
 * Logging never throws and never changes the verb's exit code. `CODE_FORGE_NO_ERROR_LOG=1` turns
 * it off; no HOME means no log. The file is cut to its newest half when it grows over 1 MB.
 */

import { readFileSync, realpathSync } from 'node:fs';
import { appendFile, mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { redact } from './redact.mjs';

export const ERROR_LOG_MAX_BYTES = 1024 * 1024;
export const MESSAGE_MAX_BYTES = 2048;
export const STACK_MAX_BYTES = 4096;
export const NO_ERROR_LOG_ENV = 'CODE_FORGE_NO_ERROR_LOG';

const PACKAGE_JSON = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'package.json');

/**
 * The subcommand words each verb knows; the first positional is logged as `sub` only when it is
 * one of these (anything else could be a user value: a path, an id, a name).
 * @type {Readonly<Record<string, readonly string[]>>}
 */
export const SUBCOMMANDS = Object.freeze({
  block: ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive'],
  gates: ['detect', 'run', 'secret-scan', 'safe-edit', 'scope', 'acceptance', 'transcript-grep'],
  jev: ['ask'],
  keys: ['list', 'set', 'test', 'remove'],
  ledger: ['calibration', 'outcome', 'tail'],
  logs: ['summary', 'clear', 'path', 'report'],
  plan: ['check'],
  proof: ['tier', 'export', 'lock', 'unlock', 'restore', 'red-green'],
  run: ['start', 'status', 'end'],
  tools: ['install'],
});

/**
 * The scrub rules, in the order a summary lists them. `label` is singular and plural.
 * @type {ReadonlyArray<{rule: string, one: string, many: string}>}
 */
export const SCRUB_RULES = Object.freeze([
  { rule: 'home', one: 'home path', many: 'home paths' },
  { rule: 'project_path', one: 'project path', many: 'project paths' },
  { rule: 'project_name', one: 'project name', many: 'project names' },
  { rule: 'email', one: 'email', many: 'emails' },
  { rule: 'op_ref', one: '1Password reference', many: '1Password references' },
  { rule: 'item_id', one: '1Password item ID', many: '1Password item IDs' },
]);

/** @typedef {{rule: string, count: number}} CleanCount */
/** @typedef {{home?: string|null, cwd?: string|null, slug?: string|null}} ScrubContext */

/**
 * @typedef {object} ErrorEntry
 * @property {string} ts
 * @property {string} version
 * @property {string} node
 * @property {string} platform
 * @property {string} arch
 * @property {string} verb
 * @property {string|null} sub
 * @property {string[]} flags
 * @property {number} exit
 * @property {string} kind
 * @property {string|null} message
 * @property {string|null} stack
 * @property {CleanCount[]} cleaned
 */

/** @param {string} s */
function escapeRegExp(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * A path and its real path (macOS `/var` → `/private/var`), longest first; a path shorter than 2
 * characters (`/`) is never scrubbed.
 * @param {string|null|undefined} p
 * @returns {string[]}
 */
function pathVariants(p) {
  if (typeof p !== 'string' || p.length < 2) return [];
  const out = new Set([p.replace(/[/\\]+$/, '')]);
  try {
    out.add(realpathSync(p));
  } catch {
    // not on disk: the given form only
  }
  return [...out].filter((v) => v.length >= 2).sort((a, b) => b.length - a.length);
}

/**
 * A scrubber for one context. `scrub(text)` replaces, in this order: op:// references, the working
 * dir (when it is not the home dir), the home dir, emails, the project slug (as a whole word),
 * 1Password item IDs (26 lowercase letters and digits). `counts()` gives the replacements made so
 * far per rule, in {@link SCRUB_RULES} order, rules with 0 left out. A later pass can add its own
 * `{rule, count}` items to the same list.
 * @param {ScrubContext} [ctx]
 */
export function createScrubber(ctx = {}) {
  const homes = pathVariants(ctx.home);
  const cwds = pathVariants(ctx.cwd).filter((c) => !homes.includes(c));
  /** @type {Array<[string, string, string]>} every path form, longest first: the most specific one wins */
  const paths = [
    ...cwds.map((c) => /** @type {[string, string, string]} */ (['project_path', c, '<project>'])),
    ...homes.map((h) => /** @type {[string, string, string]} */ (['home', h, '~'])),
  ].sort((a, b) => b[1].length - a[1].length);
  const slug = typeof ctx.slug === 'string' && ctx.slug.length > 0 ? ctx.slug : null;
  /** @type {Array<[string, RegExp, string]>} */
  const steps = [['op_ref', /op:\/\/(?:[^/\r\n"'`<>]+\/[^/\r\n"'`<>]+\/)?[^\s"'`<>)\]]+/g, 'op://<ref>']];
  // a whole path name: `/Users/bob` never eats the start of `/Users/bobby`, but is replaced before
  // any other character (`/`, `.`, `)`, `,`, a backtick, the end)
  for (const [rule, p, to] of paths) steps.push([rule, new RegExp(`${escapeRegExp(p)}(?![A-Za-z0-9_\\-])`, 'g'), to]);
  steps.push(['email', /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/g, '<email>']);
  // a whole word, but never the scheme of a `scheme://` (`op://<ref>` with the slug "op")
  if (slug) steps.push(['project_name', new RegExp(`(?<![A-Za-z0-9_<-])${escapeRegExp(slug)}(?![A-Za-z0-9_>-])(?!:\\/\\/)`, 'g'), '<slug>']);
  steps.push(['item_id', /(?<![A-Za-z0-9])[a-z0-9]{26}(?![A-Za-z0-9])/g, '<item-id>']);

  /** @type {Map<string, number>} */
  const tally = new Map();
  return {
    /** @param {string} text @returns {string} */
    scrub(text) {
      let out = text;
      for (const [rule, re, to] of steps) {
        out = out.replace(re, () => {
          tally.set(rule, (tally.get(rule) ?? 0) + 1);
          return to;
        });
      }
      return out;
    },
    /** @returns {CleanCount[]} */
    counts() {
      return SCRUB_RULES.filter((r) => (tally.get(r.rule) ?? 0) > 0).map((r) => ({ rule: r.rule, count: /** @type {number} */ (tally.get(r.rule)) }));
    },
  };
}

/**
 * Add up `{rule, count}` lists; rules keep the order they first appear in (SCRUB_RULES first).
 * @param {CleanCount[][]} lists
 * @returns {CleanCount[]}
 */
export function mergeCounts(lists) {
  /** @type {Map<string, number>} */
  const total = new Map(SCRUB_RULES.map((r) => [r.rule, 0]));
  for (const list of lists) {
    for (const item of Array.isArray(list) ? list : []) {
      if (typeof item?.rule === 'string' && Number.isSafeInteger(item.count) && item.count > 0) {
        total.set(item.rule, (total.get(item.rule) ?? 0) + item.count);
      }
    }
  }
  return [...total].filter(([, n]) => n > 0).map(([rule, count]) => ({ rule, count }));
}

/**
 * "Cleaned: 2 home paths, 1 email." / "Cleaned: nothing needed cleaning."
 * @param {CleanCount[]} counts
 * @returns {string}
 */
export function describeCounts(counts) {
  const parts = counts.filter((c) => c.count > 0).map((c) => {
    const r = SCRUB_RULES.find((x) => x.rule === c.rule);
    const label = r ? (c.count === 1 ? r.one : r.many) : c.rule.replace(/_/g, ' ');
    return `${c.count} ${label}`;
  });
  return parts.length > 0 ? `Cleaned: ${parts.join(', ')}.` : 'Cleaned: nothing needed cleaning.';
}

/**
 * @param {string|null|undefined} home
 * @returns {string|null} the log path, or null without a home.
 */
export function errorLogPath(home) {
  return typeof home === 'string' && home.length > 0 ? path.join(home, '.code-forge', 'logs', 'errors.jsonl') : null;
}

/**
 * Flag names from argv; values and positionals are dropped. `--name` is cut at `=`; a short flag
 * is `-` plus its first letter (`-pSECRET` → `-p`); nothing after a bare `--` is read. A
 * single-dash argument right after a `--name` without `=` may be that flag's value (`--msg -Hi`),
 * so it is dropped (when unsure, leave it out).
 * @param {string[]} args
 * @returns {string[]}
 */
export function flagNames(args) {
  const out = [];
  let afterLong = false;
  for (const arg of args) {
    if (typeof arg !== 'string') {
      afterLong = false;
      continue;
    }
    if (arg === '--') break;
    const long = /^--([A-Za-z][A-Za-z0-9-]{0,39})(=|$)/.exec(arg);
    if (long) {
      out.push(`--${long[1]}`);
      afterLong = long[2] === '';
      continue;
    }
    const short = /^-([A-Za-z])/.exec(arg);
    if (short && !afterLong) out.push(`-${short[1]}`);
    afterLong = false;
  }
  return out;
}

/**
 * @param {string} verb @param {string[]} args
 * @returns {string|null} the first positional when it is a known subcommand word of `verb`.
 */
export function subcommandOf(verb, args) {
  const first = args.find((a) => typeof a === 'string' && !a.startsWith('-'));
  const known = Object.hasOwn(SUBCOMMANDS, verb) ? SUBCOMMANDS[verb] : [];
  return first !== undefined && known.includes(first) ? first : null;
}

/**
 * The first `max` bytes of `text`, never ending inside a character.
 * @param {string} text @param {number} max
 * @returns {string}
 */
export function capBytes(text, max) {
  const buf = Buffer.from(text, 'utf8');
  if (buf.length <= max) return text;
  // back off to the start of a character: never cut inside one (only when the text is cut)
  let end = max;
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end -= 1;
  return buf.subarray(0, end).toString('utf8');
}

/**
 * `project.slug` from `<cwd>/.code-forge.yml`, or null.
 * @param {string} cwd
 * @returns {Promise<string|null>}
 */
export async function readProjectSlug(cwd) {
  try {
    const text = await readFile(path.join(cwd, '.code-forge.yml'), 'utf8');
    // imported here: the router must still start where the yaml package is absent
    const YAML = await import('yaml');
    const slug = YAML.parse(text)?.project?.slug;
    return typeof slug === 'string' && slug.length > 0 ? slug : null;
  } catch {
    return null;
  }
}

/** @returns {string} this package's version, or `unknown`. */
export function packageVersion() {
  try {
    const v = JSON.parse(readFileSync(PACKAGE_JSON, 'utf8')).version;
    return typeof v === 'string' ? v : 'unknown';
  } catch {
    return 'unknown';
  }
}

/**
 * Record what is written to `stream` until `stop()`; every write still reaches the stream.
 * @param {NodeJS.WritableStream} stream
 * @returns {{stop: () => string|null}} `stop()` restores `write` and gives the last non-blank text.
 */
export function captureStderr(stream) {
  const hadOwn = Object.hasOwn(stream, 'write');
  const original = stream.write;
  /** @type {string|null} */
  let last = null;
  /** @param {any} chunk @param {...any} rest */
  const wrapper = function (chunk, ...rest) {
    try {
      const text = typeof chunk === 'string' ? chunk : chunk instanceof Uint8Array ? Buffer.from(chunk).toString('utf8') : '';
      if (text.trim().length > 0) last = text;
    } catch {
      // recording never breaks a write
    }
    return original.call(stream, chunk, ...rest);
  };
  stream.write = /** @type {any} */ (wrapper);
  return {
    stop() {
      if (stream.write === wrapper) {
        if (hadOwn) stream.write = original;
        else delete (/** @type {any} */ (stream)).write;
      }
      return last;
    },
  };
}

/**
 * Append one line, then cut the file to its newest half when it is over `maxBytes`.
 * @param {string} file @param {string} line - one JSON text, no newline.
 * @param {number} [maxBytes]
 */
export async function appendLine(file, line, maxBytes = ERROR_LOG_MAX_BYTES) {
  await mkdir(path.dirname(file), { recursive: true, mode: 0o700 });
  await appendFile(file, `${line}\n`, { encoding: 'utf8', mode: 0o600 });
  const { size } = await stat(file);
  if (size <= maxBytes) return;
  const lines = (await readFile(file, 'utf8')).split('\n').filter((l) => l.length > 0);
  const budget = Math.floor(size / 2);
  const kept = [];
  let used = 0;
  for (let i = lines.length - 1; i >= 0; i -= 1) {
    const bytes = Buffer.byteLength(lines[i], 'utf8') + 1;
    if (kept.length > 0 && used + bytes > budget) break;
    kept.unshift(lines[i]);
    used += bytes;
  }
  // Accepted race: a line another code-forge process appends between the read above and the
  // rename below is lost. Failing verbs are rare and the log is best effort.
  const tmp = `${file}.tmp-${process.pid}`;
  try {
    await writeFile(tmp, `${kept.join('\n')}\n`, { encoding: 'utf8', mode: 0o600 });
    await rename(tmp, file);
  } catch (err) {
    await rm(tmp, { force: true }).catch(() => {});
    throw err;
  }
}

/**
 * @typedef {object} FailureInput
 * @property {string} verb
 * @property {string[]} args
 * @property {number} exit
 * @property {string|null} [kind] - the kind the verb reported, if any.
 * @property {boolean} [threw] - whether the verb threw (a thrown `undefined` is a crash too).
 * @property {unknown} [thrown] - what the verb threw.
 * @property {string|null} [stderrText] - the last stderr text the verb wrote.
 * @property {NodeJS.ProcessEnv} [env]
 * @property {string} [cwd]
 * @property {Date} [now]
 */

const KIND_WORD = /^[a-z][a-z0-9_]{0,39}$/;

/**
 * @param {() => unknown} get
 * @returns {string} `String(get())`, or `<unprintable error>` when reading or printing it throws.
 */
function safeString(get) {
  try {
    return String(get());
  } catch {
    return '<unprintable error>';
  }
}

/**
 * Build the scrubbed entry (no I/O except reading the project slug).
 * @param {FailureInput} input
 * @returns {Promise<ErrorEntry>}
 */
export async function buildEntry(input) {
  const env = input.env ?? process.env;
  const cwd = input.cwd ?? process.cwd();
  const crashed = input.threw === true;
  const scrubber = createScrubber({ home: env.HOME ?? null, cwd, slug: await readProjectSlug(cwd) });
  const clean = (/** @type {string} */ s) => scrubber.scrub(redact(s));
  // a verb-reported kind must be a short snake_case word; anything else is logged as `error`
  const reported = input.kind === undefined || input.kind === null ? null : typeof input.kind === 'string' && KIND_WORD.test(input.kind) ? input.kind : 'error';
  const kind = crashed ? 'crash' : reported ?? (input.exit === 2 ? 'usage' : 'error');
  /** @type {any} */
  const err = input.thrown;
  const rawMessage = crashed ? safeString(() => err?.message ?? err) : input.stderrText ?? null;
  let rawStack = null;
  try {
    rawStack = crashed && typeof err?.stack === 'string' ? err.stack : null;
  } catch {
    // a throwing `stack` getter: no stack
  }
  const message = rawMessage === null ? null : capBytes(clean(rawMessage.trim()), MESSAGE_MAX_BYTES);
  const stack = rawStack === null ? null : capBytes(clean(rawStack), STACK_MAX_BYTES);
  const flags = flagNames(input.args).map(clean);
  const sub = subcommandOf(input.verb, input.args);
  return {
    ts: (input.now ?? new Date()).toISOString(),
    version: packageVersion(),
    node: process.version,
    platform: process.platform,
    arch: process.arch,
    verb: clean(input.verb),
    sub,
    flags,
    exit: input.exit,
    kind,
    message,
    stack,
    cleaned: scrubber.counts(),
  };
}

/**
 * Log one failed verb. Never throws; returns whether a line was written.
 * @param {FailureInput} input
 * @returns {Promise<boolean>}
 */
export async function logVerbFailure(input) {
  try {
    if (input.exit === 0 && input.threw !== true) return false;
    const env = input.env ?? process.env;
    const flag = env[NO_ERROR_LOG_ENV];
    if (typeof flag === 'string' && flag !== '' && flag !== '0') return false;
    const file = errorLogPath(env.HOME);
    if (file === null) return false;
    const entry = await buildEntry(input);
    await appendLine(file, JSON.stringify(entry));
    return true;
  } catch {
    return false;
  }
}

/**
 * Every well-formed entry, oldest first (lines that do not parse are skipped).
 * @param {string|null} file
 * @returns {Promise<ErrorEntry[]|null>} null when there is no log file.
 */
export async function readErrorLog(file) {
  if (file === null) return null;
  let text;
  try {
    text = await readFile(file, 'utf8');
  } catch {
    return null;
  }
  /** @type {ErrorEntry[]} */
  const out = [];
  for (const line of text.split('\n')) {
    if (line.trim().length === 0) continue;
    try {
      const e = JSON.parse(line);
      if (e && typeof e === 'object' && typeof e.ts === 'string' && typeof e.verb === 'string') out.push(e);
    } catch {
      // a torn line: skipped
    }
  }
  return out;
}
