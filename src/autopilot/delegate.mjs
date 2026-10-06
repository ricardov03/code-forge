/**
 * The autopilot delegate (issue #5, plan autopilot §1, block B46): a closed-book session that
 * answers ONE owner-level question while the owner is away, inside the run's autopilot grant.
 *
 * Built on the System 2 pattern (`session/s2.mjs`): a bounded packet → one fresh session in an
 * empty cwd with no tools (role `delegate`, never Codex — `config/closed-book.mjs`) → the answer
 * re-validated against the source schema → the packet removed.
 *
 *   1. `grantFor(run, scope, now)` FIRST (B45). A refusal (no grant, run ended, stopped, expired,
 *      denied, not allowed, a B48 check) returns `{answered: false, to_owner: true, reason}` —
 *      no session, no row: a run with no grant behaves exactly as before.
 *   2. The packet: the scope asked, the grant's allowed scopes, the fixed deny list (text), the
 *      question, the options and a bounded context — every caller-given text passes `redact` and
 *      the secret-shape mask before it is written anywhere. At most
 *      {@link DELEGATE_MAX_PROMPT_TOKENS} estimated tokens (the context is cut on a UTF-8 boundary).
 *   3. One session at the grant's delegate level (L2 | L3), schema {@link DELEGATE_SCHEMA}.
 *   4. Exactly ONE signed `autopilot.decision` row, also when the session failed (`failure` says
 *      why: `<code>: <message>` for a spawner refusal, `session <status> (…)` for a failed
 *      session, `internal-error` — no message text — for anything else, e.g. the packet could not
 *      be written). The row is written BEFORE the answer is returned; only a row that cannot be
 *      written throws, so nothing acts unrecorded (fail closed).
 *
 * Options: each is trimmed and scrubbed, THEN deduplicated; a list given with fewer than 2
 * distinct options is a usage error. An ask WITHOUT options never acts: the delegate can only
 * pick between choices the caller named, so an open question is answered for the owner's
 * information and goes to the owner (`owner_reason: 'no-options'`).
 *
 * `acted` is true only when options were given, the answer is `within_scope`, does not
 * `escalate`, has `confidence` ≥ `autopilot.min_confidence` (inclusive; default
 * {@link DEFAULT_MIN_CONFIDENCE}), names one of the options, and the grant still allows the
 * scope once the answer is in (the window may have closed during the session). Anything else
 * goes to the owner (`to_owner: true`, `owner_reason` names the first rule that failed). The
 * delegate never acts itself: the caller (B47) acts on `acted: true` only.
 *
 * A config the run cannot provide (no snapshot and no fallback; a snapshot whose hash does not
 * match) throws before any session, with its own code (`no-config`, `config-snapshot`).
 *
 * `autopilot.min_confidence` is read from the run's config snapshot (the config the run started
 * or was last reloaded with), so a workspace file edited mid-run does not lower it.
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { maskSecretTokens } from '../config/secret-patterns.mjs';
import { appendRow } from '../ledger/write.mjs';
import { estimateTokens, matchesSchema, runRootFor, SessionError, spawnSession } from '../session/spawn.mjs';
import { snapshotFor } from '../state/config-snapshot.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { redact } from '../util/redact.mjs';
import { grantFor } from './grant.mjs';
import { ALLOWABLE_WHAT, FIXED_DENY } from './scopes.mjs';

/** Upper bound of the delegate prompt, in estimated tokens (bytes / 4) — as S2's. */
export const DELEGATE_MAX_PROMPT_TOKENS = 4000;

/** Maximum length of the answer's `reason`, in characters. */
export const DELEGATE_REASON_MAX = 400;

/** The question as stored in the decision row is cut to this many UTF-8 bytes. */
export const DECISION_QUESTION_MAX_BYTES = 1000;

/** `autopilot.min_confidence` when the config does not set it. */
export const DEFAULT_MIN_CONFIDENCE = 0.7;

/** At most this many options, each at most {@link OPTION_MAX_BYTES} bytes. */
export const MAX_OPTIONS = 20;
export const OPTION_MAX_BYTES = 200;

/** The delegate answer schema (source form; `compileSchema` makes the OpenAI strict variant). */
export const DELEGATE_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    decision: { type: 'string' },
    within_scope: { type: 'boolean' },
    confidence: { type: 'number', minimum: 0, maximum: 1 },
    reason: { type: 'string' },
    escalate: { type: 'boolean' },
  },
  required: ['decision', 'within_scope', 'confidence', 'reason', 'escalate'],
  additionalProperties: false,
});

