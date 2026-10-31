import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { mock, test } from 'node:test';

const realReview = await import('../../src/cli/review-file.mjs');
/** @type {{now: number, finishAt: number, finish: () => Promise<void>} | null} */
let slowReview = null;
// Keep the real join, queue, and results; advance only its polling clock.
mock.module('../../src/cli/review-file.mjs', { namedExports: { ...realReview,
  runReviewFile: (args, deps) => realReview.runReviewFile(args, slowReview ? { ...deps,
    now: () => slowReview.now,
    sleep: async (ms) => {
      slowReview.now += ms;
      if (slowReview.now >= slowReview.finishAt) {
        slowReview.finishAt = Infinity;
        await slowReview.finish();
      }
    },
  } : deps),
} });

const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, startRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { announceWorker, clearDone, enqueue, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');

const PROBE = async (/** @type {number} */ pid) => pid === process.pid ? 'test-worker' : null;

/** @param {string[]} args */
async function cli(args) {
  let stdout = '';
  let stderr = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { stdout += s; } },
    stderr: { write: (s) => { stderr += s; } }, probe: PROBE,
  });
  return { code, stdout, stderr };
}

/** @param {string} slug @param {{finding?: boolean, red?: boolean}} [options] */
async function fixture(slug, options = {}) {
  const repo = freshDir(slug);
  const files = freshDir('land-inputs');
  const gateOutput = path.join(files, 'gate-cwd');
  await git(['init', '-q', '-b', 'main'], repo);
  await git(['config', 'user.name', 'Fake Tester'], repo);
  await git(['config', 'user.email', 'fake@example.invalid'], repo);
  const config = {
    version: 1, project: { slug }, ...cfgFor(),
    proof: { isolation: 'worktree', worktree: { dir: freshDir('land-trees') } },
    context: { require_coder_tests: true },
    gates: {
      test: [process.execPath, '-e', `require('node:fs').writeFileSync(${JSON.stringify(gateOutput)}, process.cwd()); console.error('land gate tail'); process.exit(${options.red ? 1 : 0})`],
      lint: null, types: null, format: null,
    },
  };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  writeFile(files, 'acceptance.json', JSON.stringify([{ clause: 'land block', tests: ['block-land'] }]));
  writeFile(files, 'coder.log', '$ node --test\n');
  writeFile(files, 'report.txt', 'TESTS node --test exit=0\n===BLOCK B1 COMPLETE===\n');
  const runId = `r-${slug}`;
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: process.pid, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acceptance.json')]);
  assert.deepEqual([opened.code, opened.stderr], [0, ''], 'fixture opens exactly one block');
  const { worktree } = (await readRun(runId)).blocks.B1;
  writeFile(worktree.path, 'a.mjs', 'export const a = 2;\n');
  await git(['add', 'a.mjs'], worktree.path);
  await git(['commit', '-q', '-m', 'block changes'], worktree.path);
  const head = (await git(['rev-parse', 'HEAD'], worktree.path)).trim();
  const finding = { id: 'F1', file: 'a.mjs', severity: 'warning', line_start: 1, line_end: 1, claim: 'land finding', fix: 'fix the value' };
  const ticket = enqueue({ repoRoot: repo, tree: worktree.path, run: runId, block: 'B1', file: 'a.mjs' }).ticket;
  writeResult({ repoRoot: repo, runId, ticket, key: await loadKey(runId), result: { status: 'reviewed', approved: !options.finding, findings: options.finding ? [finding] : [] } });
  await writeSigned(runId, writeRow, {
    event: options.finding ? 'review.result' : 'review.approved', block: 'B1', file: 'a.mjs',
    content_hash: contentHash(worktree.path, 'a.mjs'), round: 1,
    ...(options.finding ? { ticket, status: 'reviewed', approved: false, open: [finding] } : {}),
  });
  announceWorker(repo, { pid: process.pid, run: runId });
  return {
    repo, files, runId, base, head, worktree, ticket, gateOutput,
    land: (extra = []) => cli(['land', 'B1', '--run', runId, '--report', path.join(files, 'report.txt'), '--transcript', path.join(files, 'coder.log'), ...extra]),
    rows: () => readAllRows(slug),
    cleanup: () => retractWorker(repo, process.pid),
  };
}

