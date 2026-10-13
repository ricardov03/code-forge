import { commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// B63b (issue #13 item 5, issue #10 item 1): a coder proves a green test run with a
// `TESTS <command> exit=<n>` line before its COMPLETE sentinel, and `context.require_coder_tests`
// (authority, from the run-start snapshot) makes `block close` refuse a report without it.

const { parseSentinel } = await import('../../src/engines/sentinel.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, reloadRun, saveRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
const { validateConfig } = await import('../../src/config/validate.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// ── parseSentinel: the TESTS line ─────────────────────────────────────────────

test('B63b (1): a COMPLETE sentinel with no TESTS line has no tests key at all', () => {
  const result = parseSentinel('work\n===BLOCK B4 COMPLETE===\n', 'B4');
  assert.deepEqual(result, { status: 'complete', blockId: 'B4' });
  assert.equal(Object.hasOwn(/** @type {object} */ (result), 'tests'), false);
});

test('B63b (1): the TESTS line before the sentinel is returned on a COMPLETE and on a FAILED result', () => {
  assert.deepEqual(parseSentinel('TESTS npm test exit=0\n===BLOCK B4 COMPLETE===\n', 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 0 } });
  assert.deepEqual(parseSentinel('  TESTS node --test test/a.test.mjs exit=1  \n===BLOCK B4 FAILED: red===\n', 'B4'), {
    status: 'failed',
    blockId: 'B4',
    reason: 'red',
    tests: { command: 'node --test test/a.test.mjs', exit: 1 },
  });
  assert.deepEqual(parseSentinel('TESTS make check exit=255\n===BLOCK B4 COMPLETE===\n', 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'make check', exit: 255 } });
});

test('B63b (1): the LAST TESTS line before the sentinel wins', () => {
  const transcript = ['TESTS npm test exit=1', 'fixed the failing case', 'TESTS npm test -- --grep fixed exit=0', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(transcript, 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test -- --grep fixed', exit: 0 } });
  // and the reverse: a later red run overrides an earlier green one
  const red = ['TESTS npm test exit=0', 'TESTS npm test exit=2', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(red, 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 2 } });
});

test('B63b (1): a TESTS line after the returned sentinel is ignored', () => {
  assert.deepEqual(parseSentinel('===BLOCK B4 COMPLETE===\nTESTS npm test exit=0\n', 'B4'), { status: 'complete', blockId: 'B4' });
  const both = ['TESTS npm test exit=3', '===BLOCK B4 COMPLETE===', 'TESTS npm test exit=0'].join('\n');
  assert.deepEqual(parseSentinel(both, 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 3 } });
});

test('B63b (1): a TESTS line quoted in prose never matches', () => {
  for (const line of ['I ran TESTS npm test exit=0 earlier', '> TESTS npm test exit=0', '`TESTS npm test exit=0`', 'tests npm test exit=0', 'TESTS npm test exit=0 (green)']) {
    assert.deepEqual(parseSentinel(`${line}\n===BLOCK B4 COMPLETE===\n`, 'B4'), { status: 'complete', blockId: 'B4' }, line);
  }
});

test('B63b (1): an exit out of 0–255, non-numeric, signed, padded, or a missing command does not match', () => {
  // the line is trimmed first, then the command must be non-empty: `TESTS  exit=0` (only spaces
  // between TESTS and exit=) has no command and does not match
  for (const line of ['TESTS npm test exit=256', 'TESTS npm test exit=1000', 'TESTS npm test exit=-1', 'TESTS npm test exit=+0', 'TESTS npm test exit=ok', 'TESTS npm test exit=0x1', 'TESTS npm test exit=00', 'TESTS npm test exit=007', 'TESTS npm test exit=', 'TESTS exit=0', 'TESTS  exit=0', 'TESTS npm test exit = 0']) {
    assert.deepEqual(parseSentinel(`${line}\n===BLOCK B4 COMPLETE===\n`, 'B4'), { status: 'complete', blockId: 'B4' }, line);
  }
});

test('B63b (1): exit=0 then exit=300 in the same segment gives the earlier exit 0 — a malformed line erases nothing', () => {
  assert.deepEqual(parseSentinel('TESTS npm test exit=0\nTESTS npm test exit=300\n===BLOCK B4 COMPLETE===\n', 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 0 } });
});

