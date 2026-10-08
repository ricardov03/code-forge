import { answerFor, cfgFor, flag, git, harness, lines, makeRepo, readRecords, stdinOf, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { planSessions, reviewFile } = await import('../../src/review/engine.mjs');
const { PacketError } = await import('../../src/review/packet.mjs');
const { reviewTicket } = await import('../../src/worker/engine.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');

const FILE = 'src/feature.mjs';
const HUNKS = ['@@ -0,0 +1,12 @@'];

/** A repo with one new 12-line file. */
async function fixture() {
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(12));
  return repo;
}

/**
 * Review FILE at `risk` on the fakes; every session answers `answer`.
 * @param {{risk?: number, cfg?: Record<string, any>, answer?: unknown, env?: Record<string, string>, setup?: (repo: string) => Promise<void>}} opts
 */
async function review({ risk, cfg = cfgFor(), answer = answerFor(HUNKS), env = {}, setup = async (/** @type {string} */ _repo) => {} }) {
  const repo = await fixture();
  await setup(repo);
  const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answer), ...env } });
  const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk, workDir: h.workDir }, { spawn: h.spawn, writeRow: h.writeRow });
  return { repo, outcome, recs: readRecords(h.records), h };
}

describe('adaptive session plans (§4.2), with fake CLIs', () => {
  test('risk < 1 ⇒ exactly 1 closed-book L2 quick session, packet on stdin, approved', async () => {
    const { repo, outcome, recs, h } = await review({ risk: 0.5 });
    assert.equal(recs.length, 1);
    assert.deepEqual(outcome.sessions.map((s) => [s.role, s.lens, s.status]), [['reviewer', 'quick', 'ok']]);
    const [rec] = recs;
    assert.equal(flag(rec.argv, '--model'), 'claude-opus-5-5');
    assert.equal(rec.argv.includes('--safe-mode'), true);
    assert.equal(flag(rec.argv, '--tools'), '');
    assert.equal(rec.stdin_is_pipe, true);
    assert.equal(stdinOf(rec).startsWith('# code-forge review packet\n## lens\n# Lens: quick'), true);
    assert.equal(rec.argv.some((/** @type {string} */ a) => a.includes('code-forge review packet')), false);
    assert.notEqual(rec.cwd, repo);
    assert.match(rec.cwd, /\/sessions\/reviewer-[0-9]+-[0-9a-f]{8}\/cwd$/); // a fresh session dir under the run root
    assert.equal(existsSync(rec.cwd), false); // removed at session end
    assert.deepEqual([outcome.status, outcome.approved, outcome.engine, outcome.depth], ['reviewed', true, 'adaptive', 'quick']);
    assert.equal(existsSync(h.workDir) && readdirSync(h.workDir).length, 0); // packet files removed
    assert.deepEqual(h.rows.map((r) => [r.event, r.depth_chosen, r.context_mode]), [['review.plan', 'quick', 'whole']]);
  });

  test('1 ≤ risk < 2 ⇒ exactly 1 L2 full session', async () => {
    const { outcome, recs } = await review({ risk: 1.5 });
    assert.equal(recs.length, 1);
    assert.deepEqual(outcome.sessions.map((s) => [s.role, s.lens, s.level]), [['reviewer', 'full', 'L2']]);
    assert.equal(stdinOf(recs[0]).includes('# Lens: full'), true);
  });

  test('risk ≥ 2 ⇒ 2 blind L2 lens sessions (A, B) + 1 L3 judge, judge last', async () => {
    const { outcome, recs } = await review({ risk: 2.5 });
    assert.equal(recs.length, 3);
    assert.deepEqual(outcome.sessions.map((s) => [s.role, s.lens, s.level]), [['reviewer', 'A', 'L2'], ['reviewer', 'B', 'L2'], ['judge', 'judge', 'L3']]);
    assert.deepEqual(recs.map((r) => flag(r.argv, '--model')), ['claude-opus-5-5', 'claude-opus-5-5', 'claude-fable-5-1']);
    const judgeIn = stdinOf(recs[2]);
    assert.deepEqual([judgeIn.includes('## reviewer A'), judgeIn.includes('## reviewer B'), judgeIn.includes('+export const v1 = 1;')], [true, true, false]);
    assert.deepEqual([outcome.status, outcome.approved, outcome.depth], ['reviewed', true, 'dual']);
  });

  test('boundaries: 0.999 ⇒ quick (1), 1 ⇒ full (1), 1.999 ⇒ full (1), 2 ⇒ A + B + judge (3)', async () => {
    /** @type {Array<[number, number, string[]]>} */
    const seen = [];
    for (const risk of [0.999, 1, 1.999, 2]) {
      const { outcome, recs } = await review({ risk });
      seen.push([risk, recs.length, outcome.sessions.map((/** @type {any} */ s) => s.lens)]);
    }
    assert.deepEqual(seen, [
      [0.999, 1, ['quick']],
      [1, 1, ['full']],
      [1.999, 1, ['full']],
      [2, 3, ['A', 'B', 'judge']],
    ]);
  });

  test('a failed review is unavailable, never approval, and a failed lens means no judge', async () => {
    const { outcome, recs } = await review({ risk: 2.5, answer: answerFor(['@@ -9 +9 @@']) });
    assert.equal(recs.length, 2);
    assert.deepEqual([outcome.status, outcome.approved, outcome.reason], ['unavailable', false, 'hunks_mismatch']);
  });
});