test('B185: clean land joins, gates, closes and merges; a finding stops before gates and close', async () => {
  for (const finding of [false, true]) {
    const f = await fixture(`b185-land-${finding}`, { finding });
    try {
      const result = await f.land(['--merge']);
      assert.equal(result.code, finding ? 1 : 0, result.stderr);
      const rows = await f.rows();
      assert.equal(rows.filter((r) => r.event === 'gate.done').length, finding ? 0 : 1, 'exactly one gate run only after a clean join');
      assert.equal(rows.filter((r) => r.event === 'block.close').length, finding ? 0 : 1, 'exactly one close only after green gates');
      assert.equal((await readRun(f.runId)).blocks.B1.status, finding ? 'open' : 'closed');
      if (finding) {
        assert.match(result.stderr, /join/);
        assert.match(result.stderr, /F1.*land finding/s);
        assert.ok(result.stderr.includes(`review-file --wait ${f.ticket}`), 'stop names the findings command');
        assert.equal((await git(['rev-parse', 'main'], f.repo)).trim(), f.base, 'finding leaves base unchanged');
      } else {
        assert.equal(result.stdout.trim().split('\n').at(-1), 'block B1 landed');
        assert.deepEqual((await git(['rev-list', '--parents', '-n', '1', 'main'], f.repo)).trim().split(' ').slice(1), [f.base, f.head], 'base has exactly the two parents of a no-ff merge');
        assert.equal(readFileSync(f.gateOutput, 'utf8'), f.worktree.path, 'gates ran in the block worktree');
        assert.ok(rows.findIndex((r) => r.event === 'gate.done') < rows.findIndex((r) => r.event === 'block.close'), 'gate completion precedes close');
      }
    } finally { f.cleanup(); }
  }
});

test('B185: red gates name their tail and next command and leave block open and base unchanged', async () => {
  const f = await fixture('b185-red', { red: true });
  try {
    const result = await f.land(['--merge']);
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /gates.*land gate tail/s);
    assert.ok(result.stderr.includes(`gates run --run ${f.runId} --block B1 --changed`));
    assert.equal((await f.rows()).filter((r) => r.event === 'gate.red').length, 1, 'one failing gate');
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 0, 'zero closes after red gates');
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
    assert.equal((await git(['rev-parse', 'main'], f.repo)).trim(), f.base);
  } finally { f.cleanup(); }
});

test('B185: merge refuses a dirty main checkout or a checkout on another branch', async () => {
  for (const dirty of [true, false]) {
    const f = await fixture(`b185-unsafe-${dirty}`);
    try {
      if (dirty) writeFile(f.repo, 'a.mjs', 'local main change\n');
      else await git(['checkout', '-q', '-b', 'other'], f.repo);
      const result = await f.land(['--merge']);
      assert.equal(result.code, 1, result.stderr);
      assert.match(result.stderr, dirty ? /merge.*dirty/s : /merge.*base branch.*main/s);
      assert.equal((await git(['rev-parse', 'main'], f.repo)).trim(), f.base);
      assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed', 'merge refusal happens after close');
      if (dirty) assert.equal(readFileSync(path.join(f.repo, 'a.mjs'), 'utf8'), 'local main change\n');
      else assert.equal((await git(['branch', '--show-current'], f.repo)).trim(), 'other');
    } finally { f.cleanup(); }
  }
});

test('B185: without merge closes and prints the merge command; JSON stays one line and help works', async () => {
  for (const json of [false, true]) {
    const f = await fixture(`b185-no-merge-${json}`);
    try {
      const help = await cli(['land', '--help']);
      assert.equal(help.code, 0);
      assert.match(help.stdout, /block land <id>.*--merge/);
      const result = await f.land(json ? ['--json'] : []);
      assert.equal(result.code, 0, result.stderr);
      assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
      assert.equal((await git(['rev-parse', 'main'], f.repo)).trim(), f.base, 'no merge leaves base unchanged');
      const command = json ? JSON.parse(result.stdout).merge : result.stdout.trim().split('; merge: ')[1];
      assert.match(command, /git .*merge --no-ff/);
      assert.ok(command.includes(f.worktree.branch), 'merge command names the block branch');
      if (json) {
        assert.equal(result.stdout.trim().split('\n').length, 1);
        assert.equal(JSON.parse(result.stdout).event, 'closed');
      } else assert.match(result.stdout.trim().split('\n').at(-1), /^block B1 closed; merge: /);
    } finally { f.cleanup(); }
  }
});

test('B185: close refusal names its reason and retry command and never merges', async () => {
  const f = await fixture('b185-close-refusal');
  try {
    writeFile(f.files, 'report.txt', 'TESTS node --test exit=1\n===BLOCK B1 COMPLETE===\n');
    const result = await f.land(['--merge', '--json']);
    assert.equal(result.code, 1, result.stderr);
    assert.match(result.stderr, /close.*TESTS exit=1/s);
    assert.ok(result.stderr.includes(`block close B1 --run ${f.runId}`));
    assert.equal(JSON.parse(result.stdout).step, 'close');
    assert.equal(result.stdout.trim().split('\n').length, 1);
    assert.equal((await f.rows()).filter((r) => r.event === 'block.close').length, 0);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
    assert.equal((await git(['rev-parse', 'main'], f.repo)).trim(), f.base);
  } finally { f.cleanup(); }
});