test('B63b (1): another block id never returns a result, and a TESTS line before another block\'s sentinel never carries over to this block', () => {
  assert.equal(parseSentinel('TESTS npm test exit=0\n===BLOCK B7 COMPLETE===\n', 'B4'), null);
  const shared = ['TESTS npm test exit=0', '===BLOCK B7 COMPLETE===', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(shared, 'B4'), { status: 'complete', blockId: 'B4' });
  const sharedFailed = ['TESTS npm test exit=0', '===BLOCK B7 FAILED: red===', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(sharedFailed, 'B4'), { status: 'complete', blockId: 'B4' });
  // this block's own line, after the other block's sentinel, still counts
  const own = ['TESTS npm test exit=1', '===BLOCK B7 COMPLETE===', 'TESTS npm test exit=0', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(own, 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 0 } });
});

test('B63b (1): a TESTS line from an earlier attempt of the same block never carries over to the next sentinel', () => {
  const retried = ['TESTS npm test exit=0', '===BLOCK B4 FAILED: x===', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(retried, 'B4'), { status: 'complete', blockId: 'B4' });
  const twice = ['TESTS npm test exit=0', '===BLOCK B4 COMPLETE===', 'fix round', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(twice, 'B4'), { status: 'complete', blockId: 'B4' });
  const fresh = ['TESTS npm test exit=0', '===BLOCK B4 COMPLETE===', 'TESTS npm test exit=2', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(fresh, 'B4'), { status: 'complete', blockId: 'B4', tests: { command: 'npm test', exit: 2 } });
});

// ── the coder brief template agrees with the parser ──────────────────────────

test('B63b (4): the coder-brief template example parses to a passing TESTS line', () => {
  const brief = readFileSync(path.join(REPO, 'skill/templates/coder-brief.md'), 'utf8');
  const fence = /```\n([\s\S]*?)```/.exec(brief.slice(brief.indexOf('## Final report')));
  assert.ok(fence, 'the final-report section has an example block');
  assert.deepEqual(parseSentinel(fence[1], 'B7'), { status: 'complete', blockId: 'B7', tests: { command: 'npm test', exit: 0 } });
});

// ── block close under context.require_coder_tests ────────────────────────────

const T0 = new Date('2026-10-12T10:00:00.000Z');
const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const EXTERNAL = 'review:\n  external: {required: true, name: Grok}\n';
const REQUIRE = 'context:\n  require_coder_tests: true\n';

/** @param {string} slug @param {string} [extra] */
const configYaml = (slug, extra = '') =>
  [
    'version: 1',
    'project:',
    `  slug: ${slug}`,
    'provider: anthropic',
    'engine: harness',
    'levels:',
    '  L0: {model: claude-haiku-4-5-20251001}',
    '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}',
    '  L3: {model: claude-fable-5-1}',
    '',
  ].join('\n') + extra;

function capture() {
  let text = '';
  return {
    /** @param {string} s */
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** @param {Function} fn @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string]>} */
async function cli(fn, args, deps = {}) {
  const stream = capture();
  const code = await fn(args, { stdout: stream, stderr: stream, probe: PROBE, now: () => T0, ...deps });
  return [code, stream.text];
}

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A repo with `.code-forge.yml` (`extra` appended), `src/a.mjs` and a transcript, committed; `run
 * start`, then block B1 opened owning `src/a.mjs`, which is then changed.
 * @param {string} slug @param {string} extra @param {string[]} [owned]
 */