describe('needs_file (§4.2): one more round, only with safe, tracked files', () => {
  test('a `../` path in needs_file is refused: unavailable, 1 session, no second round', async () => {
    const { outcome, recs } = await review({ risk: 0, answer: answerFor(HUNKS, { needs_file: ['../etc/passwd'] }) });
    assert.equal(recs.length, 1);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'needs_file-refused', false]);
  });

  test('an ignored .env, a directory and an untracked file are named refusals; the secret never reaches a session', async () => {
    const setup = async (/** @type {string} */ repo) => {
      writeFile(repo, '.gitignore', '.env\n');
      writeFile(repo, 'lib/dep.mjs', 'export const dep = 1;\n');
      await git(['add', '.gitignore', 'lib'], repo); // the reviewed file stays new
      await git(['commit', '-q', '-m', 'deps'], repo);
      writeFile(repo, '.env', 'API_TOKEN=FAKE-secret-b12a-9d1c\n');
      writeFile(repo, 'notes.txt', 'untracked notes\n');
    };
    const answer = answerFor(HUNKS, { needs_file: ['.env', 'lib', 'notes.txt', 'lib/dep.mjs'] });
    const { outcome, recs } = await review({ risk: 0, answer, setup });
    assert.equal(recs.length, 2);
    const second = stdinOf(recs[1]);
    const attached = second.slice(second.indexOf('## attached files')).split('\n').filter((l) => l.startsWith('### '));
    assert.deepEqual(attached, ['### .env (refused: secret-like path)', '### lib (not a file)', '### notes.txt (refused: not tracked by git)', '### lib/dep.mjs']);
    assert.equal(recs.filter((r) => stdinOf(r).includes('FAKE-secret-b12a')).length, 0);
    assert.deepEqual([outcome.status, outcome.sessions[0].needs_file_round], ['reviewed', true]);
  });
});

