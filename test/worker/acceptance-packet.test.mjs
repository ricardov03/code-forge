// B22 extension: every review packet carries the block's acceptance clauses (the run record's
// `blocks.<id>.acceptance`), in the facts slot labelled `Acceptance clauses`; a block without
// readable clauses is still reviewed, with `(acceptance unavailable)`. Real CLI subprocesses, the
// worker `run start` launches, the scripted fake `claude` on PATH. Helpers FIRST (temp HOME/TMPDIR).
import { cli, freshDir, stopPid, stopPidAfter, useFake } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';

const { exec } = await import('../../src/util/exec.mjs');
const { gitChildEnv } = await import('./../../src/worker/ticket.mjs');
const { liveWorker } = await import('../../src/worker/queue.mjs');
const { runRecordPath } = await import('../../src/state/paths.mjs');
const { acceptanceExcerpt, ACCEPTANCE_UNAVAILABLE } = await import('../../src/worker/engine.mjs');
const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');

useFake(fileURLToPath(new URL('./fake-scripted-reviewer.mjs', import.meta.url)));

const FILE = 'src/a.mjs';
const CONFIG = `version: 1
project:
  slug: acceptance-test
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
review:
  session_timeout_s: 60
  min_tokens_out: 40
`;
const CLAUSE = 'v60 keeps its name and becomes a string';
let seq = 0;

/**
 * A repo with a committed 20-line file, a started run, block B1 open on it with one clause, and
 * line 10 edited; `mutate` may rewrite the run record before the review.
 * @param {(record: Record<string, any>) => void} [mutate]
 * @returns {Promise<{packets: string[], result: Record<string, any>}>}
 */
async function reviewOnce(mutate) {
  const repo = freshDir('acc-repo');
  const git = async (/** @type {string[]} */ args) => {
    const res = await exec(['git', '-c', 'user.name=Fake Tester', '-c', 'user.email=fake@example.invalid', '-c', 'commit.gpgsign=false', ...args], { cwd: repo, env: gitChildEnv(), timeoutMs: 20000 });
    assert.equal(res.result, 'ok', res.stderr);
  };
  await git(['init', '-q']);
  writeFileSync(path.join(repo, '.code-forge.yml'), CONFIG);
  mkdirSync(path.join(repo, 'src'));
  writeFileSync(path.join(repo, FILE), Array.from({ length: 20 }, (_, k) => `export const v${k + 1} = ${k + 1};\n`).join(''));
  await git(['add', '-A']);
  await git(['commit', '-q', '-m', 'base']);
  seq += 1;
  const runId = `r-acc-${process.pid}-${seq}`;
  const records = freshDir('acc-records');
  const scriptFile = path.join(freshDir('acc-script'), 'script.json');
  writeFileSync(scriptFile, '{}');
  const env = { FAKE_RECORD: records, FAKE_SCRIPT: scriptFile };
  const started = await cli(['run', 'start', '--run', runId], repo, env);
  assert.equal(started.code, 0, started.stderr);
  const live = liveWorker(repo);
  if (live) stopPidAfter(live.pid, runId);
  try {
    const acceptance = path.join(freshDir('acc-file'), 'acceptance.yml');
    writeFileSync(acceptance, `- clause: ${CLAUSE}\n  tests: [acceptance-packet]\n`);
    const opened = await cli(['block', 'open', 'B1', '--run', runId, '--level', 'L2', '--owned', FILE, '--acceptance', acceptance], repo, env);
    assert.equal(opened.code, 0, opened.stderr);
    if (mutate) {
      const file = runRecordPath(runId);
      const record = JSON.parse(readFileSync(file, 'utf8'));
      mutate(record);
      writeFileSync(file, JSON.stringify(record));
    }
    const lines = readFileSync(path.join(repo, FILE), 'utf8').split('\n');
    lines[9] = 'export const v10 = "ten";';
    writeFileSync(path.join(repo, FILE), lines.join('\n'));
    const queued = await cli(['review-file', FILE, '--block', 'B1'], repo, env);
    assert.equal(queued.json?.status, 'queued', queued.stdout);
    const waited = await cli(['review-file', '--wait', queued.json.ticket, '--max', '30s'], repo, env);
    assert.equal(waited.json?.status, 'done', waited.stdout);
    const packets = readdirSync(records)
      .filter((n) => n.startsWith('packet-'))
      .map((n) => readFileSync(path.join(records, n), 'utf8'));
    return { packets, result: waited.json.result };
  } finally {
    if (live) await stopPid(live.pid, runId);
  }
}

