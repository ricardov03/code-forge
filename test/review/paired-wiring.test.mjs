// Helpers pin HOME before any src import; all fixtures live under their per-file parent.
import { answerFor, cfgFor, commitAll, freshDir, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { newFileState, runRound } = await import('../../src/review/fixloop.mjs');
const { blockEntryFor, reviewTicket } = await import('../../src/worker/engine.mjs');
const { computeFileSet } = await import('../../src/gates/scope.mjs');
const { enqueue, readTicket } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');

const FILE = 'src/a.mjs';
const TEST = 'test/a.test.mjs';
const BODY = "test('handles empty input', () => {});\ntest('handles one input', () => {});\n";
const SUMMARY = '- test/a.test.mjs\n  - handles empty input\n  - handles one input';
const FINDING = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'critical', category: 'correctness', claim: 'input is wrong', evidence: 'e', fix: 'f' };

/** @param {string} text */
function hunksOf(text) {
  return [...text.matchAll(/^- (@@[^\n]+)$/gm)].map((m) => m[1]);
}

/** @param {Record<string, any>} [extra] */
function reviewer(extra = {}) {
  /** @type {string[]} */
  const packets = [];
  return {
    packets,
    spawn: async (/** @type {Record<string, any>} */ opts) => {
      const text = readFileSync(opts.promptPath, 'utf8');
      packets.push(text);
      return { status: 'ok', exit_code: 0, answer: answerFor(hunksOf(text), extra), usage: { tokens_in: 900, tokens_out: 300 } };
    },
  };
}

/** @param {string} packet */
function pairedOf(packet) {
  const sections = [...packet.matchAll(/^## paired\n([\s\S]*?)(?=^## |$(?![\s\S]))/gm)];
  assert.equal(sections.length, 1);
  return sections[0][1].trimEnd();
}

async function fixture() {
  const repo = await makeRepo();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, TEST, "test('old case', () => {});\n");
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, FILE, 'export const a = 2;\n');
  writeFile(repo, TEST, BODY);
  const context = { peers: [TEST], ownedFiles: [FILE, TEST], changedFiles: [FILE, TEST] };
  return { repo, base, context };
}

test('reproduction: changed source and test reach the round 1 reviewer as paired names', async () => {
  const { repo, base, context } = await fixture();
  const fake = reviewer();
  const result = await reviewFile({ repoRoot: repo, file: FILE, base, cfg: cfgFor(), risk: 1, workDir: freshDir('packets'), ...context }, fake);
  assert.deepEqual([result.status, result.approved, fake.packets.length], ['reviewed', true, 1]);
  assert.equal(pairedOf(fake.packets[0]), SUMMARY);
});

test('reproduction: a fix-hunk recheck carries the changed test names', async () => {
  const { repo, base, context } = await fixture();
  const fake = reviewer({ resolved: [{ id: 'F1', resolved: true, why: 'fixed' }] });
  const deps = {
    repoRoot: repo, base, cfg: cfgFor(), workDir: freshDir('recheck'), ...context, spawn: fake.spawn,
    writeRow: async () => undefined,
    review: async () => ({ status: 'reviewed', findings: [FINDING], sessions: [{ role: 'judge' }] }),
    jev: async () => ({ ok: true, answers: { resolved: { type: 'noul', noul: 0.2 } } }),
  };
  const state = await runRound(newFileState({ file: FILE, level: 'L2' }), deps);
  assert.deepEqual(state.open.map((f) => f.id), ['F1']);
  writeFile(repo, FILE, 'export const a = 3;\n');
  await runRound(state, deps);
  assert.deepEqual([state.status, fake.packets.length], ['complete', 1]);
  assert.deepEqual(hunksOf(fake.packets[0]), ['@@ -1 +1 @@']);
  assert.equal(pairedOf(fake.packets[0]), SUMMARY);
});

let runSeq = 0;
/** A real block entry, with the same tests field that block open --tests records.
 * @param {{repo: string, base: string, owned?: string[], tests?: string[], findings?: Record<string, any>[], keyed?: boolean, needs?: string[]}} opts
 */
