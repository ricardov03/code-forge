/**
 * System 2 (plan §3.4; block B9a): a fresh L3 print-mode session, closed-book, empty cwd, the
 * packet on stdin (V3; Grok reads it through `--prompt-file`), one turn, the answer schema
 * compiled per provider (O36: OpenAI strict mode gets every property required + nullable).
 *
 * The prompt is bounded: `buildS2Prompt` keeps it at or under {@link S2_MAX_PROMPT_TOKENS}
 * estimated tokens (bytes / 4) by cutting the context on a UTF-8 boundary; a question that alone
 * does not fit is refused. `reason` is capped at 280 characters after parsing (not in the schema:
 * string length keywords are not portable to every provider's structured-output mode).
 *
 * Cost guard: more than {@link S2_HEAVY_AFTER} non-fallback S2 calls in one block ⇒ `s2-heavy`
 * (the orchestrator re-checks the decomposition); fallback calls do not count (O9).
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { currentRunRoot } from '../util/tmp.mjs';
import { estimateTokens, matchesSchema, SessionError, spawnSession } from './spawn.mjs';

/** Upper bound of the S2 prompt, in estimated tokens (bytes / 4). */
export const S2_MAX_PROMPT_TOKENS = 4000;

/** Maximum length of the answer's `reason`, in characters. */
export const S2_REASON_MAX = 280;

/** More than this many non-fallback S2 calls in a block raises `s2-heavy`. */
export const S2_HEAVY_AFTER = 6;

/** The S2 answer schema (source form; `compileSchema` makes the OpenAI strict variant). */
export const S2_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    decision: { type: 'string' },
    confidence: { type: 'number', minimum: 0, maximum: 1 },
    reason: { type: 'string' },
    overrule: { type: 'boolean' },
    ask_human: { type: 'boolean' },
    human_question: { type: ['string', 'null'] },
  },
  required: ['decision', 'confidence', 'reason', 'overrule', 'ask_human', 'human_question'],
  additionalProperties: false,
});

/** The lens preamble (system prompt) of every S2 session. */
export const S2_LENS =
  'You are System 2 for code-forge: a careful, closed-book reviewer of one decision. ' +
  'You have no tools and no files; everything you may use is in the message. ' +
  'Answer once, with a JSON object that matches the schema. Never invent facts that are not in the message.';

const HEADER = [
  '# System 2 decision',
  '',
  'Decide the question below from the material given. Reply with ONE JSON object:',
  '{"decision": string, "confidence": number 0..1, "reason": string of at most 280 characters,',
  ' "overrule": boolean (true when you disagree with System 1), "ask_human": boolean,',
  ' "human_question": string or null (the one question for the human when ask_human is true)}.',
  '',
].join('\n');

/**
 * @typedef {object} S2Packet
 * @property {string} question
 * @property {string} [context]
 * @property {string[]} [options]
 * @property {{answer?: unknown, confidence?: number}} [s1]
 */

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
 * @param {S2Packet} packet
 * @param {{maxTokens?: number}} [opts]
 * @returns {string} the prompt, at most `maxTokens` estimated tokens.
 * @throws {SessionError} `usage` when the packet has no question; `s2-too-large` when the fixed
 *   part (header, question, options, S1 answer) alone is over budget.
 */
export function buildS2Prompt(packet, opts = {}) {
  const maxTokens = opts.maxTokens ?? S2_MAX_PROMPT_TOKENS;
  if (!packet || typeof packet.question !== 'string' || packet.question.trim().length === 0) {
    throw new SessionError('usage', 's2: the packet needs a non-empty question');
  }
  let fixed = `${HEADER}## Question\n${packet.question.trim()}\n`;
  if (Array.isArray(packet.options) && packet.options.length > 0) {
    fixed += `\n## Options\n${packet.options.map((o) => `- ${String(o)}`).join('\n')}\n`;
  }
  if (packet.s1) fixed += `\n## System 1 answer\n${JSON.stringify(packet.s1)}\n`;
  const maxBytes = maxTokens * 4;
  const fixedBytes = Buffer.byteLength(fixed);
  if (fixedBytes > maxBytes) {
    throw new SessionError('s2-too-large', `s2: the question part is ${estimateTokens(fixedBytes)} tokens (max ${maxTokens})`);
  }
  const context = typeof packet.context === 'string' ? packet.context : '';
  if (context.length === 0) return fixed;
  const heading = '\n## Context\n';
  const whole = `${heading}${context}\n`;
  if (fixedBytes + Buffer.byteLength(whole) <= maxBytes) return fixed + whole;
  const note = `\n[context cut: ${Buffer.byteLength(context)} bytes given]\n`;
  const room = maxBytes - fixedBytes - Buffer.byteLength(heading) - Buffer.byteLength(note);
  if (room <= 0) return fixed; // no space for any context once the heading and the note are counted
  return `${fixed}${heading}${cutBytes(context, room)}${note}`;
}

/**
 * Whether a block has made more than {@link S2_HEAVY_AFTER} non-fallback S2 calls.
 * @param {ReadonlyArray<Record<string, any>>} rows - ledger rows
 * @param {string} block
 */
export function isS2Heavy(rows, block) {
  return rows.filter((r) => r.event === 'session' && r.role === 's2' && r.block === block && r.source === 's2').length > S2_HEAVY_AFTER;
}

/**
 * Run one S2 decision.
 * @param {{cfg: Record<string, any>, packet: S2Packet, run?: string, block?: string, slug?: string, runRoot?: string, timeoutMs?: number}} opts
 * @param {import('./spawn.mjs').SessionDeps} [deps]
 */
export async function runS2(opts, deps = {}) {
  const prompt = buildS2Prompt(opts.packet);
  const root = opts.runRoot ?? currentRunRoot();
  const dir = path.join(root, 's2', `${Date.now()}-${randomBytes(4).toString('hex')}`);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const packetPath = path.join(dir, 'packet.md');
  try {
    writeFileSync(packetPath, prompt, { mode: 0o600 });
    const result = await spawnSession(
      {
        cfg: opts.cfg,
        level: 'L3',
        role: 's2',
        promptPath: packetPath,
        schema: S2_SCHEMA,
        systemPromptText: S2_LENS,
        runRoot: root,
        run: opts.run,
        block: opts.block,
        slug: opts.slug,
        timeoutMs: opts.timeoutMs,
      },
      deps,
    );
    if (result.status !== 'ok') return result;
    // The OpenAI strict variant lets every field be null; the answer must still meet the source schema.
    if (!matchesSchema(S2_SCHEMA, result.answer)) {
      return { ...result, status: 'invalid-output', reason: 'answer does not match the S2 schema', answer: null };
    }
    if (result.answer.reason.length > S2_REASON_MAX) {
      return { ...result, status: 'invalid-output', reason: `reason is longer than ${S2_REASON_MAX} characters`, answer: null };
    }
    return result;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