async function fixture(slug, extra, owned = ['src/a.mjs']) {
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
  writeFile(repo, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  assert.deepEqual(await cli(runRun, ['start', '--cwd', repo, '--run', runId, '--worker-pid', '4242']), [0, `run ${runId} started · engine harness · worker pid 4242\n`]);
  const [code, text] = await cli(runBlock, ['open', 'B1', '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC]);
  assert.equal(code, 0, text);
  writeFile(repo, 'src/a.mjs', 'export const a = 2;\n');
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  /** @param {string[]} more @param {Record<string, any>} [deps] */
  const close = (more = [], deps = {}) => cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more], deps);
  /** B96: the close with stdout and stderr kept apart. @param {string[]} more @returns {Promise<[number, string, string]>} */
  const closeSplit = async (more) => {
    const out = capture();
    const errs = capture();
    const code = await runBlock(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${repo}/coder.log`, ...more], { stdout: out, stderr: errs, probe: PROBE, now: () => T0 });
    return [code, out.text, errs.text];
  };
  /** @param {string} text @returns {string} the path of a report file outside the repository */
  const report = (text) => {
    const dir = freshDir('report');
    writeFile(dir, 'report.md', text);
    return path.join(dir, 'report.md');
  };
  /** the review.approved row the per-file review needs, for each file given */
  const approve = async (files = ['src/a.mjs']) => {
    for (const file of files) await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file, content_hash: contentHash(repo, file), round: 1 });
  };
  /** B96: the lines the close prints for the files the gate accepted, sorted by path */
  const reviewed = (files = ['src/a.mjs']) =>
    [...files]
      .sort()
      .map((file) => `reviewed ${file} ticket ${ticketId({ run: runId, block: 'B1', file, content_hash: contentHash(repo, file) })}\n`)
      .join('');
  return { runId, repo, writeRow, close, closeSplit, report, approve, reviewed, rows: () => readAllRows(slug) };
}

/** @param {Record<string, any>} row */
const strip = ({ mac: _m, ts: _t, ...rest }) => rest;
/** @param {Array<Record<string, any>>} rows @param {string} event */
const only = (rows, event) => rows.filter((r) => r.event === event);
const GREEN = 'TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n';

/**
 * @param {Awaited<ReturnType<typeof fixture>>} fx @param {[number, string]} result @param {string} reason
 */
async function refused(fx, result, reason) {
  assert.deepEqual(result, [1, `block B1 open: coder report FAILED: ${reason}\n`]);
  const rows = await fx.rows();
  assert.deepEqual(only(rows, 'block.close'), []);
  assert.deepEqual(only(rows, 'coder.tests'), []);
  assert.equal((await readRun(fx.runId)).blocks.B1.status, 'open');
}

test('B63b (3): under review.external the key still refuses every bad report — exact stderr, exit 1, no block.close, no waiver or coder.tests row', async () => {
  const fx = await fixture('b63b-ext', EXTERNAL + REQUIRE);
  await refused(fx, await fx.close([], { isTTY: false }), 'context.require_coder_tests needs --report');
  await refused(fx, await fx.close(['--report', path.join(freshDir('missing'), 'nope.md')]), '--report cannot be read');
  await refused(fx, await fx.close(['--report', fx.report('TESTS npm test exit=0\n')]), 'no ===BLOCK B1 COMPLETE=== sentinel');
  await refused(fx, await fx.close(['--report', fx.report('TESTS npm test exit=0\n===BLOCK B1 FAILED: red===\n')]), 'no ===BLOCK B1 COMPLETE=== sentinel');
  await refused(fx, await fx.close(['--report', fx.report('TESTS npm test exit=0\n===BLOCK B2 COMPLETE===\n')]), 'no ===BLOCK B1 COMPLETE=== sentinel');
  await refused(fx, await fx.close(['--report', fx.report('===BLOCK B1 COMPLETE===\nTESTS npm test exit=0\n')]), 'no TESTS line');
  await refused(fx, await fx.close(['--report', fx.report('I ran TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n')]), 'no TESTS line');
  await refused(fx, await fx.close(['--report', fx.report('TESTS npm test exit=0\nTESTS npm test exit=1\n===BLOCK B1 COMPLETE===\n')]), 'TESTS exit=1');
  // the B58 config waiver is a close row too: none left behind by any refusal above
  assert.deepEqual(only(await fx.rows(), 'gate.reviews_waived'), []);
});

test('B63b (3): under review.external a green report closes and writes the signed coder.tests row after the waiver, before block.close', async () => {
  const fx = await fixture('b63b-ext-ok', EXTERNAL + REQUIRE);
  assert.deepEqual(await fx.close(['--report', fx.report('TESTS npm test exit=1\nTESTS node --test test/a.test.mjs exit=0\n===BLOCK B1 COMPLETE===\n')], { isTTY: false }), [0, 'block B1 closed\n']);
  const rows = await fx.rows();
  const tests = only(rows, 'coder.tests');
  assert.deepEqual(tests.map(strip), [{ run: 'r-b63b-ext-ok', event: 'coder.tests', block: 'B1', command: 'node --test test/a.test.mjs', exit: 0, tokens_source: null, cost_source: null }]);
  assert.deepEqual(verifyRow(tests[0], await loadKey('r-b63b-ext-ok')), { ok: true });
  assert.deepEqual(rows.map((r) => r.event).slice(-3), ['gate.reviews_waived', 'coder.tests', 'block.close']);
  assert.equal((await readRun('r-b63b-ext-ok')).blocks.B1.status, 'closed');
});

test('B63b (3): --no-require-reviews waives reviews, not tests — refused without a TESTS line, closed with one', async () => {
  const fx = await fixture('b63b-human', REQUIRE);
  const yes = { isTTY: true, ui: { confirm: async () => true, isCancel: () => false } };
  await refused(fx, await fx.close(['--no-require-reviews'], yes), 'context.require_coder_tests needs --report');
  await refused(fx, await fx.close(['--no-require-reviews', '--report', fx.report('===BLOCK B1 COMPLETE===\n')], yes), 'no TESTS line');
  await refused(fx, await fx.close(['--no-require-reviews', '--report', fx.report('TESTS npm test exit=2\n===BLOCK B1 COMPLETE===\n')], yes), 'TESTS exit=2');
  // the TESTS check runs before the review check: no refusal above left the human waiver row
  assert.deepEqual(only(await fx.rows(), 'gate.reviews_waived'), []);
  assert.deepEqual(await fx.close(['--no-require-reviews', '--report', fx.report('TESTS npm test exit=0\n===BLOCK B1 COMPLETE===\n')], yes), [0, 'block B1 closed\n']);
  const rows = await fx.rows();
  assert.deepEqual(only(rows, 'gate.reviews_waived').map((r) => r.by), ['human']);
  const tests = only(rows, 'coder.tests');
  assert.deepEqual(tests.map(strip), [{ run: 'r-b63b-human', event: 'coder.tests', block: 'B1', command: 'npm test', exit: 0, tokens_source: null, cost_source: null }]);
  assert.deepEqual(verifyRow(tests[0], await loadKey('r-b63b-human')), { ok: true });
  assert.deepEqual(rows.map((r) => r.event).slice(-3), ['gate.reviews_waived', 'coder.tests', 'block.close']);
});

test('B63b (3): with the per-file review on, a reviewed and ticketed report still needs its TESTS line; a green one closes with the row', async () => {
  const fx = await fixture('b63b-reviews', REQUIRE);
  await fx.approve();
  await refused(fx, await fx.close(['--report', fx.report(`===BLOCK B1 COMPLETE===\n${fx.reviewed()}`)]), 'no TESTS line');
  // B96: a green report needs no ticket line; the close prints the tool's line
  assert.deepEqual(await fx.close(['--report', fx.report(GREEN)]), [0, `${fx.reviewed()}block B1 closed\n`]);
  const tests = only(await fx.rows(), 'coder.tests');
  assert.deepEqual(tests.map(strip), [{ run: 'r-b63b-reviews', event: 'coder.tests', block: 'B1', command: 'npm test', exit: 0, tokens_source: null, cost_source: null }]);
  assert.deepEqual(verifyRow(tests[0], await loadKey('r-b63b-reviews')), { ok: true });
});

test('B63b (3): with the key absent or false, close is as before — no --report needed, a TESTS line writes no row', async () => {
  for (const [slug, extra] of [
    ['b63b-absent', ''],
    ['b63b-false', 'context:\n  require_coder_tests: false\n'],
  ]) {
    const fx = await fixture(slug, extra);
    await fx.approve();
    assert.deepEqual(await fx.close([]), [0, `${fx.reviewed()}block B1 closed\n`]);
    assert.deepEqual(only(await fx.rows(), 'coder.tests'), []);
    const ticketed = await fixture(`${slug}-r`, extra);
    await ticketed.approve();
    // B96: a hand line that names the tool's own ticket warns nothing: stderr is empty
    assert.deepEqual(await ticketed.closeSplit(['--report', ticketed.report(`${GREEN}${ticketed.reviewed()}`)]), [0, `${ticketed.reviewed()}block B1 closed\n`, '']);
    const rows = await ticketed.rows();
    assert.deepEqual(only(rows, 'coder.tests'), []);
    assert.deepEqual(rows.map((r) => r.event).slice(-1), ['block.close']);
  }
});

test('B63b (2): close reads the key from the run-start snapshot, never the live config', async () => {
  // the block owns the config so its live edit is the block's (reviewed) work, not an unowned change
  const both = ['src/a.mjs', '.code-forge.yml'];
  // on at start, off in the live file (not reloaded): still required
  const on = await fixture('b63b-live-on', REQUIRE, both);
  writeFile(on.repo, '.code-forge.yml', configYaml('b63b-live-on'));
  await on.approve(both);
  await refused(on, await on.close([]), 'context.require_coder_tests needs --report');
  // off at start, on in the live file: not required, no row
  const off = await fixture('b63b-live-off', '', both);
  writeFile(off.repo, '.code-forge.yml', configYaml('b63b-live-off', REQUIRE));
  await off.approve(both);
  assert.deepEqual(await off.close([]), [0, `${off.reviewed(both)}block B1 closed\n`]);
  assert.deepEqual(only(await off.rows(), 'coder.tests'), []);
});

const IMMUTABLE =
  "run reload: context.require_coder_tests cannot change mid-run (whether close needs the coder's green test run is fixed by the owner before the run starts) — put it back, or end the run and start a new one\n";

test('B63b (2): run reload refuses to add, remove or flip context.require_coder_tests; another context key still reloads', async () => {
  const fx = await fixture('b63b-reload', REQUIRE);
  const before = (await readRun('r-b63b-reload')).config;
  for (const extra of ['context:\n  require_coder_tests: false\n', '', 'context:\n  max_lines: 50\n']) {
    writeFile(fx.repo, '.code-forge.yml', configYaml('b63b-reload', extra));
    assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b63b-reload']), [1, IMMUTABLE]);
  }
  assert.deepEqual((await readRun('r-b63b-reload')).config, before);
  assert.deepEqual(only(await fx.rows(), 'coder.tests'), []);
  writeFile(fx.repo, '.code-forge.yml', configYaml('b63b-reload', `${REQUIRE}  max_lines: 50\n`));
  const [code, text] = await cli(runRun, ['reload', '--run', 'r-b63b-reload']);
  assert.equal(code, 0, text);
  assert.ok(text.startsWith('run r-b63b-reload: config reloaded · 1 key changed\n  context.max_lines\n'), text);

  const none = await fixture('b63b-reload-add', '');
  writeFile(none.repo, '.code-forge.yml', configYaml('b63b-reload-add', REQUIRE));
  assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b63b-reload-add']), [1, IMMUTABLE]);
  assert.deepEqual(only(await none.rows(), 'run.reload'), []);
  assert.deepEqual(only(await none.rows(), 'coder.tests'), []);
  assert.deepEqual(only(await fx.rows(), 'coder.tests'), []);
});

test('B63b (2): a run recorded before snapshots cannot gain the key at its first reload; false is no change', async () => {
  const fx = await fixture('b63b-legacy', '');
  const record = await readRun('r-b63b-legacy');
  delete record.config;
  await saveRun(record);
  const base = { version: 1, provider: 'anthropic', engine: 'harness' };
  await assert.rejects(reloadRun({ runId: 'r-b63b-legacy', config: { ...base, context: { require_coder_tests: true } }, writeRow: fx.writeRow }), {
    code: 'immutable-key',
    message: IMMUTABLE.slice('run reload: '.length, -1),
  });
  assert.equal((await readRun('r-b63b-legacy')).config, undefined);
  const reloaded = await reloadRun({ runId: 'r-b63b-legacy', config: { ...base, context: { require_coder_tests: false } }, writeRow: fx.writeRow });
  assert.equal(reloaded.changed, null);
});

test('B63b (2): the schema takes a boolean require_coder_tests and refuses anything else', () => {
  const levels = { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } };
  const base = { version: 1, project: { slug: 'b63b-schema' }, provider: 'anthropic', engine: 'harness', levels };
  /** @param {unknown} value */
  const issues = (value) => validateConfig({ ...base, context: { require_coder_tests: value } }).errors.map((e) => [e.rule, e.path]);
  assert.deepEqual(issues(true), []);
  assert.deepEqual(issues(false), []);
  assert.deepEqual(issues('yes'), [['schema', '/context/require_coder_tests']]);
  assert.deepEqual(issues(1), [['schema', '/context/require_coder_tests']]);
});

test('B63b (2): the context settings take the key and refuse a non-boolean; it adds nothing to the settings the pack uses', async () => {
  const { contextSettings } = await import('../../src/context/pack.mjs');
  assert.deepEqual(contextSettings({ context: { require_coder_tests: true } }), { rules: [], lessons: null, commands: {}, maxLines: 300 });
  assert.deepEqual(contextSettings({ context: { rules: ['CLAUDE.md'], require_coder_tests: false } }), { rules: ['CLAUDE.md'], lessons: null, commands: {}, maxLines: 300 });
  assert.throws(() => contextSettings({ context: { require_coder_tests: 'yes' } }), { code: 'bad-context', message: 'context.require_coder_tests must be true or false' });
  assert.throws(() => contextSettings({ context: { require_coder_tests: 1 } }), { code: 'bad-context', message: 'context.require_coder_tests must be true or false' });
});
