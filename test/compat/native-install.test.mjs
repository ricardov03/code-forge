import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { chmod, cp, mkdir, mkdtemp, readFile, readdir, realpath, rename, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { runDoctorCli } from '../../src/cli/doctor.mjs';
import { ledgerPath } from '../../src/ledger/paths.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { runKeyPath, runRecordPath } from '../../src/state/paths.mjs';
import { generateKey, signRow, verifyRows } from '../../src/state/signer.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const binary = path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
const build = path.join(root, 'scripts/build-native.mjs');
// The test preload moves HOME into a temporary directory; keep Go's cache outside it.
const goEnv = {
  ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off',
  GOCACHE: path.join(process.env.CODE_FORGE_REAL_HOME ?? os.homedir(), process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build'),
};

async function doctorNative(runDoctor = runDoctorCli) {
  let output = '';
  await runDoctor(['--json', '--cwd', os.homedir()], {
    stdout: { write: (s) => { output += s; } }, stderr: { write: () => {} },
    env: { ...process.env, CODE_FORGE_KEY_BACKEND: 'file' },
  });
  const rows = JSON.parse(output).rows.filter((row) => row.id === 'native');
  assert.equal(rows.length, 1, 'doctor must print exactly one native binary status row');
  return rows[0];
}

test('npm installation ships a working host binary, reports its version, and verifies Node signed rows', async (t) => {
  const isolationMessage = 'refusing to write to a real home: run with --import ./test/helpers/isolate.mjs and keep os.homedir() inside CODE_FORGE_TEST_ROOT';
  assert.ok(process.env.CODE_FORGE_TEST_ROOT, isolationMessage);
  const relativeHome = path.relative(await realpath(process.env.CODE_FORGE_TEST_ROOT), await realpath(os.homedir()));
  assert.ok(relativeHome !== '..' && !relativeHome.startsWith(`..${path.sep}`) && !path.isAbsolute(relativeHome), isolationMessage);
  const go = spawnSync('go', ['version'], { encoding: 'utf8', env: goEnv });
  if (go.error?.['code'] === 'ENOENT') return t.skip('native installation skipped: go is not on PATH');
  assert.equal(go.status, 0, go.stderr);
  // The optional accelerator must be visible in doctor even before the release build.
  await doctorNative();
  const built = spawnSync(process.execPath, [build, '--host'], { cwd: root, encoding: 'utf8', timeout: 120_000, env: goEnv });
  assert.equal(built.status, 0, `host native build: ${built.stderr}`);
  const { nativePath, runNative } = await import('../../src/util/native.mjs');
  const disabled = process.env.CODE_FORGE_NATIVE;
  const suffix = randomUUID();
  const run = `native-install-${suffix}`;
  const slug = `native-project-${suffix}`;
  const file = ledgerPath(slug);
  delete process.env.CODE_FORGE_NATIVE;
  try {
    assert.equal(nativePath(), binary, 'nativePath must resolve the one executable for this host');
    const version = await runNative(['version']);
    assert.equal(version.code, 0, version.stderr);
    const pkg = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
    assert.deepEqual(JSON.parse(version.stdout), { version: pkg.version, goVersion: go.stdout.trim().split(' ')[2] });
    const row = await doctorNative();
    assert.equal(row.status, 'OK');
    assert.ok(row.detail.includes(binary), 'doctor must name the resolved binary');
    assert.ok(row.detail.includes(pkg.version), 'doctor must name the package version');

    const key = await generateKey(run);
    await writeFile(runRecordPath(run), JSON.stringify({ project: slug }));
    await appendRow(signRow({ event: 'run.start', run, note: 'alpha' }, key), { slug });
    await appendRow(signRow({ event: 'session', run, tokens_in: 7, label: 'é😀' }, key), { slug, rotateAtBytes: 1 });
    await appendRow({ event: 'unrelated', run: 'another-run' }, { slug });
    const verify = async () => {
      const nodeRows = (await readAllRows(slug)).filter((r) => r.run === run);
      const node = verifyRows(nodeRows, key);
      const result = await runNative(['verify-ledger', '--home', os.homedir(), '--run', run]);
      assert.equal(result.code, node.ok ? 0 : 1, result.stderr);
      assert.deepEqual(JSON.parse(result.stdout), { rows: 2, ok: node.ok, bad: node.failures });
      return JSON.parse(result.stdout);
    };
    assert.deepEqual(await verify(), { rows: 2, ok: true, bad: [] });
    const original = await readFile(file, 'utf8');
    await writeFile(file, original.replace('session', 'sessioN'));
    assert.deepEqual(await verify(), { rows: 2, ok: false, bad: [{ index: 1, event: 'sessioN', reason: 'mismatch' }] });

    const packed = spawnSync('npm', ['pack', '--dry-run', '--json'], { cwd: root, encoding: 'utf8', timeout: 120_000 });
    assert.equal(packed.status, 0, packed.stderr);
    const [pack] = JSON.parse(packed.stdout);
    assert.equal(pack.files.filter((f) => f.path === path.relative(root, binary)).length, 1, 'npm must ship exactly one host executable');
    assert.ok(pack.size < 20_000_000, `package must stay under 20 MB, got ${pack.size} bytes`);
  } finally {
    if (disabled === undefined) delete process.env.CODE_FORGE_NATIVE;
    else process.env.CODE_FORGE_NATIVE = disabled;
    // Remove both the live ledger and every rotation belonging to this unique fixture.
    const ledgerFiles = await readdir(path.dirname(file)).catch((err) => {
      if (err.code === 'ENOENT') return [];
      throw err;
    });
    const ownedLedger = new RegExp(`^${slug}(?:\\.\\d+)?\\.jsonl$`);
    await Promise.all([
      rm(runKeyPath(run), { force: true }),
      rm(runRecordPath(run), { force: true }),
      ...ledgerFiles.filter((name) => ownedLedger.test(name))
        .map((name) => rm(path.join(path.dirname(file), name), { force: true })),
    ]);
  }
});

test('disabled, missing, and nonexecutable native binaries keep doctor on the Node path', async (t) => {
  // Other compatibility tests rebuild the release binary while doctor probes run.
  // Exercise lookup and doctor against a private package whose binary we control.
  const pkg = await mkdtemp(path.join(os.tmpdir(), 'native-fallback-'));
  t.after(() => rm(pkg, { recursive: true, force: true }));
  for (const relative of ['src', 'schema', 'package.json']) {
    await cp(path.join(root, relative), path.join(pkg, relative), { recursive: true });
  }
  const dependencies = path.dirname(path.dirname(fileURLToPath(import.meta.resolve('ajv/package.json'))));
  await symlink(dependencies, path.join(pkg, 'node_modules'), 'dir');
  const privateBinary = path.join(pkg, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native');
  await mkdir(path.dirname(privateBinary), { recursive: true });
  await cp(binary, privateBinary);
  const { runDoctorCli: privateDoctor } = await import(pathToFileURL(path.join(pkg, 'src/cli/doctor.mjs')).href);
  const { nativePath, runNative } = await import(pathToFileURL(path.join(pkg, 'src/util/native.mjs')).href);
  const previous = process.env.CODE_FORGE_NATIVE;
  process.env.CODE_FORGE_NATIVE = '0';
  try {
    assert.equal((await doctorNative(privateDoctor)).detail, 'not found, using Node');
    assert.equal(nativePath(), null, 'CODE_FORGE_NATIVE=0 must disable native lookup');
    assert.equal(await runNative(['version']), null, 'disabled native execution must return null');
    delete process.env.CODE_FORGE_NATIVE;
    const holding = await mkdtemp(path.join(os.tmpdir(), 'native-holding-'));
    let moved = false;
    try {
      await rename(privateBinary, path.join(holding, 'binary')).then(() => { moved = true; }, (err) => { if (err.code !== 'ENOENT') throw err; });
      assert.equal(nativePath(), null, 'a removed binary must return null');
      assert.equal((await doctorNative(privateDoctor)).detail, 'not found, using Node');
      if (moved) {
        await rename(path.join(holding, 'binary'), privateBinary);
        moved = false;
        await chmod(privateBinary, 0o644);
        try {
          assert.equal(nativePath(), null, 'a nonexecutable file must return null');
          assert.equal((await doctorNative(privateDoctor)).detail, 'not found, using Node');
        } finally { await chmod(privateBinary, 0o755); }
      }
    } finally {
      if (moved) await rename(path.join(holding, 'binary'), privateBinary);
      await rm(holding, { recursive: true, force: true });
    }
  } finally {
    if (previous === undefined) delete process.env.CODE_FORGE_NATIVE;
    else process.env.CODE_FORGE_NATIVE = previous;
  }
});
