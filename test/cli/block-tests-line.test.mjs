import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');
const { writeSigned } = await import('../../src/state/run.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { enqueue } = await import('../../src/worker/queue.mjs');

const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const A = 'src/a.mjs';

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} exit code, stdout, stderr */
async function cli(fn, args, deps = {}) {
  const stdout = capture();
  const stderr = capture();
  const code = await fn(args, { stdout, stderr, probe: PROBE, isTTY: false, ...deps });
  return [code, stdout.text, stderr.text];
}

/** @param {string} slug @param {string[]} extra - config lines after the levels. */
const configYaml = (slug, extra) =>
  ['version: 1', 'project:', `  slug: ${slug}`, 'provider: anthropic', 'engine: harness', 'levels:', '  L0: {model: claude-haiku-4-5-20251001}', '  L1: {model: claude-sonnet-5}', '  L2: {model: claude-opus-5-5}', '  L3: {model: claude-fable-5-1}', ...extra, ''].join('\n');

const ACC_DIR = freshDir('acceptance');
writeFile(ACC_DIR, 'acc.json', JSON.stringify([{ clause: 'TESTS diagnostics', tests: ['b114'] }]));
const ACC = path.join(ACC_DIR, 'acc.json');

/**
 * A committed repository with `src/a.mjs` and a transcript; `run start`
 * in process with the fake worker pid 4242; block B1 open owning `owned`.
 * @param {string} slug @param {{extra?: string[], owned?: string[]}} [opts]
 */
async function fixture(slug, { extra = [], owned = [A] } = {}) {
  const repo = realpathSync(freshDir('repo'));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  for (const f of owned) writeFile(repo, f, `export const v = '${f} 0';\n`);
  writeFile(repo, 'coder.log', '$ node --test\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const runId = `r-${slug}`;
  const [started, , startErr] = await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']);
  assert.equal(started, 0, startErr);
  const [opened, , openErr] = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC]);
  assert.equal(opened, 0, openErr);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  const where = repo;
  /** @param {string} file @param {string} content */
  const edit = (file, content) => writeFile(where, file, content);
  /** A signed row of B1 for `file` at its current hash in the block's tree. @param {string} file @param {Record<string, any>} row */
  const sign = (file, row) => writeSigned(runId, writeRow, { block: 'B1', file, content_hash: contentHash(where, file), ...row });
  /** The queue's ticket for `file`'s current content (what `review-file` would answer). @param {string} file */
  const ticket = (file) => enqueue({ repoRoot: repo, run: runId, block: 'B1', file }).ticket;
  /** @param {string} name @param {string} text @returns {string} a report file outside the repo */
  const report = (name, text) => {
    const dir = freshDir(`report-${name}`);
    writeFile(dir, 'report.md', text);
    return path.join(dir, 'report.md');
  };
  /** @param {string[]} [more] @param {Record<string, any>} [deps] */
  const close = (more = [], deps = {}) => cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more], deps);
  return { repo, runId, where, edit, sign, ticket, report, close, writeRow };
}

test('B114: malformed TESTS lines name the exact line (120 chars); missing, nonzero and valid lines keep their behavior', async () => {
  const fx = await fixture('b114-tests', { owned: [A], extra: ['context:', '  require_coder_tests: true'] });
  fx.edit(A, "export const v = 'a 1';\n");
  await fx.sign(A, { event: 'review.approved', round: 1 });
  const malformed = ['TESTS npm test exit=0 (12 pass, 0 fail)', 'TESTS npm test', 'TESTS npm test exit=00', 'TESTS npm test exit=256', 'TESTS npm test exit=-1', 'TESTS npm test exit=no', 'TESTS exit=0', `TESTS ${'x'.repeat(140)} exit=0 trailing`];
  for (const line of malformed) {
    const report = fx.report('bad', `${line}\n===BLOCK B1 COMPLETE===\n`);
    assert.deepEqual(await fx.close(['--report', report]), [1, '', `block B1 open: coder report FAILED: TESTS line malformed: ${line.slice(0, 120)}\n`]);
  }
  assert.deepEqual(await fx.close(['--report', fx.report('mixed', 'TESTS npm test exit=0\nTESTS broken\n===BLOCK B1 COMPLETE===\n')]), [1, '', 'block B1 open: coder report FAILED: TESTS line malformed: TESTS broken\n']);
  for (const text of ['===BLOCK B1 COMPLETE===\nTESTS npm test exit=0 trailing\n', 'TESTS npm test exit=0 trailing\n===BLOCK B2 COMPLETE===\n===BLOCK B1 COMPLETE===\n', 'Prose TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n']) {
    assert.deepEqual(await fx.close(['--report', fx.report('missing', text)]), [1, '', 'block B1 open: coder report FAILED: no TESTS line\n']);
  }
  assert.deepEqual(await fx.close(['--report', fx.report('nonzero', 'TESTS npm test exit=1\n===BLOCK B1 COMPLETE===\n')]), [1, '', 'block B1 open: coder report FAILED: TESTS exit=1\n']);
  const report = fx.report('valid', 'TESTS npm test exit=0 trailing\n===BLOCK B2 COMPLETE===\n  TESTS npm test exit=0  \n===BLOCK B1 COMPLETE===\nTESTS bad\n');
  assert.deepEqual(await fx.close(['--report', report]), [0, `reviewed ${A} ticket ${fx.ticket(A)}\nblock B1 closed\n`, '']);
});
