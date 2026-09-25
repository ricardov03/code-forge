/**
 * The author loop (plan §3.7, O18; §3.8 rule 2; block B9b).
 *
 * `runAuthor` is one round: a fresh L3 closed-book `author` session (empty cwd, the packet —
 * brief, facts sheet, prior draft, the human's answers — piped on stdin by B9a's spawner, V3)
 * that returns `{draft, questions[], cost}`. The Plan job refuses to run without a facts sheet
 * (`facts sheet required: run forge facts first`) and refuses a sheet built from another version
 * of the brief (`facts sheet is stale: brief changed after it was built`).
 *
 * `runAuthorLoop` is the harden loop: write the draft, hand `questions[]` to the human through the
 * injected `ask` (the human answers — the orchestrator never answers for them), re-invoke with the
 * draft and the answers, and stop when B3's `isAuthorLoopDone` says so. Each round prints its cost.
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { isAuthorLoopDone } from '../decide/author-loop.mjs';
import { estimateCostUsd } from '../ledger/prices.mjs';
import { writeSafe } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { checkSheetFresh } from './facts.mjs';
import { spawnSession } from './spawn.mjs';

export const JOBS = Object.freeze(['plan', 'harden']);

export const FACTS_REQUIRED = 'facts sheet required: run forge facts first';

/** The author answer schema (source form; `compileSchema` makes the OpenAI strict variant). */
export const AUTHOR_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    draft: { type: 'string' },
    questions: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          id: { type: 'string' },
          question: { type: 'string' },
          options: { type: 'array', items: { type: 'string' } },
          why: { type: 'string' },
          blocking: { type: 'boolean' },
        },
        required: ['id', 'question', 'options', 'why', 'blocking'],
        additionalProperties: false,
      },
    },
  },
  required: ['draft', 'questions'],
  additionalProperties: false,
});

export const AUTHOR_LENS =
  'You are the plan author for code-forge: closed-book, no tools, no files. Everything you may use is in the message. ' +
  'Never invent a flag, path, version or API that the facts sheet does not back. Reply once, with a JSON object that matches the schema.';

const JOB_RULES = {
  plan: [
    'Write the plan draft in markdown. Its §0 is the facts sheet below, VERBATIM (header lines included).',
    'Include a section titled "Acceptance clauses the facts sheet cannot back": every clause that cites a claim the sheet',
    'marks NOT-FOUND or UNVERIFIABLE, each with a tolerance naming the block; write `none` when there is none.',
  ],
  harden: [
    'Harden the draft: close every gap you can from the material given; for each decision only the human can make,',
    'ask ONE question (options, why, blocking true when the plan cannot be dispatched without the answer).',
  ],
};

/** An error raised before the session (`code`: `usage`, `facts-required`, `stale`, `bad-sheet`). */
export class AuthorError extends Error {
  /** @param {string} code @param {string} message */
  constructor(code, message) {
    super(message);
    this.name = 'AuthorError';
    this.code = code;
  }
}

/**
 * §3.8 rule 2: the Plan job needs a facts sheet, and any sheet given must match the brief.
 * @param {{job: string, briefPath: string, factsPath?: string}} opts
 * @throws {AuthorError}
 */
export function assertFacts({ job, briefPath, factsPath }) {
  if (factsPath === undefined) {
    if (job === 'plan') throw new AuthorError('facts-required', FACTS_REQUIRED);
    return;
  }
  let text;
  try {
    text = readFileSync(factsPath, 'utf8');
  } catch {
    throw new AuthorError('bad-sheet', '--facts cannot be read');
  }
  const fresh = checkSheetFresh(text, briefPath);
  if ('code' in fresh) throw new AuthorError(fresh.code, fresh.message);
}

/**
 * @param {{job: string, brief: string, facts?: string, draft?: string, answers?: string}} parts - texts.
 * @returns {string} the author packet.
 */
export function buildAuthorPacket({ job, brief, facts, draft, answers }) {
  const rules = JOB_RULES[/** @type {'plan'|'harden'} */ (job)];
  const out = [`# Author job: ${job}`, '', ...rules, '', 'Reply with {"draft": markdown, "questions": [{"id", "question", "options", "why", "blocking"}]} — questions empty when none remain.', ''];
  out.push('## Brief', '', brief.trimEnd(), '');
  if (facts !== undefined) out.push('## Facts sheet', '', facts.trimEnd(), '');
  if (draft !== undefined) out.push('## Prior draft', '', draft.trimEnd(), '');
  if (answers !== undefined) out.push("## The human's answers", '', answers.trimEnd(), '');
  return out.join('\n');
}

/**
 * @typedef {object} AuthorOpts
 * @property {Record<string, any>} cfg
 * @property {string} job - `plan` or `harden`.
 * @property {string} briefPath @property {string} [factsPath] @property {string} [draftPath] @property {string} [answersPath]
 * @property {string} [runRoot] @property {string} [run] @property {string} [block] @property {string} [slug]
 * @property {number} [timeoutMs]
 */

/**
 * @typedef {{tokens_in: number | null, tokens_out: number | null, tokens_source: string | null, usd_estimated: number | null}} AuthorCost
 */

/** @param {Record<string, any>} result @returns {AuthorCost} */
function costOf(result) {
  const row = result.row ?? {};
  let usd = null;
  try {
    usd = estimateCostUsd({ provider: result.provider, level: 'L3', tokensIn: row.tokens_in ?? 0, tokensOut: row.tokens_out ?? 0 });
  } catch {
    usd = null;
  }
  return { tokens_in: row.tokens_in ?? null, tokens_out: row.tokens_out ?? null, tokens_source: row.tokens_source ?? null, usd_estimated: usd };
}