async function workerFixture({ repo, base, owned = [FILE, TEST], tests, findings = [], keyed = true, needs = [] }) {
  const runId = `r-b98-${process.pid}-${++runSeq}`;
  await startRun({ workspace: repo, project: 'paired-fixture', runId, writeRow: async () => undefined });
  await openBlock({ runId, id: 'B98', level: 'L2', base, owned, ...(tests ? { tests } : {}), acceptance: [{ clause: 'paired tests reach the packet', tests: ['paired'] }], writeRow: async () => undefined });
  const key = await loadKey(runId);
  /** @type {Record<string, any>[]} */
  const rows = [];
  /** @type {string[]} */
  const packets = [];
  const ctx = {
    repoRoot: repo, runId, runRootDir: freshDir('worker'), cfg: cfgFor(), jevKey: null,
    ...(keyed ? { key, readRows: async () => rows } : {}),
    jev: async (/** @type {Record<string, any>} */ req) => {
      const id = Object.keys(req.questions)[0];
      return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
    },
    spawn: async (/** @type {Record<string, any>} */ opts) => {
      const text = readFileSync(opts.promptPath, 'utf8');
      packets.push(text);
      const recheck = opts.rowExtra.lens === 'recheck';
      const answer = answerFor(hunksOf(text), {
        findings: recheck ? [] : findings,
        resolved: recheck ? [{ id: 'F1', resolved: true, why: 'fixed' }] : [],
        needs_file: packets.length === 1 ? needs : [],
      });
      return { status: 'ok', exit_code: 0, answer, usage: { tokens_in: 900, tokens_out: 300 } };
    },
  };
  return {
    packets, rows, runId,
    round: async () => {
      const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B98', file: FILE });
      const writeRow = async (/** @type {Record<string, any>} */ row) => {
        rows.push(signRow({ ...row, run: runId, block: 'B98', file: FILE, content_hash: ticket.content_hash }, key));
      };
      return reviewTicket(readTicket(repo, ticket.ticket), { ...ctx, writeRow });
    },
  };
}

test('worker: owned changed stem peers reach round 1 and the signed fix-loop recheck', async () => {
  const { repo, base } = await fixture();
  writeFile(repo, 'test/unowned.test.mjs', "test('unowned case', () => {});\n");
  const w = await workerFixture({ repo, base, findings: [FINDING] });
  const first = await w.round();
  assert.deepEqual([first.status, first.approved, first.round, first.kind], ['reviewed', false, 1, 'full']);
  writeFile(repo, FILE, 'export const a = 3;\n');
  const second = await w.round();
  assert.deepEqual([second.status, second.approved, second.round, second.kind], ['reviewed', true, 2, 'recheck']);
  assert.equal(w.packets.length, 2);
  assert.deepEqual(w.packets.map(pairedOf), [SUMMARY, SUMMARY]);
  assert.equal(w.rows.filter((r) => r.event === 'review.approved').length, 1);
});

test('worker: explicit --tests pairs an unchanged owned test with a different stem', async () => {
  const repo = await makeRepo();
  const explicit = 'test/behavior.test.mjs';
  const unowned = 'test/foreign.test.mjs';
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, explicit, BODY);
  writeFile(repo, unowned, "test('outside this block', () => {});\n");
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, FILE, 'export const a = 2;\n');
  const expected = '- test/behavior.test.mjs\n  - handles empty input\n  - handles one input';
  for (const keyed of [false, true]) {
    const w = await workerFixture({ repo, base, owned: [FILE, explicit], tests: [explicit, unowned], keyed, findings: keyed ? [FINDING] : [] });
    const first = await w.round();
    assert.deepEqual([first.status, first.approved, w.packets.length], ['reviewed', !keyed, 1]);
    assert.equal(pairedOf(w.packets[0]), expected);
    if (keyed) {
      writeFile(repo, FILE, 'export const a = 3;\n');
      const second = await w.round();
      assert.deepEqual([second.status, second.approved, w.packets.length], ['reviewed', true, 2]);
      assert.equal(pairedOf(w.packets[1]), expected);
    }
  }
});

test('needs_file: worker attaches only the new untracked owned changed test', async () => {
  for (const keyed of [false, true]) {
    const repo = await makeRepo();
    const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
    writeFile(repo, FILE, 'export const a = 1;\n');
    writeFile(repo, TEST, BODY);
    writeFile(repo, 'test/foreign.test.mjs', "test('foreign body', () => {});\n");
    writeFile(repo, '.gitignore', 'test/ignored.test.mjs\n');
    writeFile(repo, 'test/ignored.test.mjs', "test('ignored body', () => {});\n");
    const w = await workerFixture({ repo, base, owned: ['src/**', 'test/a.test.mjs', 'test/ignored.test.mjs'], keyed, needs: [TEST, 'test/foreign.test.mjs', 'test/ignored.test.mjs'] });
    const result = await w.round();
    assert.deepEqual([result.status, result.approved, w.packets.length], ['reviewed', true, 2]);
    assert.equal(pairedOf(w.packets[0]), SUMMARY);
    const attachments = w.packets[1].split('## attached files (read-only, requested in needs_file)\n');
    assert.equal(attachments.length, 2);
    assert.equal(attachments[1], "### test/a.test.mjs\n1| test('handles empty input', () => {});\n2| test('handles one input', () => {});\n### test/foreign.test.mjs (refused: untracked, not owned by this block)\n### test/ignored.test.mjs (refused: untracked, not owned by this block)\n");
    assert.equal(w.packets[0], w.packets[1].slice(0, w.packets[0].length));
  }
});

