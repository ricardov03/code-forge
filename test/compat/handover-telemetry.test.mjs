import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const scratch = mkdtempSync(path.join(process.env.TMPDIR || '/tmp', 'cf-handover-'));
const binary = path.join(scratch, 'native/bin/host/code-forge-native');
const script = path.join(scratch, 'bin/code-forge.mjs');
const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
before(() => {
  mkdirSync(path.dirname(binary), { recursive: true });
  mkdirSync(path.dirname(script), { recursive: true });
  copyFileSync(path.join(root, 'bin/code-forge'), path.join(scratch, 'bin/code-forge'));
  writeFileSync(script, `import { readFileSync, existsSync } from 'node:fs';
process.stdout.write(JSON.stringify({args:process.argv.slice(2),input:readFileSync(0,'utf8'),token:process.env.PROBE_TOKEN,recorded:process.env.PROBE_FILE ? existsSync(process.env.PROBE_FILE) : null}));
process.stderr.write('probe stderr\\n'); process.exitCode=Number(process.env.PROBE_EXIT || 0);
`);
  linkGoBinary(binary, { ldflags: `-X main.version=${pkg.version}` });
});
after(() => rmSync(scratch, { recursive: true, force: true }));

function fixture() {
  const home = mkdtempSync(path.join(scratch, 'home-'));
  return { home, repo: home, file: path.join(home, '.code-forge/native/handovers.jsonl') };
}
function env(f, extra = {}) {
  return { ...process.env, HOME: f.home, CODE_FORGE_NODE: process.execPath, CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1', CODE_FORGE_NO_ERROR_LOG: '1', PATH: path.dirname(process.execPath) + path.delimiter + process.env.PATH, ...extra };
}
function run(f, args, extra = {}, entry = binary, input = '') {
  const r = spawnSync(entry, args, { cwd: f.home, env: env(f, extra), input, encoding: 'utf8' });
  assert.equal(r.error, undefined, 'command starts');
  assert.equal(r.signal, null, 'command exits normally');
  return { code: r.status, stdout: r.stdout, stderr: r.stderr };
}
function rows(f) {
  return existsSync(f.file) ? readFileSync(f.file, 'utf8').trim().split('\n').filter(Boolean).map((line) => JSON.parse(line)) : [];
}

test('B282 clause: hand-overs record private-safe reasons before exec and Go reports counts', async () => {
  const f = fixture();
  const args = ['unported-telemetry', '--private', '/private/customer', 'secret-id'];
  const input = 'private stdin\n';
  const expected = run(f, args, { CODE_FORGE_HANDOVER_LOG: '0', PROBE_TOKEN: 'unchanged', PROBE_EXIT: '3' }, binary, input);
  assert.equal(JSON.parse(expected.stdout).input, input, 'hand-over forwards every stdin byte');
  assert.deepEqual(run(f, args, { PROBE_TOKEN: 'unchanged', PROBE_EXIT: '3' }, binary, input), expected, 'telemetry preserves every output byte and exit 3');
  assert.equal(rows(f).length, 1, 'one exec writes exactly one row');
  const row = rows(f)[0];
  assert.deepEqual(Object.keys(row).sort(), ['at', 'reason', 'sub', 'verb', 'version'], 'row has only five safe fields');
  assert.deepEqual({ ...row, at: 'time' }, { at: 'time', verb: 'unknown', sub: 'unknown', reason: 'unrouted', version: pkg.version }, 'unknown command tokens are replaced without flag values, paths or ids');
  assert.match(row.at, /^\d{4}-\d\d-\d\dT.*Z$/, 'timestamp is UTC ISO');
  assert.equal(Number.isNaN(Date.parse(row.at)), false, 'timestamp is valid');
  // B451 runs close --help in Go; a token-like close argument still hands the whole close to Node.
  const closeArgs = ['block', 'close', 'private-id', '--reason=sk-private'];
  assert.deepEqual(run(f, closeArgs, {}, binary, input), run(f, closeArgs, { CODE_FORGE_HANDOVER_LOG: '0' }, binary, input), 'close hand-over preserves Node bytes');
  assert.equal(rows(f).length, 2, 'close adds exactly one row');
  assert.equal(rows(f)[1].reason, 'close-unsupported-flag', 'close has its own stable reason');
  assert.equal(rows(f)[1].sub, 'close', 'subcommand is recorded without the block id');
  run(f, args);
  assert.equal(rows(f).length, 3, 'repeat hand-over adds exactly one row');
  const beforeExec = fixture();
  assert.equal(JSON.parse(run(beforeExec, ['doctor'], { PROBE_FILE: beforeExec.file }).stdout).recorded, true, 'row exists before Node starts');
  assert.equal(statSync(f.file).mode & 0o777, 0o600, 'log mode is 0600');
  assert.equal(statSync(path.dirname(f.file)).mode & 0o777, 0o700, 'directory mode is 0700');
  const report = run(f, ['logs', 'handovers', '--json'], { CODE_FORGE_NODE: '/nonexistent' });
  assert.equal(report.code, 0, 'report runs without Node');
  assert.equal(report.stderr, '', 'report has no stderr');
  assert.deepEqual(JSON.parse(report.stdout), { total: 3, rows: [
    { verb: 'unknown', sub: 'unknown', reason: 'unrouted', count: 2, last_at: rows(f)[2].at },
    { verb: 'block', sub: 'close', reason: 'close-unsupported-flag', count: 1, last_at: rows(f)[1].at },
  ] }, 'JSON sorts by count and reports the latest timestamp');
  assert.equal(run(f, ['logs', 'handovers']).stdout, 'count  verb  sub  reason\n2  unknown  unknown  unrouted\n1  block  close  close-unsupported-flag\ntotal  3\n', 'text table sorts by count and includes total');
  assert.deepEqual(JSON.parse(run(f, ['logs', 'handovers', '--json', '--since', '2999-01-01']).stdout), { total: 0, rows: [] }, 'since excludes older rows');
  assert.equal(rows(f).length, 3, 'Go reports do not record rows');
  const empty = fixture();
  assert.deepEqual(run(empty, ['logs', 'handovers']), { code: 0, stdout: 'no hand-overs recorded\n', stderr: '' }, 'missing file is empty');
  mkdirSync(path.dirname(empty.file), { recursive: true });
  writeFileSync(empty.file, '');
  assert.deepEqual(run(empty, ['logs', 'handovers']), { code: 0, stdout: 'no hand-overs recorded\n', stderr: '' }, 'empty file has the exact empty message');
  assert.deepEqual(JSON.parse(run(empty, ['logs', 'handovers', '--json']).stdout), { total: 0, rows: [] }, 'empty JSON has zero total and no rows');
  for (const flags of [['--bad'], ['--since'], ['--since', 'yesterday'], ['--since', '2026-01-01', '--since', '2026-02-01'], ['--json=yes'], ['--json', '--json'], ['extra']]) {
    const bad = run(empty, ['logs', 'handovers', ...flags]);
    assert.equal(bad.code, 2, flags + ': usage exit 2');
    assert.equal(bad.stdout, '', flags + ': no stdout');
    assert.match(bad.stderr, /^logs handovers: .+\nusage:/, flags + ': clear usage message');
  }
  const parity = fixture();
  assert.deepEqual(await runParity(['version'], { fixture: parity, goCommand: path.join(root, `native/bin/${process.platform}-${process.arch}/code-forge-native`), env: { CODE_FORGE_HANDOVER_LOG: '0', CODE_FORGE_NODE: process.execPath } }), { equal: true, diffs: [] }, 'real fallback has zero parity diffs when telemetry is disabled');
});

test('B282 privacy: only known verbs and their own subcommands reach the log', () => {
  const f = fixture();
  for (const args of [['my-secret-project-name'], ['doctor'], ['block', 'my-secret-project-name'], ['doctor', 'close']]) {
    assert.equal(run(f, args).code, 0, 'hand-over succeeds');
  }
  assert.deepEqual(rows(f).map(({ verb, sub }) => ({ verb, sub })), [
    { verb: 'unknown', sub: '' },
    { verb: 'doctor', sub: '' },
    { verb: 'block', sub: 'unknown' },
    { verb: 'doctor', sub: 'unknown' },
  ], 'known verbs are preserved and subcommands are scoped to their verb');
  assert.equal(readFileSync(f.file, 'utf8').includes('my-secret-project-name'), false, 'private command tokens are absent from the entire log');
});

test('B282 boundary: disabled and failed recording preserve behavior; cap and parallel execs keep complete rows', async () => {
  const f = fixture();
  const expected = run(f, ['doctor'], { CODE_FORGE_HANDOVER_LOG: '0' });
  assert.equal(rows(f).length, 0, 'opt-out writes zero rows');
  assert.deepEqual(run(f, ['doctor'], { CODE_FORGE_NATIVE: '0' }, path.join(scratch, 'bin/code-forge')), expected, 'Node-only entry preserves bytes');
  assert.equal(rows(f).length, 0, 'Node-only entry writes zero rows');
  assert.equal(run(f, ['internal', 'version'], { CODE_FORGE_NODE: '/nonexistent' }).code, 0, 'Go command does not start Node');
  assert.equal(rows(f).length, 0, 'Go command writes zero rows');
  chmodSync(f.home, 0o500);
  try { assert.deepEqual(run(f, ['doctor']), expected, 'read-only home preserves output and exit'); }
  finally { chmodSync(f.home, 0o700); }
  assert.equal(rows(f).length, 0, 'read-only home writes zero rows');
  mkdirSync(path.dirname(f.file), { recursive: true });
  const old = Array.from({ length: 5000 }, (_, i) => ({ at: new Date(Date.UTC(2020, 0, 1, 0, 0, i)).toISOString(), verb: 'doctor', sub: '', reason: 'unrouted', version: pkg.version }));
  writeFileSync(f.file, old.map((r) => JSON.stringify(r)).join('\n') + '\n');
  assert.deepEqual(run(f, ['doctor']), expected, 'trim preserves Node output and exit');
  assert.equal(rows(f).length, 4000, '5001 rows trim to exactly 4000');
  assert.deepEqual(rows(f)[0], old[1001], 'trim keeps the newest 3999 prior rows');
  assert.equal(rows(f).at(-1).reason, 'unrouted', 'trim includes this hand-over');
  assert.equal(statSync(f.file).mode & 0o777, 0o600, 'trim restores private file mode');
  const filtered = JSON.parse(run(f, ['logs', 'handovers', '--json', '--since', old[4999].at]).stdout);
  assert.equal(filtered.total, 2, 'since includes the boundary timestamp and the new row');
  assert.equal(filtered.rows[0].count, 2, 'since counts exactly two matching rows');
  const parallel = fixture();
  const child = () => new Promise((resolve, reject) => {
    const p = spawn(binary, ['doctor'], { cwd: parallel.home, env: env(parallel), stdio: ['ignore', 'ignore', 'pipe'] });
    p.on('error', reject); p.on('close', resolve);
  });
  assert.deepEqual(await Promise.all([child(), child()]), [0, 0], 'both parallel hand-overs succeed');
  assert.equal(rows(parallel).length, 2, 'two parallel execs produce exactly two valid JSON lines');
  assert.deepEqual(rows(parallel).map((r) => r.reason), ['unrouted', 'unrouted'], 'both complete rows have the right reason');
  assert.equal(run(parallel, ['doctor'], { CODE_FORGE_NODE: '/nonexistent' }).code, 127, 'hand-over requires Node');
});
