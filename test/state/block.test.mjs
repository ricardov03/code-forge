// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { captureStream, countOccurrences, fakeProbe, rowSink, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { runBlock } from '../../src/cli/block.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { briefPointer, checkScope, claimPath, closeBlock, openBlock, rebaseBlock } from '../../src/state/block.mjs';
import { readRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';
import { loadKey, signRow, verifyRow } from '../../src/state/signer.mjs';
import { dirtyFiles, git } from '../fixtures/repos/two-blocks/build.mjs';

const ACCEPTANCE = [{ clause: 'the thing works', tests: ['test/a.test.mjs'] }];
const PROBE = fakeProbe({ 4242: 'start-A' });

/** @param {string} ws @param {string} runId @param {(row: Record<string, any>) => Promise<unknown>} writeRow */
const start = (ws, runId, writeRow) => startRun({ workspace: ws, project: 'two-blocks', runId, workerPid: 4242, writeRow, probe: PROBE });

test('`block open` twice with overlapping globs is refused, and the registry keeps only the first', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await start(ws, 'r-open', writeRow);
    await openBlock({ runId: 'r-open', id: 'B1', level: 'L2', owned: ['src/state/**'], acceptance: ACCEPTANCE, writeRow });
    await assert.rejects(() => openBlock({ runId: 'r-open', id: 'B2', level: 'L1', owned: ['src/state/run.mjs'], acceptance: ACCEPTANCE, writeRow }), {
      code: 'overlap',
      message: 'block B2 owns src/state/run.mjs, which overlaps src/state/** owned by open block B1',
    });
    await assert.rejects(() => openBlock({ runId: 'r-open', id: 'B1', level: 'L2', owned: ['x.txt'], acceptance: ACCEPTANCE, writeRow }), { code: 'already-open' });
    assert.deepEqual(Object.keys((await readRun('r-open')).blocks), ['B1']);
  });
});

test('`block open` records base = HEAD, level, attempt, acceptance, forecast, and writes a signed dispatch row', async () => {
  await withFixture(async ({ ws, headSha }) => {
    const { rows, writeRow } = rowSink();
    await start(ws, 'r-rec', writeRow);
    const { block } = await openBlock({ runId: 'r-rec', id: 'B8', level: 'L2', owned: ['a.txt', 'b.txt'], acceptance: ACCEPTANCE, lines: 640, writeRow });
    assert.deepEqual({ ...block, opened_at: 'x' }, {
      block: 'B8', base_sha: headSha, owned_files: ['a.txt', 'b.txt'], level: 'L2', attempt: 1, opened_at: 'x', acceptance: ACCEPTANCE, lines_forecast: 640, status: 'open',
    });
    const dispatch = rows.filter((r) => r.event === 'dispatch');
    assert.equal(dispatch.length, 1);
    assert.deepEqual(verifyRow(dispatch[0], await loadKey('r-rec')), { ok: true });
    await assert.rejects(() => openBlock({ runId: 'r-rec', id: 'B9', level: 'L1', owned: ['c.txt'], acceptance: [{ clause: 'x', tests: [] }], writeRow }), { code: 'bad-acceptance' });
    await assert.rejects(() => openBlock({ runId: 'r-rec', id: 'B9', level: 'L1', owned: ['../c.txt'], acceptance: ACCEPTANCE, writeRow }), { code: 'bad-owned' });
  });
});

