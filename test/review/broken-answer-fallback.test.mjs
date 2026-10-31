import { answerFor, cfgFor, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { test } from 'node:test';

const { judgeAgentReview } = await import('../../src/review/agent-waiver.mjs');
const { reviewFile } = await import('../../src/review/engine.mjs');
const { initialConfigState } = await import('../../src/state/config-snapshot.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { verifyRow, loadKey } = await import('../../src/state/signer.mjs');

const FILE = 'src/a.mjs';
const NEXT = { provider: 'xai', model: 'grok-4.7', effort: 'high' };
const LAST = { provider: 'anthropic', model: 'claude-sonnet-5', effort: 'max' };
const AGREE = { status: 'ok', answer: { verdict: 'agree', reason: 'Evidence covers the case.' } };
const INVALID = { status: 'ok', answer: { verdict: 'agree', reason: null } };
let seq = 0;

/** @param {'L2' | 'L3'} level */
function config(level) {
  const cfg = /** @type {Record<string, any>} */ (cfgFor({ second_levels: {} }));
  cfg.levels[level] = { ...cfg.levels[level], fallback: [NEXT, LAST] };
  return cfg;
}

/** @param {Record<string, any>} cfg @param {(opts: any) => Promise<any>} judge */
async function judgeOn(cfg, judge) {
  return judgeAgentReview({ evidence: 'unchanged packet' }, {
    record: { project: 'broken-answer-test', config: initialConfigState(cfg) },
    runId: `r-broken-answer-${++seq}`, id: 'B1', judge,
  });
}

/** @param {Record<string, any>} cfg */
async function waiverFixture(cfg) {
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(1));
  const slug = `broken-answer-${++seq}`;
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, config: cfg, writeRow });
  await openBlock({ runId, id: 'B1', level: 'L1', owned: [FILE], acceptance: [{ clause: 'case', tests: ['t'] }], writeRow });
  await writeSigned(runId, writeRow, { event: 'review.triage', block: 'B1', file: FILE, finding: 'W1', severity: 'warning', claim: 'Boundary case', evidence: `${FILE}:1` });
  const rows = () => readAllRows(slug);
  const before = (await rows()).length;
  return {
    rows, before, key: await loadKey(runId),
    invoke: async (/** @type {(opts: any) => Promise<any>} */ judge) => {
      let output = '';
      const stream = { write: (/** @type {string} */ s) => { output += s; return true; } };
      const code = await runBlock(['waive', 'B1', 'W1', '--run', runId, '--file', FILE, '--reason', `${FILE}:1 covers the boundary`, '--yes'],
        /** @type {any} */ ({ stdout: stream, stderr: stream, isTTY: false, waiverJudge: judge }));
      return { code, output };
    },
  };
}

test('clause 1: invalid judge output is broken, and the CLI reports judge answer broken with zero writes', async () => {
  const cfg = cfgFor();
  const answer = await judgeOn(cfg, async () => INVALID);
  assert.equal(answer.verdict, 'broken', 'invalid output must not be a disagreement');
  const f = await waiverFixture(cfg);
  const out = await f.invoke(async () => INVALID);
  assert.equal(out.code, 1);
  assert.match(out.output, /judge answer broken/i);
  assert.doesNotMatch(out.output, /disagreed/i);
  assert.match(out.output, /nothing was recorded/);
  assert.equal((await f.rows()).length - f.before, 0, 'broken judge writes zero rows');
});

test('clause 1 boundary: failed and throwing judges are broken, while an actual disagreement stops immediately', async () => {
  for (const status of ['failed', 'timeout', 'throw']) {
    let calls = 0;
    const answer = await judgeOn(cfgFor(), async () => {
      calls++;
      if (status === 'throw') throw new Error('session failed');
      return { status, answer: AGREE.answer };
    });
    assert.equal(answer.verdict, 'broken', `${status} must be broken`);
    assert.equal(calls, 2, `${status} gets exactly one same-model retry`);
  }
  const f = await waiverFixture(config('L3'));
  let calls = 0;
  const out = await f.invoke(async () => {
    calls++;
    return { status: 'ok', answer: { verdict: 'disagree', reason: 'Evidence is insufficient.' } };
  });
  assert.equal(out.code, 1);
  assert.match(out.output, /disagreed: Evidence is insufficient/);
  assert.equal(calls, 1, 'a valid disagreement never retries or falls back');
  assert.equal((await f.rows()).length - f.before, 0);
});

