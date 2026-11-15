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

// B690a: Go never starts Node. A case Go does not serve gets one refusal line, exit 1,
// and still writes its hand-over row; an unknown verb gets Node's unknown-verb text and
// writes nothing. These tests use a block attempt case Go does not serve: a run file that is
// a directory (B690a fix1: an unknown block subcommand is now Node's usage error, exit 2;
// B744: a secret-like --run value now runs in Go with Node's "no run record" text).
const runId = 'private-run';
const unported = ['block', 'attempt', 'B1', '--run', runId];
// B744: keys list with a store index that is a directory is a second case Go does not serve.
const keysUnported = ['keys', 'list'];
const refusal = 'code-forge: block attempt is not supported in this case yet (block-unported-subcommand). Please report it: code-forge logs report\n';
function fixture() {
  const home = mkdtempSync(path.join(scratch, 'home-'));
  mkdirSync(path.join(home, '.code-forge/runs', runId + '.json'), { recursive: true });
  mkdirSync(path.join(home, '.code-forge/store/index.json'), { recursive: true });
  return { home, repo: home, file: path.join(home, '.code-forge/native/handovers.jsonl') };
}
function env(f, extra = {}) {
  return { ...process.env, HOME: f.home, CODE_FORGE_NODE: process.execPath, CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1', CODE_FORGE_NO_ERROR_LOG: '1', CODE_FORGE_KEY_BACKEND: 'file', PATH: path.dirname(process.execPath) + path.delimiter + process.env.PATH, ...extra };
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

test('B282 clause: refusals record private-safe reasons and Go reports counts', async () => {
  const f = fixture();
  const args = ['block', 'attempt', 'secret-id', '--run', runId];
  const input = 'private stdin\n';
  const expected = run(f, args, { CODE_FORGE_HANDOVER_LOG: '0', PROBE_TOKEN: 'unchanged', PROBE_EXIT: '3' }, binary, input);
  assert.deepEqual(expected, { code: 1, stdout: '', stderr: refusal }, 'B690a: one refusal line, exit 1, the Node probe never runs');
  assert.equal(rows(f).length, 0, 'opt-out writes zero rows');
  assert.deepEqual(run(f, args, { PROBE_TOKEN: 'unchanged', PROBE_EXIT: '3' }, binary, input), expected, 'telemetry preserves every output byte and exit 1');
  assert.equal(rows(f).length, 1, 'one refusal writes exactly one row');
  const row = rows(f)[0];
  assert.deepEqual(Object.keys(row).sort(), ['at', 'reason', 'sub', 'verb', 'version'], 'row has only five safe fields');
  assert.deepEqual({ ...row, at: 'time' }, { at: 'time', verb: 'block', sub: 'attempt', reason: 'block-unported-subcommand', version: pkg.version }, 'the row keeps no flag values, tokens or ids');
  assert.equal(readFileSync(f.file, 'utf8').includes(runId) || readFileSync(f.file, 'utf8').includes('secret-id'), false, 'the token and the id are absent from the log');
  assert.match(row.at, /^\d{4}-\d\d-\d\dT.*Z$/, 'timestamp is UTC ISO');
  assert.equal(Number.isNaN(Date.parse(row.at)), false, 'timestamp is valid');
  // B744: a token-like close argument now runs in Go with Node's text and writes no row.
  const closeArgs = ['block', 'close', 'private-id', '--run', 'r1', '--reason=sk-private'];
  assert.deepEqual(run(f, closeArgs, {}, binary, input), { code: 1, stdout: '', stderr: 'block close: no run record for r1\n' }, 'B744: close prints Node\'s text');
  assert.equal(rows(f).length, 1, 'B744: a close Go serves writes no row');
  assert.deepEqual(run(f, keysUnported, {}, binary, input), { code: 1, stdout: '', stderr: 'code-forge: keys list is not supported in this case yet (unrouted). Please report it: code-forge logs report\n' }, 'keys refusal names the subcommand');
  assert.equal(rows(f).length, 2, 'keys adds exactly one row');
  assert.equal(rows(f)[1].reason, 'unrouted', 'keys has its own stable reason');
  assert.equal(rows(f)[1].sub, 'list', 'the keys subcommand is recorded');
  run(f, args);
  assert.equal(rows(f).length, 3, 'repeat refusal adds exactly one row');
  assert.equal(statSync(f.file).mode & 0o777, 0o600, 'log mode is 0600');
  assert.equal(statSync(path.dirname(f.file)).mode & 0o777, 0o700, 'directory mode is 0700');
  const report = run(f, ['logs', 'handovers', '--json'], { CODE_FORGE_NODE: '/nonexistent' });
  assert.equal(report.code, 0, 'report runs without Node');
  assert.equal(report.stderr, '', 'report has no stderr');
  assert.deepEqual(JSON.parse(report.stdout), { total: 3, rows: [
    { verb: 'block', sub: 'attempt', reason: 'block-unported-subcommand', count: 2, last_at: rows(f)[2].at },
    { verb: 'keys', sub: 'list', reason: 'unrouted', count: 1, last_at: rows(f)[1].at },
  ] }, 'JSON sorts by count and reports the latest timestamp');
  assert.equal(run(f, ['logs', 'handovers']).stdout, 'count  verb  sub  reason\n2  block  attempt  block-unported-subcommand\n1  keys  list  unrouted\ntotal  3\n', 'text table sorts by count and includes total');
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
  for (const [args, code] of [[['my-secret-project-name'], 1], [['block', 'my-secret-project-name'], 2], [['my-secret-project-name', 'close'], 1], [['block', 'attempt', 'my-secret-project-name', '--run', runId], 1]]) {
    assert.equal(run(f, args).code, code, `${args[0]} ${args[1] || ''}: B690a unknown verb or refusal exit 1, B690a fix1 unknown block subcommand usage exit 2`);
  }
  // B690a: an unknown verb gets Node's unknown-verb text and writes no row; fix1: so does a usage error
  assert.deepEqual(rows(f).map(({ verb, sub }) => ({ verb, sub })), [
    { verb: 'block', sub: 'attempt' },
  ], 'known verbs are preserved and subcommands are scoped to their verb');
  assert.equal(readFileSync(f.file, 'utf8').includes('my-secret-project-name'), false, 'private command tokens are absent from the entire log');
});

test('B282 boundary: disabled and failed recording preserve behavior; cap and parallel execs keep complete rows', async () => {
  const f = fixture();
  const expected = run(f, unported, { CODE_FORGE_HANDOVER_LOG: '0' });
  assert.deepEqual(expected, { code: 1, stdout: '', stderr: refusal }, 'opt-out keeps the refusal');
  assert.equal(rows(f).length, 0, 'opt-out writes zero rows');
  assert.equal(run(f, unported, { CODE_FORGE_NATIVE: '0' }, path.join(scratch, 'bin/code-forge')).code, 0, 'Node-only entry runs the Node probe');
  assert.equal(rows(f).length, 0, 'Node-only entry writes zero rows');
  assert.equal(run(f, ['internal', 'version'], { CODE_FORGE_NODE: '/nonexistent' }).code, 0, 'Go command does not start Node');
  assert.equal(rows(f).length, 0, 'Go command writes zero rows');
  // B744: the fixture made ~/.code-forge (the run file case), so that is the read-only directory
  const state = path.join(f.home, '.code-forge');
  chmodSync(state, 0o500);
  try { assert.deepEqual(run(f, unported), expected, 'read-only home preserves output and exit'); }
  finally { chmodSync(state, 0o700); }
  assert.equal(rows(f).length, 0, 'read-only home writes zero rows');
  mkdirSync(path.dirname(f.file), { recursive: true });
  const old = Array.from({ length: 5000 }, (_, i) => ({ at: new Date(Date.UTC(2020, 0, 1, 0, 0, i)).toISOString(), verb: 'unknown', sub: '', reason: 'unrouted', version: pkg.version }));
  writeFileSync(f.file, old.map((r) => JSON.stringify(r)).join('\n') + '\n');
  assert.deepEqual(run(f, unported), expected, 'trim preserves the refusal output and exit');
  assert.equal(rows(f).length, 4000, '5001 rows trim to exactly 4000');
  assert.deepEqual(rows(f)[0], old[1001], 'trim keeps the newest 3999 prior rows');
  assert.equal(rows(f).at(-1).reason, 'block-unported-subcommand', 'trim includes this refusal');
  assert.equal(statSync(f.file).mode & 0o777, 0o600, 'trim restores private file mode');
  const filtered = JSON.parse(run(f, ['logs', 'handovers', '--json', '--since', old[4999].at]).stdout);
  assert.equal(filtered.total, 2, 'since includes the boundary timestamp and the new row');
  assert.deepEqual(filtered.rows.map((r) => r.count), [1, 1], 'since counts exactly two matching rows, one per reason');
  const parallel = fixture();
  const child = () => new Promise((resolve, reject) => {
    const p = spawn(binary, unported, { cwd: parallel.home, env: env(parallel), stdio: ['ignore', 'ignore', 'pipe'] });
    p.on('error', reject); p.on('close', resolve);
  });
  assert.deepEqual(await Promise.all([child(), child()]), [1, 1], 'both parallel refusals exit 1');
  assert.equal(rows(parallel).length, 2, 'two parallel execs produce exactly two valid JSON lines');
  assert.deepEqual(rows(parallel).map((r) => r.reason), ['block-unported-subcommand', 'block-unported-subcommand'], 'both complete rows have the right reason');
  assert.deepEqual(run(parallel, unported, { CODE_FORGE_NODE: '/nonexistent' }), expected, 'B690a: the refusal needs no Node');
});