/**
 * One author round.
 * @param {AuthorOpts} opts
 * @param {import('./spawn.mjs').SessionDeps} [deps]
 * @returns {Promise<{status: string, reason?: string | null, draft?: string, questions?: import('../decide/author-loop.mjs').AuthorQuestion[], cost?: AuthorCost}>}
 * @throws {AuthorError} before any session runs.
 */
export async function runAuthor(opts, deps = {}) {
  if (!JOBS.includes(opts.job)) throw new AuthorError('usage', `--job must be one of ${JOBS.join(', ')}`);
  assertFacts(opts);
  /** @param {string | undefined} file @param {string} flag */
  const read = (file, flag) => {
    if (file === undefined) return undefined;
    try {
      return readFileSync(file, 'utf8');
    } catch {
      throw new AuthorError('usage', `--${flag} cannot be read`);
    }
  };
  const packet = buildAuthorPacket({
    job: opts.job,
    brief: /** @type {string} */ (read(opts.briefPath, 'brief')),
    facts: read(opts.factsPath, 'facts'),
    draft: read(opts.draftPath, 'draft'),
    answers: read(opts.answersPath, 'answers'),
  });
  const root = opts.runRoot ?? currentRunRoot();
  const dir = path.join(root, 'author', `${Date.now()}-${randomBytes(4).toString('hex')}`);
  mkdirSync(dir, { recursive: true, mode: 0o700 });
  const packetPath = path.join(dir, 'packet.md');
  try {
    writeFileSync(packetPath, packet, { mode: 0o600 });
    const result = await spawnSession(
      { cfg: opts.cfg, level: 'L3', role: 'author', promptPath: packetPath, schema: AUTHOR_SCHEMA, systemPromptText: AUTHOR_LENS, runRoot: root, run: opts.run, block: opts.block, slug: opts.slug, timeoutMs: opts.timeoutMs },
      deps,
    );
    if (result.status !== 'ok') return { status: result.status, reason: result.reason ?? null };
    return { status: 'ok', draft: result.answer.draft, questions: result.answer.questions, cost: costOf(result) };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/**
 * @typedef {object} AskReply
 * @property {Record<string, string>} answers - question id ⇒ the human's answer.
 * @property {string[]} [skipped] - NON-blocking ids the human chose to skip.
 */

/**
 * The harden loop (§3.7).
 * @param {AuthorOpts & {draftOut: string, ask: (questions: import('../decide/author-loop.mjs').AuthorQuestion[], round: number) => Promise<AskReply>, maxRounds?: number}} opts
 *   `draftOut`: where each round's draft is written (the plans dir); `ask`: the human.
 * @param {import('./spawn.mjs').SessionDeps} [deps]
 * @returns {Promise<{status: string, reason?: string | null, rounds: Array<{round: number, questions: number, cost: AuthorCost}>, draftPath: string}>}
 */
export async function runAuthorLoop(opts, deps = {}) {
  const maxRounds = opts.maxRounds ?? 6;
  const stderr = deps.stderr ?? process.stderr;
  const root = opts.runRoot ?? currentRunRoot();
  const answersPath = path.join(root, 'author', `answers-${randomBytes(4).toString('hex')}.json`);
  mkdirSync(path.dirname(answersPath), { recursive: true, mode: 0o700 });
  mkdirSync(path.dirname(opts.draftOut), { recursive: true });
  /** @type {Array<{round: number, questions: number, cost: AuthorCost}>} */
  const rounds = [];
  let draftPath = opts.draftPath;
  let answers = opts.answersPath;
  try {
    for (let round = 1; round <= maxRounds; round += 1) {
      const res = await runAuthor({ ...opts, runRoot: root, draftPath, answersPath: answers }, deps);
      if (res.status !== 'ok') return { status: res.status, reason: res.reason ?? null, rounds, draftPath: opts.draftOut };
      const questions = /** @type {import('../decide/author-loop.mjs').AuthorQuestion[]} */ (res.questions);
      const cost = /** @type {AuthorCost} */ (res.cost);
      writeFileSync(opts.draftOut, /** @type {string} */ (res.draft));
      rounds.push({ round, questions: questions.length, cost });
      writeSafe(stderr, `author round ${round}: ${questions.length} question(s), tokens ${cost.tokens_in ?? '?'}/${cost.tokens_out ?? '?'} (${cost.tokens_source ?? 'unknown'}), ~$${cost.usd_estimated ?? '?'}\n`);
      if (isAuthorLoopDone(questions)) return { status: 'ok', rounds, draftPath: opts.draftOut };
      const reply = await opts.ask(questions, round);
      if (isAuthorLoopDone(questions, reply.skipped ?? [])) return { status: 'ok', rounds, draftPath: opts.draftOut };
      // the next round is closed-book: it sees each question's text beside the human's answer
      const answered = questions.map((q) => ({ id: q.id, question: q.question, options: q.options ?? [], answer: reply.answers[q.id] ?? null }));
      writeFileSync(answersPath, `${JSON.stringify(answered, null, 2)}\n`, { mode: 0o600 });
      draftPath = opts.draftOut;
      answers = answersPath;
    }
    return { status: 'max-rounds', reason: `questions remain after ${maxRounds} rounds`, rounds, draftPath: opts.draftOut };
  } finally {
    rmSync(answersPath, { force: true });
  }
}