test('clause 2: retry the same L3 model once, then advance the configured ladder and record only its agreement', async () => {
  const cfg = config('L3');
  const original = structuredClone(cfg);
  const f = await waiverFixture(cfg);
  const calls = [];
  const packets = [];
  const paths = [];
  const out = await f.invoke(async (opts) => {
    const entry = opts.cfg.levels.L3;
    calls.push([entry.provider ?? opts.cfg.provider, entry.model, entry.effort]);
    paths.push(opts.promptPath);
    packets.push(readFileSync(opts.promptPath, 'utf8'));
    return entry.model === LAST.model ? AGREE : INVALID;
  });
  assert.equal(out.code, 0, out.output);
  assert.deepEqual(calls, [
    ['anthropic', cfg.levels.L3.model, undefined], ['anthropic', cfg.levels.L3.model, undefined],
    ['xai', NEXT.model, 'high'], ['xai', NEXT.model, 'high'], ['anthropic', LAST.model, 'max'],
  ]);
  assert.equal(new Set(packets).size, 1, 'all five attempts see exactly the same packet');
  assert.equal(paths.every((p) => !existsSync(p)), true, 'packet is removed after recovery');
  const rows = await f.rows();
  assert.equal(rows.length - f.before, 1, 'only the final agreement writes a row');
  const waiver = rows.at(-1);
  assert.deepEqual([waiver.event, waiver.verdict, waiver.by], ['review.waived', 'agree', 'agent']);
  assert.equal(verifyRow(waiver, f.key).ok, true);
  assert.deepEqual(cfg, original, 'the pinned config stays unchanged');
});

test('clause 2 boundary: exhaust every L3 model with one retry each before giving up with zero writes', async () => {
  const cfg = config('L3');
  const f = await waiverFixture(cfg);
  const models = [];
  const out = await f.invoke(async (opts) => { models.push(opts.cfg.levels.L3.model); return INVALID; });
  assert.deepEqual(models, [cfg.levels.L3.model, cfg.levels.L3.model, NEXT.model, NEXT.model, LAST.model, LAST.model]);
  assert.equal(out.code, 1);
  assert.match(out.output, /judge answer broken/i);
  assert.match(out.output, /nothing was recorded/);
  assert.equal((await f.rows()).length - f.before, 0);
});

/** @param {boolean} recover */
async function reviewWith(recover) {
  const cfg = config('L2');
  const original = structuredClone(cfg);
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(2));
  const models = [];
  const packets = [];
  const rows = [];
  const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk: 0, workDir: freshDir('packets') }, {
    spawn: async (opts) => {
      const entry = (opts.cfg ?? cfg).levels.L2;
      models.push(entry.model);
      packets.push(readFileSync(opts.promptPath, 'utf8'));
      return { status: 'ok', exit_code: 0, provider: entry.provider ?? cfg.provider, model: entry.model,
        usage: { tokens_in: 100, tokens_out: 100 },
        answer: answerFor(['@@ -0,0 +1,2 @@'], { passed: recover && entry.model === LAST.model ? true : 'invalid' }) };
    },
    writeRow: async (row) => { rows.push(row); },
  });
  assert.deepEqual(cfg, original, 'review recovery does not mutate config');
  return { outcome, models, packets, rows, cfg };
}

test('clause 3: an invalid verdict retries once then uses every level fallback even with empty second_levels', async () => {
  const { outcome, models, packets, rows, cfg } = await reviewWith(true);
  assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
  assert.deepEqual(models, [cfg.levels.L2.model, cfg.levels.L2.model, NEXT.model, NEXT.model, LAST.model]);
  assert.equal(outcome.sessions[0].attempts, 5);
  assert.equal(outcome.sessions[0].model, LAST.model);
  assert.equal(new Set(packets).size, 1, 'all five reviewer attempts see the same packet');
  assert.equal(rows.filter((r) => r.event === 'review.schema_invalid').length, 4, 'all four broken answers are diagnosed');
});

test('clause 3 boundary: exhausted schema failures remain unavailable and never approve the file', async () => {
  const { outcome, models, rows, cfg } = await reviewWith(false);
  assert.deepEqual(models, [cfg.levels.L2.model, cfg.levels.L2.model, NEXT.model, NEXT.model, LAST.model, LAST.model]);
  assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'schema', false]);
  assert.equal(outcome.sessions[0].attempts, 6);
  assert.equal(rows.filter((r) => r.event === 'review.schema_invalid').length, 6);
});