describe('consensus (§4.4), with fake CLIs', () => {
  // B32: openai (Codex) cannot run a reviewer closed-book, so the second provider here is xai.
  test('two effective providers (anthropic + xai) + one L3 judge', async () => {
    const cfg = cfgFor({ multimodel: true, second_provider: 'xai', second_levels: { L2: { provider: 'xai', model: 'grok-4.7' } }, min_tokens_out: 5 }); // the fake grok reports 9 tokens out
    const { outcome, recs } = await review({ risk: 2, cfg });
    assert.equal(recs.length, 3);
    assert.deepEqual(outcome.sessions.map((s) => [s.lens, s.provider, s.model]), [
      ['full', 'anthropic', 'claude-opus-5-5'],
      ['full', 'xai', 'grok-4.7'],
      ['judge', 'anthropic', 'claude-fable-5-1'],
    ]);
    assert.deepEqual(recs.map((r) => r.name).sort(), ['claude', 'claude', 'grok']);
    assert.equal(cfg.levels.L2.model, 'claude-opus-5-5'); // the pinned config is restored after the swap
    assert.deepEqual([outcome.status, outcome.approved, outcome.engine], ['reviewed', true, 'consensus']);
  });

  test('the second reviewer falls back on its own ladder, never onto the first reviewer\'s provider (nor onto openai, B32)', async () => {
    const second = { provider: 'xai', model: 'grok-4.7', fallback: [{ provider: 'anthropic', model: 'claude-sonnet-5' }, { provider: 'openai', model: 'gpt-6-sol' }, { provider: 'xai', model: 'grok-4.6' }] };
    const cfg = cfgFor({ multimodel: true, second_provider: 'xai', second_levels: { L2: second }, min_tokens_out: 5 });
    const { outcome, recs } = await review({ risk: 2, cfg, env: { FAKE_402_MODELS: 'grok-4.7' } });
    assert.deepEqual(recs.map((r) => [r.name, flag(r.argv, '--model') ?? flag(r.argv, '-m')]).sort(), [
      ['claude', 'claude-fable-5-1'],
      ['claude', 'claude-opus-5-5'],
      ['grok', 'grok-4.6'],
      ['grok', 'grok-4.7'],
    ]);
    // the engine drops the anthropic entry; the spawner skips the openai one (still step 1 of the ladder)
    assert.deepEqual([outcome.sessions[1].provider, outcome.sessions[1].model, outcome.sessions[1].fallback_step], ['xai', 'grok-4.6', 2]);
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
  });

  test('the same effective provider twice is refused before any session', async () => {
    const cfg = cfgFor({ multimodel: true, second_provider: 'anthropic', second_levels: { L2: { model: 'claude-sonnet-5' } } });
    const { outcome, recs } = await review({ risk: 2, cfg });
    assert.equal(recs.length, 0);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['refused', 'consensus-same-provider', false]);
  });
});

