import { captureStream, withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFile, mkdir, mkdtemp, readdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';

/**
 * B33 end to end: `ledger add coder` records a signed manual row, `run status` shows spent /
 * budget, and the next session of the run is refused by `spawnSession` with 0 spawns.
 */

const { runLedger } = await import('../../src/cli/ledger.mjs');
const { runReport } = await import('../../src/cli/report.mjs');
const { runRun } = await import('../../src/cli/run.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { spawnSession } = await import('../../src/session/spawn.mjs');

const FAKE_CLAUDE = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'bin', 'fake-claude');
const TMP_PARENT = await mkdtemp(path.join(os.tmpdir(), 'cf-b33-'));
after(() => rm(TMP_PARENT, { recursive: true, force: true }));

test('manual coder spend is a signed row that run status, report and the budget stop all count', async () => {
  await withFixture(async ({ ws }) => {
    await appendFile(path.join(ws, '.code-forge.yml'), 'budget:\n  usd: 20\n');
    const started = captureStream();
    assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-b33', '--worker-pid', String(process.pid)], { stdout: started, stderr: captureStream() }), 0);

    const added = captureStream();
    const addErr = captureStream();
    const code = await runLedger(['add', 'coder', '--run', 'r-b33', '--block', 'B1', '--usd', '20.13', '--note', 'cloud session'], { stdout: added, stderr: addErr });
    assert.equal(code, 0, addErr.text);
    assert.equal(added.text, 'recorded coder spend $20.1300 for run r-b33 block B1 · run total $20.1300\n');
    const manual = (await readAllRows('two-blocks')).filter((r) => r.manual === true);
    assert.equal(manual.length, 1);
    assert.deepEqual(
      [manual[0].event, manual[0].role, manual[0].run, manual[0].block, manual[0].usd, manual[0].cost_source, manual[0].note],
      ['session', 'coder', 'r-b33', 'B1', 20.13, 'manual', 'cloud session'],
    );
    assert.deepEqual(verifyRow(manual[0], await loadKey('r-b33')), { ok: true });

    const status = captureStream();
    const statusErr = captureStream();
    assert.equal(await runRun(['status', '--run', 'r-b33'], { stdout: status, stderr: statusErr }), 0);
    const parsed = JSON.parse(status.text);
    assert.deepEqual([parsed.spent_usd, parsed.budget_usd, parsed.unknown_usd_sessions], [20.13, 20, 0]);
    assert.equal(statusErr.text, '', 'no unknown-price line when every session is priced');

    const records = path.join(TMP_PARENT, 'records');
    await rm(records, { recursive: true, force: true });
    await mkdir(records, { recursive: true });
    const prompt = path.join(TMP_PARENT, 'prompt.md');
    await writeFile(prompt, 'review this');
    const stderr = captureStream();
    const cfg = { provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((l) => [l, { provider: 'anthropic', model: 'fake-opus' }])), budget: { usd: 20 } };
    const result = await spawnSession(
      { cfg, level: 'L2', role: 'reviewer', promptPath: prompt, run: 'r-b33', block: 'B1', slug: 'two-blocks', runRoot: TMP_PARENT },
      { bins: { claude: FAKE_CLAUDE }, stderr, env: { PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', FAKE_RECORD: records } },
    );
    assert.deepEqual([result.status, result.reason], ['unavailable', 'budget']);
    assert.equal(stderr.text, 'code-forge: budget.usd 20.00 reached (spent 20.13); raise budget.usd or end the run\n');
    assert.deepEqual(await readdir(records), [], '0 spawns');
    const events = (await readAllRows('two-blocks')).map((r) => r.event);
    assert.deepEqual(events, ['run.start', 'session', 'budget.refused']);

    const report = captureStream();
    assert.equal(await runReport(['--slug', 'two-blocks'], { stdout: report, stderr: captureStream() }), 0);
    assert.equal(report.text.includes('  run r-b33: $20.1300 · 1 session(s)\n'), true, report.text);

    // two unknown-price sessions (one background coder): run status names them on stderr
    await appendRow({ event: 'session', run: 'r-b33', block: 'B1', role: 'reviewer', usd: null, usd_unknown: true }, { slug: 'two-blocks' });
    await appendRow({ event: 'session.background', run: 'r-b33', block: 'B1', role: 'coder', usd: null, usd_unknown: true }, { slug: 'two-blocks' });
    const later = captureStream();
    const laterErr = captureStream();
    assert.equal(await runRun(['status', '--run', 'r-b33'], { stdout: later, stderr: laterErr }), 0);
    assert.deepEqual([JSON.parse(later.text).spent_usd, JSON.parse(later.text).unknown_usd_sessions], [20.13, 2]);
    assert.equal(laterErr.text, '2 sessions have no known price; the budget does not count them\n');
  });
});

test('ledger add: bad input is a usage error (exit 2) and an unknown run is exit 1 — no row written', async () => {
  await withFixture(async () => {
    const cases = [
      [['add', 'reviewer', '--run', 'r', '--block', 'B1', '--usd', '1'], 2],
      [['add', 'coder', '--block', 'B1', '--usd', '1'], 2],
      [['add', 'coder', '--run', 'r', '--block', 'B1', '--usd', '-1'], 2],
      [['add', 'coder', '--run', 'r', '--block', 'B1', '--usd', '0'], 2],
      [['add', 'coder', '--run', 'r', '--block', 'B1', '--usd', 'ten'], 2],
      [['add', 'coder', '--run', 'r', '--block', '../x', '--usd', '1'], 2],
      [['add', 'coder', '--run', '../r', '--block', 'B1', '--usd', '1'], 2],
      // rounds to 0.0000 at 4 decimals first, then fails "> 0"
      [['add', 'coder', '--run', 'r', '--block', 'B1', '--usd', '0.00004'], 2],
      [['add', 'coder', '--run', 'r-missing', '--block', 'B1', '--usd', '1'], 1],
    ];
    const codes = [];
    for (const [args] of cases) codes.push(await runLedger(/** @type {string[]} */ (args), { stdout: captureStream(), stderr: captureStream() }));
    assert.deepEqual(codes, cases.map(([, c]) => c));
    assert.deepEqual(await readAllRows('two-blocks'), []);
  });
});

test('ledger add: --usd 0.00005 rounds to 0.0001 and is kept; when the run total cannot be read after the write, the line says "unknown" and the exit is still 0', async () => {
  await withFixture(async ({ home, ws }) => {
    assert.equal(await runRun(['start', '--cwd', ws, '--run', 'r-unk', '--worker-pid', String(process.pid)], { stdout: captureStream(), stderr: captureStream() }), 0);
    // a corrupt line in the MIDDLE of the ledger makes every later full read throw
    await appendFile(path.join(home, '.code-forge', 'ledger', 'two-blocks.jsonl'), 'not json\n');
    const stdout = captureStream();
    const stderr = captureStream();
    assert.equal(await runLedger(['add', 'coder', '--run', 'r-unk', '--block', 'B1', '--usd', '0.00005'], { stdout, stderr }), 0, stderr.text);
    assert.equal(stdout.text, 'recorded coder spend $0.0001 for run r-unk block B1 · run total unknown\n');
  });
});
