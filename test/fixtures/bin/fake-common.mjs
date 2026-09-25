/**
 * Shared behaviour of the fake provider CLIs (`fake-claude`, `fake-codex`, `fake-grok`). Each fake
 * records what it received and prints the output SHAPE of the real CLI (copied from a real
 * `claude -p --output-format json` result; Codex `--json` JSONL events and Grok's JSON are
 * best-effort shapes, [A14]). No auth field, no key, no network.
 *
 * Environment knobs (tests only):
 *  - `FAKE_RECORD`: directory; the fake writes `<name>-<pid>.json` `{name, seq, pid, argv, cwd,
 *    stdin_is_pipe, stdin_b64}` there before doing anything else.
 *  - `FAKE_402_MODELS`: comma list of model ids that answer HTTP 402 (payment required).
 *  - `FAKE_SLEEP_MS`: wait this long before answering (timeout and background tests).
 *  - `FAKE_ANSWER`: the JSON answer to return (default: a valid S2 answer).
 *  - `FAKE_EXIT`: exit with this code after printing a normal (non-error) result.
 *  - `FAKE_CODEX_QUIET`: fake-codex puts the answer ONLY in the `-o` file (no agent_message event).
 */

import { fstatSync, writeFileSync } from 'node:fs';
import path from 'node:path';

/** Monotonic start stamp (system-wide clock on macOS/Linux): records sort oldest first on it. */
const SEQ = process.hrtime.bigint().toString();

export const DEFAULT_ANSWER = Object.freeze({
  decision: 'proceed',
  confidence: 0.82,
  reason: 'fake answer',
  overrule: false,
  ask_human: false,
  human_question: null,
});

/** @returns {Promise<{isPipe: boolean, bytes: Buffer}>} stdin content when stdin is a pipe/socket. */
export async function readStdin() {
  const st = fstatSync(0);
  const isPipe = st.isFIFO() || st.isSocket();
  if (!isPipe) return { isPipe, bytes: Buffer.alloc(0) };
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return { isPipe, bytes: Buffer.concat(chunks) };
}

/**
 * @param {string} name @param {{isPipe: boolean, bytes: Buffer}} stdin
 * @param {Record<string, unknown>} [extra] - merged in; a second call rewrites the same file.
 */
export function record(name, stdin, extra = {}) {
  const dir = process.env.FAKE_RECORD;
  if (!dir) return;
  const entry = {
    name,
    seq: SEQ,
    pid: process.pid,
    argv: process.argv.slice(2),
    cwd: process.cwd(),
    stdin_is_pipe: stdin.isPipe,
    stdin_b64: stdin.bytes.toString('base64'),
    ...extra,
  };
  writeFileSync(path.join(dir, `${name}-${process.pid}.json`), JSON.stringify(entry));
}

/** @param {string[]} argv @param {...string} names @returns {string | undefined} */
export function flagValue(argv, ...names) {
  for (let i = 0; i < argv.length - 1; i += 1) {
    if (names.includes(argv[i])) return argv[i + 1];
  }
  return undefined;
}

/** @param {string | undefined} model */
export function pays402(model) {
  return (process.env.FAKE_402_MODELS ?? '').split(',').filter(Boolean).includes(model ?? '');
}

export async function maybeSleep() {
  const ms = Number(process.env.FAKE_SLEEP_MS ?? 0);
  if (ms > 0) await new Promise((resolve) => setTimeout(resolve, ms));
}

/** @returns {unknown} */
export function answer() {
  return process.env.FAKE_ANSWER ? JSON.parse(process.env.FAKE_ANSWER) : DEFAULT_ANSWER;
}

/** @param {Buffer | string} prompt @returns {number} a deterministic token count for the fake usage. */
export function tokensFor(prompt) {
  return Math.max(1, Math.ceil(Buffer.byteLength(prompt) / 4));
}
