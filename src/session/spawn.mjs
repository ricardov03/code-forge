/**
 * Isolated sessions: the spawner (plan §5.2, §5.6, §6.1, V3, V9; block B9a).
 *
 * `spawnSession(opts, deps)` resolves a level (`resolveLevel`, B1), builds the provider argv
 * (`buildArgv`, B4), and runs it through B0.1's `exec` — argv only, never a shell, with a timeout
 * that kills the child's process group, and a pid-registry entry for as long as the child lives.
 *
 * Builder contract (V3): when the build carries `stdinFile`, its CONTENT (bytes, unchanged) goes to
 * the child's stdin through `exec`'s `input` — closed-book prompts never sit in argv. Grok is the
 * exception by construction: its builder puts the packet in `--prompt-file` and returns no
 * `stdinFile`, so nothing is piped. When the build carries `outPath` (Codex, always), the answer is
 * read from it and the file is deleted. When the build carries `env` (the Codex coder's
 * `CODEX_HOME`, B4.1), it is merged over the child's environment; a foreground session removes
 * that home when the child exits.
 *
 * Retry ladder (§5.6, V9): step 0 is the level's own model; step `k + 1` is `fallback[k]`. For a
 * Claude coder, `fallback[0]` (same provider) rides on `--fallback-model` inside step 0 and is not
 * spawned again. A step that reports unavailability (CLI missing, login expired, HTTP 402, or
 * rate-limited twice in a row) hands over to the next step; any other failure is final.
 *
 * Every spawn prints `level=<Lx> provider=<p> model=<id> effort=<e> fallback_step=<n>` on stderr,
 * and every attempt writes one ledger row (`event: session`) with `tokens_source` `reported` when
 * the CLI's JSON carried usage, else `estimated` (bytes / 4).
 *
 * A coder argv that `isForbidden` matches is refused before anything spawns (§8.4).
 *
 * `background: true` (coders, §5.2): the child is started detached with stdout/stderr going to
 * `<run-root>/sessions/<id>/session.log`, its pid written to `<run-root>/sessions/<id>/pid.json`
 * and to the run root's pid registry; completion is the process exit plus the sentinel in the log.
 * `exec` cannot outlive its caller (it waits, and SIGTERMs live groups on the parent's exit), so
 * this one path uses `node:child_process.spawn` directly — argv array, `shell: false`.
 */

import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { closeSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { resolveLevel } from '../config/known-ids.mjs';
import { compileSchema } from '../config/schema-compile.mjs';
import { buildArgv } from '../engines/builders/index.mjs';
import { removeCodexHome } from '../engines/codex-home.mjs';
import { cliNameForProvider } from '../engines/provider-cli.mjs';
import { parseSentinel } from '../engines/sentinel.mjs';
import { parseUsage } from '../engines/usage-parse.mjs';
import { appendRow } from '../ledger/write.mjs';
import { exec } from '../util/exec.mjs';
import { isForbidden, mergeForbidden } from '../util/forbidden.mjs';
import { readStartTime, registerPid, UNKNOWN_START_TIME } from '../util/reaper.mjs';
import { redact, writeSafe } from '../util/redact.mjs';
import { currentRunRoot, pidsDir, runRoot, tmpBase, untrustedReason } from '../util/tmp.mjs';

export const ROLES = Object.freeze(['coder', 'reviewer', 'judge', 's2', 'author', 'facts']);
export const LEVELS = Object.freeze(['L0', 'L1', 'L2', 'L3']);

/** Default wall-clock cap of one foreground session. */
export const DEFAULT_TIMEOUT_MS = 15 * 60 * 1000;

/** Unavailability reasons that move the ladder to the next step (§5.6). */
export const UNAVAILABLE_REASONS = Object.freeze(['cli-missing', 'login-expired', 'http-402', 'rate-limited']);

/** An error the spawner raises before anything runs (`code`: `usage`, `forbidden`). */
export class SessionError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'SessionError';
    this.code = code;
  }
}

/**
 * The temp root of run `runId`: an existing trusted root is reused as it is (its owner record is
 * the run's, not this short-lived verb's); a missing one is created by `runRoot`.
 * @param {string} runId @param {string} [root] - the `tmp.root` config value.
 * @returns {string}
 */
export function runRootFor(runId, root) {
  if (typeof runId !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(runId)) {
    throw new SessionError('usage', 'a run id is one path segment of letters, digits, ".", "_" or "-"');
  }
  const dir = path.join(tmpBase(root), runId);
  if (existsSync(dir) && untrustedReason(dir) === null) return realpathSync(dir);
  return runRoot(runId, { root });
}

/** How much of a timed-out session's stderr is kept (B30). */
export const DIAGNOSTIC_TAIL_BYTES = 4096;

/** A timed-out session's stderr over this size keeps no text at all (bounds the redact cost). */
export const STDERR_MAX_BYTES = 1024 * 1024;