describe('the B11 hook', () => {
  test('reviewTicket reviews the ticket file at the rules risk (0 ⇒ 1 quick session) and removes its packet dir', async () => {
    const repo = await fixture();
    const cfg = cfgFor();
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const ticket = { ticket: '0123456789abcdef01234567', run: 'r-b12a-test', block: 'B12a', file: FILE, content_hash: 'x', enqueued_at: '' };
    const outcome = await reviewTicket(ticket, /** @type {any} */ (h.ctx));
    assert.equal(readRecords(h.records).length, 1);
    assert.deepEqual([outcome.status, outcome.approved, outcome.engine, outcome.sessions.length, outcome.sessions[0].lens], ['reviewed', true, 'adaptive', 1, 'quick']);
    assert.equal(existsSync(`${h.runRootDir}/packets/${ticket.ticket}`), false);
  });

  test("the ids '../..', '' and 'x/y' are ticket-id-invalid with 0 sessions; a sibling packet dir and the run root survive", async () => {
    const repo = await fixture();
    const h = harness({ repoRoot: repo, cfg: cfgFor(), env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const sibling = path.join(h.runRootDir, 'packets', '0123456789abcdef0123456a');
    mkdirSync(sibling, { recursive: true });
    writeFileSync(path.join(sibling, 'packet.md'), 'another ticket in flight\n');
    /** @type {Array<[string, string, boolean, number]>} */
    const seen = [];
    for (const id of ['../..', '', 'x/y']) {
      const ticket = { ticket: id, run: 'r-b12a-test', block: 'B12a', file: FILE, content_hash: 'x', enqueued_at: '' };
      const outcome = await reviewTicket(ticket, /** @type {any} */ (h.ctx));
      seen.push([outcome.status, /** @type {string} */ (outcome.reason), outcome.approved, outcome.sessions.length]);
    }
    assert.deepEqual(seen, [
      ['unavailable', 'ticket-id-invalid', false, 0],
      ['unavailable', 'ticket-id-invalid', false, 0],
      ['unavailable', 'ticket-id-invalid', false, 0],
    ]);
    assert.equal(readRecords(h.records).length, 0);
    assert.equal(existsSync(path.join(sibling, 'packet.md')), true);
    assert.equal(existsSync(h.runRootDir), true);
    assert.equal(existsSync(path.dirname(h.runRootDir)), true);
  });

  test('once a run record exists, a ticket for a block it does not list is unavailable/block-unknown with 0 sessions; opening the block makes it reviewable', async () => {
    const repo = await fixture();
    const runId = 'r-b12a-blocks';
    const writeRow = async () => {};
    await startRun({ workspace: repo, project: 'b12a-test', runId, writeRow });
    const h = harness({ repoRoot: repo, cfg: cfgFor(), runId, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const ticket = { ticket: '0123456789abcdef01234569', run: runId, block: 'B12a', file: FILE, content_hash: 'x', enqueued_at: '' };
    const unknown = await reviewTicket(ticket, /** @type {any} */ (h.ctx));
    assert.deepEqual([unknown.status, unknown.reason, unknown.approved, unknown.sessions.length], ['unavailable', 'block-unknown', false, 0]);
    assert.equal(readRecords(h.records).length, 0);
    // a prototype-chain name is not a listed block either
    const proto = await reviewTicket({ ...ticket, block: 'constructor' }, /** @type {any} */ (h.ctx));
    assert.deepEqual([proto.status, proto.reason], ['unavailable', 'block-unknown']);

    await openBlock({ runId, id: 'B12a', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'reviewed', tests: ['t'] }], writeRow });
    const opened = await reviewTicket(ticket, /** @type {any} */ (h.ctx));
    assert.deepEqual([opened.status, opened.approved, opened.sessions.length], ['reviewed', true, 1]);
    assert.equal(readRecords(h.records).length, 1);
  });

  test('a ticket whose `file` is a tracked directory is refused bad-path through the hook: 0 sessions spawn, no packet dir is left', async () => {
    const repo = await fixture();
    await git(['add', '-A'], repo);
    await git(['commit', '-q', '-m', 'track src'], repo);
    writeFileSync(path.join(repo, FILE), lines(13)); // a change git would show under `src`
    const h = harness({ repoRoot: repo, cfg: cfgFor(), env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const ticket = { ticket: '0123456789abcdef0123456b', run: 'r-b12a-test', block: 'B12a', file: 'src', content_hash: 'x', enqueued_at: '' };
    await assert.rejects(reviewTicket(ticket, /** @type {any} */ (h.ctx)), (err) => {
      assert.deepEqual([err instanceof PacketError, /** @type {any} */ (err).code, /** @type {Error} */ (err).message], [true, 'bad-path', 'not a regular file']);
      return true;
    });
    assert.equal(readRecords(h.records).length, 0);
    assert.equal(existsSync(path.join(h.runRootDir, 'packets', ticket.ticket)), false);
  });

  test('a corrupt run record is unavailable, never a review against HEAD', async () => {
    const repo = await fixture();
    const runId = 'r-b12a-corrupt';
    mkdirSync(path.dirname(runRecordPath(runId)), { recursive: true });
    writeFileSync(runRecordPath(runId), '{ not json');
    const h = harness({ repoRoot: repo, cfg: cfgFor(), runId, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const ticket = { ticket: '0123456789abcdef01234568', run: runId, block: 'B12a', file: FILE, content_hash: 'x', enqueued_at: '' };
    const outcome = await reviewTicket(ticket, /** @type {any} */ (h.ctx));
    assert.equal(readRecords(h.records).length, 0);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'run-record-unreadable', false]);
  });
});

describe('B34 review topology: single reviewer at low risk, no multimodel for docs blocks', () => {
  const multi = (/** @type {Record<string, any>} */ extra = {}) =>
    cfgFor({ multimodel: true, second_provider: 'xai', second_levels: { L2: { provider: 'xai', model: 'grok-4.7' } }, min_tokens_out: 5, ...extra }); // B32: codex cannot review closed-book

  test('multimodel on, risk 1 ⇒ exactly 1 reviewer session, no judge (fake CLIs)', async () => {
    const { outcome, recs } = await review({ risk: 1, cfg: multi() });
    assert.equal(recs.length, 1);
    assert.deepEqual(outcome.sessions.map((s) => [s.role, s.lens, s.provider]), [['reviewer', 'full', 'anthropic']]);
    assert.deepEqual([outcome.engine, outcome.depth, outcome.approved], ['adaptive', 'full', true]);
  });

  test('multimodel on, risk 2 ⇒ 2 reviewers (2 providers) + 1 judge; single_reviewer_max_risk 2 ⇒ 1 session', async () => {
    assert.deepEqual(planSessions({ risk: 2, cfg: multi() }).sessions.map((s) => [s.role, s.slot]), [['reviewer', 'A'], ['reviewer', 'B'], ['judge', null]]);
    assert.equal(planSessions({ risk: 2, cfg: multi() }).mode, 'consensus');
    assert.deepEqual(planSessions({ risk: 2, cfg: multi({ single_reviewer_max_risk: 2 }) }).sessions.map((s) => s.lens), ['full']);
    assert.deepEqual(planSessions({ risk: 0, cfg: multi({ single_reviewer_max_risk: 0 }) }).sessions.map((s) => s.lens), ['quick']);
  });

  test('a docs block: multimodel off by default (risk 2 ⇒ adaptive A + B + judge, one provider, fake CLIs); multimodel_for_docs ⇒ consensus', async () => {
    const repo = await fixture();
    const cfg = multi();
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk: 2, kind: 'docs', workDir: h.workDir }, { spawn: h.spawn, writeRow: h.writeRow });
    const recs = readRecords(h.records);
    assert.equal(recs.length, 3);
    assert.deepEqual(recs.map((r) => r.name), ['claude', 'claude', 'claude']);
    assert.deepEqual([outcome.engine, outcome.depth], ['adaptive', 'dual']);
    assert.equal(planSessions({ risk: 2, cfg, kind: 'contract' }).mode, 'adaptive');
    assert.equal(planSessions({ risk: 2, cfg: multi({ multimodel_for_docs: true }), kind: 'docs' }).mode, 'consensus');
  });
});

describe('B34 fix round 1: kind derived from the file, contract blocks, fractional max risk', () => {
  const multi = (/** @type {Record<string, any>} */ extra = {}) =>
    cfgFor({ multimodel: true, second_provider: 'xai', second_levels: { L2: { provider: 'xai', model: 'grok-4.7' } }, min_tokens_out: 5, ...extra }); // B32: codex cannot review closed-book

  test('reviewFile with no kind on a .md file ⇒ docs ⇒ adaptive (1 provider, 3 sessions), not consensus', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'docs/guide.md', lines(12));
    const cfg = multi();
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const outcome = await reviewFile({ repoRoot: repo, file: 'docs/guide.md', base: null, cfg, risk: 2, workDir: h.workDir }, { spawn: h.spawn, writeRow: h.writeRow });
    assert.deepEqual(readRecords(h.records).map((r) => r.name), ['claude', 'claude', 'claude']);
    assert.deepEqual([outcome.engine, outcome.depth], ['adaptive', 'dual']);
  });

  test('reviewFile with no kind on a code file ⇒ consensus at risk 2 (2 providers + judge)', async () => {
    const { outcome, recs } = await review({ risk: 2, cfg: multi() });
    assert.equal(recs.length, 3);
    assert.deepEqual(recs.map((r) => r.name).sort(), ['claude', 'claude', 'grok']);
    assert.equal(outcome.engine, 'consensus');
  });

  test('a contract block is multimodel-off too; single_reviewer_max_risk 1.5 ⇒ risk 1.5 single, risk 1.6 consensus', () => {
    assert.deepEqual(planSessions({ risk: 3, cfg: multi(), kind: 'contract' }).sessions.map((s) => s.lens), ['A', 'B', 'judge']);
    assert.equal(planSessions({ risk: 3, cfg: multi(), kind: 'contract' }).mode, 'adaptive');
    assert.deepEqual(planSessions({ risk: 1.5, cfg: multi({ single_reviewer_max_risk: 1.5 }) }).sessions.map((s) => s.lens), ['full']);
    assert.equal(planSessions({ risk: 1.6, cfg: multi({ single_reviewer_max_risk: 1.5 }) }).mode, 'consensus');
  });

  test('the worker hook with no run record takes the ticket file\'s kind: a .md ticket ⇒ adaptive, never consensus', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'README.md', lines(12));
    // a high-tier path floor makes the rules risk 3, so the topology (not the risk) decides consensus
    const cfg = { ...multi(), proof: { tiers: { high: { paths: ['README.md'] } } } };
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)) } });
    const ticket = { ticket: '0123456789abcdef0123456b', run: 'r-b34-norun', block: 'B34', file: 'README.md', content_hash: 'x', enqueued_at: '' };
    const outcome = /** @type {Record<string, any>} */ (await reviewTicket(ticket, /** @type {any} */ (h.ctx)));
    assert.deepEqual([outcome.engine, outcome.depth, outcome.risk], ['adaptive', 'dual', 3]);
    assert.deepEqual(readRecords(h.records).map((r) => r.name), ['claude', 'claude', 'claude']);
  });
});

