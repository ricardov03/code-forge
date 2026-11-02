import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { mock, test } from 'node:test';

const realBlock = await import('../../src/state/block.mjs');
/** @type {import('../../src/state/block.mjs').CloseBlocker | null} */
let extraBlocker = null;
mock.module('../../src/state/block.mjs', { namedExports: {
  ...realBlock,
  closeBlock: (opts) => realBlock.closeBlock({ ...opts, extraChecks: [
    ...(opts.extraChecks ?? []),
    // Simulate a future gate code through the real ready/close collection path.
    ...(extraBlocker ? [async () => ({ ok: false, reason: extraBlocker.detail, blockers: [extraBlocker] })] : []),
  ] }),
} });

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');

const PROBE = async (pid) => pid === 4242 ? 'start-A' : null;

async function cli(args, deps = {}) {
  let out = '', err = '';
  const code = await runBlock(args, {
    stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, probe: PROBE, ...deps,
  });
  return { code, out, err };
}

async function fixture(slug, clean = false, configOverrides = {}, openFlags = []) {
  const repo = realpathSync(freshDir(slug));
  const files = freshDir('ready-inputs');
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: ['node', '--test'] }, ...configOverrides };
  writeFile(repo, '.code-forge.yml', JSON.stringify(config));
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  writeFile(files, 'acceptance.json', JSON.stringify([{ clause: 'ready diagnostics', tests: ['ready'] }]));
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, probe: PROBE, config, writeRow });
  const opened = await cli(['open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, 'acceptance.json'), ...openFlags]);
  assert.deepEqual([opened.code, opened.err], [0, '']);
  writeFile(repo, 'a.mjs', 'export const a = 2;\n');
  if (clean) {
    await writeSigned(runId, writeRow, { event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(repo, 'a.mjs'), round: 1 });
    await writeSigned(runId, writeRow, { event: 'gate.done', block: 'B1', all_ok: true });
  } else writeFile(repo, 'orphan.mjs', 'export const orphan = 1;\n');
  // Deliberately omit the transcript: ready must not write a transcript-missing audit row.
  return {
    repo,
    runId,
    files,
    writeRow,
    invoke: (verb, extra = [], deps = {}) => cli([verb, 'B1', '--run', runId, '--worker-pid', '4242', ...extra], deps),
    rows: () => readAllRows(slug),
    record: () => readRun(runId),
  };
}

function expectedBlockers(runId) {
  return [
    { code: 'no_green_gates', file: null, detail: `no green gates run; run: code-forge gates run --run ${runId} --block B1`, fix: `code-forge gates run --run ${runId} --block B1` },
    { code: 'unowned_change', file: 'orphan.mjs', detail: 'orphan.mjs changed but no block of the run owns it — block claim it, or put it back', fix: `code-forge block claim B1 orphan.mjs --run ${runId}` },
    { code: 'unreviewed', file: 'a.mjs', detail: 'no review.approved row for a.mjs at its current content', fix: `code-forge review-file --wait-block B1 --run ${runId}` },
  ];
}

const blockerText = (blockers) => blockers.map((b) => `${b.code} ${b.file ?? '-'} ${b.detail}\n  fix: ${b.fix}\n`).join('');

test('ready and close omit reviewed files restored to base while retaining every other blocker', async () => {
  const f = await fixture('ready-reverted-base');
  await writeSigned(f.runId, f.writeRow, {
    event: 'review.approved', block: 'B1', file: 'a.mjs', content_hash: contentHash(f.repo, 'a.mjs'), round: 1,
  });
  for (const reverted of [true, false]) {
    writeFile(f.repo, 'a.mjs', reverted ? 'export const a = 1;\n' : 'export const a = 3;\n');
    const blockers = expectedBlockers(f.runId).slice(0, reverted ? 2 : 3);
    for (const verb of ['ready', 'close']) {
      const text = await f.invoke(verb);
      assert.equal(text.code, 1, text.err);
      assert.equal(text.out, blockerText(blockers) + (verb === 'ready' ? `block B1 not ready: ${blockers.length} blockers\n` : ''),
        `${verb}: reverted file contributes zero blockers; changed unapproved file contributes one`);
      const json = await f.invoke(verb, ['--json']);
      assert.equal(json.code, 1, json.err);
      assert.deepEqual(JSON.parse(json.out).blockers, blockers);
    }
    assert.equal((await f.record()).blocks.B1.status, 'open');
  }
});

test('block ready lists every close blocker with an exact fix command in text and JSON', async () => {
  const f = await fixture('ready-blockers');
  const expected = expectedBlockers(f.runId);
  const text = await f.invoke('ready');
  assert.equal(text.code, 1, text.err);
  assert.equal(text.out, `${blockerText(expected)}block B1 not ready: 3 blockers\n`, 'all three distinct blockers and exactly three fix lines');
  const json = await f.invoke('ready', ['--json']);
  assert.equal(json.code, 1, json.err);
  assert.equal(json.out.trim().split('\n').length, 1, 'one JSON object');
  assert.deepEqual(JSON.parse(json.out), { block: 'B1', ready: false, blockers: expected });
});

test('block ready boundary: an approved block with green gates has zero blockers', async () => {
  const f = await fixture('ready-clean', true);
  const result = await f.invoke('ready');
  assert.deepEqual(result, { code: 0, out: 'block B1 ready\n', err: '' });
  const json = await f.invoke('ready', ['--json']);
  assert.equal(json.code, 0, json.err);
  assert.deepEqual(JSON.parse(json.out), { block: 'B1', ready: true, blockers: [] });
  assert.equal((await f.record()).blocks.B1.status, 'open', 'ready does not close a clean block');
});

test('block close prints the same complete blocker list as block ready and refuses', async () => {
  const f = await fixture('ready-close');
  const expected = blockerText(expectedBlockers(f.runId));
  assert.equal((await f.invoke('ready')).out, `${expected}block B1 not ready: 3 blockers\n`);
  const result = await f.invoke('close');
  assert.equal(result.code, 1, result.err);
  assert.equal(result.out, expected, 'close prints all three blockers with the same details and fixes');
  assert.match(result.err, /^block B1 open:/m);
  assert.equal((await f.record()).blocks.B1.status, 'open', 'blocked close leaves the block open');
  const json = await f.invoke('close', ['--json']);
  assert.equal(json.code, 1, json.err);
  assert.deepEqual(JSON.parse(json.out), {
    block: 'B1', ok: false, reason: json.err.trim().slice('block B1 open: '.length), blockers: expectedBlockers(f.runId),
  });
  const clean = await fixture('ready-close-clean', true);
  const closed = await clean.invoke('close');
  assert.equal(closed.code, 0, closed.err);
  assert.equal(closed.out.endsWith('block B1 closed\n'), true, 'clean close still succeeds');
  assert.equal(closed.out.includes('  fix:'), false, 'clean close has zero blocker fixes');
});

test('stopped close JSON omits blockers when a dead worker stops the block', async () => {
  const f = await fixture('ready-close-stopped', true);
  const result = await f.invoke('close', ['--json'], { probe: async () => null });
  assert.equal(result.code, 1, result.err);
  assert.equal((await f.record()).blocks.B1.status, 'stopped', 'exercise a stopped close');
  assert.match(result.err, /^block B1 stopped \(worker.down\):/m);
  assert.deepEqual(JSON.parse(result.out), {
    block: 'B1', ok: false, reason: 'pinned worker pid 4242 is not running',
  });
});

test('block ready and refused close retain every lockfile blocker', async () => {
  const f = await fixture('ready-lockfiles', true);
  const files = ['npm-shrinkwrap.json', 'package-lock.json'];
  for (const file of files) {
    writeFile(f.repo, file, '{');
    const claimed = await f.invoke('claim', [file]);
    assert.equal(claimed.code, 0, claimed.err);
  }
  const blockers = files.map((file) => ({
    code: 'lockfile', file, detail: 'cannot read valid lock JSON',
    fix: `no automatic fix: lockfile; repair or regenerate ${file} as valid lock JSON with HTTPS resolved URLs on review.lockfile_registries and nonempty integrity for resolved packages`,
  }));
  const ready = await f.invoke('ready');
  assert.deepEqual(ready, { code: 1, out: `${blockerText(blockers)}block B1 not ready: 2 blockers\n`, err: '' });
  const json = await f.invoke('ready', ['--json']);
  assert.equal(json.code, 1, json.err);
  assert.deepEqual(JSON.parse(json.out), { block: 'B1', ready: false, blockers });
  const closed = await f.invoke('close');
  assert.equal(closed.code, 1, closed.err);
  assert.equal(closed.out, blockerText(blockers), 'close retains both lockfile diagnostics');
  assert.equal((await f.record()).blocks.B1.status, 'open');
  assert.equal((await f.rows()).some((r) => r.event === 'block.close'), false);
  for (const file of files) writeFile(f.repo, file, '{}');
  assert.deepEqual(await f.invoke('ready'), { code: 0, out: 'block B1 ready\n', err: '' });
});

test('light limits give manual recovery instructions in ready and close text and JSON', async () => {
  const f = await fixture('ready-light-limits', true, { light: { max_files: 1, max_lines: 1 } }, ['--kind', 'light']);
  const blockers = [{
    code: 'light_limits', file: null, detail: '1 files, 2 lines exceed light block limits',
    fix: 'no automatic fix: light_limits; stop this attempt, then reopen with --kind code --attempt <n+1> and the original base, ownership and acceptance, or split the block within light limits',
  }];
  for (const verb of ['ready', 'close']) {
    const text = await f.invoke(verb);
    assert.equal(text.code, 1, text.err);
    assert.equal(text.out, blockerText(blockers) + (verb === 'ready' ? 'block B1 not ready: 1 blockers\n' : ''));
    const json = await f.invoke(verb, ['--json']);
    assert.equal(json.code, 1, json.err);
    assert.deepEqual(JSON.parse(json.out).blockers, blockers);
  }
  assert.equal((await f.record()).blocks.B1.status, 'open');
});

for (const cause of ['ticket', 'sentinel', 'tests', 'unreadable', 'report', 'malformed', 'nonzero']) {
  test(`coder report ${cause} gives a specific manual repair in ready and close text and JSON`, async () => {
    const f = await fixture(`ready-report-${cause}`, true, { context: { require_coder_tests: true } });
    const report = path.join(f.files, 'report.md');
    const sentinel = '===BLOCK B1 COMPLETE===';
    const green = `TESTS node --test exit=0\n${sentinel}\n`;
    const cases = {
      ticket: { text: green, file: 'a.mjs', reason: 'no review-file ticket id for a.mjs', repair: `add the review-file ticket id for a.mjs to ${report}` },
      sentinel: { text: 'TESTS node --test exit=0\n', reason: `no ${sentinel} sentinel`, repair: `add ${sentinel} to ${report}` },
      tests: { text: `${sentinel}\n`, reason: 'no TESTS line', repair: `add TESTS <command> exit=<n> with the actual test result before ${sentinel} in ${report}` },
      unreadable: { reason: '--report cannot be read', repair: `restore readable report ${report} or correct --report` },
      report: { reason: 'context.require_coder_tests needs --report', repair: 'create a coder report and pass its path with --report' },
      malformed: { text: `TESTS broken\n${sentinel}\n`, reason: 'TESTS line malformed: TESTS broken', repair: `correct the malformed TESTS line to TESTS <command> exit=<n> with the actual test result before ${sentinel} in ${report}` },
      nonzero: { text: `TESTS node --test exit=1\n${sentinel}\n`, reason: 'TESTS exit=1', repair: `fix the failing tests, rerun them, and update the TESTS line in ${report} with the actual command and exit code` },
    };
    const expected = cases[cause];
    if (expected.text !== undefined) writeFile(f.files, 'report.md', expected.text);
    const flags = cause === 'report' ? [] : ['--report', report];
    const deps = cause === 'ticket' ? { resolveTicket: () => { throw new Error('ticket unavailable'); } } : {};
    const blockers = [{
      code: 'coder_report_failed', file: expected.file ?? null, detail: `coder report FAILED: ${expected.reason}`,
      fix: `no automatic fix: coder_report_failed; ${expected.repair}; rerun block ready`,
    }];
    for (const verb of ['ready', 'close']) {
      const text = await f.invoke(verb, flags, deps);
      assert.equal(text.code, 1, text.err);
      assert.equal(text.out, blockerText(blockers) + (verb === 'ready' ? 'block B1 not ready: 1 blockers\n' : ''));
      const json = await f.invoke(verb, [...flags, '--json'], deps);
      assert.equal(json.code, 1, json.err);
      assert.deepEqual(JSON.parse(json.out).blockers, blockers);
    }
    assert.equal((await f.record()).blocks.B1.status, 'open');
    // Apply the manual report repair; ready can only check the edited report.
    writeFile(f.files, 'report.md', `reviewed a.mjs ticket ${'a'.repeat(24)}\n${green}`);
    assert.deepEqual(await f.invoke('ready', ['--report', report], deps), { code: 0, out: 'block B1 ready\n', err: '' });
  });
}

test('review blockers without valid finding ids suggest reopening in ready and close', async () => {
  const f = await fixture('ready-malformed-finding', true);
  try {
    for (const code of ['not_approved', 'late_unruled', 'review_cap', 'autopilot_waiver_invalid']) {
      for (const finding of [undefined, null, '', { id: 'W1' }, 1, 'W1']) {
        extraBlocker = /** @type {any} */ ({ code, file: 'a.mjs', finding, detail: 'review remains blocked' });
        const valid = typeof finding === 'string' && finding.length > 0;
        const blockers = [{
          code, file: 'a.mjs', detail: 'review remains blocked', ...(valid ? { finding } : {}),
          fix: `code-forge block ${valid ? 'waive B1 W1' : 'reopen B1'} --run ${f.runId} --file a.mjs --reason '<reason>'`,
        }];
        for (const verb of ['ready', 'close']) {
          const text = await f.invoke(verb);
          assert.equal(text.code, 1, text.err);
          assert.equal(text.out, blockerText(blockers) + (verb === 'ready' ? 'block B1 not ready: 1 blockers\n' : ''));
          const json = await f.invoke(verb, ['--json']);
          assert.equal(json.code, 1, json.err);
          assert.deepEqual(JSON.parse(json.out).blockers, blockers);
        }
      }
    }
    assert.equal((await f.record()).blocks.B1.status, 'open');
  } finally {
    extraBlocker = null;
  }
});

test('unknown gate codes never suggest review-file in ready or close', async () => {
  const f = await fixture('ready-unknown-code', true);
  extraBlocker = { code: 'future_gate', detail: 'manual intervention required' };
  try {
    const blockers = [{ ...extraBlocker, file: null, fix: 'no automatic fix: future_gate' }];
    for (const verb of ['ready', 'close']) {
      const text = await f.invoke(verb);
      assert.equal(text.code, 1, text.err);
      assert.equal(text.out, blockerText(blockers) + (verb === 'ready' ? 'block B1 not ready: 1 blockers\n' : ''));
      const json = await f.invoke(verb, ['--json']);
      assert.equal(json.code, 1, json.err);
      assert.deepEqual(JSON.parse(json.out).blockers, blockers);
      assert.doesNotMatch(text.out + json.out, /review-file/);
    }
    assert.equal((await f.record()).blocks.B1.status, 'open');
  } finally {
    extraBlocker = null;
  }
});

test('block ready and refused close list a transcript rule break once per diagnostic', async () => {
  for (const delegated of [false, true]) {
    const f = await fixture(`ready-rule-break-${delegated}`, true, { close: { require_human: delegated } });
    const transcript = path.join(f.files, 'coder.log');
    writeFile(f.files, 'coder.log', 'safe\ngit stash\n');
    if (delegated) await startGrant({
      runId: f.runId, writeRow: f.writeRow,
      input: { allow: 'close:routine', delegate: 'L2', until: new Date(Date.now() + 60000).toISOString() },
    });
    const blockers = [{
      code: 'rule_break', file: null, detail: 'transcript line 2: git-stash',
      fix: `code-forge block close B1 --run ${f.runId} --decide --reason '<reason>' --transcript ${transcript}`,
    }];
    if (delegated) blockers.push({
      code: 'close-not-routine', file: null, detail: 'rule_break transcript line 2: git-stash',
      fix: blockers[0].fix,
    });
    const flags = ['--transcript', transcript];
    const ready = await f.invoke('ready', flags);
    assert.deepEqual(ready, { code: 1, out: `${blockerText(blockers)}block B1 not ready: ${blockers.length} blockers\n`, err: '' });
    const json = await f.invoke('ready', [...flags, '--json']);
    assert.equal(json.code, 1, json.err);
    assert.deepEqual(JSON.parse(json.out), { block: 'B1', ready: false, blockers });
    const closed = await f.invoke('close', flags);
    assert.equal(closed.code, 1, closed.err);
    assert.equal(closed.out, blockerText(blockers));
    assert.equal((await f.record()).blocks.B1.status, 'open');
    assert.equal((await f.rows()).some((r) => r.event === 'block.close'), false);
  }
});

test('block ready writes no ledger rows or run changes on either result', async () => {
  for (const clean of [false, true]) {
    const f = await fixture(`ready-readonly-${clean}`, clean);
    const before = { rows: await f.rows(), record: await f.record() };
    for (const flags of [[], ['--json']]) {
      const result = await f.invoke('ready', flags);
      assert.equal(result.code, clean ? 0 : 1, result.err);
      assert.deepEqual(await f.rows(), before.rows, 'zero ledger rows added, including transcript-missing rows');
      assert.deepEqual(await f.record(), before.record, 'run record unchanged');
    }
    const deadWorker = await f.invoke('ready', ['--json'], { probe: async () => null });
    assert.equal(deadWorker.code, 1, deadWorker.err);
    const blockers = JSON.parse(deadWorker.out).blockers;
    assert.equal(blockers.filter((b) => b.code === 'worker.down').length, 1, 'one dead-worker blocker');
    assert.equal(blockers.length, clean ? 1 : 4, 'worker failure does not hide other blockers');
    assert.deepEqual(await f.rows(), before.rows, 'worker failure writes zero ledger rows');
    assert.deepEqual(await f.record(), before.record, 'worker failure does not stop the block');
  }
});

test('block ready routine-close preview writes no ledger rows on either result', async () => {
  for (const waived of [false, true]) {
    const f = await fixture(`ready-routine-readonly-${waived}`, true, { close: { require_human: true } });
    if (waived) await writeSigned(f.runId, f.writeRow, { event: 'gate.reviews_waived', block: 'B1', by: 'human' });
    await startGrant({
      runId: f.runId, writeRow: f.writeRow,
      input: { allow: 'close:routine', delegate: 'L2', until: new Date(Date.now() + 60000).toISOString() },
    });
    const before = { rows: await f.rows(), record: await f.record() };
    for (const flags of [[], ['--json']]) {
      const result = await f.invoke('ready', flags);
      assert.equal(result.code, waived ? 1 : 0, result.err);
      assert.equal(result.err, '', 'missing transcript produces no audit warning in the preview');
      if (waived) assert.match(result.out, /close-not-routine/, 'routine policy runs even though the review gate passes');
      assert.equal((await f.rows()).length, before.rows.length, 'routine preview adds zero ledger rows');
      assert.deepEqual(await f.rows(), before.rows);
      assert.deepEqual(await f.record(), before.record);
    }
  }
});

test('each unproven file names the supported proof command in text and JSON', async () => {
  const f = await fixture('ready-unproven-files', true);
  const files = ['a.mjs', 'b module.mjs'];
  writeFile(f.repo, files[1], 'export const b = 2;\n');
  const claimed = await f.invoke('claim', [files[1]]);
  assert.equal(claimed.code, 0, claimed.err);
  for (const file of files) await writeSigned(f.runId, f.writeRow, {
    event: 'review.approved', block: 'B1', file, content_hash: contentHash(f.repo, file), round: 1, risk: 2,
  });
  const blockers = files.map((file) => ({
    code: 'unproven', file, detail: `${file} is high tier and has no proven red→green row covering it`,
    fix: `code-forge proof red-green B1 --run ${f.runId} --test '<test-file>'`,
  }));
  const ready = await f.invoke('ready');
  assert.deepEqual(ready, { code: 1, out: `${blockerText(blockers)}block B1 not ready: 2 blockers\n`, err: '' });
  const fixes = ready.out.split('\n').filter((line) => line.startsWith('  fix:'));
  assert.equal(fixes.length, 2);
  assert.equal(new Set(fixes).size, 1, 'one proof run covers all changed source files without an unsupported file flag');
  const json = await f.invoke('ready', ['--json']);
  assert.equal(json.code, 1, json.err);
  assert.deepEqual(JSON.parse(json.out), { block: 'B1', ready: false, blockers });
  const closed = await f.invoke('close');
  assert.equal(closed.code, 1, closed.err);
  assert.equal(closed.out, blockerText(blockers), 'close names the same file in each proof fix');
});
