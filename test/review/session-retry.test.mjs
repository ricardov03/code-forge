import { answerFor, cfgFor, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { after, describe, test } from 'node:test';

const { reviewFile } = await import('../../src/review/engine.mjs');
const { spawnSession, stderrTail, DIAGNOSTIC_TAIL_BYTES, STDERR_MAX_BYTES } = await import('../../src/session/spawn.mjs');
const { registerSecret, clearSecrets } = await import('../../src/util/redact.mjs');

after(() => clearSecrets());

const FILE = 'src/feature.mjs';
const HUNKS = ['@@ -0,0 +1,12 @@'];

/** A spawn result that passes the stub guard for FILE. */
const OK = { status: 'ok', answer: answerFor(HUNKS), usage: { tokens_in: 100, tokens_out: 80, tokens_source: 'reported' }, exit_code: 0, provider: 'openai', model: 'fake-model' };
/** @param {string} tail */
const TIMEOUT = (tail = 'stalled') => ({ status: 'timeout', reason: 'killed after 300000 ms', answer: null, exit_code: null, provider: 'openai', model: 'fake-model', duration_ms: 300000, stderr_tail: tail, stdout_bytes: tail.length * 10, stdout_events: ['turn.started', `of.${tail}`] });
const FAILED = { status: 'failed', reason: 'exit 1', answer: null, exit_code: 1, provider: 'openai', model: 'fake-model' };

/**
 * Review FILE (risk 0 ⇒ one quick session) with a scripted spawn: call k returns `script[k]`.
 * @param {Array<Record<string, any> | 'throw'>} script
 */
async function review(script) {
  const repo = await makeRepo();
  writeFile(repo, FILE, lines(12));
  /** @type {Array<Record<string, any>>} */
  const calls = [];
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  /** @type {string[]} */
  const packets = [];
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    calls.push(opts);
    packets.push(readFileSync(opts.promptPath, 'utf8')); // the packet CONTENT each attempt got
    const step = script[calls.length - 1];
    if (step === 'throw') throw new Error('spawn refused');
    return structuredClone(step);
  };
  const cfg = cfgFor();
  const outcome = await reviewFile({ repoRoot: repo, file: FILE, base: null, cfg, risk: 0, workDir: path.join(freshDir('runroot'), 'packets', 't') }, { spawn, writeRow: async (row) => void rows.push(row) });
  return { outcome, calls, packets, rows, notes: rows.filter((r) => r.event === 'review.session_timeout') };
}

describe('a timed-out review session is retried once (B30)', () => {
  test('timeout, timeout ⇒ exactly 2 attempts, then unavailable: timeout, 2 notes', async () => {
    const { outcome, calls, rows, notes } = await review([TIMEOUT('first'), TIMEOUT('second'), OK]);
    assert.equal(calls.length, 2);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'timeout', false]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.lens, s.status, s.reason, s.attempts]), [['quick', 'unavailable', 'timeout', 2]]);
    assert.deepEqual(notes.map((n) => [n.lens, n.level, n.attempt, n.retried, n.stderr_tail, n.stdout_bytes, n.stdout_events, n.duration_ms]), [
      ['quick', 'L2', 1, true, 'first', 50, ['turn.started', 'of.first'], 300000],
      ['quick', 'L2', 2, false, 'second', 60, ['turn.started', 'of.second'], 300000],
    ]);
    assert.deepEqual(rows.filter((r) => r.event === 'review.unavailable').map((r) => r.reason), ['timeout']);
  });

  test('both attempts get the same packet content and the same level', async () => {
    const { calls, packets } = await review([TIMEOUT(), TIMEOUT()]);
    assert.equal(calls.length, 2);
    assert.equal(packets.length, 2);
    assert.equal(packets[0].startsWith('# code-forge review packet'), true);
    assert.equal(packets[1], packets[0]);
    assert.deepEqual([calls[0].level, calls[1].level, calls[0].role, calls[1].role], ['L2', 'L2', 'reviewer', 'reviewer']);
    assert.deepEqual([calls[0].rowExtra.lens, calls[1].rowExtra.lens], ['quick', 'quick']);
  });

  test('timeout, then ok ⇒ reviewed and approved, 2 attempts recorded, 1 note', async () => {
    const { outcome, calls, notes } = await review([TIMEOUT(), OK]);
    assert.equal(calls.length, 2);
    assert.deepEqual([outcome.status, outcome.approved], ['reviewed', true]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.attempts]), [['ok', 2]]);
    assert.deepEqual(notes.map((n) => [n.attempt, n.retried]), [[1, true]]);
  });

  test('an exit failure ⇒ 1 attempt, unavailable: exit, 0 timeout notes', async () => {
    const { outcome, calls, notes } = await review([FAILED, OK]);
    assert.equal(calls.length, 1);
    assert.deepEqual([outcome.status, outcome.reason], ['unavailable', 'exit']);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => s.attempts), [1]);
    assert.equal(notes.length, 0);
  });

  test('a spawn that throws ⇒ 1 attempt, unavailable: exit, attempts 1, 0 notes', async () => {
    const { outcome, calls, notes } = await review(['throw', OK]);
    assert.equal(calls.length, 1);
    assert.deepEqual([outcome.status, outcome.reason, outcome.approved], ['unavailable', 'exit', false]);
    assert.deepEqual(outcome.sessions.map((/** @type {any} */ s) => [s.status, s.reason, s.attempts]), [['unavailable', 'exit', 1]]);
    assert.equal(notes.length, 0);
  });

  test('a note that fails to write never stops the retry: timeout then ok ⇒ reviewed, 2 attempts', async () => {
    const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');
    let n = 0;
    const out = await spawnWithTimeoutRetry(
      async () => (n++ === 0 ? TIMEOUT() : OK),
      { level: 'L2', role: 'reviewer' },
      async () => {
        throw new Error('ledger down');
      },
    );
    assert.deepEqual([out.attempts, out.res?.status, n], [2, 'ok', 2]);
  });

  test('an ok first try ⇒ 1 attempt, 0 notes', async () => {
    const { outcome, calls, notes } = await review([OK]);
    assert.deepEqual([calls.length, outcome.status, outcome.sessions[0].attempts, notes.length], [1, 'reviewed', 1, 0]);
  });
});