test('`block rebase` refuses a base that is not an ancestor of HEAD, stops on a commit touching an owned file, and moves a clean base', async () => {
  await withFixture(async ({ ws, baseSha, headSha, sideSha }) => {
    const { writeRow } = rowSink();
    await start(ws, 'r-reb', writeRow);
    await openBlock({ runId: 'r-reb', id: 'S', level: 'L1', owned: ['b.txt'], acceptance: ACCEPTANCE, base: sideSha, writeRow });
    await assert.rejects(() => rebaseBlock({ runId: 'r-reb', id: 'S', writeRow }), { code: 'base-not-ancestor' });
    assert.equal((await readRun('r-reb')).blocks.S.base_sha, sideSha);

    await openBlock({ runId: 'r-reb', id: 'E', level: 'L1', owned: ['e.txt'], acceptance: ACCEPTANCE, base: baseSha, writeRow });
    await assert.rejects(() => rebaseBlock({ runId: 'r-reb', id: 'E', writeRow }), { code: 'owned-conflict' });
    assert.equal((await readRun('r-reb')).blocks.E.base_sha, baseSha);

    await openBlock({ runId: 'r-reb', id: 'X', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, base: baseSha, writeRow });
    assert.deepEqual(await rebaseBlock({ runId: 'r-reb', id: 'X', writeRow }), { old_base: baseSha, new_base: headSha });
    assert.equal((await readRun('r-reb')).blocks.X.base_sha, headSha);
    await assert.rejects(() => rebaseBlock({ runId: 'r-reb', id: 'X', head: '--output=/tmp/x', writeRow }), { code: 'bad-ref' });
  });
});

test('`block rebase` counts BOTH sides of a rename: moving an owned file away upstream is an owned-conflict', async () => {
  await withFixture(async ({ ws, headSha }) => {
    const { writeRow } = rowSink();
    await start(ws, 'r-ren', writeRow);
    await openBlock({ runId: 'r-ren', id: 'E', level: 'L1', owned: ['e.txt'], acceptance: ACCEPTANCE, writeRow });
    git(['mv', 'e.txt', 'renamed.txt'], ws);
    git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'rename e.txt'], ws);
    await assert.rejects(() => rebaseBlock({ runId: 'r-ren', id: 'E', writeRow }), { code: 'owned-conflict', message: /e\.txt/ });
    assert.equal((await readRun('r-ren')).blocks.E.base_sha, headSha);
  });
});

test('owned entries: `.`/`..` inside a brace alternative, character classes and extglobs are refused; claim takes exact paths only', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await start(ws, 'r-own', writeRow);
    for (const owned of ['{..,src}/x.mjs', './src/x.mjs', 'src/@(a|b)/**', 'src/{a', '']) {
      await assert.rejects(() => openBlock({ runId: 'r-own', id: 'B1', level: 'L1', owned: [owned], acceptance: ACCEPTANCE, writeRow }), { code: 'bad-owned' }, owned);
    }
    await assert.rejects(() => openBlock({ runId: 'r-own', id: 'B1', level: 'L1', owned: ['src/[ab]*.ts'], acceptance: ACCEPTANCE, writeRow }), {
      code: 'bad-owned',
      message:
        'owned files: invalid path "src/[ab]*.ts": a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}',
    });
    const route = await openBlock({ runId: 'r-own', id: 'R', level: 'L1', owned: ['pages/[id].vue', 'app/(group)/page.tsx'], acceptance: ACCEPTANCE, writeRow });
    assert.deepEqual(route.block.owned_files, ['pages/[id].vue', 'app/(group)/page.tsx']);
    await openBlock({ runId: 'r-own', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow });
    await assert.rejects(() => claimPath({ runId: 'r-own', id: 'B1', file: '*.txt', writeRow }), { code: 'bad-owned' });
    assert.deepEqual((await readRun('r-own')).blocks.B1.owned_files, ['a.txt']);
  });
});

test('a failed ledger write leaves the registry unchanged', async () => {
  await withFixture(async ({ ws }) => {
    const { writeRow } = rowSink();
    await start(ws, 'r-fail', writeRow);
    const failing = async () => {
      throw new Error('disk full');
    };
    await assert.rejects(() => openBlock({ runId: 'r-fail', id: 'B1', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow: failing }), /disk full/);
    assert.deepEqual((await readRun('r-fail')).blocks, {});
  });
});

