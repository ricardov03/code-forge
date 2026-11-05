import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFile, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { runParity } from '../helpers/parity.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const cli = path.join(root, 'bin/code-forge.mjs');
let scratch;
let binary;
let fixture;
const entries = Array.from({ length: 12 }, (_, i) => ({
  ts: '2099-01-02T03:04:05.000Z', verb: i % 2 ? 'run' : 'block', sub: null,
  kind: i % 3 ? 'exit' : 'warning', exit: i % 3 ? 2 : 0,
  message: `\n  message ${i} <&> é\u2028  \nsecond line`, fp: String(i % 4).padStart(12, '0'),
  ...(i % 3 ? {} : { warning: 'recovered' }), extra: { z: -0, a: [1e-7, '<&>'] },
}));

before(async () => {
  scratch = await mkdtemp(path.join(os.tmpdir(), 'cf-logs-'));
  // Installation tests rename/chmod/remove the release binary in parallel.
  // Build once into a private package so direct calls and handovers stay isolated.
  const pkg = path.join(scratch, 'package');
  binary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
  await mkdir(path.dirname(binary), { recursive: true });
  const { version } = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
  const built = spawnSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X main.version=${version} -X main.reviewWaitNodeVersion=${process.version}`, '-o', binary, './cmd/code-forge-native'], {
    cwd: path.join(root, 'native'), encoding: 'utf8', timeout: 120_000,
    env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off',
      GOCACHE: path.join(process.env.CODE_FORGE_REAL_HOME ?? os.homedir(), process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build') },
  });
  assert.equal(built.status, 0, `private native binary builds: ${built.error ?? built.stderr}`);
  await mkdir(path.join(pkg, 'bin'));
  await symlink(cli, path.join(pkg, 'bin/code-forge.mjs'));
  await copyFile(path.join(root, 'bin/code-forge'), path.join(pkg, 'bin/code-forge'));
  await mkdir(path.join(pkg, 'src/util'), { recursive: true });
  await copyFile(path.join(root, 'src/util/known-fixes.json'), path.join(pkg, 'src/util/known-fixes.json'));
  fixture = { repo: path.join(scratch, 'repo'), home: path.join(scratch, 'home') };
  await mkdir(fixture.repo);
  await mkdir(fixture.home);
});
after(async () => { await rm(scratch, { recursive: true, force: true }); });

async function seed(text) {
  const dir = path.join(fixture.home, '.code-forge', 'logs');
  await rm(path.join(fixture.home, '.code-forge'), { recursive: true, force: true });
  if (text !== null) {
    await mkdir(dir, { recursive: true });
    await writeFile(path.join(dir, 'errors.jsonl'), text);
  }
}

function invoke(args, { node = false, logging = false, home = fixture.home, nodePath = '/nonexistent/code-forge-node', native = binary } = {}) {
  const result = spawnSync(node ? process.execPath : native, node ? [cli, ...args] : args, {
    cwd: fixture.repo, encoding: 'utf8', timeout: 15_000,
    env: { PATH: process.env.PATH, HOME: home, TMPDIR: scratch,
      CODE_FORGE_NODE: nodePath, CODE_FORGE_NO_ERROR_LOG: logging ? '0' : '1' },
  });
  assert.equal(result.error, undefined, `${args}: process starts`);
  assert.equal(result.signal, null, `${args}: process exits normally`);
  return { code: result.status, stdout: result.stdout, stderr: result.stderr };
}

async function parity(args, logging = false, native = binary) {
  // The harness deliberately disables error logging. A preload enables it in
  // both its Node reference and a transparent launcher for the native command.
  const env = { CODE_FORGE_NODE: process.execPath, ...(logging ? {
    NODE_OPTIONS: '--import=data:text/javascript,' + encodeURIComponent("process.env.CODE_FORGE_NO_ERROR_LOG = '0';"),
  } : {}) };
  const goCommand = logging ? [process.execPath, '--input-type=module', '-e', `
    import { spawnSync } from 'node:child_process';
    const result = spawnSync(${JSON.stringify(native)}, process.argv.slice(1), { env: process.env, stdio: 'inherit' });
    if (result.error) throw result.error;
    process.exit(result.status ?? 1);
  `, '--'] : native;
  assert.deepEqual(await runParity(args, { fixture, goCommand, env }),
    { equal: true, diffs: [] }, `${args.join(' ')}: zero exit, stream, and state differences`);
}

test('B247 clause: logs read verbs run without Node with byte parity; clear and report hand over', async () => {
  await seed(entries.map((e) => JSON.stringify(e)).join('\n') + '\n{torn\nnull\n{}\n');
  const cases = [[], ['path'], ['--last', '2'], ['--last=20', '--json'], ['--json'],
    ['summary'], ['summary', '--days', '1'], ['summary', '--days=9007199254740991', '--json'], ['summary', '--json']];
  for (const args of cases) {
    await parity(['logs', ...args]);
    await parity(['logs', ...args], true);
    const expected = invoke(['logs', ...args], { node: true });
    assert.equal(expected.code, 0, `${args}: Node reference exits zero`);
    assert.deepEqual(invoke(['logs', ...args]), expected, `${args}: Go succeeds with unavailable Node and exact output`);
  }
  await seed(entries.map((e) => JSON.stringify(e)).join('\n'));
  const withBreadcrumb = invoke(['logs', '--json'], { logging: true });
  assert.deepEqual(withBreadcrumb, invoke(['logs', '--json'], { node: true }), 'listing with logging enabled starts no Node');
  // Successful default invocations retain Node's breadcrumb state, too.
  await seed(null);
  const result = invoke(['logs', 'path'], { logging: true });
  assert.equal(result.code, 0, 'path with logging enabled starts no Node');
  assert.deepEqual(JSON.parse(await readFile(path.join(fixture.home, '.code-forge/logs/breadcrumbs.json'), 'utf8')),
    ['logs path'], 'exactly one breadcrumb is recorded');
  const launcher = path.join(scratch, 'package/bin/code-forge');
  await parity(['logs', 'path'], false, launcher);
  assert.deepEqual(invoke(['logs', 'path'], { native: launcher }), invoke(['logs', 'path'], { node: true }), 'private npm entry matches Node output');
  for (const args of [['clear', '--yes'], ['report']]) {
    await seed(null); // report has no entries: no network, model, or terminal.
    await parity(['logs', ...args], true);
    assert.equal(invoke(['logs', ...args]).code, 127, `${args[0]} requires Node`);
  }
});

test('B247 boundary: empty/missing logs match; unsupported arguments and legacy data hand over intact', async () => {
  for (const text of [null, '', 'null\n{}\nnot-json\n', JSON.stringify({ ...entries[0], ts: '2000-01-01T00:00:00Z' })]) {
    await seed(text);
    for (const args of [[], ['path'], ['--json'], ['summary'], ['summary', '--json']]) {
      await parity(['logs', ...args]);
      assert.deepEqual(invoke(['logs', ...args]), invoke(['logs', ...args], { node: true }), `${args}: boundary runs without Node`);
    }
  }
  for (const breadcrumbs of ['not-json', '["run",1e999,"logs path"]',
    '["run start",null,"invalid value!","logs path","logs","logs summary","logs path","run end"]']) {
    await writeFile(path.join(fixture.home, '.code-forge/logs/breadcrumbs.json'), breadcrumbs);
    await parity(['logs', 'path'], true);
  }
  for (const args of [['--last', '0'], ['--last=01'], ['--last=-1'], ['--last=1.5'], ['--last=9007199254740992'],
    ['--last'], ['--last='], ['--last', '--json'], ['--last', '1', '--last', '2'], ['--json=true'], ['--json', '--json'],
    ['--unknown'], ['--'], ['-x'], ['list'], ['unknown'], ['path', 'extra'], ['summary', '--days', 'NaN'],
    ['summary', '--days'], ['summary', '--unknown'], ['path', '--json'], ['--json', 'extra'],
    ['--help'], ['path', '-h'], ['summary', '--help'], ['clear', '--help'], ['report', '--help']]) {
    await parity(['logs', ...args], true); // includes usage-error log and breadcrumb state.
  }
  await seed(JSON.stringify({ ...entries[1], fp: undefined }) + '\n');
  for (const args of [[], ['summary']]) {
    await parity(['logs', ...args]);
    assert.equal(invoke(['logs', ...args]).code, 127, `${args}: legacy fingerprint computation hands over`);
  }
  await parity(['logs', '--json']); // raw JSON needs no fingerprint computation.
  assert.equal(invoke(['logs', '--json']).code, 0, 'legacy JSON listing runs in Go');
  assert.equal(invoke(['logs', 'path'], { home: '' }).code, 127, 'missing HOME hands its error logging to Node');
  assert.deepEqual(invoke(['logs', 'path'], { home: '', nodePath: process.execPath }),
    invoke(['logs', 'path'], { home: '', node: true }), 'missing HOME preserves exit code and both streams');
  for (const text of [
    JSON.stringify({ ...entries[1], ts: 'not-a-date' }),
    [entries[1], { ...entries[1], verb: 'Run', fp: 'abcdef123456' }].map((e) => JSON.stringify(e)).join('\n'),
    '{"ts":"2099-01-02T03:04:05Z","verb":"run","message":"\\ud800"}',
    '{"ts":"2099-01-02T03:04:05Z","verb":"run","exit":1e999}',
  ]) {
    await seed(text);
    await parity(['logs', 'summary'], true);
    assert.equal(invoke(['logs', 'summary']).code, 127, 'nonportable dates, collation, or JSON hand over without partial output');
  }
  // An installed package with known-fix data must delegate the text listing.
  // The symlink uses the unmodified real Node entry as the fallback reference.
  const pkg = path.join(scratch, 'installed');
  const native = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
  await mkdir(path.dirname(native), { recursive: true });
  await copyFile(binary, native);
  await mkdir(path.join(pkg, 'bin'));
  await symlink(cli, path.join(pkg, 'bin/code-forge.mjs'));
  await mkdir(path.join(pkg, 'src/util'), { recursive: true });
  await writeFile(path.join(pkg, 'src/util/known-fixes.json'), JSON.stringify([
    { fp: entries[1].fp, fixed_in: '0.16.3', summary: 'A known fix', issue: null },
  ]));
  await seed(JSON.stringify(entries[1]));
  await parity(['logs'], true, native);
  assert.equal(invoke(['logs'], { native }).code, 127, 'nonempty known-fix table requires Node');
  assert.equal(invoke(['logs', '--json'], { native }).code, 0, 'known-fix table does not prevent native JSON listing');
  await writeFile(path.join(pkg, 'src/util/known-fixes.json'), '{corrupt');
  await parity(['logs'], true, native);
  assert.deepEqual(invoke(['logs'], { native, nodePath: process.execPath }),
    invoke(['logs'], { node: true }), 'corrupt known-fix table preserves Node exit code and both streams');
  assert.equal(invoke(['logs'], { native }).code, 127, 'corrupt known-fix table requires Node');
});