/** How many stdout event types a timed-out session keeps. */
export const STDOUT_EVENTS_MAX = 20;

/** The longest event type kept. */
const EVENT_TYPE_MAX = 40;

/**
 * Packet lines shorter than this are too common to tell apart from ordinary output, so they are
 * NOT filtered. Accepted risk (B30): a packet line under 16 characters (a brace, `return x;`) can
 * appear in a stderr tail; such a line carries no meaningful packet content on its own.
 */
const PACKET_LINE_MIN = 16;

/** The tail note when the packet cannot be read: no stderr text is kept at all (fail closed). */
export const TAIL_WITHHELD_PACKET = 'withheld: packet unreadable';
/** The tail note when stderr is over `STDERR_MAX_BYTES`. */
export const TAIL_WITHHELD_LARGE = 'stderr too large, withheld';
/** The tail note when the last 4 KB of stderr is one partial line. */
export const TAIL_WITHHELD_LONG_LINE = 'one long line, withheld';

/** @param {Buffer} buf @param {number} at @returns {number} the first character start at or after `at`. */
function charStart(buf, at) {
  let i = at;
  while (i < buf.length && (buf[i] & 0xc0) === 0x80) i += 1;
  return i;
}

/** @param {unknown} text @returns {string} */
function asText(text) {
  return Buffer.isBuffer(text) ? text.toString('utf8') : typeof text === 'string' ? text : '';
}

const PEM_BEGIN = /-----BEGIN [A-Z0-9 ]*-----/;
const PEM_END = /-----END [A-Z0-9 ]*-----/;

/**
 * The stderr tail of a timed-out session (B30), never packet content:
 *  1. no packet text (unreadable) ⇒ no text: `{tail: null, note: 'withheld: packet unreadable'}`;
 *     stderr over `STDERR_MAX_BYTES` ⇒ `{tail: null, note: 'stderr too large, withheld'}`;
 *  2. on the FULL raw text: every PEM block (`-----BEGIN` … `-----END` lines, an unterminated one
 *     to the end) and every line repeating a packet line (≥ 16 chars) is dropped;
 *  3. the FULL remaining text goes through B0 `redact`;
 *  4. over `maxBytes`, the last `maxBytes` bytes are kept, starting on a character, with the first
 *     (partial) line dropped; when that piece has no line break the tail is withheld.
 * @param {string | Buffer | null | undefined} text @param {string | null} packetText @param {number} [maxBytes]
 * @returns {{tail: string | null, note?: string}}
 */
export function stderrTail(text, packetText, maxBytes = DIAGNOSTIC_TAIL_BYTES) {
  if (typeof packetText !== 'string') return { tail: null, note: TAIL_WITHHELD_PACKET };
  const raw = asText(text);
  if (raw.length === 0) return { tail: '' };
  if (Buffer.byteLength(raw) > STDERR_MAX_BYTES) return { tail: null, note: TAIL_WITHHELD_LARGE };
  const packetLines = [
    ...new Set(
      packetText
        .split('\n')
        .map((l) => l.trim())
        .filter((l) => l.length >= PACKET_LINE_MIN),
    ),
  ];
  let inPem = false;
  /** @type {string[]} */
  const kept = [];
  for (const line of raw.split('\n')) {
    const t = line.trim();
    if (inPem) {
      if (PEM_END.test(t)) inPem = false;
      continue;
    }
    if (PEM_BEGIN.test(t)) {
      inPem = !PEM_END.test(t);
      continue;
    }
    if (t.length >= PACKET_LINE_MIN && packetLines.some((p) => t.includes(p))) continue;
    kept.push(line);
  }
  const safe = /** @type {string} */ (redact(kept.join('\n')));
  const buf = Buffer.from(safe, 'utf8');
  if (buf.length <= maxBytes) return { tail: safe };
  const from = charStart(buf, buf.length - maxBytes);
  const piece = buf.subarray(from).toString('utf8');
  if (buf[from - 1] === 0x0a) return { tail: piece }; // the cut fell exactly on a line start
  const nl = piece.indexOf('\n');
  if (nl === -1) return { tail: '', note: TAIL_WITHHELD_LONG_LINE };
  return { tail: piece.slice(nl + 1) };
}

/**
 * What a timed-out session's stdout says without any of its text (B30): the last
 * `STDOUT_EVENTS_MAX` event types (JSON lines with a string `type`, each cut to 40 characters and
 * kept only when it is all `[a-z0-9_.-]`) and the total byte length. Non-JSON stdout gives only
 * `stdout_bytes`. Event payloads, which can echo the packet, are never kept.
 * @param {string | Buffer | null | undefined} text
 * @returns {{stdout_bytes: number, stdout_events?: string[]}}
 */