describe('B55 schema fallback, through the real spawner and the fake CLIs', () => {
  const INVALID = { passed: 'maybe', summary: 'not a review' };
  const second = { min_tokens_out: 5, second_levels: { L2: { provider: 'xai', model: 'grok-4.7' } } }; // the fake grok reports 9 tokens out
  /**
   * Review FILE where claude-opus-5-5 answers off-schema and every other model validly.
   * @param {{failures: number, fellBack: boolean}} seen - the packet's history.
   * @param {Record<string, any>} cfg @param {number} risk
   * @param {string | null} [throwFor] - a model whose spawn throws.
   */
  async function missed(seen, cfg, risk = 0, throwFor = null) {
    const repo = await fixture();
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)), FAKE_ANSWER_BY_MODEL: JSON.stringify({ 'claude-opus-5-5': INVALID }) } });
    const spawn = async (/** @type {Record<string, any>} */ o) => {
      if (throwFor !== null && o.cfg?.levels?.[o.level]?.model === throwFor) throw new Error('spawn exploded');
      return h.spawn(o);
    };
    const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk, workDir: h.workDir }, { spawn, writeRow: h.writeRow, schemaHistory: async () => seen });
    return { outcome, recs: readRecords(h.records), rows: h.rows, packetFiles: readdirSync(h.workDir) };
  }

  test('the packet\'s second miss: claude\'s off-schema answer is logged, then grok (second_levels.L2) reviews the same packet ⇒ approved', async () => {
    const { outcome, recs, rows, packetFiles } = await missed({ failures: 1, fellBack: false }, cfgFor(second));
    assert.deepEqual(recs.map((r) => [r.name, flag(r.argv, '--model') ?? flag(r.argv, '-m')]), [['claude', 'claude-opus-5-5'], ['grok', 'grok-4.7']]);
    assert.deepEqual(packetFiles, []); // the packet file is removed after the second-level try
    // the session's tokens are both sessions' (the fakes: claude 42 out, grok 9 out; in = packet bytes / 4 each)
    const inEach = Math.max(1, Math.ceil(Buffer.byteLength(stdinOf(recs[0])) / 4));
    assert.deepEqual(outcome.sessions.map((s) => [s.tokens_in, s.tokens_out]), [[2 * inEach, 42 + 9]]);
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
    assert.deepEqual(outcome.sessions.map((s) => [s.provider, s.model, s.schema_fallback, s.attempts]), [['xai', 'grok-4.7', { level: 'review.second_levels.L2', provider: 'xai', model: 'grok-4.7' }, 2]]);
    assert.deepEqual(rows.map((r) => r.event), ['review.plan', 'review.schema_invalid', 'review.schema_fallback']);
    const [, miss, fb] = rows;
    assert.deepEqual([miss.provider, miss.model, miss.second_level, miss.answer_kind, miss.top_keys, miss.errors_total], ['anthropic', 'claude-opus-5-5', false, 'json', { passed: 'string', summary: 'string' }, 3]);
    assert.deepEqual(miss.schema_errors.map((/** @type {any} */ e) => [e.path, e.keyword, e.params]), [
      ['', 'required', { missingProperty: 'reviewed_hunks' }],
      ['', 'required', { missingProperty: 'findings' }],
      ['/passed', 'type', { type: 'boolean' }],
    ]);
    assert.equal(JSON.stringify(rows).includes('not a review'), false); // the answer's text never reaches a row
    assert.deepEqual([fb.from_model, fb.to_provider, fb.to_model, fb.attempts, fb.packet_hash], ['claude-opus-5-5', 'xai', 'grok-4.7', 2, miss.packet_hash]);
  });

  test('a second-level spawn that throws: unavailable: exit, the packet file removed, the first session\'s tokens kept', async () => {
    const { outcome, recs, rows, packetFiles } = await missed({ failures: 1, fellBack: false }, cfgFor(second), 0, 'grok-4.7');
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'exit', false]);
    assert.deepEqual(recs.map((r) => r.name), ['claude']);
    assert.deepEqual(packetFiles, []);
    assert.deepEqual(rows.map((r) => r.event), ['review.plan', 'review.schema_invalid', 'review.schema_fallback', 'review.unavailable']);
    const inFirst = Math.max(1, Math.ceil(Buffer.byteLength(stdinOf(recs[0])) / 4));
    assert.deepEqual(outcome.sessions.map((s) => [s.provider, s.model, s.tokens_in, s.tokens_out, s.attempts]), [[null, null, inFirst, 42, 2]]);
  });

  test('the packet\'s first miss: one session, unavailable: schema, the answer logged', async () => {
    const { outcome, recs, rows } = await missed({ failures: 0, fellBack: false }, cfgFor(second));
    assert.equal(recs.length, 1);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'schema', false]);
    assert.deepEqual(rows.map((r) => r.event), ['review.plan', 'review.schema_invalid', 'review.unavailable']);
  });

  test('no ledger writer: nothing is logged, nothing is tried; the session keeps its schema verdict and model', async () => {
    const cfg = cfgFor(second);
    const repo = await fixture();
    const h = harness({ repoRoot: repo, cfg, env: { FAKE_ANSWER: JSON.stringify(answerFor(HUNKS)), FAKE_ANSWER_BY_MODEL: JSON.stringify({ 'claude-opus-5-5': INVALID }) } });
    const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk: 0, workDir: h.workDir }, { spawn: h.spawn, schemaHistory: async () => ({ failures: 1, fellBack: false }) });
    assert.deepEqual([outcome.status, outcome.reason], ['unavailable', 'schema']);
    assert.deepEqual(outcome.sessions.map((s) => [s.provider, s.model, s.status, s.reason, Object.hasOwn(s, 'schema_fallback')]), [['anthropic', 'claude-opus-5-5', 'unavailable', 'schema', false]]);
    assert.deepEqual(readRecords(h.records).map((r) => r.name), ['claude']);
    assert.equal(h.rows.length, 0);
  });

  test('consensus mode never swaps a provider: the second miss stays unavailable', async () => {
    const { outcome, recs, rows } = await missed({ failures: 1, fellBack: false }, cfgFor({ ...second, multimodel: true, second_provider: 'xai' }), 2);
    assert.deepEqual([outcome.engine, outcome.status, outcome.reason], ['consensus', 'unavailable', 'schema']);
    assert.deepEqual(recs.map((r) => r.name).sort(), ['claude', 'grok']);
    assert.equal(rows.filter((r) => r.event === 'review.schema_fallback').length, 0);
  });
});
