import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { helpPageParity, isolatedEnv, runParity } from '../helpers/parity.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { processStartTime } from '../../src/state/run.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker, enqueue, writeResult } from '../../src/worker/queue.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const BIN = path.join(ROOT, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const scratch = await mkdtemp(path.join(tmpdir(), 'cf-human-'));
const RUN = 'r-human', KEY = Buffer.alloc(32, 29);
after(() => rm(scratch, { recursive: true, force: true }));

// Real terminals for both engines. Wait until the initial frame is complete,
// then send one key at a time; capture stdout and stderr separately. No env
// override can confirm a production prompt, and no implementation text is read.
const PTY = String.raw`
import json, os, pty, select, struct, subprocess, sys, termios, time
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
        sys.exit(124)
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
        mutation = os.environ.get('HUMAN_MUTATION')
        if mutation == 'remove-proof':
            os.unlink('sample.mjs')
        elif mutation == 'change-close-state':
            filename = os.path.join(os.environ['HOME'], '.code-forge/runs/r-human.json')
            with open(filename) as stream: record = json.load(stream)
            # B380 closes acceptance revisions natively; a pending approval still hands over.
            record['autopilot_approvals'] = [{'status': 'active', 'until': '2000-01-01T00:00:00.000Z'}]
            with open(filename, 'w') as stream: json.dump(record, stream)
        for key in bytes.fromhex(os.environ['HUMAN_KEYS']).split(b'|'):
            os.write(master, key)
            time.sleep(.08)
        sent = True
os.close(master)
sys.stdout.buffer.write(out)
sys.stderr.buffer.write(err)
if not sent:
    sys.exit(125)
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
async function parity(f, args, keys, expected, extra = {}, page) {
  const terminal = keys !== undefined;
  const r = await runParity(args, { fixture: f, goCommand: terminal ? ['python3', '-c', PTY, BIN] : BIN,
    nodeCommand: terminal ? ['python3', '-c', PTY, process.execPath, CLI] : [process.execPath, CLI], samePaths: true, tempRoot: scratch,
    env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', NO_COLOR: '1', TERM: 'xterm-256color', HUMAN_KEYS: keys ?? '', ...extra },
    checkProcess(engine, result) {
      assert.equal(result.code, expected, `${engine}: exact exit ${expected}: ${result.stderr}`);
      if (extra.FORCE_COLOR === '1') {
        assert.match(String(result.stdout), /\x1b\[\d+(?:;\d+)*m/, `${engine}: forced-colour stdout contains SGR colour escapes`);
      } else {
        assert.doesNotMatch(String(result.stdout), /\x1b\[\d+(?:;\d+)*m/, `${engine}: NO_COLOR stdout contains zero SGR colour escapes`);
      }
    } });
  // B613: a page means Go prints the help page around Node's usage; nothing else may differ.
  if (page) assert.deepEqual(r, helpPageParity(r, page), `${args.join(' ')}: only stdout differs, Go prints the help page around Node usage`);
  else assert.deepEqual(r.diffs, [], `${args.join(' ')}: zero stdout, stderr, state and signed-row differences`);
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
  for (const kind of ['close', 'human-pass']) {
    await parity(await fixture(kind), argv(kind), '79', 0, { HUMAN_COLUMNS: kind === 'close' ? '80' : '240', NO_COLOR: undefined, FORCE_COLOR: '1' });
  }
  await parity(await fixture('human-pass'), ['block', 'human-pass', 'B1', '--run', RUN, '--note', 'checked'], '79', 0, { TERM: 'linux' });
});

test('B380: human-pass --help and -h print Node usage natively with no hand-over', async () => {
  for (const flags of [['--help'], ['-h'], ['B1', '--run', RUN, '--help'], ['B1', '--note', 'x', '-h']]) {
    const f = await fixture('human-pass'), args = ['block', 'human-pass', ...flags];
    // B613: `block human-pass --help` (the help flag right after the sub-verb) is the Go help page.
    await parity(f, args, undefined, 0, {}, flags.length === 1 ? { summary: "Record the owner's device check and close (owner)" } : undefined);
    const native = launch(BIN, args, f, { CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1' });
    assert.equal(native.status, 0, `${flags}: usage exits 0 with Node unavailable: ${native.stderr}`);
    assert.match(native.stdout, /block human-pass <id> --run <r> --note <text>/, `${flags}: human-pass usage on stdout`);
    const report = launch(BIN, ['logs', 'handovers', '--json'], f);
    assert.deepEqual(JSON.parse(report.stdout), { total: 0, rows: [] }, `${flags}: exactly zero handovers`);
  }
});

async function assertNoClose(f) {
  const rows = (await readFile(path.join(f.home, '.code-forge/ledger/human.jsonl'), 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
  assert.equal(rows.filter((r) => r.event === 'block.close' || r.event === 'block.human_pass').length, 0, 'no signed close or human-pass rows');
  return rows;
}
async function rejected(kind, args, keys, expected, extra = {}) {
  const f = await fixture(kind);
  await parity(f, args, keys, expected, extra);
  const result = keys === undefined ? launch(BIN, args, f, extra)
    : launch('python3', ['-c', PTY, BIN, ...args], f, { HUMAN_KEYS: keys, ...extra });
  assert.equal(result.status, expected, `${kind}: exact native rejection: ${result.stderr}`);
  await assertNoClose(f);
}

test('human prompts reject no terminal, cancellation and bad reason without recording a close', async () => {
  for (const kind of ['decide', 'decide-finding', 'close', 'human-pass']) {
    await rejected(kind, argv(kind), undefined, 2);
    await rejected(kind, argv(kind), undefined, 2, { CODE_FORGE_NO_ERROR_LOG: '0' });
    for (const keys of ['03', '1b', '6e', '0d', '09|0d']) await rejected(kind, argv(kind), keys.replace('|', '7c'), 1);
  }
  for (const reason of ['', ' ', 'bad\nreason']) {
    await rejected('decide', ['block', 'close', 'B1', '--run', RUN, '--decide', '--reason', reason], undefined, 2);
  }
  await rejected('human-pass', ['block', 'human-pass', 'B1', '--run', RUN, '--note', 'checked', '--proof', 'missing'], undefined, 2);
});

test('Space then Enter confirms human close, decide and human-pass', async () => {
  for (const kind of ['decide', 'decide-finding', 'close', 'human-pass']) {
    await parity(await fixture(kind), argv(kind), '207c0d', 0);
  }
});

test('human-pass proof removed during confirmation is a usage error without Node fallback', async () => {
  await rejected('human-pass', argv('human-pass'), '79', 2, { HUMAN_MUTATION: 'remove-proof' });
});

test('close state changes after confirmation exit 1 without a second prompt or handover', async () => {
  for (const kind of ['close', 'decide', 'decide-finding']) {
    const f = await fixture(kind);
    const result = launch('python3', ['-c', PTY, BIN, ...argv(kind)], f,
      { HUMAN_KEYS: '79', HUMAN_MUTATION: 'change-close-state', CODE_FORGE_HANDOVER_LOG: '1' });
    assert.equal(result.status, 1, `${kind}: ${result.stderr}`);
    assert.equal(result.stderr, 'block close: state changed after human confirmation\n');
    assert.equal(result.stdout.split('\x1b[?25l').length - 1, 1, 'exactly one prompt start');
    await assertNoClose(f);
    const report = launch(BIN, ['logs', 'handovers', '--json'], f);
    assert.deepEqual(JSON.parse(report.stdout), { total: 0, rows: [] });
  }
});

test('decide with auto-claim matches Node claims before cancellation', async () => {
  const f = await fixture('decide');
  const content = 'export const outsider = true;\n';
  await writeFile(path.join(f.repo, 'outsider.mjs'), content);
  const record = JSON.parse(await readFile(path.join(f.home, '.code-forge/runs', `${RUN}.json`), 'utf8'));
  const mirror = { ...record }; delete mirror.worker;
  await writeFile(path.join(f.repo, '.code-forge/runs', `${RUN}.json`), JSON.stringify(mirror) + '\n');
  const saved = process.env.HOME;
  try {
    process.env.HOME = f.home;
    await appendRow(signRow({ run: RUN, event: 'review.approved', block: 'B1', file: 'outsider.mjs',
      content_hash: createHash('sha256').update(content).digest('hex') }, KEY), { slug: 'human' });
    announceWorker(f.repo, { pid: process.pid, run: RUN });
  } finally { process.env.HOME = saved; }
  const prepare = async (copy) => {
    const oldHome = process.env.HOME;
    try {
      process.env.HOME = copy.home;
      for (const file of ['sample.mjs', 'outsider.mjs']) {
        const queued = enqueue({ repoRoot: copy.repo, run: RUN, block: 'B1', file });
        assert.equal(queued.status, 'queued');
        writeResult({ repoRoot: copy.repo, runId: RUN, ticket: queued.ticket, key: KEY,
          result: { status: 'reviewed', approved: true, ts: '2020-01-01T00:00:00.000Z', findings: [] } });
      }
    } finally { process.env.HOME = oldHome; }
    return async () => {
      const rows = await assertNoClose(copy);
      assert.equal(rows.filter((r) => r.event === 'block.claim' && r.path === 'outsider.mjs').length, 1, 'Node and Go retain the automatic claim after cancellation');
    };
  };
  for (const json of [false, true]) {
    const args = [...argv('decide'), '--auto-claim', ...(json ? ['--json'] : [])];
    const result = await runParity(args, { fixture: f, goCommand: ['python3', '-c', PTY, BIN],
      nodeCommand: ['python3', '-c', PTY, process.execPath, CLI], samePaths: true, prepare, tempRoot: scratch,
      env: { CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: '/nonexistent', NO_COLOR: '1', TERM: 'xterm-256color', HUMAN_KEYS: '6e' },
      checkProcess(engine, processResult) { assert.equal(processResult.code, 1, `${engine}: ${processResult.stderr}`); } });
    assert.deepEqual(result.diffs, [], `auto-claim before decide prompt matches Node exactly, json=${json}`);
  }
});