export function stdoutEvents(text) {
  const raw = asText(text);
  const bytes = Buffer.byteLength(raw);
  /** @type {string[]} */
  const types = [];
  const lines = raw.split('\n');
  // scan from the end: stop once enough events are kept, or after a bounded number of lines
  for (let i = lines.length - 1, scanned = 0; i >= 0 && types.length < STDOUT_EVENTS_MAX && scanned < 5000; i -= 1, scanned += 1) {
    const line = lines[i].trim();
    if (!line.startsWith('{')) continue;
    const event = tryJSON(line);
    if (!event || typeof event !== 'object' || typeof event.type !== 'string') continue;
    const type = event.type.slice(0, EVENT_TYPE_MAX);
    if (/^[a-z0-9_.-]+$/.test(type)) types.unshift(type);
  }
  return types.length > 0 ? { stdout_bytes: bytes, stdout_events: types } : { stdout_bytes: bytes };
}

/** @param {number} bytes */
export const estimateTokens = (bytes) => Math.ceil(bytes / 4);

/** @param {string} file @returns {number} its size in bytes, 0 when unreadable. */
function fileSize(file) {
  try {
    return statSync(file).size;
  } catch {
    return 0;
  }
}

/** @param {unknown} text @returns {any} the parsed JSON value, or undefined. */
function tryJSON(text) {
  if (typeof text !== 'string' || text.trim().length === 0) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

/**
 * The spawn steps of a level: step 0 = the level itself, step k+1 = `fallback[k]`. For a Claude
 * coder whose `fallback[0]` is a different Claude model, that entry rides on `--fallback-model`
 * (V9) and is not a step of its own.
 * @param {{provider: string, model: string, effort?: string, fallback: ReadonlyArray<{provider: string, model: string, effort?: string}>}} resolved
 * @param {string} role
 * @returns {Array<{provider: string, model: string, effort?: string, fallback_step: number, flagFallback?: {provider: string, model: string}}>}
 */
export function ladderFor(resolved, role) {
  const [first] = resolved.fallback;
  const onFlag = role === 'coder' && resolved.provider === 'anthropic' && first?.provider === 'anthropic' && first.model !== resolved.model;
  const steps = [{ provider: resolved.provider, model: resolved.model, effort: resolved.effort, fallback_step: 0, ...(onFlag ? { flagFallback: first } : {}) }];
  resolved.fallback.forEach((entry, k) => {
    if (onFlag && k === 0) return;
    steps.push({ provider: entry.provider, model: entry.model, effort: entry.effort, fallback_step: k + 1 });
  });
  return steps;
}

/** HTTP statuses that mean "this model is unavailable" (§5.6). */
const STATUS_REASONS = Object.freeze({ 401: 'login-expired', 402: 'http-402', 403: 'login-expired', 429: 'rate-limited' });

/** @param {unknown} code @returns {string | null} */
function reasonForStatus(code) {
  const n = typeof code === 'string' && /^\d{3}$/.test(code) ? Number(code) : code;
  return Number.isInteger(n) && Object.hasOwn(STATUS_REASONS, /** @type {number} */ (n)) ? STATUS_REASONS[/** @type {401|402|403|429} */ (n)] : null;
}

/**
 * Why a failed attempt counts as "unavailable" (§5.6), or null. Structured fields first: Claude's
 * `api_error_status`, an `error.status`, a Codex error event's `unexpected status <code>` message.
 * The text fallback reads stderr only, and only lines that start with the CLI's own error prefix
 * (`Error:` / `API Error:`) — never stdout, where a bare `402` can be ordinary content.
 * @param {import('../util/exec.mjs').ExecResult} res
 * @param {any} parsed - the CLI's parsed JSON result object (Codex: its last error event), if any.
 * @returns {string | null}
 */
export function classifyUnavailable(res, parsed) {
  if (res.error && /ENOENT/.test(res.error)) return 'cli-missing';
  if (res.result === 'ok' && parsed?.is_error !== true) return null;
  for (const status of [parsed?.api_error_status, parsed?.error?.status]) {
    const reason = reasonForStatus(status);
    if (reason) return reason;
  }
  const message = typeof parsed?.message === 'string' ? parsed.message : typeof parsed?.error?.message === 'string' ? parsed.error.message : '';
  const codex = /^unexpected status (\d{3})\b/.exec(message);
  if (codex && reasonForStatus(codex[1])) return reasonForStatus(codex[1]);
  for (const raw of res.stderr.split('\n')) {
    const line = raw.trim();
    const coded = /^(?:API )?Error:\s*(?:HTTP\s+)?(\d{3})\b/.exec(line);
    if (coded && reasonForStatus(coded[1])) return reasonForStatus(coded[1]);
    if (/^(?:API )?Error:.*\b(?:not logged in|login (?:expired|required))/i.test(line)) return 'login-expired';
    if (/^(?:API )?Error:.*\brate.?limit/i.test(line)) return 'rate-limited';
  }
  return null;
}

/**
 * Read one CLI's answer and usage from its stdout (and `outPath` text for Codex).
 * @param {string} cli @param {string} stdout @param {string | null} outText
 * @returns {{parsed: any, text: string, answer: any, usage: import('../engines/usage-parse.mjs').UsageResult}}
 */
export function readAnswer(cli, stdout, outText) {
  if (cli === 'codex') {
    const events = stdout.split('\n').map(tryJSON).filter((e) => e && typeof e === 'object');
    const usageEvent = events.filter((e) => e.usage && typeof e.usage === 'object').at(-1);
    const errorEvent = events.filter((e) => e.type === 'error' || e.type === 'turn.failed').at(-1);
    const text = outText ?? '';
    return { parsed: errorEvent ?? null, text, answer: tryJSON(text), usage: parseUsage('codex', usageEvent ?? null) };
  }
  const parsed = tryJSON(stdout.trim());
  // Claude's envelope carries `result` + `structured_output`; Grok's carries `text` + `structuredOutput`.
  const text = typeof parsed?.result === 'string' ? parsed.result : typeof parsed?.text === 'string' ? parsed.text : stdout;
  const answer = parsed?.structured_output ?? parsed?.structuredOutput ?? tryJSON(text);
  return { parsed: parsed ?? null, text, answer, usage: parseUsage(/** @type {"claude"|"grok"} */ (cli), parsed ?? null) };
}

/**
 * @typedef {'strict' | 'plain'} SchemaVariant - `strict` is the OpenAI strict-mode compilation
 *   (every property required, optional ones nullable); `plain` is the source schema as given.
 */

/** @param {string} provider @returns {SchemaVariant} */
function variantFor(provider) {
  return provider === 'openai' ? 'strict' : 'plain';
}

/**
 * One compiled schema per (SOURCE schema object, variant), for the life of the source object, so
 * every attempt of every session hands the provider — and the validator — the same object.
 * @type {WeakMap<object, Map<SchemaVariant, Record<string, any>>>}
 */
const compiledSchemas = new WeakMap();

/**
 * @param {Record<string, any>} source @param {"anthropic" | "openai" | "xai"} provider
 * @returns {Record<string, any>} the cached compiled schema for `provider`'s variant.
 */
function compiledFor(source, provider) {
  const variant = variantFor(provider);
  let byVariant = compiledSchemas.get(source);
  if (byVariant === undefined) {
    byVariant = new Map();
    compiledSchemas.set(source, byVariant);
  }
  let compiled = byVariant.get(variant);
  if (compiled === undefined) {
    compiled = compileSchema(source, provider, { strict: variant === 'strict' });
    byVariant.set(variant, compiled);
  }
  return compiled;
}

/**
 * One validator per schema object, for the life of that object. Ajv caches by schema object and
 * registers `$id` process-wide, so a shared instance fed a fresh clone per attempt either throws
 * "schema with key or id … already exists" on the second session or grows without bound; each
 * schema object instead gets its own Ajv, compiled once from a copy without `$id`.
 * @type {WeakMap<object, import('ajv').ValidateFunction>}
 */
const validators = new WeakMap();

/** @param {Record<string, any>} schema @returns {import('ajv').ValidateFunction} */
function validatorFor(schema) {
  let validate = validators.get(schema);
  if (validate === undefined) {
    const body = { ...schema };
    delete body.$id;
    validate = new Ajv2020({ allErrors: true, strict: false }).compile(body);
    validators.set(schema, validate);
  }
  return validate;
}

/**
 * @param {Record<string, any>} schema - a stable object: the caller's source schema, or the
 *   cached compiled variant the provider was actually given — never a fresh clone per call.
 * @param {unknown} value
 * @returns {boolean} whether `value` is an object that validates against `schema`.
 */
export function matchesSchema(schema, value) {
  return value !== null && typeof value === 'object' && validatorFor(schema)(value) === true;
}

/**
 * A strict-variant answer carries `null` for every property the SOURCE schema left optional
 * (OpenAI strict mode has no optional fields, see `schema-compile.mjs`); restore the source shape
 * by dropping those nulls, recursively through `properties` and `items`. Mutates and returns
 * `value`; a `null` on a property the source requires is kept as the answer gave it.
 * @param {unknown} value @param {Record<string, any> | undefined} node - the source schema node.
 */
function dropStrictNulls(value, node) {
  if (!node || typeof node !== 'object' || value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) {
    if (node.items && typeof node.items === 'object' && !Array.isArray(node.items)) {
      for (const item of value) dropStrictNulls(item, node.items);
    }
    return value;
  }
  const props = node.properties;
  if (!props || typeof props !== 'object') return value;
  const required = new Set(Array.isArray(node.required) ? node.required : []);
  const obj = /** @type {Record<string, unknown>} */ (value);
  for (const key of Object.keys(obj)) {
    if (!Object.hasOwn(props, key)) continue;
    if (obj[key] === null && !required.has(key)) delete obj[key];
    else dropStrictNulls(obj[key], props[key]);
  }
  return value;
}

/** The child's environment: the caller's, minus code-forge's own and Jev variables (§8.2). */
function childEnv(/** @type {NodeJS.ProcessEnv} */ env) {
  /** @type {NodeJS.ProcessEnv} */
  const out = {};
  for (const [name, value] of Object.entries(env)) {
    if (/^(CODE_FORGE_|JEV_)/.test(name)) continue;
    out[name] = value;
  }
  return out;
}

/**
 * @typedef {object} SessionOpts
 * @property {Record<string, any>} cfg - the loaded project config (levels, provider).
 * @property {"L0"|"L1"|"L2"|"L3"} level
 * @property {"coder"|"reviewer"|"judge"|"s2"|"author"|"facts"} role
 * @property {string} promptPath - the brief (coder) or the packet (closed-book).
 * @property {string} [cwd] - coder: the project tree (default `process.cwd()`); facts (B9b.1): an
 *   existing read-only snapshot of the project to run in, inside `<runRoot>/facts/` or the
 *   `facts/` area of {@link factsTmpRoot}, and under no git work tree (anything else, `''`
 *   included, is a `usage` SessionError; default: a fresh empty directory); every
 *   other closed-book role ignores it and always runs in a fresh empty directory.
 * @property {Record<string, any>} [schema] - source answer schema; compiled per provider.
 * @property {string} [systemPromptText]
 * @property {number} [maxBudgetUsd]
 * @property {number} [timeoutMs]
 * @property {boolean} [background]
 * @property {string} [runRoot] - default `currentRunRoot()`.
 * @property {string} [run] @property {string} [block] - copied to the ledger row.
 * @property {string} [slug] - ledger project slug; no slug and no `writeRow` ⇒ no row.
 * @property {Record<string, any>} [rowExtra] - extra ledger fields (e.g. S2's `source`).
 * @property {ReadonlyArray<any>} [forbidden] - the list checked for coders (default `mergeForbidden()`:
 *   FORBIDDEN plus the coder-only entries, B4.2).
 */

/**
 * @typedef {object} SessionDeps
 * @property {Partial<Record<"claude"|"codex"|"grok", string>>} [bins] - replaces argv[0] (tests: the fakes).
 * @property {typeof exec} [exec]
 * @property {(row: Record<string, any>) => Promise<unknown>} [writeRow]
 * @property {{write: (s: string) => unknown}} [stderr]
 * @property {NodeJS.ProcessEnv} [env]
 */

/**
 * @typedef {{status: string, reason?: string | null, answer?: any, text?: string} & Record<string, any>} SessionResult -
 *   `status`: `ok`, `failed`, `timeout`, `invalid-output`, `unavailable` or `started` (background).
 */

/** @param {string} p @returns {string} `p` realpath'd when it exists, else resolved as given. */
function realOrResolved(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/** @param {string} p @returns {boolean} true when anything (a symlink included) is at `p`. */
function entryExists(p) {
  try {
    lstatSync(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * B9b.1: the nearest directory at or above `dir` (`dir` itself included, up to `/`) holding a
 * `.git` entry — a directory (a work tree), a file (a linked work tree or submodule) or any other
 * entry of that name — or null when no ancestor has one. Git and the harness's CLAUDE.md /
 * `.claude` discovery walk up exactly this way, so a facts snapshot below such a directory would
 * see the live repo.
 * @param {string} dir - an absolute, realpath'd directory.
 * @returns {string | null}
 */
export function gitWorkTreeAncestor(dir) {
  for (let d = dir; ; d = path.dirname(d)) {
    if (entryExists(path.join(d, '.git'))) return d;
    if (path.dirname(d) === d) return null;
  }
}

/**
 * B9b.1: the private root a facts snapshot goes to when the run root itself sits inside a git work
 * tree (`tmp.root` configured inside the project): `<os.tmpdir()>/code-forge/facts-<hash of the
 * run root>`, a B0.1 run root (0700, owned by this process, `owner.json`, swept once it dies).
 * `create: true` creates it through `runRoot` (trust checks included); otherwise it is only named.
 * @param {string} runRootDir
 * @param {{create?: boolean}} [opts]
 * @returns {string}
 */
export function factsTmpRoot(runRootDir, opts = {}) {
  const name = `facts-${createHash('sha256').update(realOrResolved(runRootDir)).digest('hex').slice(0, 16)}`;
  return opts.create ? runRoot(name) : path.join(tmpBase(), name);
}

/**
 * B9b.1, fail closed: the facts role's `cwd` override must be an existing directory inside a facts
 * snapshot area — `<runRoot>/facts/`, or the `facts/` area of the run root's private tmp root
 * ({@link factsTmpRoot}) when that root exists and is trusted (all realpath'd) — never the live
 * tree (`process.cwd()`) and never inside a git work tree: every ancestor up to `/` is walked and
 * any `.git` entry (a file or a directory) refuses, because git and the harness's CLAUDE.md /
 * `.claude` discovery would walk up into the real project.
 * @param {unknown} cwd @param {string} runRootDir
 */
function assertFactsCwd(cwd, runRootDir) {
  const refuse = (/** @type {string} */ why) => new SessionError('usage', `facts cwd refused: ${why}`);
  if (typeof cwd !== 'string' || cwd.length === 0) throw refuse('it must be a non-empty path');
  let real;
  try {
    real = realpathSync(path.resolve(cwd));
    if (!statSync(real).isDirectory()) throw refuse('it is not a directory');
  } catch (err) {
    if (err instanceof SessionError) throw err;
    throw refuse('it does not exist');
  }
  /** @type {string[]} */
  const areas = [];
  const tmpRoot = factsTmpRoot(runRootDir);
  for (const area of [path.join(runRootDir, 'facts'), ...(untrustedReason(tmpRoot) === null ? [path.join(tmpRoot, 'facts')] : [])]) {
    try {
      areas.push(realpathSync(area));
    } catch {
      // not built (yet): not an area
    }
  }
  if (areas.length === 0) throw refuse('the run root has no facts snapshot area');
  if (!areas.some((area) => real.startsWith(`${area}${path.sep}`))) throw refuse('it is outside the run root facts snapshot area');
  let live = null;
  try {
    live = realpathSync(process.cwd());
  } catch {
    // the live cwd is gone: nothing can equal it
  }
  if (real === live) throw refuse('it is a live work tree');
  const workTree = gitWorkTreeAncestor(real);
  if (workTree !== null) throw refuse(`inside a git work tree (${workTree})`);
}

/**
 * @param {SessionOpts} opts
 * @param {SessionDeps} [deps]
 * @returns {Promise<SessionResult>}
 */
export async function spawnSession(opts, deps = {}) {
  const { cfg, level, role, promptPath } = opts;
  if (!LEVELS.includes(level)) throw new SessionError('usage', `level must be one of ${LEVELS.join(', ')}`);
  if (!ROLES.includes(role)) throw new SessionError('usage', `role must be one of ${ROLES.join(', ')}`);
  if (typeof promptPath !== 'string' || !path.isAbsolute(promptPath)) throw new SessionError('usage', 'promptPath must be an absolute path');
  const stderr = deps.stderr ?? process.stderr;
  const runRootDir = opts.runRoot ?? currentRunRoot();
  if (role === 'facts' && opts.cwd !== undefined) assertFactsCwd(opts.cwd, runRootDir);
  const writeRow = deps.writeRow ?? (opts.slug ? (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: /** @type {string} */ (opts.slug) }) : null);
  const steps = ladderFor(resolveLevel(cfg, level), role);
  if (opts.background) steps.length = 1;

  const attempts = [];
  let rateLimitedOnce = false;
  for (let i = 0; i < steps.length; i += 1) {
    const step = steps[i];
    const sessionDir = path.join(runRootDir, 'sessions', `${role}-${Date.now()}-${randomBytes(4).toString('hex')}`);
    mkdirSync(sessionDir, { recursive: true, mode: 0o700 });
    let keep = false;
    try {
      const built = buildStep(opts, step, sessionDir);
      const cli = built.cli;
      built.argv[0] = deps.bins?.[cli] ?? built.argv[0];
      if (role === 'coder') {
        const hit = isForbidden(built.argv, opts.forbidden ?? mergeForbidden());
        if (hit) throw new SessionError('forbidden', `coder argv refused before spawn: forbidden entry ${hit.id}`);
      }
      writeSafe(stderr, `level=${level} provider=${step.provider} model=${step.model} effort=${step.effort ?? 'none'} fallback_step=${step.fallback_step}\n`);
      const base = { role, level, provider: step.provider, model: step.model, effort: step.effort ?? null, fallback_step: step.fallback_step, run: opts.run ?? null, block: opts.block ?? null, ...(role === 's2' ? { source: step.fallback_step > 0 ? 's2-fallback' : 's2' } : {}), ...(opts.rowExtra ?? {}) };

      if (opts.background) {
        keep = true;
        // B4.1: the builder's env (the Codex coder's CODEX_HOME) wins over the caller's; a
        // background session's home stays until the run root is swept.
        const bg = startBackground(built, sessionDir, runRootDir, { ...childEnv(deps.env ?? process.env), ...(built.env ?? {}) });
        if (writeRow) await writeRow({ event: 'session.background', ...base, pid: bg.pid, status: 'started' });
        return { status: 'started', ...bg, provider: step.provider, model: step.model, effort: step.effort ?? null, fallback_step: step.fallback_step, attempts };
      }

      const attempt = await runForeground(built, opts, deps);
      const row = {
        event: 'session',
        ...base,
        status: attempt.status,
        ...(attempt.reason ? { reason: attempt.reason } : {}),
        tokens_in: attempt.usage.tokens_in,
        tokens_out: attempt.usage.tokens_out,
        tokens_source: attempt.usage.tokens_source,
        duration_ms: attempt.duration_ms,
        ...(level === 'L3' && step.fallback_step > 0 ? { l3_fallback: true } : {}),
      };
      if (writeRow) await writeRow(row);
      attempts.push({ fallback_step: step.fallback_step, provider: step.provider, model: step.model, status: attempt.status, reason: attempt.reason ?? null });
      if (attempt.status === 'unavailable' && attempt.reason === 'rate-limited' && !rateLimitedOnce) {
        rateLimitedOnce = true; // rate-limited once: the same step gets one more try (§5.6 "twice in a row")
        i -= 1;
        continue;
      }
      rateLimitedOnce = false;
      if (attempt.status === 'unavailable' && i + 1 < steps.length) continue;
      return { ...attempt, provider: step.provider, model: step.model, effort: step.effort ?? null, fallback_step: step.fallback_step, row, attempts };
    } finally {
      if (!keep) rmSync(sessionDir, { recursive: true, force: true });
    }
  }
  throw new Error('spawnSession: unreachable — the ladder always has a step');
}

/**
 * Build one step's argv; closed-book roles get a fresh empty cwd inside the session dir (the facts
 * role: the caller's snapshot when `opts.cwd` names one), and Codex
 * its `-o` file and schema file next to it (never inside the cwd).
 * @param {SessionOpts} opts
 * @param {{provider: string, model: string, effort?: string, flagFallback?: {provider: string, model: string}}} step
 * @param {string} sessionDir
 */
function buildStep(opts, step, sessionDir) {
  const closedBook = opts.role !== 'coder';
  // B9b.1: the facts delegate may run in the caller's read-only project snapshot; nobody else may
  const snapshot = opts.role === 'facts' && opts.cwd !== undefined ? realpathSync(path.resolve(opts.cwd)) : null; // validated by assertFactsCwd
  const cwd = snapshot ?? (closedBook ? path.join(sessionDir, 'cwd') : path.resolve(opts.cwd ?? process.cwd()));
  if (closedBook && snapshot === null) mkdirSync(cwd, { mode: 0o700 });
  const cli = cliNameForProvider(step.provider);
  if (cli === undefined) throw new SessionError('usage', `provider "${step.provider}" has no CLI mapping`);
  /** @type {Record<string, any>} */
  const params = { provider: step.provider, role: opts.role, model: step.model, promptPath: opts.promptPath, cwd };
  if (step.effort) params.effort = step.effort;
  if (step.flagFallback) params.fallback = [step.flagFallback];
  if (cli === 'claude' && opts.maxBudgetUsd !== undefined) params.maxBudgetUsd = opts.maxBudgetUsd;
  if (closedBook && opts.systemPromptText !== undefined && cli !== 'codex') params.systemPromptText = opts.systemPromptText;
  if (cli === 'codex') params.outPath = path.join(sessionDir, 'out.json');
  /** @type {Record<string, any> | null} */
  let compiled = null;
  const strict = variantFor(step.provider) === 'strict';
  if (closedBook && opts.schema) {
    compiled = compiledFor(opts.schema, /** @type {"anthropic"|"openai"|"xai"} */ (step.provider));
    if (cli === 'codex') {
      params.schemaPath = path.join(sessionDir, 'schema.json');
      writeFileSync(params.schemaPath, JSON.stringify(compiled), { mode: 0o600 });
    } else {
      params.schema = compiled;
    }
  }
  const built = buildArgv(/** @type {any} */ (params));
  return { ...built, argv: [...built.argv], compiled, strict };
}

/**
 * @param {{cli: string, argv: string[], cwd: string, stdinFile?: string, outPath?: string, env?: Record<string, string>, compiled: Record<string, any> | null, strict: boolean}} built
 * @param {SessionOpts} opts
 * @param {SessionDeps} deps
 * @returns {Promise<SessionResult & {usage: {tokens_in: number, tokens_out: number, tokens_source: string}, duration_ms: number}>}
 */
async function runForeground(built, opts, deps) {
  const input = built.stdinFile ? readFileSync(built.stdinFile) : undefined;
  const started = Date.now();
  const run = deps.exec ?? exec;
  /** @type {import('../util/exec.mjs').ExecResult} */
  let res;
  try {
    res = await run(built.argv, {
      cwd: built.cwd,
      env: { ...childEnv(deps.env ?? process.env), ...(built.env ?? {}) },
      timeoutMs: opts.timeoutMs ?? DEFAULT_TIMEOUT_MS,
      ...(input !== undefined ? { input } : {}),
    });
  } finally {
    // B4.1: the Codex coder's per-session CODEX_HOME (rules + auth copy) dies with the session.
    if (built.env?.CODEX_HOME) removeCodexHome(built.env.CODEX_HOME);
  }
  const duration_ms = Date.now() - started;
  /** @type {string | null} */
  let outText = null;
  if (built.outPath) {
    outText = existsSync(built.outPath) ? readFileSync(built.outPath, 'utf8') : null;
    rmSync(built.outPath, { force: true });
  }
  const read = readAnswer(built.cli, res.stdout, outText);
  const inputBytes = input?.length ?? fileSize(opts.promptPath);
  const usage =
    read.usage.tokensSource === 'reported'
      ? { tokens_in: read.usage.tokensIn, tokens_out: read.usage.tokensOut, tokens_source: 'reported' }
      : { tokens_in: estimateTokens(inputBytes), tokens_out: estimateTokens(Buffer.byteLength(res.stdout) + Buffer.byteLength(outText ?? '')), tokens_source: 'estimated' };
  const common = { usage, duration_ms, text: read.text, exit_code: res.code, stderr: redact(res.stderr) };

  const unavailable = classifyUnavailable(res, read.parsed);
  if (unavailable) return { status: 'unavailable', reason: unavailable, answer: null, ...common };
  if (res.timedOut) {
    // B30: what the CLI printed last, so a hang's cause can be seen — stderr redacted, capped and
    // packet-free; stdout only as event types and a byte count, never its text
    /** @type {string | null} */
    let packetText = input !== undefined ? Buffer.from(input).toString('utf8') : null;
    if (packetText === null) {
      try {
        packetText = readFileSync(opts.promptPath, 'utf8');
      } catch {
        packetText = null; // fail closed: no tails without the packet to filter against
      }
    }
    const err = stderrTail(res.stderr, packetText);
    const tails = { stderr_tail: err.tail, ...(err.note ? { tail_note: err.note } : {}), ...stdoutEvents(res.stdout) };
    return { status: 'timeout', reason: `killed after ${opts.timeoutMs ?? DEFAULT_TIMEOUT_MS} ms`, answer: null, ...common, ...tails };
  }
  if (res.result !== 'ok' || read.parsed?.is_error === true) return { status: 'failed', reason: `exit ${res.code}`, answer: null, ...common };
  if (built.compiled) {
    // validate against the schema the provider was actually given (the cached variant, stable across attempts)
    if (!matchesSchema(built.compiled, read.answer)) {
      return { status: 'invalid-output', reason: 'answer does not match the schema', answer: null, ...common };
    }
    // a strict answer says `null` where the source schema says "optional"; callers get the source shape
    return { status: 'ok', answer: built.strict ? dropStrictNulls(read.answer, opts.schema) : read.answer, ...common };
  }
  return { status: 'ok', answer: read.answer ?? read.text, ...(opts.role === 'coder' ? { sentinel: parseSentinel(read.text) } : {}), ...common };
}

/**
 * Start a detached child writing to a log; record its pid file and registry entry.
 * @param {{cli: string, argv: string[], cwd: string, stdinFile?: string, outPath?: string}} built
 * @param {string} sessionDir @param {string} runRootDir @param {NodeJS.ProcessEnv} env
 */
function startBackground(built, sessionDir, runRootDir, env) {
  const logPath = path.join(sessionDir, 'session.log');
  const pidFile = path.join(sessionDir, 'pid.json');
  const logFd = openSync(logPath, 'a', 0o600);
  const inFd = built.stdinFile ? openSync(built.stdinFile, 'r') : null;
  try {
    const [command, ...args] = built.argv;
    const child = spawn(command, args, { cwd: built.cwd, env, shell: false, detached: true, stdio: [inFd ?? 'ignore', logFd, logFd] });
    child.on('error', () => {}); // a missing CLI surfaces as a dead pid + an empty log, not a crash
    const pid = child.pid;
    if (typeof pid !== 'number') throw new Error(`spawn: could not start ${built.cli}`);
    registerPid(pidsDir(runRootDir), pid, command);
    const start_time = readStartTime(pid) ?? UNKNOWN_START_TIME;
    writeFileSync(pidFile, `${JSON.stringify({ pid, start_time, argv0: command, log: logPath, ...(built.outPath ? { outPath: built.outPath } : {}) })}\n`, { mode: 0o600 });
    child.unref();
    return { pid, pidFile, logPath, sessionDir };
  } finally {
    closeSync(logFd);
    if (inFd !== null) closeSync(inFd);
  }
}