test('brief pointer: exact text, ≤ 200 bytes; a pointer over 200 bytes is refused', () => {
  const pointer = briefPointer('briefs/B8.md', '# B8 brief\nDo the thing.\nReport.\n');
  assert.equal(pointer, 'BRIEF briefs/B8.md lines=3 sha=7b6bd4a6 <<<EOM>>>');
  assert.ok(Buffer.byteLength(pointer) <= 200);
  assert.equal(briefPointer('b.md', 'no newline').includes('lines=1 '), true);
  // `BRIEF ` (6) + path + ` lines=1 sha=xxxxxxxx <<<EOM>>>` (31): a 163-byte path is exactly 200.
  const atLimit = briefPointer(`b/${'x'.repeat(161)}`, 'a\n');
  assert.equal(Buffer.byteLength(atLimit), 200);
  assert.throws(() => briefPointer(`b/${'x'.repeat(162)}`, 'a\n'), { code: 'pointer-too-long' });
  assert.throws(() => briefPointer(`b/${'é'.repeat(81)}`, 'a\n'), { code: 'pointer-too-long' }); // 164 bytes, 83 chars
  assert.throws(() => briefPointer('my brief.md', 'a\n'), { code: 'bad-brief-path' });
});

test('orphans reach the registry through checkScope and refuse every gate; `block claim` clears them', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    await start(ws, 'r-scope', writeRow);
    await openBlock({ runId: 'r-scope', id: 'X', level: 'L1', owned: ['a.txt', 'b.txt'], acceptance: ACCEPTANCE, writeRow });
    await openBlock({ runId: 'r-scope', id: 'Y', level: 'L1', owned: ['c.txt'], acceptance: ACCEPTANCE, writeRow });
    const files = dirtyFiles(ws);
    assert.deepEqual(await checkScope({ runId: 'r-scope', id: 'X', files, writeRow }), { ok: false, scope: ['a.txt'], orphans: ['d.txt'] });
    assert.deepEqual(await checkScope({ runId: 'r-scope', id: 'Y', files, writeRow }), { ok: false, scope: ['c.txt'], orphans: ['d.txt'] });
    assert.deepEqual((await readRun('r-scope')).orphans, ['d.txt']);
    assert.deepEqual(rows.filter((r) => r.event === 'block.orphan').map((r) => r.path), ['d.txt']);
    await assert.rejects(() => claimPath({ runId: 'r-scope', id: 'Y', file: 'a.txt', writeRow }), { code: 'overlap' });
    await claimPath({ runId: 'r-scope', id: 'X', file: 'd.txt', writeRow });
    assert.deepEqual((await readRun('r-scope')).orphans, []);
    assert.deepEqual(await checkScope({ runId: 'r-scope', id: 'Y', files, writeRow }), { ok: true, scope: ['c.txt'], orphans: [] });
  });
});

test('closeBlock: clean ⇒ closed; forged or unsigned gate row ⇒ ledger.tamper; pinned pid mismatch ⇒ worker.replaced', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    await start(ws, 'r-close', writeRow);
    for (const id of ['A', 'F', 'U', 'W']) {
      await openBlock({ runId: 'r-close', id, level: 'L1', owned: [`${id}.txt`], acceptance: ACCEPTANCE, writeRow });
    }
    const key = await loadKey('r-close');
    const approved = (/** @type {string} */ block) => signRow({ event: 'review.approved', run: 'r-close', block, path: `${block}.txt` }, key);
    const ledger = [...rows, approved('A'), { ...approved('F'), path: 'other.txt' }, { event: 'review.approved', run: 'r-close', block: 'U' }];
    const close = (/** @type {string} */ id, /** @type {number} */ livePid) => closeBlock({ runId: 'r-close', id, rows: ledger, writeRow, livePid, probe: PROBE });

    assert.deepEqual(await close('A', 4242), { ok: true, status: 'closed' });
    assert.deepEqual(await close('F', 4242), { ok: false, status: 'stopped', event: 'ledger.tamper', reason: 'review.approved: mismatch' });
    assert.deepEqual(await close('U', 4242), { ok: false, status: 'stopped', event: 'ledger.tamper', reason: 'review.approved: unsigned' });
    const replaced = await close('W', 5151);
    assert.equal(replaced.event, 'worker.replaced');
    const blocks = (await readRun('r-close')).blocks;
    assert.deepEqual(Object.fromEntries(Object.entries(blocks).map(([id, b]) => [id, b.status])), { A: 'closed', F: 'stopped', U: 'stopped', W: 'stopped' });
    assert.deepEqual(rows.filter((r) => ['ledger.tamper', 'worker.replaced', 'block.close'].includes(r.event)).map((r) => [r.event, r.block]), [
      ['block.close', 'A'], ['ledger.tamper', 'F'], ['ledger.tamper', 'U'], ['worker.replaced', 'W'],
    ]);
  });
});

