/**
 * The AI cleaning pass of an error report (block B28): a second pass after the deterministic scrub,
 * never the only one.
 *
 * One closed-book session through {@link spawnSession} (plan §5.2; skill/references/security.md
 * §4): print mode, a fresh empty temp cwd, the packet on stdin, no tools — the `reviewer` role,
 * whose closed-book shape is exactly that, at level L1 resolved the normal way (`resolveLevel` on
 * the project's `.code-forge.yml`; outside a project, the shipped defaults of its provider or of
 * `anthropic`). The model only LISTS what it would hide (`{items: [{text, kind, reason}]}`); this
 * module replaces every exact substring itself, longest first, with `<kind>`, never inside one of
 * the tool's own placeholders. An item whose text is not in the report (or is blank or shorter
 * than 3 characters) is ignored. Anything but a valid answer
 * — no CLI, login expired, a timeout, bad JSON — makes the pass `unavailable` with a plain reason.
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { resolveLevel } from '../config/known-ids.mjs';
import { defaultsForProvider } from '../config/defaults/index.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { SCRUB_PLACEHOLDERS } from '../util/error-log.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { spawnSession } from './spawn.mjs';

/** The kinds an item may have; any other kind the model names counts as `other`. */
export const AI_KINDS = Object.freeze(['person', 'company', 'project', 'host', 'url', 'account', 'path', 'secret', 'other']);

/** Singular and plural for the cleaning summary. */
const KIND_LABELS = Object.freeze({
  person: ['person', 'people'],
  company: ['company', 'companies'],
  project: ['project', 'projects'],
  host: ['host', 'hosts'],
  url: ['URL', 'URLs'],
  account: ['account', 'accounts'],
  path: ['path', 'paths'],
  secret: ['secret', 'secrets'],
  other: ['other item', 'other items'],
});

/** The whole packet the AI may see, in bytes (cost guard); a longer report is cut first. */
export const AI_MAX_BYTES = 16 * 1024;

/** Wall-clock cap of the AI session. */
export const AI_TIMEOUT_MS = 180_000;

/** An item shorter than this is ignored: replacing it would mangle ordinary words. */
export const MIN_ITEM_LENGTH = 3;

/** The answer schema (source form; `spawnSession` compiles it per provider). */
export const SCRUB_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    items: {
      type: 'array',
      items: {
        type: 'object',
        properties: { text: { type: 'string' }, kind: { type: 'string' }, reason: { type: 'string' } },
        required: ['text', 'kind', 'reason'],
        additionalProperties: false,
      },
    },
  },
  required: ['items'],
  additionalProperties: false,
});

/** The system prompt of the session. */
export const SCRUB_LENS =
  'You check an error report before it is posted publicly. You have no tools and no files; the report is in the message. ' +
  'You never rewrite the report: you only list what should be hidden. Answer once, with JSON only.';

/**
 * @param {string} report - the title and body, already cleaned by the deterministic scrub.
 * @returns {string} the packet.
 */
export function buildScrubPrompt(report) {
  return [
    '# Find private details in this error report',
    '',
    'Find anything in the text that could identify a person, a company, a private project, a host, an internal URL, an account, a path, or anything that looks like a secret or credential.',
    'Placeholders such as <project>, <slug>, <email>, <item-id>, op://<ref> and ~ are already cleaned: leave them out. Public names (code-forge, Node, GitHub, npm, an OS) are fine.',
    'Return JSON only, exactly this shape:',
    '{ "items": [{ "text": "<exact substring>", "kind": "person|company|project|host|url|account|path|secret|other", "reason": "<short>" }] }',
    'Each "text" must be copied exactly from the report. Return { "items": [] } when nothing should be hidden.',
    '',
    '## Report',
    '',
    report,
    '',
  ].join('\n');
}

/** The report text that fits in the packet with the prompt around it, in bytes. */
export const AI_REPORT_MAX_BYTES = AI_MAX_BYTES - Buffer.byteLength(buildScrubPrompt(''));

/**
 * @typedef {{text: string, kind: string}} ScrubItem
 */