test('B190: slow reviews close within the default or explicit max, including a held findings round', async (t) => {
  for (const [index, extra] of [[], ['--max', '120s'], ['--max', '2m']].entries()) {
    const f = await fixture(`b190-slow-${index}`, { finding: index === 2 });
    const start = Date.now();
    try {
      if (index === 2) {
        writeFile(f.worktree.path, 'a.mjs', 'export const a = 3;\n');
        await git(['add', 'a.mjs'], f.worktree.path);
        await git(['commit', '-q', '-m', 'fix the finding'], f.worktree.path);
      } else clearDone({ repoRoot: f.repo, tree: f.worktree.path, run: f.runId, block: 'B1', file: 'a.mjs' });
      slowReview = { now: start, finishAt: start + 95_000, finish: async () => {
        const ticket = enqueue({ repoRoot: f.repo, tree: f.worktree.path, run: f.runId, block: 'B1', file: 'a.mjs' }).ticket;
        writeResult({ repoRoot: f.repo, runId: f.runId, ticket, key: await loadKey(f.runId), result: { status: 'reviewed', approved: true, findings: [] } });
        await writeSigned(f.runId, (row) => appendRow(row, { slug: `b190-slow-${index}` }), {
          event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(f.worktree.path, 'a.mjs'), round: 2,
        });
      } };
      t.mock.method(Date, 'now', () => slowReview.now);
      const result = await f.land(extra);
      assert.equal(result.code, 0, `slow review must close: ${result.stderr}`);
      assert.ok(slowReview.now - start >= 95_000, 'review finishes after the old 90s default');
      assert.equal((await readRun(f.runId)).blocks.B1.status, 'closed');
      const rows = await f.rows();
      assert.equal(rows.filter((r) => r.event === 'gate.done').length, 1, 'exactly one gate run');
      assert.equal(rows.filter((r) => r.event === 'block.close').length, 1, 'exactly one close after review approval');
    } finally { t.mock.restoreAll(); slowReview = null; f.cleanup(); }
  }
});

test('B190: max 1s times out at join and names a larger max in the land retry', async (t) => {
  const f = await fixture('b190-timeout');
  const start = Date.now();
  try {
    clearDone({ repoRoot: f.repo, tree: f.worktree.path, run: f.runId, block: 'B1', file: 'a.mjs' });
    slowReview = { now: start, finishAt: Infinity, finish: async () => {} };
    t.mock.method(Date, 'now', () => slowReview.now);
    const result = await f.land(['--max', '1s', '--merge', '--json']);
    assert.equal(result.code, 1, result.stderr);
    const refused = JSON.parse(result.stdout);
    assert.deepEqual([refused.step, refused.reason], ['join', 'wait_timeout']);
    const nextLines = result.stderr.split('\n').filter((line) => line.startsWith('next: '));
    assert.equal(nextLines.length, 1, 'exactly one next line');
    assert.ok(nextLines[0].includes(`block land B1 --run ${f.runId}`), 'retry repeats land for the same run');
    assert.ok(nextLines[0].includes(`--report ${path.join(f.files, 'report.txt')} --transcript ${path.join(f.files, 'coder.log')}`), 'retry preserves report and transcript');
    assert.match(nextLines[0], /--merge.*--json/);
    const duration = /--max (\d+(?:ms|s|m)?)/.exec(refused.next)?.[1];
    assert.ok(duration, 'retry names --max');
    const retryMs = duration.endsWith('m') ? Number(duration.slice(0, -1)) * 60_000 : realReview.parseMax(duration);
    assert.ok(retryMs > 1000, 'retry gives a larger wait budget');
    assert.equal(slowReview.now - start, 1000, 'total polling wait is exactly the shared 1s budget');
    assert.equal((await f.rows()).filter((r) => ['gate.done', 'block.close'].includes(r.event)).length, 0, 'no gates or closes on timeout');
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  } finally { t.mock.restoreAll(); slowReview = null; f.cleanup(); }
});

test('B190: malformed max is a duration usage error before landing', async () => {
  const f = await fixture('b190-bad-max');
  try {
    for (const value of ['soon', '-1s', '1.5m']) {
      const result = await f.land(['--max', value]);
      assert.equal(result.code, 2, result.stderr);
      assert.match(result.stderr, /--max must look like/);
      assert.equal(result.stdout, '', 'usage errors have no success output');
    }
    assert.equal((await f.rows()).filter((r) => ['gate.done', 'block.close'].includes(r.event)).length, 0);
    assert.equal((await readRun(f.runId)).blocks.B1.status, 'open');
  } finally { f.cleanup(); }
});