/** The lens preamble (system prompt) of every delegate session. */
export const DELEGATE_LENS =
  'You are the autopilot delegate for code-forge: you answer one question for the project owner while they are away, ' +
  'and only inside the scope the owner granted. You have no tools and no files; everything you may use is in the message. ' +
  'Answer once, with a JSON object that matches the schema. Never invent facts that are not in the message. ' +
  'When the question is outside the scope, touches anything on the deny list, or you are unsure, set escalate to true.';

const HEADER = [
  '# Autopilot delegate decision',
  '',
  'The owner is away and granted you the scopes below until a fixed time. Decide the question from the',
  'material given. Reply with ONE JSON object:',
  `{"decision": string (exactly one of the options when options are listed), "within_scope": boolean`,
  ` (true only when answering decides nothing beyond the scope asked), "confidence": number 0..1,`,
  ` "reason": string of at most ${DELEGATE_REASON_MAX} characters, "escalate": boolean (true hands the question back to the owner)}.`,
  '',
].join('\n');

/** @typedef {import('./grant.mjs').Grant} Grant */
/** @typedef {(row: Record<string, any>) => Promise<unknown>} WriteRow */
/**
 * @typedef {import('../session/spawn.mjs').SessionDeps & {
 *   writeRow?: WriteRow, checks?: import('./grant.mjs').GrantCheck[], now?: () => Date,
 * }} DelegateDeps - `writeRow` receives the session rows and the decision row; `now` is the clock.
 */
/**
 * @typedef {object} DelegateAsk
 * @property {string} runId
 * @property {string} scope - one of the allowable scopes (`waive:nit`, …).
 * @property {string} question
 * @property {string} [context]
 * @property {string[]} [options]
 * @property {Record<string, any>} [cfg] - used only when the run record holds no config snapshot.
 * @property {number} [timeoutMs]
 */
/**
 * @typedef {{
 *   answered: boolean, acted: boolean, to_owner: boolean, reason: string | null,
 *   grant_id: string | null, answer: Record<string, any> | null, row: Record<string, any> | null,
 * }} DelegateResult - `reason`: the refusal (not answered), the failure, or why it goes to the owner.
 */

/** @param {string} text @returns {string} `text` with registered secrets and secret-shaped tokens masked. */
export const scrub = (text) => maskSecretTokens(redact(text));

/**
 * Cut `text` to at most `maxBytes` UTF-8 bytes without splitting a character.
 * @param {string} text @param {number} maxBytes
 */
function cutBytes(text, maxBytes) {
  const buf = Buffer.from(text, 'utf8');
  if (buf.length <= maxBytes) return text;
  let cut = Math.max(0, maxBytes);
  while (cut > 0 && (buf[cut] & 0xc0) === 0x80) cut -= 1;
  return buf.subarray(0, cut).toString('utf8');
}

/**
 * Validate and clean the options: each trimmed and scrubbed, THEN deduplicated (two options that
 * differ only by a secret collapse into one); a given list needs at least 2 distinct options and
 * at most {@link MAX_OPTIONS}. No list (or an empty one) means an open question: `[]`.
 * @param {unknown} options @returns {string[]}
 * @throws {SessionError} `usage`
 */
export function cleanOptions(options) {
  if (options === undefined || options === null) return [];
  if (!Array.isArray(options)) throw new SessionError('usage', 'delegate: options must be a list of strings');
  if (options.length === 0) return [];
  const out = [];
  for (const option of options) {
    if (typeof option !== 'string' || option.trim().length === 0) throw new SessionError('usage', 'delegate: every option must be a non-empty string');
    const text = scrub(option.trim());
    if (Buffer.byteLength(text) > OPTION_MAX_BYTES) throw new SessionError('usage', `delegate: an option is longer than ${OPTION_MAX_BYTES} bytes`);
    if (!out.includes(text)) out.push(text);
  }
  if (out.length < 2) throw new SessionError('usage', 'delegate: give at least 2 distinct options');
  if (out.length > MAX_OPTIONS) throw new SessionError('usage', `delegate: at most ${MAX_OPTIONS} options`);
  return out;
}

/**
 * The delegate packet. Texts must already be scrubbed.
 * @param {{scope: string, grantScopes: string[], question: string, options?: string[], context?: string}} packet
 * @param {{maxTokens?: number}} [opts]
 * @returns {string} at most `maxTokens` estimated tokens.
 * @throws {SessionError} `usage` without a question; `delegate-too-large` when the fixed part
 *   (header, scopes, deny list, question, options) alone is over budget.
 */
