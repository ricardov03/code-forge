/**
 * `code-forge jev ask <question-id> --state <file>` (plan §3.2, B3's row in §10.3). A direct,
 * single-question call to Jev — the debugging/manual-use entry point, not the orchestrator's own
 * decision pipeline (that lives inside B9's session runner and B12's review engine, which import
 * `src/decide/**` directly rather than shelling out to this verb).
 *
 *   jev ask <id> --state <file.json> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>]
 *           [--block <id>] [--plan <file>] [--rules]
 *
 * B36: `--block` records the block the answer is for (and `--plan` the plan file's base name) on
 * the decision row — `plan check` refuses a block whose level is not the lane recorded this way.
 * `--rules` answers `lane`, `risk` or `security_sensitive` with the deterministic fallback rules
 * (`decide/fallback-rules.mjs`, for a run without Jev): the state file is then an object carrying
 * all six fallback facts (`filesChanged`, `linesAdded` numbers; `touchesMigration`,
 * `touchesPolicyOrMiddleware`, `pathFloorHit` booleans; `keywordsFound` a string list) — a missing
 * or mistyped one is a usage error naming it, with nothing recorded. `system1.disable` still
 * applies. No key is resolved, no request is sent, and the row says `source: 'rules'`.
 * The ledger slug is `--slug`, else `project.slug`, else the directory-name slug `run` and
 * `author` use (`slugFor`; B36 — it used to fall back to `code-forge`). B50: the config and the
 * directory name are the PROJECT ROOT's (`projectRootFor`): inside git, the nearest regular-file
 * `.code-forge.yml` from the cwd up to the git top level, else the git top level; outside git, the
 * nearest one up to the home directory when the cwd is under it, else the cwd alone. A run from a
 * subfolder thus writes to the project's ledger. An unknown flag is a usage error.
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
import { loadConfigFile, DEFAULT_CONFIG_FILENAME, projectRootFor, slugFor } from '../config/load.mjs';
import path from 'node:path';
import { buildQuestionPayload, isKnownQuestion, isQuestionDisabled } from '../decide/questions.mjs';
import { askJev as realAskJev } from '../decide/jev-client.mjs';
import { decide } from '../decide/thresholds.mjs';
import { resolveDispatchQuestionsByRules } from '../decide/fallback-rules.mjs';
import { createDefaultKeyStore, resolveKey } from '../keys/store.mjs';
import { appendRow as realAppendRow } from '../ledger/write.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]\n';

/** The block id shape the run record uses (`state/block.mjs`). */
const BLOCK_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;

/** Flags that take a value, and the one boolean flag. */
const VALUE_FLAGS = Object.freeze(['--state', '--cwd', '--slug', '--key-ref', '--block', '--plan']);
const BOOLEAN_FLAGS = Object.freeze(['--rules']);

/**
 * Strict flag walk over everything after `ask <id>`: a value flag takes the next token (never one
 * that starts with `--`, so a bare `--slug --cwd x` never reads `--cwd` as the slug), a boolean
 * flag takes none, and an unknown token, a repeated flag or a missing value is a usage error.
 * @param {string[]} rest
 * @returns {{ok: true, values: Record<string, string>, booleans: Set<string>} | {ok: false}}
 */
function parseAskFlags(rest) {
  /** @type {Record<string, string>} */
  const values = {};
  const booleans = new Set();
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (BOOLEAN_FLAGS.includes(arg)) {
      if (booleans.has(arg)) return { ok: false };
      booleans.add(arg);
      continue;
    }
    if (!VALUE_FLAGS.includes(arg) || Object.hasOwn(values, arg)) return { ok: false };
    const next = rest[i + 1];
    if (next === undefined || next.startsWith('--')) return { ok: false };
    values[arg] = next;
    i += 1;
  }
  return { ok: true, values, booleans };
}

/** The six fallback facts `--rules` needs, each with its type (`decide/fallback-rules.mjs`). */
const RULE_FACTS = Object.freeze({
  filesChanged: 'number',
  linesAdded: 'number',
  touchesMigration: 'boolean',
  touchesPolicyOrMiddleware: 'boolean',
  pathFloorHit: 'boolean',
  keywordsFound: 'string[]',
});

/**
 * @param {unknown} state
 * @returns {string[]} the fallback facts that are missing or of the wrong type (all six when the
 *   state is not a plain object).
 */
function badRuleFacts(state) {
  if (state === null || typeof state !== 'object' || Array.isArray(state)) return Object.keys(RULE_FACTS);
  const facts = /** @type {Record<string, unknown>} */ (state);
  return Object.entries(RULE_FACTS)
    .filter(([name, type]) => {
      const v = facts[name];
      if (type === 'number') return typeof v !== 'number' || !Number.isFinite(v) || v < 0;
      if (type === 'boolean') return typeof v !== 'boolean';
      return !Array.isArray(v) || !v.every((k) => typeof k === 'string');
    })
    .map(([name]) => name);
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

  const parsed = parseAskFlags(args.slice(2));
  if (!parsed.ok || parsed.values['--state'] === undefined) {
    err(USAGE);
    return 2;
  }
  const stateFlag = { value: parsed.values['--state'] };
  const cwdFlag = { value: parsed.values['--cwd'] };
  const slugFlag = { value: parsed.values['--slug'] };
  const keyRefFlag = { value: parsed.values['--key-ref'] };
  const blockFlag = { value: parsed.values['--block'] };
  const planFlag = { value: parsed.values['--plan'] };
  if (blockFlag.value !== undefined && !BLOCK_ID.test(blockFlag.value)) {
    err('jev ask: --block must be a block id (letters, digits, . _ -; at most 32 characters)\n');
    return 2;
  }
  const rules = parsed.booleans.has('--rules');
  /** @type {Record<string, unknown>} */
  const scope = {
    ...(blockFlag.value !== undefined ? { block: blockFlag.value } : {}),
    ...(planFlag.value !== undefined ? { plan: path.basename(planFlag.value) } : {}),
  };
  if (rules && !['lane', 'risk', 'security_sensitive'].includes(id)) {
    err(`jev ask: no rule answers "${id}" (--rules covers lane, risk, security_sensitive)\n`);
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

  // B50: the project's config and ledger, also from a subfolder of the project
  const root = projectRootFor(cwd);
  const configPath = path.join(root, DEFAULT_CONFIG_FILENAME);
  const loaded = await loadConfig(configPath);
  let cfg = {};
  if (loaded.ok) {
    cfg = loaded.config;
  } else if (loaded.error !== 'not-found') {
    err(`jev ask: ${loaded.message}\n`);
    return 1;
  }

  // the same ledger as the rest of the tool: --slug, else project.slug, else the directory name
  const slug = slugFlag.value ?? slugFor(cfg, root);

  if (isQuestionDisabled(id, cfg)) {
    err(`jev ask: question "${id}" is disabled by system1.disable\n`);
    return 1;
  }

  if (rules) {
    const bad = badRuleFacts(state);
    if (bad.length > 0) {
      err(`jev ask: --rules needs the fallback facts in the --state object; missing or wrong type: ${bad.join(', ')}\n`);
      return 2;
    }
    const byRules = resolveDispatchQuestionsByRules(state);
    const value = byRules[/** @type {'lane'|'risk'|'security_sensitive'} */ (id)].value;
    const decisionId = makeId();
    await write({ event: 'decision', decision_id: decisionId, question: id, answer: value, source: 'rules', ...scope }, { slug });
    out(`${JSON.stringify({ decision_id: decisionId, question: id, answer: value, source: 'rules', ...scope }, null, 2)}\n`);
    return 0;
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
      ...scope,
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