test('closeBlock: every row verifies but the dispatch row is gone ⇒ ledger.tamper; an orphan ⇒ stays open', async () => {
  await withFixture(async ({ ws }) => {
    const { rows, writeRow } = rowSink();
    await start(ws, 'r-disp', writeRow);
    await openBlock({ runId: 'r-disp', id: 'D', level: 'L1', owned: ['a.txt'], acceptance: ACCEPTANCE, writeRow });
    await openBlock({ runId: 'r-disp', id: 'O', level: 'L1', owned: ['c.txt'], acceptance: ACCEPTANCE, writeRow });
    const key = await loadKey('r-disp');
    const withoutDispatch = [...rows.filter((r) => !(r.event === 'dispatch' && r.block === 'D')), signRow({ event: 'block.attempt', run: 'r-disp', block: 'D', attempt: 2 }, key)];
    assert.deepEqual(await closeBlock({ runId: 'r-disp', id: 'D', rows: withoutDispatch, writeRow, livePid: 4242, probe: PROBE }), {
      ok: false, status: 'stopped', event: 'ledger.tamper', reason: 'no verified dispatch row',
    });
    await checkScope({ runId: 'r-disp', id: 'O', files: ['c.txt', 'z.txt'], writeRow });
    assert.deepEqual(await closeBlock({ runId: 'r-disp', id: 'O', rows, writeRow, livePid: 4242, probe: PROBE }), { ok: false, status: 'open', reason: 'orphans: z.txt' });
    assert.equal((await readRun('r-disp')).blocks.O.status, 'open');
  });
});

test('CLI `block`: unknown subcommand ⇒ usage + exit 2 without touching the run; missing --level ⇒ 2; brief outside the workspace ⇒ 2; close refusal ⇒ 1 with one stderr line', async () => {
  await withFixture(async ({ ws, home }) => {
    const usage = captureStream();
    assert.equal(await runBlock(['opne', 'B1', '--run', 'r-missing'], { stdout: captureStream(), stderr: usage }), 2);
    assert.equal(countOccurrences(usage.text, 'usage: code-forge block open'), 1);

    await start(ws, 'r-cli2', async () => {});
    await writeFile(path.join(ws, 'acc.yml'), '- clause: c\n  tests: [t]\n');
    const noLevel = captureStream();
    assert.equal(await runBlock(['open', 'B1', '--run', 'r-cli2', '--owned', 'a.txt', '--acceptance', path.join(ws, 'acc.yml')], { stdout: captureStream(), stderr: noLevel }), 2);
    assert.equal(noLevel.text, 'block open: block open needs --level, --owned and --acceptance\n');
    await writeFile(path.join(home, 'outside.md'), 'x\n');
    const outside = captureStream();
    const openArgs = ['open', 'B1', '--run', 'r-cli2', '--level', 'L1', '--owned', 'a.txt', '--acceptance', path.join(ws, 'acc.yml')];
    assert.equal(await runBlock([...openArgs, '--brief', path.join(home, 'outside.md')], { stdout: captureStream(), stderr: outside }), 2);
    assert.equal(outside.text, 'block open: --brief must be a file inside the workspace\n');
    assert.deepEqual((await readRun('r-cli2')).blocks, {});

    assert.equal(await runBlock(openArgs, { stdout: captureStream(), stderr: captureStream() }), 0);
    const refused = captureStream();
    assert.equal(await runBlock(['close', 'B1', '--run', 'r-cli2', '--worker-pid', '5151'], { stdout: captureStream(), stderr: refused, probe: PROBE }), 1);
    assert.equal(refused.text, 'block B1 stopped (worker.replaced): live worker pid 5151 is not the pinned pid 4242\n');
  });
});

