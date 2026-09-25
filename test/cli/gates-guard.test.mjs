// `gates run --run <r> --block <id>` goes through the proof lock (`runGatesGuarded`, B10a
// follow-up wired by B12b). Both runners are mocked (module mocks, `npm test` flags), so the test
// counts which one `gates run` calls; no gate command ever runs. HOME/cwd are pinned by the
// `--import ./test/helpers/isolate.mjs` preload.
import assert from 'node:assert/strict';
import { mkdtempSync, realpathSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, mock, test } from 'node:test';

if (typeof mock.module !== 'function') throw new Error('run with --experimental-test-module-mocks (npm test does)');

const CWD = realpathSync(mkdtempSync(path.join(os.tmpdir(), 'cf-b12b-gates-')));
after(() => rmSync(CWD, { recursive: true, force: true }));

/** @type {{guarded: Array<Record<string, any>>, plain: number}} */
const calls = { guarded: [], plain: 0 };
/** @type {Record<string, any>} */
let guardedReply = { ok: true, result: 'ran', value: { results: [], allOk: true } };
mock.module(new URL('../../src/proof/lock.mjs', import.meta.url).href, {
  namedExports: {
    runGatesGuarded: async (/** @type {Record<string, any>} */ opts) => {
      calls.guarded.push(opts);
      return guardedReply;
    },
  },
});
mock.module(new URL('../../src/gates/run.mjs', import.meta.url).href, {
  namedExports: {
    runGates: async () => {
      calls.plain += 1;
      return { results: [], allOk: true };
    },
  },
});
const { runGatesVerb } = await import('../../src/cli/gates.mjs');

/** @param {string[]} args @returns {Promise<[number, string, string]>} */
async function run(args) {
  let out = '';
  let err = '';
  const code = await runGatesVerb(['run', '--cwd', CWD, ...args], { stdout: { write: (s) => ((out += s), true) }, stderr: { write: (s) => ((err += s), true) } });
  return [code, out, err];
}

const PAIR = 'gates run: --run <id> and --block <id> go together, each with a value\n';

test('only one of --run/--block, or a flag with no value ⇒ exit 2 and no gate runs', async () => {
  calls.guarded = [];
  calls.plain = 0;
  assert.deepEqual(await run(['--run', 'r1']), [2, '', PAIR]);
  assert.deepEqual(await run(['--block', 'B1']), [2, '', PAIR]);
  assert.deepEqual(await run(['--run', '--block', 'B1']), [2, '', PAIR]);
  assert.deepEqual(await run(['--run', 'r1', '--block']), [2, '', PAIR]);
  assert.deepEqual([calls.guarded.length, calls.plain], [0, 0]);
});

test('another block holds the proof lock ⇒ exit 1 with exactly {"result":"proof.busy","holder":"A"}', async () => {
  guardedReply = { ok: false, result: 'proof.busy', holder: 'A' };
  const [code, out] = await run(['--run', 'r1', '--block', 'B']);
  guardedReply = { ok: true, result: 'ran', value: { results: [], allOk: true } };
  assert.equal(code, 1);
  assert.equal(out, '{"result":"proof.busy","holder":"A"}\n');
});

test('with --run/--block the guarded runner runs the gates exactly once and plain runGates is never called; without them the reverse', async () => {
  calls.guarded = [];
  calls.plain = 0;
  const [code] = await run(['--run', 'r1', '--block', 'B1']);
  assert.equal(code, 0);
  assert.deepEqual([calls.guarded.length, calls.plain], [1, 0]);
  assert.deepEqual([calls.guarded[0].runId, calls.guarded[0].blockId, calls.guarded[0].cwd], ['r1', 'B1', CWD]);
  assert.equal((await run([]))[0], 0);
  assert.deepEqual([calls.guarded.length, calls.plain], [1, 1]);
});