describe('the diagnostic record of a timed-out session (B30)', () => {
  const SECRET = 'sk-FAKE-b30-tail-secret-0123456789';
  const PACKET_LINE = '+export const packetOnlyLine = "do not leak this packet line";';
  registerSecret(SECRET);
  const CFG = { provider: 'anthropic', levels: { L2: { model: 'claude-opus-5-5' } } };

  /**
   * One timed-out spawnSession on a fake exec (Claude: the packet goes on stdin).
   * @param {{stdout: string, stderr: string, packet?: string}} out
   */
  async function timedOut({ stdout, stderr, packet = `# code-forge review packet\n${PACKET_LINE}\n` }) {
    const dir = freshDir('spawn');
    const promptPath = path.join(dir, 'packet.md');
    writeFileSync(promptPath, packet);
    /** @type {Array<string[]>} */
    const argvs = [];
    const res = await spawnSession(/** @type {any} */ ({ cfg: CFG, level: 'L2', role: 'reviewer', promptPath, runRoot: dir, timeoutMs: 1000 }), {
      stderr: { write: () => true },
      env: {},
      exec: /** @type {any} */ (
        async (/** @type {string[]} */ argv) => {
          argvs.push(argv);
          return { result: 'failed', code: null, signal: 'SIGKILL', stdout, stderr, timedOut: true };
        }
      ),
    });
    assert.equal(argvs.length, 1);
    return res;
  }

  test('stdout keeps NO text: only the last 20 event types and the byte count; a packet line and a secret appear 0 times', async () => {
    const events = Array.from({ length: 30 }, (_, i) => JSON.stringify({ type: `item.delta_${i}`, text: `${PACKET_LINE} ${SECRET}` }));
    const stdout = `${events.join('\n')}\n{"type":"BAD TYPE!"}\n{"type":"${'x'.repeat(60)}"}\n${PACKET_LINE}\n{"echo":"${SECRET}"}\n`;
    const res = await timedOut({ stdout, stderr: 'ERROR: stream stalled\n' });
    assert.equal(res.status, 'timeout');
    assert.equal(res.stdout_bytes, Buffer.byteLength(stdout));
    assert.deepEqual(res.stdout_events, [...Array.from({ length: 19 }, (_, i) => `item.delta_${i + 11}`), 'x'.repeat(40)]);
    assert.equal('stdout_tail' in res, false);
    const { spawnWithTimeoutRetry } = await import('../../src/review/session-retry.mjs');
    /** @type {Array<Record<string, any>>} */
    const notes = [];
    await spawnWithTimeoutRetry(async () => res, { level: 'L2', role: 'reviewer', rowExtra: { lens: 'quick' } }, async (row) => void notes.push(row));
    assert.equal(notes.length, 2);
    const text = JSON.stringify(notes);
    assert.equal(text.split(SECRET).length - 1, 0);
    assert.equal(text.split('packetOnlyLine').length - 1, 0);
    assert.equal(text.includes('stdout_tail'), false);
    assert.deepEqual(notes.map((n) => [n.stderr_tail, n.stdout_bytes, n.stdout_events.length]), [
      ['ERROR: stream stalled\n', Buffer.byteLength(stdout), 20],
      ['ERROR: stream stalled\n', Buffer.byteLength(stdout), 20],
    ]);
  });

  test('non-JSON stdout gives only stdout_bytes', async () => {
    const res = await timedOut({ stdout: `plain text ${SECRET}\nmore\n`, stderr: '' });
    assert.equal(res.stdout_bytes, Buffer.byteLength(`plain text ${SECRET}\nmore\n`));
    assert.equal('stdout_events' in res, false);
  });

  test('a readable packet: stderr keeps its last 4 KB, redacted, packet lines dropped, ordinary lines kept', async () => {
    const stderr = `${'x'.repeat(9000)}\nauth header ${SECRET}\n  ${PACKET_LINE}\nERROR: stream stalled\n`;
    const res = await timedOut({ stdout: '', stderr });
    assert.equal(res.tail_note, undefined);
    assert.equal(res.stderr_tail, `auth header [REDACTED]\nERROR: stream stalled\n`); // the 9 KB first line is the cut partial line
    assert.equal(res.stderr_tail.split(SECRET).length - 1, 0);
    assert.equal(res.stderr_tail.split('packetOnlyLine').length - 1, 0);
  });

  test('a PEM block in stderr: 0 of its base64 lines survive', async () => {
    const b64 = Array.from({ length: 8 }, (_, i) => `MIIFAKEb30${String(i).repeat(54)}`);
    const stderr = `before\n-----BEGIN PRIVATE KEY-----\n${b64.join('\n')}\n-----END PRIVATE KEY-----\nafter\n-----BEGIN CERTIFICATE-----\n${b64[0]}\n`;
    const res = await timedOut({ stdout: '', stderr });
    assert.equal(res.stderr_tail, 'before\nafter');
    assert.equal(b64.filter((l) => res.stderr_tail.includes(l)).length, 0);
    assert.equal(res.stderr_tail.includes('MIIFAKEb30'), false);
  });

  test('stderr over 1 MB keeps no text, with a note', async () => {
    const res = await timedOut({ stdout: '', stderr: `${'z'.repeat(STDERR_MAX_BYTES)}\nlast\n` });
    assert.deepEqual([res.stderr_tail, res.tail_note], [null, 'stderr too large, withheld']);
  });

  test('an unreadable packet ⇒ no stderr text (fail closed), with a note', async () => {
    // Grok reads its packet from --prompt-file (nothing on stdin); the file vanishes mid-session
    const dir = freshDir('spawn-nopacket');
    const promptPath = path.join(dir, 'packet.md');
    writeFileSync(promptPath, `# code-forge review packet\n${PACKET_LINE}\n`);
    const res = await spawnSession(
      /** @type {any} */ ({ cfg: { provider: 'xai', levels: { L2: { provider: 'xai', model: 'grok-4.7', effort: 'high' } } }, level: 'L2', role: 'reviewer', promptPath, runRoot: dir, timeoutMs: 1000 }),
      {
        stderr: { write: () => true },
        env: {},
        exec: /** @type {any} */ (
          async () => {
            rmSync(promptPath);
            return { result: 'failed', code: null, signal: 'SIGKILL', stdout: '{"type":"turn.started"}', stderr: 'err', timedOut: true };
          }
        ),
      },
    );
    assert.deepEqual([res.status, res.stderr_tail, res.tail_note, res.stdout_events], ['timeout', null, 'withheld: packet unreadable', ['turn.started']]);
    assert.deepEqual(stderrTail('text', null), { tail: null, note: 'withheld: packet unreadable' });
  });

  test('stderrTail: short text whole, Buffer decoded, never mid-character, one long line withheld', () => {
    assert.deepEqual(stderrTail('short', ''), { tail: 'short' });
    assert.deepEqual(stderrTail('', ''), { tail: '' });
    assert.deepEqual(stderrTail(Buffer.from('from a buffer\n'), ''), { tail: 'from a buffer\n' });
    // 101 bytes of "é\n" units: the cut backs off to a character, then drops the partial line
    assert.deepEqual(stderrTail('é\n'.repeat(1000), '', 101), { tail: 'é\n'.repeat(33) });
    assert.deepEqual(stderrTail('é'.repeat(3000), '', 101), { tail: '', note: 'one long line, withheld' });
    assert.deepEqual(stderrTail(`${'y'.repeat(2000)}\nlast line\n`, '', 101), { tail: 'last line\n' });
    assert.equal(Buffer.byteLength(/** @type {string} */ (stderrTail(`${'w'.repeat(30)}\n`.repeat(1000), '').tail)) <= DIAGNOSTIC_TAIL_BYTES, true);
  });
});

test('the default review.session_timeout_s is 300 in the schema and in the worker', async () => {
  const schema = JSON.parse(readFileSync(new URL('../../schema/code-forge.schema.json', import.meta.url), 'utf8'));
  const { DEFAULT_SESSION_TIMEOUT_S } = await import('../../src/worker/loop.mjs');
  assert.deepEqual([schema.properties.review.properties.session_timeout_s.default, DEFAULT_SESSION_TIMEOUT_S], [300, 300]);
});