/**
 * The usable items of an answer: a string `text` of at least {@link MIN_ITEM_LENGTH} characters
 * that is not blank;
 * an unknown `kind` becomes `other`.
 * @param {unknown} answer
 * @returns {ScrubItem[]|null} null when the answer is not `{items: [...]}`.
 */
export function readItems(answer) {
  if (!answer || typeof answer !== 'object' || !Array.isArray(/** @type {any} */ (answer).items)) return null;
  /** @type {ScrubItem[]} */
  const out = [];
  for (const item of /** @type {any} */ (answer).items) {
    if (!item || typeof item.text !== 'string' || item.text.trim().length === 0 || item.text.length < MIN_ITEM_LENGTH) continue;
    const lower = typeof item.kind === 'string' ? item.kind.toLowerCase() : '';
    const kind = AI_KINDS.includes(lower) ? lower : 'other';
    out.push({ text: item.text, kind });
  }
  return out;
}

/** The placeholders the tool itself writes: the deterministic scrub's and the AI pass's own. */
export const PROTECTED_PLACEHOLDERS = Object.freeze([...SCRUB_PLACEHOLDERS, ...AI_KINDS.map((k) => `<${k}>`)]);

/**
 * Replace every exact occurrence of each item's text with `<kind>`, in one pass over each
 * ORIGINAL text: match ranges are collected for every item, longest text first, then the output
 * is built. Only the tool's own placeholders ({@link PROTECTED_PLACEHOLDERS}) are protected: a
 * range that starts or ends inside one (an item "path" against `<path>`) is skipped, while a range
 * that fully contains one is replaced as a whole (`acme-<project>-prod` → `<host>`). Any other
 * `<…>` text (`<jdoe@acme-corp.com>`, `Map<AcmeBillingClient>`) is ordinary text. A range that
 * overlaps one already taken is skipped. Counts are ranges replaced per kind; a missing text
 * counts 0.
 * @param {string[]} texts
 * @param {ScrubItem[]} items
 * @returns {{texts: string[], counts: Array<{kind: string, count: number}>}}
 */
export function applyItems(texts, items) {
  const sorted = [...items].sort((a, b) => b.text.length - a.text.length);
  /** @type {Map<string, number>} */
  const tally = new Map();
  const out = texts.map((t) => {
    /** @type {Array<[number, number]>} */
    const guarded = [];
    for (const p of PROTECTED_PLACEHOLDERS) {
      for (let at = t.indexOf(p); at !== -1; at = t.indexOf(p, at + p.length)) guarded.push([at, at + p.length]);
    }
    /** @type {Array<{start: number, end: number, kind: string}>} */
    const taken = [];
    const blocked = (/** @type {number} */ a, /** @type {number} */ b) =>
      // a range that is exactly one of our placeholders is never replaced (nothing new to hide)
      guarded.some(([s, e]) => (a === s && b === e) || (a < e && s < b && !(a <= s && e <= b))) || taken.some((r) => a < r.end && r.start < b);
    for (const { text, kind } of sorted) {
      for (let at = t.indexOf(text); at !== -1; at = t.indexOf(text, at + 1)) {
        const end = at + text.length;
        if (blocked(at, end)) continue;
        taken.push({ start: at, end, kind });
        tally.set(kind, (tally.get(kind) ?? 0) + 1);
      }
    }
    taken.sort((a, b) => a.start - b.start);
    let built = '';
    let pos = 0;
    for (const r of taken) {
      built += `${t.slice(pos, r.start)}<${r.kind}>`;
      pos = r.end;
    }
    return built + t.slice(pos);
  });
  return { texts: out, counts: AI_KINDS.filter((k) => (tally.get(k) ?? 0) > 0).map((k) => ({ kind: k, count: /** @type {number} */ (tally.get(k)) })) };
}

/**
 * "AI pass: 1 host, 1 company." / "AI pass: nothing found." — counts only, never the text.
 * @param {Array<{kind: string, count: number}>} counts
 * @returns {string}
 */
