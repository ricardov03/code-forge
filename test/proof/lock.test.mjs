import assert from 'node:assert/strict';
import { before, test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { proofRow } from '../../src/proof/export.mjs';
import { acquireProofLock, runGatesGuarded, withGateSlot } from '../../src/proof/lock.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { OWNED_A, OWNED_B, SLUG, buildExportIgnoreRepo, captureStream, tempDir } from './helpers.mjs';

const RUN = 'r-b10a-lock';
const GATES = { test: [process.execPath, '--version'] };
const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: SLUG });
/** @type {string} */
let ws;

before(async () => {
  ws = tempDir('lock-ws');
  buildExportIgnoreRepo(ws);
  await startRun({ workspace: ws, project: SLUG, runId: RUN, writeRow });
  const acceptance = [{ clause: 'c1', tests: ['t1'] }];
  await openBlock({ runId: RUN, id: 'A', level: 'L2', owned: [...OWNED_A], acceptance, writeRow });
  await openBlock({ runId: RUN, id: 'B', level: 'L2', owned: [...OWNED_B], acceptance, writeRow });
});

/** @param {string[]} args */
async function cli(args) {
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runProof(args, { stdout, stderr });
  return { code, stdout: stdout.text, stderr: stderr.text };
}

test('proof lock pauses another block\'s gate run: the second gate call returns proof.busy', async () => {
  const locked = await cli(['lock', 'A', '--run', RUN]);
  assert.equal(locked.code, 0);
  assert.equal(JSON.parse(locked.stdout).locked.block, 'A');

  const gate = await runGatesGuarded({ runId: RUN, blockId: 'B', gates: GATES, cwd: ws });
  assert.deepEqual(gate, { ok: false, result: 'proof.busy', holder: 'A' });
});

test('proof unlock releases: the other block\'s gate then runs and its gate_running flag is cleared', async () => {
  const unlocked = await cli(['unlock', 'A', '--run', RUN]);
  assert.deepEqual([unlocked.code, JSON.parse(unlocked.stdout)], [0, { unlocked: 'A' }]);

  const gate = await runGatesGuarded({ runId: RUN, blockId: 'B', gates: GATES, cwd: ws });
  assert.deepEqual([gate.ok, gate.result, gate.ok && gate.value.allOk], [true, 'ran', true]);
  const record = await readRun(RUN);
  assert.deepEqual([record.proof_lock, record.blocks.B.gate_running], [undefined, undefined]);
});

test('proof lock is refused with proof.busy while another block\'s gate is running', async () => {
  const inside = await withGateSlot({
    runId: RUN,
    blockId: 'B',
    fn: () => acquireProofLock({ runId: RUN, blockId: 'A', writeRow }).then(
      () => 'acquired',
      (err) => err.code,
    ),
  });
  assert.deepEqual(inside, { ok: true, result: 'ran', value: 'proof.busy' });
  assert.equal((await readRun(RUN)).proof_lock, undefined);
});

test('proof rows go through the B6 writer, signed, with isolation export and lock', async () => {
  const before = (await readAllRows(SLUG)).length;
  const exported = await cli(['export', 'A', '--run', RUN]);
  assert.equal(exported.code, 0, exported.stderr);
  assert.deepEqual(JSON.parse(exported.stdout).untracked, ['.env']); // config: copy_untracked [.env]
  assert.equal((await cli(['lock', 'B', '--run', RUN])).code, 0);
  assert.equal((await cli(['unlock', 'B', '--run', RUN])).code, 0);

  const fresh = (await readAllRows(SLUG)).slice(before);
  const key = await loadKey(RUN);
  assert.deepEqual(
    fresh.map((r) => [r.event, r.block, r.isolation, r.step, verifyRow(r, key).ok]),
    [
      ['proof', 'A', 'export', 'export', true],
      ['proof', 'B', 'lock', 'lock', true],
      ['proof', 'B', 'lock', 'unlock', true],
    ],
  );
  assert.throws(() => proofRow({ block: 'A', isolation: 'worktree' }), { code: 'bad-isolation' });
});