export function buildDelegatePrompt(packet, opts = {}) {
  const maxTokens = opts.maxTokens ?? DELEGATE_MAX_PROMPT_TOKENS;
  if (typeof packet.question !== 'string' || packet.question.trim().length === 0) throw new SessionError('usage', 'delegate: the question must be non-empty');
  const what = /** @type {Record<string, string>} */ (ALLOWABLE_WHAT);
  const allowed = packet.grantScopes.map((s) => `- ${s}: ${what[s] ?? s}`).join('\n');
  const deny = Object.entries(FIXED_DENY).map(([s, w]) => `- ${s}: ${w}`).join('\n');
  let fixed =
    `${HEADER}## Scope asked\n${packet.scope}: ${what[packet.scope] ?? packet.scope}\n` +
    `\n## Scopes the owner granted\n${allowed}\n` +
    `\n## Never allowed (fixed deny list, enforced in code; escalate anything that touches these)\n${deny}\n` +
    `\n## Question\n${packet.question.trim()}\n`;
  if (Array.isArray(packet.options) && packet.options.length > 0) fixed += `\n## Options\n${packet.options.map((o) => `- ${o}`).join('\n')}\n`;
  const maxBytes = maxTokens * 4;
  const fixedBytes = Buffer.byteLength(fixed);
  if (fixedBytes > maxBytes) throw new SessionError('delegate-too-large', `delegate: the question part is ${estimateTokens(fixedBytes)} tokens (max ${maxTokens})`);
  const context = typeof packet.context === 'string' ? packet.context : '';
  if (context.length === 0) return fixed;
  const heading = '\n## Context\n';
  const whole = `${heading}${context}\n`;
  if (fixedBytes + Buffer.byteLength(whole) <= maxBytes) return fixed + whole;
  const note = `\n[context cut: ${Buffer.byteLength(context)} bytes given]\n`;
  const room = maxBytes - fixedBytes - Buffer.byteLength(heading) - Buffer.byteLength(note);
  if (room <= 0) return fixed;
  return `${fixed}${heading}${cutBytes(context, room)}${note}`;
}

/**
 * `autopilot.min_confidence` of a config, or the default when unset or not a number in [0, 1].
 * @param {Record<string, any> | null | undefined} cfg @returns {number}
 */
export function minConfidenceOf(cfg) {
  const value = cfg?.autopilot?.min_confidence;
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1 ? value : DEFAULT_MIN_CONFIDENCE;
}

/**
 * The first rule that keeps a valid answer from acting, or null when it may act.
 * @param {{decision: string, within_scope: boolean, confidence: number, escalate: boolean}} answer
 * @param {string[]} options @param {number} minConfidence
 * @returns {'no-options' | 'outside-scope' | 'escalated' | 'low-confidence' | 'not-an-option' | null}
 */
export function ownerReason(answer, options, minConfidence) {
  if (options.length === 0) return 'no-options';
  if (answer.within_scope !== true) return 'outside-scope';
  if (answer.escalate !== false) return 'escalated';
  if (!(answer.confidence >= minConfidence)) return 'low-confidence';
  if (!options.includes(answer.decision)) return 'not-an-option';
  return null;
}

/**
 * The run's config: its snapshot in force, else `fallback`.
 * @param {Record<string, any>} record @param {Record<string, any> | undefined} fallback
 * @returns {Record<string, any>}
 * @throws {StateError} `no-config` when neither exists; `config-snapshot` when the snapshot is damaged.
 */
function runConfig(record, fallback) {
  let snap;
  try {
    snap = snapshotFor(record, '');
  } catch (err) {
    // only `snapshotFor`'s own hash check is a damaged snapshot; anything else keeps its code
    if (err instanceof Error && err.message === 'config-snapshot' && !(/** @type {any} */ (err).code)) {
      throw new StateError('config-snapshot', `run ${record.run_id}'s config snapshot does not match its hash`);
    }
    throw err;
  }
  if (snap !== null) return snap.config;
  if (fallback && typeof fallback === 'object') return fallback;
  throw new StateError('no-config', `run ${record.run_id} has no config snapshot and no project config was given`);
}

/**
 * Ask the delegate one question. See the module doc for the order and the rules.
 * @param {DelegateAsk} ask @param {DelegateDeps} [deps]
 * @returns {Promise<DelegateResult>}
 * @throws {SessionError} `usage` for a malformed ask; {@link StateError} for an unknown scope,
 *   an unknown run, a missing config, or a decision row that could not be written.
 */
