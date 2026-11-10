import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { chmod, mkdir, mkdtemp, readFile, readdir, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { assemblePack, packTokens } from '../../src/context/pack.mjs';
import { initialConfigState } from '../../src/state/config-snapshot.mjs';
import { mergeForbidden } from '../../src/util/forbidden.mjs';
import { verifyRow } from '../../src/state/signer.mjs';
import { runParity } from '../helpers/parity.mjs';
import { linkGoBinary } from '../helpers/go-binary.mjs';

const ROOT = fileURLToPath(new URL('../../', import.meta.url));
const CLI = path.join(ROOT, 'bin/code-forge.mjs');
const NO_NODE = '/nonexistent-b290-node';
// Strip the real gate's session identity from every child, including build/git.
function env(extra = {}) {
  const out = { ...process.env, ...extra };
  for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete out[key];
  return out;
}
const scratch = await realpath(await mkdtemp(path.join(os.tmpdir(), 'context-pack-')));
const pkg = path.join(scratch, 'package');
const binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const { version } = JSON.parse(await readFile(path.join(ROOT, 'package.json'), 'utf8'));
after(() => rm(scratch, { recursive: true, force: true }));
before(async () => {
  await mkdir(path.dirname(binary), { recursive: true });
  await mkdir(path.join(pkg, 'bin'));
  await symlink(CLI, path.join(pkg, 'bin/code-forge.mjs'));
  linkGoBinary(binary, { trimpath: true, ldflags: `-X main.version=${version}` });
});

async function fixture() {
  const dir = await mkdtemp(path.join(scratch, 'fixture-'));
  const f = { repo: path.join(dir, 'repo'), home: path.join(dir, 'home') };
  await mkdir(f.repo); await mkdir(f.home);
  const git = spawnSync('git', ['init', '-q', f.repo], { env: env(), encoding: 'utf8' });
  assert.equal(git.status, 0, 'fixture git init: ' + git.stderr);
  const config = { version: 1, tmp: { root: path.join(f.repo, '.code-forge/tmp') } };
  f.record = { run_id: 'sample', project: 'sample', workspace: f.repo, blocks: { B1: {
    level: 'L2', owned_files: ['src/café.mjs'], test_files: ['test/sample.test.mjs'],
    worktree: { path: f.repo, branch: 'sample' },
    brief: { path: 'brief.md' }, acceptance: [{ clause: 'works é 🐱', tests: ['test/sample.test.mjs'] }, { clause: 'inspect', kind: 'human_check' }],
  } } };
  f.key = Buffer.alloc(32, 7);
  await mkdir(path.join(f.home, '.code-forge/runs'), { recursive: true });
  await writeFile(path.join(f.home, '.code-forge/runs/sample.key'), f.key, { mode: 0o600 });
  await writeFile(path.join(f.home, '.code-forge/runs/sample.json'), JSON.stringify(f.record));
  await mkdir(path.join(config.tmp.root, 'sample'), { recursive: true, mode: 0o700 });
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify(config));
  await writeFile(path.join(f.repo, 'brief.md'), 'Do the task.\r\n');
  return { ...f, config, record: f.record, key: f.key };
}
const args = ['pack', '--run', 'sample', '--block', 'B1'];
function launch(f, a, extra = {}, executable = binary) {
  return spawnSync(executable, executable === binary ? ['context', ...a] : [CLI, 'context', ...a], {
    cwd: f.repo, encoding: 'utf8', env: env({ HOME: f.home, CODE_FORGE_NATIVE: '0',
      CODE_FORGE_HANDOVER_LOG: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1', ...extra }),
  });
}
async function parity(f, a, extra = {}) {
  assert.deepEqual(await runParity(['context', ...a], { fixture: f, goCommand: binary, samePaths: true,
    env: { CODE_FORGE_NODE: NO_NODE, ...extra },
    // Relocation moves tmp.root inside the snapshot. Re-hash the relocated
    // fixture so this tests a valid snapshot rather than two hash refusals.
    prepare: f.record?.config ? async (copy) => {
      const file = path.join(copy.home, '.code-forge/runs/sample.json');
      const record = JSON.parse(await readFile(file, 'utf8'));
      record.config = initialConfigState(record.config.snapshots[record.config.hash]);
      await writeFile(file, JSON.stringify(record));
    } : undefined,
  }), { equal: true, diffs: [] }, JSON.stringify(a) + ': zero exit, stream, pack-byte or signed-state differences');
}
async function configure(f, context) {
  f.config.context = context;
  await writeFile(path.join(f.repo, '.code-forge.yml'), JSON.stringify(f.config));
}

test('B290 clause: local context packs match Node bytes, streams and signed writes without starting Node', async () => {
  const f = await fixture();
  const speed = launch(f, args);
  assert.equal(speed.status, 0, 'normal pack succeeds with CODE_FORGE_NODE=/nonexistent (speed proof): ' + speed.stderr);
  assert.equal(speed.stderr, '', 'normal pack has exactly zero stderr bytes');
  const file = path.join(f.config.tmp.root, 'sample/context/B1.md');
  const expected = assemblePack({ settings: null, sources: { rules: [], lessons: null }, block: {
    run: 'sample', block: 'B1', level: 'L2', owned: f.record.blocks.B1.owned_files,
    tests: f.record.blocks.B1.test_files, worktree: f.record.blocks.B1.worktree,
    brief: { path: 'brief.md', text: 'Do the task.' }, acceptance: f.record.blocks.B1.acceptance,
    codeForge: { node: process.execPath, bin: CLI, version },
  } });
  assert.equal(speed.stdout, expected.text, 'stdout equals every expected pack byte');
  // B364: the Go pack's forbidden section names every coder-enforced rule of the Node table.
  const goLines = speed.stdout.split('\n');
  const start = goLines.indexOf('## forbidden');
  assert.ok(start >= 0, 'the Go pack has a ## forbidden section');
  const ruleLines = goLines.slice(start + 2, goLines.indexOf('safe ways:', start));
  assert.deepEqual(ruleLines, mergeForbidden().map((e) => `- ${e.id}: ${e.description}`), 'the Go pack lists each coder-enforced rule of the Node table, in order');
  assert.equal(await readFile(file, 'utf8'), expected.text, 'saved pack equals every expected byte');
  const ledgerFile = path.join(f.home, '.code-forge/ledger/sample.jsonl');
  const first = await readFile(ledgerFile, 'utf8');
  const rows = first.trimEnd().split('\n').map((line) => JSON.parse(line));
  assert.equal(rows.length, 1, 'first pack appends exactly one row');
  assert.equal(verifyRow(rows[0], f.key).ok, true, 'context.pack MAC verifies');
  assert.equal(rows[0].event, 'context.pack', 'row event is context.pack');
  const json = launch(f, [...args, '--json', '--path-only']);
  assert.equal(json.status, 0, 'JSON pack runs natively');
  assert.deepEqual(JSON.parse(json.stdout), { path: file, tokens: packTokens(expected.text), lines: expected.lines, cut: 0, sha256: rows[0].sha256 }, 'JSON returns exactly five expected fields');
  assert.equal(await readFile(ledgerFile, 'utf8'), first, 'identical pack appends zero ledger bytes');
  // Fresh copies need identical absolute paths because the SHA covers header paths.
  await rm(ledgerFile);
  for (const flags of [[], ['--json'], ['--path-only'], ['--json', '--path-only']]) await parity(f, [...args, ...flags]);
  await writeFile(path.join(f.repo, 'rules.md'), 'one\r\ntwo\nthree\nfour\n');
  await writeFile(path.join(f.repo, 'lessons.md'), 'learn\n'.repeat(40));
  for (const context of [{}, { rules: ['rules.md'], lessons: 'lessons.md', commands: { test: ['node', 'a b', "it\u0027s", '$HOME'] }, max_lines: 45 }]) {
    await configure(f, context);
    await parity(f, args); await parity(f, [...args, '--json']);
  }
  // tmp.root is fixed by the run snapshot while context remains live.
  f.record.config = initialConfigState(structuredClone(f.config));
  f.config.tmp.root = path.join(f.repo, '.code-forge/unused-live-root');
  await writeFile(path.join(f.home, '.code-forge/runs/sample.json'), JSON.stringify(f.record));
  await configure(f, { rules: ['rules.md'], max_lines: 20 });
  await parity(f, [...args, '--json']);
  f.record.blocks.B1.acceptance[0].clause = 'works\nacross lines';
  await writeFile(path.join(f.home, '.code-forge/runs/sample.json'), JSON.stringify(f.record));
  await parity(f, [...args, '--json']);
  // Signed last-row dedup ignores invalid signatures and respects the last verified row.
  const result = launch(f, args);
  assert.equal(result.status, 0, 'configured pack stays native');
  const before = await readFile(ledgerFile, 'utf8');
  await writeFile(ledgerFile, before + JSON.stringify({ event: 'context.pack', run: 'sample', block: 'B1', mac: 'invalid' }) + '\n');
  await parity(f, args);
  assert.equal(launch(f, args).status, 0, 'verified previous row supports native dedup');
  assert.equal(await readFile(ledgerFile, 'utf8'), before + JSON.stringify({ event: 'context.pack', run: 'sample', block: 'B1', mac: 'invalid' }) + '\n', 'unverified final row causes zero extra rows');
  // Contention on the shared ledger append lock must delay publication until release.
  await rm(ledgerFile);
  const lock = path.join(f.home, '.code-forge/ledger/sample.append.lock');
  await writeFile(lock, '{}');
  const child = spawn(binary, ['context', ...args], { cwd: f.repo, env: env({ HOME: f.home, CODE_FORGE_NATIVE: '0', CODE_FORGE_NODE: NO_NODE, CODE_FORGE_NO_ERROR_LOG: '1' }), stdio: ['ignore', 'pipe', 'pipe'] });
  const ended = once(child, 'close');
  let stderr = ''; child.stderr.on('data', (s) => { stderr += s; });
  try {
    for (let i = 0; i < 100; i++) {
      if ((await readdir(path.dirname(file))).some((s) => s.endsWith('.tmp'))) break;
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    assert.equal(child.exitCode, null, 'concurrent writer waits on the held append lock');
    await rm(lock);
    assert.deepEqual(await ended, [0, null], 'writer succeeds after lock release: ' + stderr);
    const concurrent = (await readFile(ledgerFile, 'utf8')).trimEnd().split('\n').map((line) => JSON.parse(line));
    assert.equal(concurrent.length, 1, 'contended append publishes exactly one complete row');
    assert.equal(verifyRow(concurrent[0], f.key).ok, true, 'contended row MAC verifies');
  } finally { child.kill('SIGKILL'); await rm(lock, { force: true }); }
});

test('B290 boundary: usage/refusals are exact and unported context cases hand over before writes', async () => {
  const f = await fixture();
  const usage = launch(f, ['pack']);
  assert.equal(usage.status, 2, 'ported missing-argument usage runs without Node');
  assert.equal(usage.stdout, '', 'usage has zero stdout bytes');
  assert.equal(usage.stderr, 'context: context pack needs --run and --block\nusage: code-forge context pack --run <id> --block <id> [--json] [--path-only]\n', 'exact missing-argument text');
  for (const a of [['pack'], ['pack', '--run'], ['pack', '--run='], ['pack', '--block'], [...args, 'extra'], [...args, '--run', 'sample'], [...args, '--json=1'], [...args, '--json', '--json'], [...args, '--path-only=1'], [...args, '--path-only', '--path-only'], ['pack', '--run', 'sample', '--block', '!'], ['pack', '--run', 'BAD', '--block', 'B1'], ['pack', '--run', 'missing', '--block', 'B1'], ['pack', '--run', 'sample', '--block', 'B2']]) await parity(f, a);
  const contexts = [null, false, [], { rules: 'x' }, { rules: [''] }, { rules: ['rules.md', './rules.md'] }, { lessons: 1 }, { commands: [] }, { commands: { other: ['x'] } }, { commands: { test: [] } }, { commands: { test: ['a\nb'] } }, { require_coder_tests: 1 }, { max_lines: 19 }, { max_lines: 20.5 }, { rules: ['missing'] }, { rules: ['/outside'] }, { rules: ['../outside'] }, { rules: ['a\nb'] }, { rules: ['.'] }];
  for (const context of contexts) { await configure(f, context); await parity(f, args); }
  await symlink('/nonexistent-outside', path.join(f.repo, 'outside'));
  await configure(f, { rules: ['outside'] }); await parity(f, args);
  await symlink('loop', path.join(f.repo, 'loop'));
  await configure(f, { lessons: 'loop' }); await parity(f, args);
  await configure(f, {});
  const entry = f.record.blocks.B1;
  /** @type {Array<[string, any]>} */
  const malformedHeaders = [['level', 'L2\nforged'], ['owned_files', ['a\nb']], ['test_files', ['a\nb']], ['worktree', { path: 'a\nb', branch: 'sample' }], ['worktree', { path: f.repo, branch: 'a\nb' }], ['brief', { path: 'missing' }]];
  for (const [key, value] of malformedHeaders) {
    const old = entry[key]; entry[key] = value;
    await writeFile(path.join(f.home, '.code-forge/runs/sample.json'), JSON.stringify(f.record));
    await parity(f, args); entry[key] = old;
  }
  await writeFile(path.join(f.home, '.code-forge/runs/sample.json'), JSON.stringify(f.record));
  f.config.tmp.root = 'relative';
  await configure(f, {}); await parity(f, args);
  f.config.tmp.root = path.join(f.repo, '.code-forge/tmp');
  await configure(f, {});
  // A blocked destination and a rename failure cover both publication stages.
  const packDir = path.join(f.config.tmp.root, 'sample/context');
  await mkdir(packDir); await chmod(packDir, 0o500);
  await parity(f, args); await chmod(packDir, 0o700); await rm(packDir, { recursive: true });
  await mkdir(path.join(packDir, 'B1.md'), { recursive: true });
  await parity(f, args); await rm(packDir, { recursive: true });
  await chmod(path.join(f.home, '.code-forge/runs/sample.key'), 0o644); await parity(f, args);
  await chmod(path.join(f.home, '.code-forge/runs/sample.key'), 0o600);
  await rm(path.join(f.home, '.code-forge/runs/sample.key')); await parity(f, args);
  await writeFile(path.join(f.home, '.code-forge/runs/sample.key'), f.key, { mode: 0o600 });
  for (const a of [[], ['unknown'], [...args, '--unknown'], ['--help'], ['pack', '--help'], ['pack', '--run', 'sample', '--block', 'B1\r']]) {
    const result = launch(f, a);
    assert.equal(result.status, 127, JSON.stringify(a) + ': unported case exits 127 without Node');
    assert.equal(result.stdout, '', 'handover has zero stdout bytes');
    assert.match(result.stderr, /code-forge-native: node executable .* not found:/, 'handover reports missing Node');
    await parity(f, a, { CODE_FORGE_NODE: process.execPath });
  }
  await parity(f, args, { CODE_FORGE_NODE: process.execPath, CODE_FORGE_NO_ERROR_LOG: '0' });
  assert.equal(launch(f, args, { CODE_FORGE_NO_ERROR_LOG: '0' }).status, 127, 'logging-enabled pack hands over before Go writes');
  const files = await readdir(path.join(f.home, '.code-forge'));
  assert.deepEqual(files, ['runs'], 'refusals and unavailable handovers create zero state directories');
  assert.deepEqual(await readdir(path.join(f.config.tmp.root, 'sample')), [], 'refusals and unavailable handovers create zero pack files or temporary files');
  const handed = launch(f, ['unknown'], { CODE_FORGE_NATIVE: '1', CODE_FORGE_HANDOVER_LOG: '1' });
  assert.equal(handed.status, 127, 'telemetry-enabled unknown context is handed to unavailable Node');
  const telemetry = spawnSync(binary, ['logs', 'handovers', '--json'], { cwd: f.repo, encoding: 'utf8', env: env({ HOME: f.home, CODE_FORGE_NATIVE: '0' }) });
  assert.equal(telemetry.status, 0, 'handover report reads the private fixture telemetry');
  const reported = JSON.parse(telemetry.stdout);
  assert.equal(reported.total, 1, 'exactly one handover was recorded');
  assert.deepEqual(reported.rows.map(({ verb, sub, reason, count }) => ({ verb, sub, reason, count })), [{ verb: 'context', sub: 'unknown', reason: 'unrouted', count: 1 }], 'public handover reason is unrouted');
  await rm(path.join(f.config.tmp.root, 'sample'), { recursive: true });
  assert.equal(launch(f, args).status, 127, 'missing run root hands its owner protocol over to Node');
  assert.deepEqual(await readdir(f.config.tmp.root), [], 'missing-root handover creates exactly zero run-root entries');
});
