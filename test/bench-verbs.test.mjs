import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { accessSync, constants, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const script = path.join(root, 'scripts/bench-verbs.mjs');
const env = { ...process.env, GOCACHE: path.join(process.env.CODE_FORGE_REAL_HOME ?? os.homedir(), process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build') };
const run = (command, args) => spawnSync(command, args, { cwd: root, env, encoding: 'utf8', timeout: 120_000 });

function registered() {
  const result = run('npm', ['run', '--json']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout)['bench:verbs'], 'node scripts/bench-verbs.mjs', 'npm registers exactly one verb benchmark command');
}

test('verb bench measures the npm entry in paired isolated lanes with parity and identical saved JSON', () => {
  registered();
  const go = run('go', ['version']);
  if (go.status === 0) {
    const built = run(process.execPath, ['scripts/build-native.mjs', '--host']);
    assert.equal(built.status, 0, `host build: ${built.stderr}`);
  }
  let native = false;
  try { accessSync(path.join(root, 'native/bin', `${process.platform}-${process.arch}`, 'code-forge-native'), constants.X_OK); native = true; } catch { /* optional binary */ }
  const dir = mkdtempSync(path.join(os.tmpdir(), 'bench-verbs-test-'));
  try {
    const out = path.join(dir, 'report.json');
    const result = run(process.execPath, [script, '--runs', '2', '--json', '--out', out]);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const report = JSON.parse(result.stdout);
    assert.deepEqual(Object.keys(report).sort(), ['cases', 'host', 'leftover_processes', 'native_sockets', 'params']);
    assert.equal(readFileSync(out, 'utf8'), result.stdout, '--out is byte-identical to stdout');
    assert.deepEqual(report.params, { runs: 2, warmups: 2, cases: [] });
    assert.deepEqual(report.host, { platform: process.platform, arch: process.arch, node: process.version, cpus: os.cpus().length });
    assert.deepEqual(report.leftover_processes, { count: 0, list: [] }, 'zero live descendants or fixture workers');
    assert.deepEqual(report.native_sockets, [], 'zero native sockets before fixture removal');
    const engines = { 'logs path': 'go', 'logs --last 5 --json': 'go', 'logs summary --json': 'go', 'ledger tail': 'go', '--version': 'node-handover', 'status --json': 'node-handover' };
    assert.deepEqual(report.cases.map((entry) => entry.case), Object.keys(engines), 'all six verb cases in order');
    for (const entry of report.cases) {
      assert.equal(entry.parity, native ? true : null, `${entry.case}: parity is true, or unmeasured without native`);
      assert.equal(entry.engine, native ? engines[entry.case] : null, `${entry.case}: unavailable-Node probe identifies the engine`);
      if (!native) assert.equal(entry.go.skipped, 'no native binary', `${entry.case}: Go explicitly skipped`);
      else assert.equal(typeof entry.speedup, 'number', `${entry.case}: numeric speedup without a performance threshold`);
      for (const lane of native ? ['node', 'go'] : ['node']) {
        const metrics = entry[lane];
        assert.deepEqual(Object.keys(metrics).sort(), ['median_ms', 'min_ms', 'p90_ms', 'peak_rss_mb', 'samples'], `${entry.case} ${lane}: complete measurements`);
        assert.equal(metrics.samples.length, 2, `${entry.case} ${lane}: exactly two measured runs, excluding warmups`);
        for (const key of ['median_ms', 'min_ms', 'p90_ms']) assert.ok(Number.isFinite(metrics[key]) && metrics[key] >= 0, `${entry.case} ${lane} ${key}: finite nonnegative timing`);
        for (const sample of metrics.samples) {
          assert.deepEqual(Object.keys(sample).sort(), ['peak_rss_mb', 'wall_ms']);
          assert.ok(Number.isFinite(sample.wall_ms) && sample.wall_ms >= 0, `${entry.case} ${lane}: measured sample`);
          assert.ok(sample.peak_rss_mb === null || Number.isFinite(sample.peak_rss_mb) && sample.peak_rss_mb >= 0, `${entry.case} ${lane}: RSS measured or explicitly unavailable`);
        }
        assert.ok(metrics.peak_rss_mb === null || Number.isFinite(metrics.peak_rss_mb), `${entry.case} ${lane}: aggregate RSS measured or unavailable`);
      }
    }
    const filtered = run(process.execPath, [script, '--runs', '1', '--case', 'logs path', '--case', '--version', '--json']);
    assert.equal(filtered.status, 0, filtered.stderr);
    assert.deepEqual(JSON.parse(filtered.stdout).cases.map((entry) => entry.case), ['logs path', '--version'], 'repeatable case filter keeps exactly two selected cases');
    const packed = run('npm', ['pack', '--dry-run', '--json']);
    assert.equal(packed.status, 0, packed.stderr);
    assert.equal(JSON.parse(packed.stdout)[0].files.filter((file) => file.path === 'scripts/bench-verbs.mjs').length, 0, 'maintainer benchmark is excluded from npm');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('verb bench rejects bad flags and missing or invalid values with usage exit 2', () => {
  registered();
  for (const args of [['--wat'], ['--runs', '0'], ['--runs', '1.5'], ['--runs'], ['--out'], ['--case'], ['--case', 'unknown']]) {
    const result = run(process.execPath, [script, ...args, '--json']);
    assert.equal(result.status, 2, `${args.join(' ')}: usage exit 2`);
    assert.equal(result.stdout, '', `${args.join(' ')}: zero report bytes`);
    assert.match(result.stderr, /^bench-verbs: .+\nusage: node scripts\/bench-verbs\.mjs /, `${args.join(' ')}: diagnostic and usage`);
  }
});