test('CLI `block open` prints the brief pointer and refuses an overlapping owned set with exit 1', async () => {
  await withFixture(async ({ ws }) => {
    await start(ws, 'r-cli', async () => {});
    await mkdir(path.join(ws, 'briefs'));
    await writeFile(path.join(ws, 'briefs', 'B8.md'), '# B8 brief\nDo the thing.\nReport.\n');
    await writeFile(path.join(ws, 'acc.yml'), '- clause: the thing works\n  tests: [test/a.test.mjs]\n');
    const stdout = captureStream();
    const stderr = captureStream();
    const args = ['--run', 'r-cli', '--level', 'L2', '--acceptance', path.join(ws, 'acc.yml'), '--brief', path.join(ws, 'briefs', 'B8.md')];
    assert.equal(await runBlock(['open', 'B8', '--owned', 'a.txt', 'b.txt', ...args], { stdout, stderr }), 0, stderr.text);
    assert.equal(stdout.text.split('\n')[1], 'BRIEF briefs/B8.md lines=3 sha=7b6bd4a6 <<<EOM>>>');
    const refused = captureStream();
    assert.equal(await runBlock(['open', 'B9', '--owned', 'b.txt', ...args], { stdout: captureStream(), stderr: refused }), 1);
    assert.equal(refused.text, 'block open: block B9 owns b.txt, which overlaps b.txt owned by open block B8\n');
    const dispatch = (await readAllRows('two-blocks')).filter((r) => r.event === 'dispatch');
    assert.deepEqual(dispatch.map((r) => [r.block, r.owned_files]), [['B8', ['a.txt', 'b.txt']]]);

    // `block close` reads the REAL ledger back (B6 added tokens_source/cost_source after signing).
    // B12b: the changed owned file a.txt needs a signed review.approved row for its current hash
    // (b.txt is unchanged); with no transcript the close WARNs and writes gate.transcript_missing.
    const unreviewed = captureStream();
    assert.equal(await runBlock(['close', 'B8', '--run', 'r-cli', '--worker-pid', '4242'], { stdout: unreviewed, stderr: unreviewed, probe: PROBE }), 1);
    assert.equal(unreviewed.text.split('\n').at(-2), 'block B8 open: unreviewed a.txt');
    await writeSigned('r-cli', (row) => appendRow(row, { slug: 'two-blocks' }), { event: 'review.approved', block: 'B8', file: 'a.txt', content_hash: contentHash(ws, 'a.txt') });
    const closed = captureStream();
    assert.equal(await runBlock(['close', 'B8', '--run', 'r-cli', '--worker-pid', '4242'], { stdout: closed, stderr: closed, probe: PROBE }), 0);
    assert.equal(closed.text, 'WARN block B8: no coder transcript found (--transcript, run record, .code-forge/runs/r-cli/B8.log); the transcript grep did not run\nblock B8 closed\n');
    assert.deepEqual((await readAllRows('two-blocks')).map((r) => r.event), ['dispatch', 'gate.transcript_missing', 'review.approved', 'gate.transcript_missing', 'block.close']);
    // B19: the live close row says the block completed (the report's cost_per_block reads it)
    assert.deepEqual((await readAllRows('two-blocks')).filter((r) => r.event === 'block.close').map((r) => [r.block, r.status]), [['B8', 'complete']]);
  });
});
