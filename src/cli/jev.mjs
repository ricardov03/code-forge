/**
 * `code-forge jev ask <question-id> --state <file>` (plan §3.2, B3's row in §10.3). A direct,
 * single-question call to Jev — the debugging/manual-use entry point, not the orchestrator's own
 * decision pipeline (that lives inside B9's session runner and B12's review engine, which import
 * `src/decide/**` directly rather than shelling out to this verb).
 *
 *   jev ask <id> --state <file.json> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>]
 *
 * Resolves the Jev key through B2's chain (`resolveKey`, earlier batch — the seam rule allows a
 * Wave 1 module to import a block in an earlier batch statically), asks Jev, runs the answer
 * through `thresholds.decide()`, and appends one `{event: 'decision', ...}` row through B6's
 * writer (also an earlier batch) — the shape `ledger/calibration.mjs` already expects:
 * `decision_id`, `question`, `confidence` (plan §6.3's calibration buckets read exactly these).
 *
 * Never prints the key: it only ever reaches the `Authorization` header inside `askJev`, and
 * `resolveKey` has already registered it with `redact()` before this module sees it.
 */

import { randomUUID } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { loadConfigFile, DEFAULT_CONFIG_FILENAME } from '../config/load.mjs';
import path from 'node:path';
import { buildQuestionPayload, isKnownQuestion, isQuestionDisabled } from '../decide/questions.mjs';
import { askJev as realAskJev } from '../decide/jev-client.mjs';
import { decide } from '../decide/thresholds.mjs';
import { createDefaultKeyStore, resolveKey } from '../keys/store.mjs';
import { appendRow as realAppendRow } from '../ledger/write.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>]\n';

/**
 * @param {string[]} args @param {string} flag
 * @returns {{ok: true, value: string|undefined} | {ok: false}} same "flag given with no usable
 *   value is a usage error" contract as `ledger.mjs`'s `readOptionalFlag` — a bare `--slug --cwd x`
 *   must never silently read `--cwd` as `--slug`'s value.
 */
function readOptionalFlag(args, flag) {
  const i = args.indexOf(flag);
  if (i < 0) return { ok: true, value: undefined };
  const next = args[i + 1];
  if (next === undefined || next.startsWith('--')) return { ok: false };
  return { ok: true, value: next };
}

/**
 * @param {string[]} args
 * @param {object} deps
 * @param {import('../keys/store.mjs').KeyStore} [deps.store]
 * @param {typeof loadConfigFile} [deps.loadConfig]
 * @param {typeof realAskJev} [deps.askJev]
 * @param {typeof realAppendRow} [deps.appendRow]
 * @param {{write: (s: string) => unknown}} [deps.stdout]
 * @param {{write: (s: string) => unknown}} [deps.stderr]
 * @param {NodeJS.ProcessEnv} [deps.env]
 * @param {() => number} [deps.now]
 * @param {() => string} [deps.makeId]
 * @returns {Promise<number>}
 */
export async function runJev(args, deps = {}) {
  const {
    store,
    loadConfig = loadConfigFile,
    askJev: ask = realAskJev,
    appendRow: write = realAppendRow,
    stdout = process.stdout,
    stderr = process.stderr,
    env = process.env,
    now = Date.now,
    makeId = randomUUID,
  } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  const [sub, id] = args;
  if (sub !== 'ask' || typeof id !== 'string' || id.startsWith('--')) {
    err(USAGE);
    return 2;
  }
  if (!isKnownQuestion(id)) {
    err(`jev ask: unknown question id "${id}"\n`);
    return 2;
  }

  const stateFlag = readOptionalFlag(args, '--state');
  const cwdFlag = readOptionalFlag(args, '--cwd');
  const slugFlag = readOptionalFlag(args, '--slug');
  const keyRefFlag = readOptionalFlag(args, '--key-ref');
  if (!stateFlag.ok || !cwdFlag.ok || !slugFlag.ok || !keyRefFlag.ok || !stateFlag.value) {
    err(USAGE);
    return 2;
  }
  const cwd = cwdFlag.value ?? process.cwd();

  let state;
  try {
    state = JSON.parse(await readFile(path.resolve(cwd, stateFlag.value), 'utf8'));
  } catch (thrown) {
    err(`jev ask: could not read --state file: ${thrown?.code ?? thrown?.message ?? String(thrown)}\n`);
    return 1;
  }

  const configPath = path.join(cwd, DEFAULT_CONFIG_FILENAME);
  const loaded = await loadConfig(configPath);
  let cfg = {};
  if (loaded.ok) {
    cfg = loaded.config;
  } else if (loaded.error !== 'not-found') {
    err(`jev ask: ${loaded.message}\n`);
    return 1;
  }

  if (isQuestionDisabled(id, cfg)) {
    err(`jev ask: question "${id}" is disabled by system1.disable\n`);
    return 1;
  }

  const keyStore = store ?? (await createDefaultKeyStore(env));
  const keyRef = keyRefFlag.value ?? cfg?.keys?.jev;
  let resolved;
  try {
    resolved = await resolveKey('jev', { store: keyStore, ref: keyRef, env, now: now() });
  } catch (thrown) {
    err(`jev ask: ${thrown?.message ?? String(thrown)}\n`);
    return 2;
  }
  if (resolved.value === null) {
    for (const reason of resolved.errors) {
      err(`jev ask: ${reason}\n`);
    }
    err('jev ask: no Jev key resolved (set one with "code-forge keys set jev")\n');
    return 1;
  }

  const questions = buildQuestionPayload([id], cfg);
  const result = await ask({ state, questions, key: resolved.value });
  if (!result.ok) {
    const failure = /** @type {import('../decide/jev-client.mjs').JevFailure} */ (result);
    err(`jev ask: request failed (${failure.kind}${failure.status ? `, status ${failure.status}` : ''})\n`);
    return 1;
  }

  const answer = result.answers[id];
  if (!answer) {
    err(`jev ask: Jev did not answer question "${id}"\n`);
    return 1;
  }
  const decision = decide(id, answer, cfg);
  const slug = slugFlag.value ?? cfg?.project?.slug ?? 'code-forge';
  const decisionId = makeId();

  await write(
    {
      event: 'decision',
      decision_id: decisionId,
      question: id,
      answer: decision.value,
      stage: decision.stage,
      confidence: decision.confidence,
      margin: decision.margin,
      source: 'jev',
      tokens_in: result.usage?.input_tokens,
      tokens_out: result.usage?.output_tokens,
    },
    { slug },
  );

  out(`${JSON.stringify({ decision_id: decisionId, question: id, answer, decision }, null, 2)}\n`);
  return 0;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function jev(args) {
  return runJev(args, {});
}
