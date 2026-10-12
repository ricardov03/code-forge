import { commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

// B66 (issue #13 item 8): `code-forge ledger outside-review` records the rounds and findings of
// an outside review of one PR as a signed `outside_review` row. Every test runs the real CLI
// (`bin/code-forge.mjs`) in a temp repository and a temp run under the temp HOME the review
// helpers set at import time, and pins exact stdout, exit codes and ledger rows.

const { readAllRows } = await import('../../src/ledger/write.mjs');
const { ledgerPath } = await import('../../src/ledger/paths.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { runLedger } = await import('../../src/cli/ledger.mjs');
const { clearSecrets, registerSecret } = await import('../../src/util/redact.mjs');

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
/** A live stand-in for the pinned worker. */
const WORKER = spawn(process.execPath, ['-e', 'setTimeout(() => {}, 600000)'], { stdio: 'ignore' });
after(() => WORKER.kill('SIGKILL'));
after(() => clearSecrets());

const USAGE = 'usage: code-forge ledger outside-review --run <r> --pr <n> --rounds <n> --findings <n> [--link <url>] [--block <id>]...\n';

/** @param {string} slug */
const configYaml = (slug) =>
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
  ].join('\n');

/**
 * The real CLI.
 * @param {string[]} args @param {string} [cwd]
 * @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
const cf = (args, cwd = os.tmpdir()) =>
  new Promise((resolve) => {
    execFile(process.execPath, [BIN, ...args], { cwd, env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' } }, (error, stdout, stderr) =>
      resolve({ code: error ? (typeof error.code === 'number' ? error.code : -1) : 0, stdout, stderr }),
    );
  });

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A committed repo, a run started through the CLI, and blocks B1 and B2 opened in it.
 * @param {string} slug @param {string} runId
 */
async function fixture(slug, runId) {
  const repo = await makeRepo();
  writeFile(repo, '.code-forge.yml', configYaml(slug));
  writeFile(repo, 'src/b1.mjs', "export const v = 'b1';\n");
  writeFile(repo, 'src/b2.mjs', "export const v = 'b2';\n");
  await commitAll(repo);
  const started = await cf(['run', 'start', '--cwd', repo, '--run', runId, '--worker-pid', String(WORKER.pid)]);
  assert.equal(started.code, 0, started.stderr);
  for (const [id, owned] of [
    ['B1', 'src/b1.mjs'],
    ['B2', 'src/b2.mjs'],
  ]) {
    const opened = await cf(['block', 'open', id, '--run', runId, '--level', 'L1', '--owned', owned, '--acceptance', ACC], repo);
    assert.equal(opened.code, 0, opened.stderr);
  }
  /** @param {string[]} args */
  const review = (args) => cf(['ledger', 'outside-review', ...args], repo);
  const ledgerText = () => {
    try {
      return readFileSync(ledgerPath(slug), 'utf8');
    } catch {
      return '';
    }
  };
  return { repo, runId, slug, review, ledgerText, outside: async () => (await readAllRows(slug)).filter((r) => r.event === 'outside_review') };
}

test('B66 (1): outside-review writes ONE signed outside_review row and prints one line; duplicate --block ids collapse in first-seen order', async () => {
  const fx = await fixture('b66-one', 'r-b66-one');
  const startedAt = Date.now();
  const res = await fx.review(['--run', fx.runId, '--pr', '12', '--rounds', '3', '--findings', '7', '--link', 'https://github.com/o/r/pull/12', '--block', 'B2', '--block', 'B1', '--block', 'B2']);
  assert.deepEqual(res, { code: 0, stdout: 'outside_review pr 12 · rounds 3 · findings 7 · blocks B2, B1\n', stderr: '' });
  const rows = await fx.outside();
  assert.equal(rows.length, 1);
  const { ts, mac, ...rest } = rows[0];
  assert.deepEqual(rest, {
    run: 'r-b66-one',
    event: 'outside_review',
    pr: 12,
    rounds: 3,
    findings: 7,
    link: 'https://github.com/o/r/pull/12',
    blocks: ['B2', 'B1'],
    tokens_source: null,
    cost_source: null,
  });
  assert.equal(typeof ts, 'string');
  const tsMs = Date.parse(ts);
  assert.ok(Number.isFinite(tsMs) && tsMs >= startedAt - 1000 && tsMs <= Date.now() + 1000, `ts ${ts} is not inside the test window`);
  assert.match(mac, /^[0-9a-f]{64}$/);
  assert.deepEqual(verifyRow(rows[0], await loadKey('r-b66-one')), { ok: true });

  // no --link and no --block: link null, blocks [], the line says "blocks -"; zeros are allowed
  const bare = await fx.review(['--run', fx.runId, '--pr', '13', '--rounds', '0', '--findings', '0']);
  assert.deepEqual(bare, { code: 0, stdout: 'outside_review pr 13 · rounds 0 · findings 0 · blocks -\n', stderr: '' });
  const afterBare = await fx.outside();
  assert.equal(afterBare.length, 2);
  const second = afterBare[1];
  assert.deepEqual([second.pr, second.rounds, second.findings, second.link, second.blocks], [13, 0, 0, null, []]);
  assert.deepEqual(verifyRow(second, await loadKey('r-b66-one')), { ok: true });

  // a link of exactly 2048 characters is kept
  const long = `https://example.com/${'a'.repeat(2048 - 'https://example.com/'.length)}`;
  assert.equal(long.length, 2048);
  assert.equal((await fx.review(['--run', fx.runId, '--pr', '14', '--rounds', '1', '--findings', '1', '--link', long])).code, 0);
  const afterLong = await fx.outside();
  assert.equal(afterLong.length, 3);
  assert.equal(afterLong[2].link, long);
});

test('B66 (1): a second call for the same PR replaces the first in report --json; both rows stay in the ledger', async () => {
  const fx = await fixture('b66-replace', 'r-b66-replace');
  assert.equal((await fx.review(['--run', fx.runId, '--pr', '5', '--rounds', '1', '--findings', '4', '--block', 'B1'])).code, 0);
  const again = await fx.review(['--run', fx.runId, '--pr', '5', '--rounds', '2', '--findings', '6', '--link', 'http://ci.example/5', '--block', 'B1', '--block', 'B2']);
  assert.deepEqual(again, { code: 0, stdout: 'outside_review pr 5 · rounds 2 · findings 6 · blocks B1, B2\n', stderr: '' });
  assert.equal((await fx.outside()).length, 2);
  const report = await cf(['report', '--slug', fx.slug, '--json']);
  assert.equal(report.code, 0, report.stderr);
  assert.deepEqual(JSON.parse(report.stdout).outside_review, [
    { pr: 5, rounds: 2, findings: 6, link: 'http://ci.example/5', blocks: ['B1', 'B2'], code_forge_usd: 0, unknown_usd_sessions: 0 },
  ]);
});

test('B66 (1): every usage error exits 2 with the usage line and writes nothing', async () => {
  const fx = await fixture('b66-usage', 'r-b66-usage');
  const before = fx.ledgerText();
  const ok = ['--run', fx.runId, '--pr', '1', '--rounds', '1', '--findings', '1'];
  /** @param {string} flag @param {string} value @returns {string[]} the ok args with `flag` set to `value` */
  const withFlag = (flag, value) => {
    const args = [...ok];
    args[args.indexOf(flag) + 1] = value;
    return args;
  };
  /** @param {string} flag @returns {string[]} the ok args without `flag` */
  const without = (flag) => {
    const args = [...ok];
    args.splice(args.indexOf(flag), 2);
    return args;
  };
  /** @type {Array<[string[], string]>} */
  const cases = [
    [without('--run'), '--run is required'],
    [without('--pr'), '--pr is required'],
    [without('--rounds'), '--rounds is required'],
    [without('--findings'), '--findings is required'],
    [withFlag('--run', '../x'), '--run must be a run id (letters, digits, ".", "_", "-")'],
    [withFlag('--pr', '0'), '--pr must be a positive integer'],
    [withFlag('--pr', '-1'), '--pr must be a positive integer'],
    [withFlag('--pr', '01'), '--pr must be a positive integer'],
    [withFlag('--pr', '1.5'), '--pr must be a positive integer'],
    [withFlag('--pr', 'abc'), '--pr must be a positive integer'],
    [withFlag('--rounds', '+1'), '--rounds must be a non-negative integer'],
    [withFlag('--rounds', '00'), '--rounds must be a non-negative integer'],
    [withFlag('--rounds', '-1'), '--rounds must be a non-negative integer'],
    [withFlag('--findings', '1e2'), '--findings must be a non-negative integer'],
    [withFlag('--findings', '07'), '--findings must be a non-negative integer'],
    [[...ok, '--link', 'ftp://example.com/x'], '--link must be an https:// or http:// URL with no whitespace'],
    [[...ok, '--link', 'https://example.com/a b'], '--link must be an https:// or http:// URL with no whitespace'],
    [[...ok, '--link', 'example.com/pull/1'], '--link must be an https:// or http:// URL with no whitespace'],
    [[...ok, '--link', `https://example.com/${'a'.repeat(2029)}`], '--link is longer than 2048 characters'],
    [[...ok, '--link'], '--link requires a value'],
    [[...ok, '--block'], '--block requires a value'],
    [[...ok, '--block', '--pr'], '--block requires a value'],
    [[...ok, '--pr', '2'], '--pr is given more than once'],
    [[...ok, '--colour', 'red'], 'unknown argument at position 9'],
  ];
  const results = [];
  for (const [args] of cases) results.push(await fx.review(args));
  assert.deepEqual(
    results.map((r) => [r.code, r.stdout, r.stderr]),
    cases.map(([, message]) => [2, '', `ledger outside-review: ${message}\n${USAGE}`]),
  );
  assert.equal(fx.ledgerText(), before, 'no usage error wrote a ledger row');
});

test('B66 (1): an unknown block exits 1 naming the id and writes nothing; an unknown run exits 1', async () => {
  const fx = await fixture('b66-unknown', 'r-b66-unknown');
  const before = fx.ledgerText();
  const block = await fx.review(['--run', fx.runId, '--pr', '9', '--rounds', '1', '--findings', '0', '--block', 'B1', '--block', 'B9']);
  assert.deepEqual(block, { code: 1, stdout: '', stderr: 'ledger outside-review: block "B9" is not a block of run r-b66-unknown; nothing written\n' });
  const run = await fx.review(['--run', 'r-b66-nope', '--pr', '9', '--rounds', '1', '--findings', '0']);
  assert.deepEqual(run, { code: 1, stdout: '', stderr: 'ledger outside-review: no run record for r-b66-nope\n' });
  assert.equal(fx.ledgerText(), before);
  assert.deepEqual(await fx.outside(), []);
});

test('B66 (1): --link passes through redact before it is stored — a link carrying a registered secret is stored masked', async () => {
  const fx = await fixture('b66-redact', 'r-b66-redact');
  // through the real binary: the secret the CLI knows is the run key, loaded before the redaction
  const key = (await loadKey(fx.runId)).toString('hex');
  const res = await fx.review(['--run', fx.runId, '--pr', '21', '--rounds', '1', '--findings', '2', '--link', `https://github.com/o/r/pull/21?token=${key}`]);
  assert.equal(res.code, 0, res.stderr);
  // in process: a FAKE token registered as a secret (as `keys` does for a resolved key)
  const token = 'ghp_FAKE0123456789abcdefFAKE';
  registerSecret(token);
  const stdout = { text: '', write(/** @type {string} */ s) { this.text += s; return true; } };
  const stderr = { text: '', write(/** @type {string} */ s) { this.text += s; return true; } };
  assert.equal(await runLedger(['outside-review', '--run', fx.runId, '--pr', '22', '--rounds', '0', '--findings', '0', '--link', `https://ci.example/build?t=${token}`], { stdout, stderr }), 0, stderr.text);
  assert.equal(stdout.text, 'outside_review pr 22 · rounds 0 · findings 0 · blocks -\n');
  assert.equal(stderr.text, '');
  const links = (await fx.outside()).map((r) => String(r.link));
  // checked first, so a failure below never prints a secret
  assert.equal(links.some((l) => l.includes(key) || l.includes(token)), false, 'a stored link still carries a secret');
  assert.deepEqual(
    (await fx.outside()).map((r) => r.link),
    ['https://github.com/o/r/pull/21?token=[REDACTED]', 'https://ci.example/build?t=[REDACTED]'],
  );
  const text = fx.ledgerText();
  assert.equal(text.split(key).length - 1, 0);
  assert.equal(text.split(token).length - 1, 0);
  for (const row of await fx.outside()) assert.deepEqual(verifyRow(row, await loadKey(fx.runId)), { ok: true });

  // a loose URL (no --link before it) is an unknown argument: exit 2, named by position only, nothing written
  const before = fx.ledgerText();
  // through the real binary, where the FAKE token is not a registered secret, so only the message
  // shape keeps it out of stderr
  const loose = await fx.review(['--run', fx.runId, '--pr', '23', '--rounds', '1', '--findings', '1', `https://x?token=${token}`]);
  assert.equal(loose.stderr.includes(token), false, 'stderr echoed the loose token');
  assert.deepEqual(loose, { code: 2, stdout: '', stderr: `ledger outside-review: unknown argument at position 9\n${USAGE}` });
  assert.equal(fx.ledgerText(), before);
});
