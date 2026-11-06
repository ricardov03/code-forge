import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const script = path.join(root, 'scripts/bench-block.mjs');
const env = {
  ...process.env,
  GOCACHE: path.join(process.env.CODE_FORGE_REAL_HOME ?? os.homedir(), process.platform === 'darwin' ? 'Library/Caches' : '.cache', 'go-build'),
};
const run = (command, args) => spawnSync(command, args, { cwd: root, env, encoding: 'utf8', timeout: 120_000 });

function registered() {
  const result = run('npm', ['run', '--json']);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout)['bench:block'], 'node scripts/bench-block.mjs', 'npm exposes exactly the block benchmark command');
}

test('block bench measures Node, Go and npm entry lanes with no leftover processes and persists the same JSON', async (t) => {
  registered();
  const go = run('go', ['version']);
  const hasGo = go.error?.['code'] !== 'ENOENT';
  if (hasGo) {
    assert.equal(go.status, 0, go.stderr);
    const built = run(process.execPath, ['scripts/build-native.mjs', '--host']);
    assert.equal(built.status, 0, `host native build: ${built.stderr}`);
  }
  const dir = mkdtempSync(path.join(os.tmpdir(), 'bench-block-test-'));
  try {
    const out = path.join(dir, 'bench.json');
    const result = run(process.execPath, [script, '--files', '2', '--review-ms', '10', '--ledger-rows', '50', '--json', '--out', out]);
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const report = JSON.parse(result.stdout);
    assert.deepEqual(Object.keys(report).sort(), ['entry', 'go', 'node', 'params']);
    assert.deepEqual(report.params, { files: 2, review_ms: 10, ledger_rows: 50 });
    assert.deepEqual(JSON.parse(readFileSync(out, 'utf8')), report, '--out contains the entire stdout report');
    for (const mode of ['node', 'go', 'entry']) {
      await t.test(`${mode} reports all four block measurements`, (check) => {
        assert.deepEqual(report[mode].leftover_processes, { count: 0, list: [] }, `${mode}: zero live descendants or fixture workers`);
        assert.deepEqual(report[mode].native_sockets, [], `${mode}: zero native sockets before fixture removal`);
        if (mode !== 'node' && !hasGo) return check.skip('Go is not on PATH; Node and packaging checks still run');
        const metrics = report[mode];
        assert.deepEqual(Object.keys(metrics).sort(), ['cli_runs', 'home', 'leftover_processes', 'native_sockets', 'peak_rss_mb', 'processes', 'run_id', 'slug', 'wall_ms'], `${mode} reports measurements, leak checks and its isolated identity`);
        for (const key of ['cli_runs', 'peak_rss_mb', 'processes', 'wall_ms']) {
          assert.equal(typeof metrics[key], 'number', `${mode} ${key} is numeric`);
          assert.ok(Number.isFinite(metrics[key]), `${mode} ${key} is finite`);
        }
        assert.equal(metrics.cli_runs, 4, `${mode} starts two enqueues, one block wait and one status`);
        assert.ok(metrics.processes >= metrics.cli_runs, `${mode} samples at least ${metrics.cli_runs} distinct descendants, got ${metrics.processes}`);
        assert.ok(metrics.peak_rss_mb > 0, `${mode} peak RSS is positive, got ${metrics.peak_rss_mb}`);
        assert.ok(metrics.wall_ms > 0, `${mode} wall time is positive, got ${metrics.wall_ms}`);
      });
    }
    await t.test('the passes have separate run ids, project slugs and HOMEs', () => {
      for (const key of ['run_id', 'slug', 'home']) {
        assert.equal(typeof report.node[key], 'string', `Node reports ${key}`);
        assert.equal(typeof report.go[key], 'string', `Go reports ${key}, including when skipped`);
        assert.notEqual(report.node[key], report.go[key], `${key} is isolated between passes`);
        assert.equal(typeof report.entry[key], 'string', `entry reports ${key}, including when skipped`);
        assert.equal(new Set([report.node[key], report.go[key], report.entry[key]]).size, 3, `${key} has exactly three isolated values`);
      }
    });
    const packed = run('npm', ['pack', '--dry-run', '--json']);
    assert.equal(packed.status, 0, packed.stderr);
    assert.equal(JSON.parse(packed.stdout)[0].files.filter((file) => file.path === 'scripts/bench-block.mjs').length, 0, 'npm ships zero copies of the maintainer benchmark');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('block bench counts CLI children that exit before the first periodic sample', () => {
  // Only the observer runs in this process. Keep its initial RSS snapshot, but
  // postpone periodic snapshots until after every CLI child has exited.
  const bootstrap = `
    const interval = globalThis.setInterval;
    globalThis.setInterval = (callback, ms, ...args) =>
      interval(callback, ms === 10 ? 120_000 : ms, ...args);
    await import(${JSON.stringify(new URL('../scripts/bench-block.mjs', import.meta.url).href)});
  `;
  const result = run(process.execPath, ['--input-type=module', '--eval', bootstrap, script, '--files', '2', '--review-ms', '0', '--ledger-rows', '0', '--json']);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  const { node: metrics } = JSON.parse(result.stdout);
  assert.equal(metrics.cli_runs, 4, 'Node starts two enqueues, one block wait and one status before the next sample');
  assert.ok(metrics.wall_ms < 120_000, `Node finishes before the first periodic sample, took ${metrics.wall_ms} ms`);
  assert.ok(metrics.processes >= metrics.cli_runs, `Node counts at least ${metrics.cli_runs} started CLI children between samples, got ${metrics.processes}`);
  assert.deepEqual(metrics.leftover_processes, { count: 0, list: [] }, 'Node leaves zero fixture workers or live descendants between samples');
  assert.deepEqual(metrics.native_sockets, [], 'Node leaves zero native sockets between samples');
});

test('block bench refuses a zero-file block with usage and no JSON report', () => {
  registered();
  const result = run(process.execPath, [script, '--files', '0', '--json']);
  assert.equal(result.status, 2, 'zero files exits with usage code 2');
  assert.equal(result.stdout, '', 'invalid input prints zero report lines');
  assert.match(result.stderr, /^bench-block: --files needs a whole number >= 1\nusage: /);
  const mode = run(process.execPath, [script, '--mode', 'unknown']);
  assert.equal(mode.status, 2, 'unknown lane exits with usage code 2');
  assert.equal(mode.stdout, '', 'unknown lane prints zero report lines');
  assert.match(mode.stderr, /--mode needs node, go or entry\nusage: /, 'usage names all three valid lanes');
});
