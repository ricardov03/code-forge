import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { isolatedEnv, runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const BIN = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const scratch = await mkdtemp('/tmp/cf-human-');
const RUN = 'r-human', KEY = Buffer.alloc(32, 29);
after(() => rm(scratch, { recursive: true, force: true }));

// Real terminals for both engines. Wait until the initial frame is complete,
// then send one key at a time; capture stdout and stderr separately. No env
// override can confirm a production prompt, and no implementation text is read.
const PTY = String.raw`
import os, pty, select, struct, subprocess, sys, termios, time
master, slave = pty.openpty()
termios.tcsetwinsize(slave, (24, int(os.environ.get('HUMAN_COLUMNS', '240'))))
p = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=subprocess.PIPE)
os.close(slave)
out, err, sent = b'', b'', False
deadline = time.monotonic() + 20
fds = [master, p.stderr.fileno()]
while fds:
    if time.monotonic() > deadline:
        p.kill()
        raise RuntimeError('terminal prompt timed out')
    ready, _, _ = select.select(fds, [], [], .05)
    for fd in ready:
        try: data = os.read(fd, 65536)
        except OSError: data = b''
        if not data:
            fds.remove(fd)
            continue
        if fd == master: out += data
        else: err += data
    if not sent and out.endswith(b'\r\n') and b'Only the owner may say yes.' in out:
        for key in bytes.fromhex(os.environ['HUMAN_KEYS']).split(b'|'):
            os.write(master, key)
            time.sleep(.08)
        sent = True
os.close(master)
sys.stdout.buffer.write(out)
sys.stderr.buffer.write(err)
sys.exit(p.wait())
`;
function launch(command, argv, f, extra = {}) {
  return spawnSync(command, argv, { cwd: f.repo, encoding: 'utf8', env: isolatedEnv({ PATH: process.env.PATH,
    HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', CODE_FORGE_NO_ERROR_LOG: '1',
    CODE_FORGE_KEY_BACKEND: 'file', NO_COLOR: '1', TERM: 'xterm-256color', ...extra }) });
}
async function fixture(kind) {
  const root = await mkdtemp(path.join(scratch, 'f-'));
  const f = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  await writeFile(path.join(f.repo, '.gitignore'), '.code-forge/\n');
  await writeFile(path.join(f.repo, 'sample.mjs'), 'export const value = 1;\n');
  for (const args of [['init', '-qb', 'main'], ['add', '.'], ['-c', 'user.name=Fixture', '-c', 'user.email=f@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'base']]) {
    const r = launch('git', args, f); assert.equal(r.status, 0, r.stderr);
  }
  const base = launch('git', ['rev-parse', 'HEAD'], f).stdout.trim();
  const content = 'export const value = 2;\n';
  await writeFile(path.join(f.repo, 'sample.mjs'), content);
  const record = { run_id: RUN, project: 'human', workspace: f.repo, status: 'active',
    worker: { pid: process.pid, started_at: await processStartTime(process.pid) },
    config: initialConfigState(kind === 'close' ? { close: { require_human: true } } : {}),
    blocks: { B1: { status: kind === 'human-pass' ? 'awaiting_human_check' : 'open', owned_files: ['sample.mjs'],
      base_sha: base, tree_at_open: {}, kind: 'code', test_files: [], human_checks: kind === 'human-pass' ? ['Device works'] : [] } }, orphans: [] };
  const runs = path.join(f.home, '.code-forge/runs');
  await mkdir(runs, { recursive: true });
  await writeFile(path.join(runs, `${RUN}.json`), JSON.stringify(record, null, 2) + '\n');
  await writeFile(path.join(runs, `${RUN}.key`), KEY, { mode: 0o600 });
  const logs = path.join(f.repo, '.code-forge/runs', RUN);
  await mkdir(logs, { recursive: true });
  await writeFile(path.join(logs, 'B1.log'), 'Implementation complete.\n');
  const oldHome = process.env.HOME;
  try {
    process.env.HOME = f.home;
    for (const row of [{ event: 'dispatch', block: 'B1', level: 'L2' },
      { event: 'review.approved', block: 'B1', file: 'sample.mjs', content_hash: createHash('sha256').update(content).digest('hex') },
      ...(kind === 'decide-finding' ? [{ event: 'review.result', block: 'B1', file: 'sample.mjs', content_hash: createHash('sha256').update(content).digest('hex'), status: 'reviewed', approved: false, open: [{ id: 'F1', severity: 'warning', claim: 'Check the device' }] }] : [])]) {
      await appendRow(signRow({ run: RUN, ...row }, KEY), { slug: 'human' });
    }
  } finally { process.env.HOME = oldHome; }
  return f;
}
function argv(kind) {
  return ['block', kind === 'human-pass' ? kind : 'close', 'B1', '--run', RUN,
    ...(kind.startsWith('decide') ? ['--decide', '--reason', 'Owner checked the implementation'] : []),
    ...(kind === 'human-pass' ? ['--note', 'Device checked', '--proof', 'sample.mjs'] : [])];
}
async function parity(f, args, keys, expected, extra = {}) {
  const terminal = keys !== undefined;
  const r = await runParity(args, { fixture: f, goCommand: terminal ? ['python3', '-c', PTY, BIN] : BIN,
    nodeCommand: terminal ? ['python3', '-c', PTY, process.execPath, CLI] : [process.execPath, CLI], samePaths: true,
    env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', NO_COLOR: '1', TERM: 'xterm-256color', HUMAN_KEYS: keys ?? '', ...extra },
    checkProcess(engine, result) { assert.equal(result.code, expected, `${engine}: exact exit ${expected}: ${result.stderr}`); } });
  assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero stdout, stderr, state and signed-row differences`);
}

test('human close, decide and human-pass have terminal, state, signed-row and zero-handover parity', async () => {
  for (const kind of ['decide', 'decide-finding', 'close', 'human-pass']) {
    for (const json of [false, true]) {
      const f = await fixture(kind), args = [...argv(kind), ...(json ? ['--json'] : [])];
      await parity(f, args, '79', 0); // y confirms, as in clack.
      const native = launch('python3', ['-c', PTY, BIN, ...args], f, { HUMAN_KEYS: '79', CODE_FORGE_NATIVE: '1' });
      assert.equal(native.status, 0, `${kind}: succeeds with Node unavailable: ${native.stderr}`);
      const report = launch(BIN, ['logs', 'handovers', '--json'], f);
      assert.deepEqual(JSON.parse(report.stdout), { total: 0, rows: [] }, `${kind}: exactly zero handovers`);
      const rows = (await readFile(path.join(f.home, '.code-forge/ledger/human.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
      assert.equal(rows.filter((r) => !verifyRow(r, KEY).ok).length, 0, `${kind}: every one of ${rows.length} rows verifies`);
      assert.equal(rows.filter((r) => r.event === (kind === 'human-pass' ? 'block.human_pass' : 'block.close')).length, 1, `${kind}: exactly one signed close row`);
      assert.equal(rows.filter((r) => r.event === 'review.waived').length, kind === 'decide-finding' ? 1 : 0, `${kind}: exact signed human decision count`);
    }
  }
  await parity(await fixture('close'), argv('close'), '79', 0, { HUMAN_COLUMNS: '80', NO_COLOR: '', FORCE_COLOR: '1' });
  await parity(await fixture('human-pass'), ['block', 'human-pass', 'B1', '--run', RUN, '--note', 'checked'], '79', 0, { TERM: 'linux' });
});

test('human prompts reject no terminal, cancellation and bad reason without recording a close', async () => {
  for (const kind of ['decide', 'decide-finding', 'close', 'human-pass']) {
    const f = await fixture(kind);
    await parity(f, argv(kind), undefined, 2);
    await parity(f, argv(kind), undefined, 2, { CODE_FORGE_NO_ERROR_LOG: '0' });
    for (const keys of ['03', '1b', '6e', '0d', '20|0d', '09|0d']) await parity(f, argv(kind), keys.replace('|', '7c'), keys === '20|0d' ? 0 : 1);
  }
  for (const reason of ['', ' ', 'bad\nreason']) {
    await parity(await fixture('decide'), ['block', 'close', 'B1', '--run', RUN, '--decide', '--reason', reason], undefined, 2);
  }
  await parity(await fixture('human-pass'), ['block', 'human-pass', 'B1', '--run', RUN, '--note', 'checked', '--proof', 'missing'], undefined, 2);
});