test('worker: a failing peer listing still reviews, with no paired context or untracked attachment', async () => {
  for (const keyed of [false, true]) {
    const repo = await makeRepo();
    const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
    writeFile(repo, FILE, 'export const a = 1;\n');
    writeFile(repo, TEST, BODY);
    const w = await workerFixture({ repo, base, tests: [TEST], keyed, needs: [TEST] });
    // Lose the base object after dispatch: the new ticket file still has a readable
    // no-index diff, but listing this block's changed peers really fails in git.
    rmSync(path.join(repo, '.git', 'objects', base.slice(0, 2), base.slice(2)));
    await assert.rejects(computeFileSet({ cwd: repo, base }));
    const result = await w.round();
    assert.deepEqual([result.status, result.approved, w.packets.length], ['reviewed', true, 2]);
    assert.deepEqual(w.packets.map((p) => (p.match(/^## paired$/gm) ?? []).length), [0, 0]);
    assert.deepEqual(w.packets.map((p) => (p.match(/^## moved code$/gm) ?? []).length), [0, 0]);
    assert.equal(w.packets[1].split('## attached files (read-only, requested in needs_file)\n')[1], '### test/a.test.mjs (refused: untracked, not owned by this block)\n');
  }
});

test('worker: test_files drops each glob character before ownership supplies concrete test paths', async () => {
  const { repo, base } = await fixture();
  const tests = ['test/*.test.mjs', 'test/?.test.mjs', 'test/[a.test.mjs', 'test/a].test.mjs', 'test/{a,b}.test.mjs', 'test/a}.test.mjs', TEST];
  const w = await workerFixture({ repo, base, owned: [FILE, 'test'], tests });
  const entry = await blockEntryFor(w.runId, 'B98');
  assert.deepEqual(entry.testFiles, [TEST]);
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, w.packets.length], ['reviewed', true, 1]);
  assert.equal(pairedOf(w.packets[0]), SUMMARY);
});

test('needs_file: peers alone supplies the same changed list to paired context and attachment', async () => {
  const repo = await makeRepo();
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, TEST, BODY);
  const fake = reviewer();
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const result = await fake.spawn(opts);
    result.answer.needs_file = fake.packets.length === 1 ? [TEST] : [];
    return result;
  };
  const result = await reviewFile({ repoRoot: repo, file: FILE, base, cfg: cfgFor(), risk: 1, workDir: freshDir('peers-only'), peers: [TEST], ownedFiles: [FILE, TEST] }, { spawn });
  assert.deepEqual([result.status, result.approved, fake.packets.length], ['reviewed', true, 2]);
  assert.deepEqual(fake.packets.map(pairedOf), [SUMMARY, SUMMARY]);
  assert.equal(fake.packets[1].split('## attached files (read-only, requested in needs_file)\n')[1], "### test/a.test.mjs\n1| test('handles empty input', () => {});\n2| test('handles one input', () => {});\n");
});

test('no pairs: unowned, missing, nonregular and unsafe candidates leave packet bytes unchanged', async () => {
  const { repo, base } = await fixture();
  const link = 'test/link.test.mjs';
  const directory = 'test/directory.test.mjs';
  mkdirSync(path.join(repo, directory));
  symlinkSync(path.join(repo, TEST), path.join(repo, link));
  writeFile(repo, '.env', 'FAKE_VALUE=private\n');
  const trials = [
    {},
    { ownedFiles: [FILE], changedFiles: [TEST], testFiles: [TEST] },
    { ownedFiles: ['test/missing.test.mjs'], testFiles: ['test/missing.test.mjs'] },
    { ownedFiles: [link], testFiles: [link] },
    { ownedFiles: [directory], testFiles: [directory] },
    { ownedFiles: ['../outside.test.mjs', '.env'], testFiles: ['../outside.test.mjs', '.env'] },
  ];
  /** @type {string[]} */
  const packets = [];
  for (const context of trials) {
    const fake = reviewer();
    const result = await reviewFile({ repoRoot: repo, file: FILE, base, cfg: cfgFor(), risk: 0.5, workDir: freshDir('empty'), ...context }, fake);
    assert.deepEqual([result.status, result.approved, fake.packets.length], ['reviewed', true, 1]);
    packets.push(fake.packets[0]);
  }
  assert.equal(packets.length, 6);
  assert.deepEqual(packets.map((p) => (p.match(/^## paired$/gm) ?? []).length), [0, 0, 0, 0, 0, 0]);
  assert.equal(new Set(packets).size, 1);
  assert.deepEqual(packets.map((p) => p.split('FAKE_VALUE').length - 1), [0, 0, 0, 0, 0, 0]);
  const w = await workerFixture({ repo, base, owned: [FILE] });
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, w.packets.length], ['reviewed', true, 1]);
  assert.equal((w.packets[0].match(/^## paired$/gm) ?? []).length, 0);
});