export async function askDelegate(ask, deps = {}) {
  const clock = deps.now ?? (() => new Date());
  const { runId, scope } = ask;
  if (typeof ask.question !== 'string' || ask.question.trim().length === 0) throw new SessionError('usage', 'delegate: the question must be non-empty');
  if (ask.context !== undefined && typeof ask.context !== 'string') throw new SessionError('usage', 'delegate: the context must be text');
  const options = cleanOptions(ask.options); // scrubbed, then deduplicated

  const gate = await grantFor(runId, scope, clock(), { writeRow: deps.writeRow, checks: deps.checks });
  if (!gate.ok) return { answered: false, acted: false, to_owner: true, reason: gate.reason, grant_id: gate.grant?.grant_id ?? null, answer: null, row: null };
  const grant = gate.grant;

  const record = await readRun(runId);
  const cfg = runConfig(record, ask.cfg);
  const slug = record.project;
  const writeRow = deps.writeRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug }));

  const question = scrub(ask.question.trim());
  const context = typeof ask.context === 'string' ? scrub(ask.context) : '';

  /** @type {Record<string, any> | null} */
  let answer = null;
  /** @type {string | null} */
  let failure = null;
  /** @type {{provider: string | null, model: string | null}} */
  let ran = { provider: null, model: null };
  /** @type {string | null} */
  let dir = null;
  try {
    const prompt = buildDelegatePrompt({ scope, grantScopes: grant.scopes, question, options, context });
    const root = runRootFor(runId, cfg?.tmp?.root);
    dir = path.join(root, 'delegate', `${Date.now()}-${randomBytes(4).toString('hex')}`);
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    const packetPath = path.join(dir, 'packet.md');
    writeFileSync(packetPath, prompt, { mode: 0o600 });
    const result = await spawnSession(
      {
        cfg,
        level: /** @type {'L2' | 'L3'} */ (grant.delegate),
        role: 'delegate',
        promptPath: packetPath,
        schema: DELEGATE_SCHEMA,
        systemPromptText: DELEGATE_LENS,
        runRoot: root,
        run: runId,
        slug,
        timeoutMs: ask.timeoutMs,
        rowExtra: { grant_id: grant.grant_id },
      },
      deps, // the session row goes to `deps.writeRow` too, else to the run's ledger (`slug`)
    );
    ran = { provider: result.provider ?? null, model: result.model ?? null };
    if (result.status !== 'ok') failure = `session ${result.status}${result.reason ? ` (${result.reason})` : ''}`;
    // the OpenAI strict variant lets every field be null; the answer must still meet the source schema
    else if (!matchesSchema(DELEGATE_SCHEMA, result.answer)) failure = 'session invalid-output (answer does not match the delegate schema)';
    else if (result.answer.reason.length > DELEGATE_REASON_MAX) failure = `session invalid-output (reason is longer than ${DELEGATE_REASON_MAX} characters)`;
    else answer = result.answer;
  } catch (err) {
    // a spawner refusal keeps its code and message; anything else (the packet dir or file could
    // not be written, a CLI that could not start) is recorded without its text, which may carry paths
    failure = err instanceof SessionError ? `${err.code}: ${err.message}` : 'internal-error';
  } finally {
    try {
      if (dir !== null) rmSync(dir, { recursive: true, force: true });
    } catch {
      // the run root sweep removes it later; the decision row must still be written
    }
  }

  /** @type {string | null} */
  let toOwnerWhy = failure === null ? ownerReason(/** @type {any} */ (answer), options, minConfidenceOf(cfg)) : 'session-failed';
  if (toOwnerWhy === null) {
    // the session may have outlived the window or a stop: the grant is asked again before acting
    try {
      const again = await grantFor(runId, scope, clock(), { writeRow: deps.writeRow, checks: deps.checks });
      if (!again.ok) toOwnerWhy = `grant-${again.reason}`;
    } catch {
      failure = 'internal-error'; // the grant could not be re-read: fail closed, still one row
      toOwnerWhy = 'internal-error';
    }
  }
  const acted = toOwnerWhy === null;
  const row = {
    event: 'autopilot.decision',
    grant_id: grant.grant_id,
    scope,
    question: cutBytes(question, DECISION_QUESTION_MAX_BYTES),
    options,
    decision: answer === null ? null : scrub(answer.decision),
    within_scope: answer === null ? null : answer.within_scope,
    confidence: answer === null ? null : answer.confidence,
    reason: answer === null ? null : scrub(answer.reason),
    escalate: answer === null ? null : answer.escalate,
    acted,
    to_owner: !acted,
    owner_reason: toOwnerWhy,
    failure: failure === null ? null : scrub(failure),
    level: grant.delegate,
    provider: ran.provider,
    model: ran.model,
    ts: clock().toISOString(),
  };
  const signed = /** @type {Record<string, any>} */ (await writeSigned(runId, async (r) => {
    await writeRow(r);
    return r;
  }, row));
  return {
    answered: answer !== null,
    acted,
    to_owner: !acted,
    reason: failure ?? toOwnerWhy,
    grant_id: grant.grant_id,
    answer: answer === null ? null : { ...answer, decision: row.decision, reason: row.reason },
    row: signed,
  };
}