export function describeAiCounts(counts) {
  const parts = counts.filter((c) => c.count > 0).map((c) => {
    const label = Object.hasOwn(KIND_LABELS, c.kind) ? KIND_LABELS[/** @type {keyof typeof KIND_LABELS} */ (c.kind)] : [c.kind, c.kind];
    return `${c.count} ${c.count === 1 ? label[0] : label[1]}`;
  });
  return parts.length > 0 ? `AI pass: ${parts.join(', ')}.` : 'AI pass: nothing found.';
}

/** @param {{status: string, reason?: string|null}} res @returns {string} */
function plainReason(res) {
  if (res.status === 'unavailable') {
    if (res.reason === 'cli-missing') return 'the model CLI is not installed';
    if (res.reason === 'login-expired') return 'the model CLI is not logged in';
    if (res.reason === 'rate-limited' || res.reason === 'http-402') return 'the model is not available right now';
    return 'the model is not available';
  }
  if (res.status === 'timeout') return 'it timed out';
  if (res.status === 'invalid-output') return 'its answer was not valid JSON';
  return 'the session failed';
}

/**
 * The config L1 is resolved from: the project's when its L1 resolves, else the shipped defaults
 * alone (of the project's provider, or `anthropic`; never the project's own levels mixed in).
 * @param {string} cwd
 * @returns {Promise<Record<string, any>>}
 * @throws {Error} with a fixed message when the project config cannot be read.
 */
async function scrubConfig(cwd) {
  const unreadable = new Error('the project config could not be read');
  let loaded;
  try {
    loaded = await loadProjectConfig(cwd);
  } catch {
    throw unreadable;
  }
  if (!loaded.ok) {
    if (loaded.error !== 'not-found') throw unreadable;
    return /** @type {Record<string, any>} */ (defaultsForProvider('anthropic'));
  }
  try {
    resolveLevel(loaded.config, 'L1');
    return loaded.config;
  } catch {
    const provider = typeof loaded.config?.provider === 'string' ? loaded.config.provider : 'anthropic';
    return /** @type {Record<string, any>} */ (defaultsForProvider(provider) ?? defaultsForProvider('anthropic'));
  }
}

/**
 * Run the AI pass over `report` (scrubbed, and cut to {@link AI_REPORT_MAX_BYTES}); a packet over
 * {@link AI_MAX_BYTES} is refused, never sent.
 * @param {{report: string, cwd: string, runRoot?: string, timeoutMs?: number}} opts
 * @param {import('./spawn.mjs').SessionDeps} [deps]
 * @returns {Promise<{ok: true, items: ScrubItem[]} | {ok: false, reason: string}>}
 */
export async function runAiScrub(opts, deps = {}) {
  const packet = buildScrubPrompt(opts.report);
  if (Buffer.byteLength(packet) > AI_MAX_BYTES) return { ok: false, reason: 'the report is over 16 KB' };
  let cfg;
  try {
    cfg = await scrubConfig(opts.cwd);
  } catch (e) {
    return { ok: false, reason: /** @type {Error} */ (e).message };
  }
  let dir = null;
  try {
    const root = opts.runRoot ?? currentRunRoot();
    dir = path.join(root, 'scrub', `${Date.now()}-${randomBytes(4).toString('hex')}`);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const packetPath = path.join(dir, 'packet.md');
    writeFileSync(packetPath, packet, { mode: 0o600 });
    const res = await spawnSession(
      { cfg, level: 'L1', role: 'reviewer', promptPath: packetPath, schema: SCRUB_SCHEMA, systemPromptText: SCRUB_LENS, runRoot: root, timeoutMs: opts.timeoutMs ?? AI_TIMEOUT_MS },
      deps,
    );
    if (res.status !== 'ok') return { ok: false, reason: plainReason(res) };
    const items = readItems(res.answer);
    return items === null ? { ok: false, reason: 'its answer was not valid JSON' } : { ok: true, items };
  } catch {
    return { ok: false, reason: 'the session could not start' };
  } finally {
    try {
      if (dir !== null) rmSync(dir, { recursive: true, force: true });
    } catch {
      // the packet dir lives in the run root, which the next sweep removes
    }
  }
}