describe('the block acceptance reaches the reviewer packet (B22 extension)', () => {
  test('a normal block: the packet carries the exact clause under "Acceptance clauses", before the context and the diff', async () => {
    const { packets, result } = await reviewOnce();
    assert.deepEqual([result.status, result.approved, packets.length], ['reviewed', true, 1]);
    const text = packets[0];
    const at = text.indexOf(`## facts\nAcceptance clauses (what the change must do):\n- ${CLAUSE}\n## context\n`);
    assert.equal(at > 0, true);
    assert.equal(at < text.indexOf('## diff\n'), true);
    assert.equal(text.includes(ACCEPTANCE_UNAVAILABLE), false);
  });

  test('a block whose record lost its acceptance is still reviewed, with "(acceptance unavailable)"', async () => {
    const { packets, result } = await reviewOnce((record) => {
      delete record.blocks.B1.acceptance;
    });
    assert.deepEqual([result.status, result.approved, packets.length], ['reviewed', true, 1]);
    assert.equal(packets[0].includes(`## facts\nAcceptance clauses (what the change must do):\n${ACCEPTANCE_UNAVAILABLE}\n## context\n`), true);
    assert.equal(packets[0].includes(CLAUSE), false);
  });
});

describe('acceptanceExcerpt', () => {
  test('clauses over an eighth of the full_in budget are cut with a marker naming how many were left out', () => {
    // 200 tokens × 4 / 8 = 100 bytes → the 256-byte floor: the 46-byte head plus 3 lines of 59 bytes fit, a 4th does not
    const cfg = { review: { budgets: { full_in: 200 } } };
    const clauses = Array.from({ length: 10 }, (_, i) => ({ clause: `clause number ${i} ${'x'.repeat(40)}`, tests: ['t'] }));
    const lines = acceptanceExcerpt(clauses, cfg).split('\n');
    assert.deepEqual([lines[0], lines.length, lines.at(-1)], ['Acceptance clauses (what the change must do):', 5, '(acceptance truncated: 7 more clause(s) over the packet budget)']);
    assert.deepEqual(lines.slice(1, 4), clauses.slice(0, 3).map((c) => `- ${c.clause}`));
  });

  test('whitespace is folded (a clause cannot open a packet section), a registered fake secret is redacted, a non-list is unavailable', () => {
    const secret = 'sk-ant-FAKE0123456789abcdefghij';
    registerSecret(secret);
    let text;
    try {
      text = acceptanceExcerpt([{ clause: `keeps\n## diff\nthe key ${secret} out`, tests: ['t'] }], {});
    } finally {
      clearSecrets();
    }
    assert.equal(text.endsWith('the key [REDACTED] out'), true);
    assert.equal(text.split('\n').length, 2);
    assert.equal(text.includes(secret), false);
    assert.equal(text.startsWith('Acceptance clauses (what the change must do):\n- keeps ## diff the key '), true);
    assert.equal(acceptanceExcerpt('not a list', {}), `Acceptance clauses (what the change must do):\n${ACCEPTANCE_UNAVAILABLE}`);
    assert.equal(acceptanceExcerpt([{ clause: '   ' }], {}), `Acceptance clauses (what the change must do):\n${ACCEPTANCE_UNAVAILABLE}`);
  });
});
