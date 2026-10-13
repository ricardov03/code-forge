// Real tickets and Git reads: count child spawns, never inspect src text.
import { answerFor, cfgFor, commitAll, freshDir, git, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { readFileSync, rmSync } from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { test } from 'node:test';

const { reviewTicket } = await import('../../src/worker/engine.mjs');
const { enqueue, readTicket } = await import('../../src/worker/queue.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { loadKey, signRow } = await import('../../src/state/signer.mjs');

process.env.CODE_FORGE_KEY_BACKEND = 'file';
const FILE = 'src/a.mjs';
const PEER = 'test/a.test.mjs';
const BODY = "test('handles input', () => {});\n";
const PAIRED = '- test/a.test.mjs\n  - handles input';
const FINDING = { id: 'F1', file: FILE, line_start: 1, line_end: 1, severity: 'critical', category: 'correctness', claim: 'wrong input', evidence: 'e', fix: 'f' };
let seq = 0;

/** @param {{keyed?: boolean, changed?: boolean, newFile?: boolean}} [opts] */
async function fixture({ keyed = false, changed = true, newFile = false } = {}) {
  const repo = await makeRepo();
  if (!newFile) writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(repo, PEER, "test('old case', () => {});\n");
  await commitAll(repo);
  const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
  if (changed) writeFile(repo, FILE, 'export const a = 2;\n');
  writeFile(repo, PEER, BODY);
  const runId = `r-b112-${process.pid}-${++seq}`;
  await startRun({ workspace: repo, project: 'lazy-peers', runId, writeRow: async () => undefined });
  await openBlock({ runId, id: 'B112', level: 'L2', base, owned: [FILE, PEER], tests: [PEER], acceptance: [{ clause: 'peer diffs are lazy', tests: ['peer-diffs-lazy'] }], writeRow: async () => undefined });
  const key = await loadKey(runId);
  /** @type {Record<string, any>[]} */
  const rows = [];
  /** @type {string[]} */
  const packets = [];
  return {
    repo, base, packets,
    round: async () => {
      const ticket = enqueue({ repoRoot: repo, run: runId, block: 'B112', file: FILE });
      return reviewTicket(readTicket(repo, ticket.ticket), {
        repoRoot: repo, runId, runRootDir: freshDir('worker'), cfg: cfgFor(), jevKey: null,
        ...(keyed ? { key, readRows: async () => rows } : {}),
        writeRow: async (row) => { rows.push(signRow({ ...row, run: runId, block: 'B112', file: FILE, content_hash: ticket.content_hash }, key)); },
        jev: async (req) => {
          const id = Object.keys(req.questions)[0];
          return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
        },
        spawn: async (opts) => {
          const text = readFileSync(opts.promptPath, 'utf8');
          packets.push(text);
          const recheck = opts.rowExtra.lens === 'recheck';
          return {
            status: 'ok', exit_code: 0, usage: { tokens_in: 900, tokens_out: 300 },
            answer: answerFor([...text.matchAll(/^- (@@[^\n]+)$/gm)].map((m) => m[1]), {
              findings: keyed && !recheck ? [FINDING] : [],
              resolved: recheck ? [{ id: 'F1', resolved: true, why: 'fixed' }] : [],
            }),
          };
        },
      });
    },
  };
}

/** Count the real listing and peer patch reads for just this ticket.
 * @param {import('node:test').TestContext} t @param {string} repo
 */
function countGit(t, repo) {
  const counts = { listing: 0, status: 0, peerDiffs: 0 };
  const spawn = childProcess.spawn;
  t.mock.method(childProcess, 'spawn', (/** @type {string} */ command, /** @type {string[]} */ args, /** @type {any} */ opts) => {
    if (command === 'git' && opts.cwd === repo) {
      if (args.includes('diff') && args.includes('--name-only')) counts.listing += 1;
      if (args.includes('status') && args.includes('--porcelain')) counts.status += 1;
      if (args.includes('diff') && !args.includes('--name-only') && args.slice(args.indexOf('--') + 1).includes(PEER)) counts.peerDiffs += 1;
    }
    return spawn(command, args, opts);
  });
  syncBuiltinESMExports();
  t.after(() => { t.mock.restoreAll(); syncBuiltinESMExports(); });
  return counts;
}

test('no peer diff reads when an unchanged review ticket never asks for peerDiffs', async (t) => {
  const w = await fixture({ changed: false });
  const counts = countGit(t, w.repo);
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, w.packets.length], ['no_change', false, 0]);
  assert.deepEqual(counts, { listing: 1, status: 1, peerDiffs: 0 });
});

test('recheck asks for peerDiffs twice but reads once per ticket and keeps paired packets', async (t) => {
  const w = await fixture({ keyed: true });
  const first = await w.round();
  assert.deepEqual([first.status, first.approved, first.round], ['reviewed', false, 1]);
  writeFile(w.repo, FILE, 'export const a = 3;\n');
  const counts = countGit(t, w.repo);
  const second = await w.round();
  assert.deepEqual([second.status, second.approved, second.round, second.kind, w.packets.length], ['reviewed', true, 2, 'recheck', 2]);
  assert.deepEqual(counts, { listing: 1, status: 1, peerDiffs: 1 });
  assert.deepEqual(w.packets.map((p) => [...p.matchAll(/^## paired\n([\s\S]*?)(?=^## |$(?![\s\S]))/gm)].map((m) => m[1].trimEnd())), [[PAIRED], [PAIRED]]);
});

test('failed listing gives empty paired context and no peer diff reads despite explicit tests', async (t) => {
  const w = await fixture({ newFile: true });
  rmSync(path.join(w.repo, '.git', 'objects', w.base.slice(0, 2), w.base.slice(2)));
  const counts = countGit(t, w.repo);
  const result = await w.round();
  assert.deepEqual([result.status, result.approved, w.packets.length], ['reviewed', true, 1]);
  assert.deepEqual(counts, { listing: 1, status: 1, peerDiffs: 0 });
  assert.deepEqual(w.packets.map((p) => (p.match(/^## (?:paired|moved code)$/gm) ?? []).length), [0]);
});
