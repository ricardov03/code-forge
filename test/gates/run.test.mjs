import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { runGates } from '../../src/gates/run.mjs';
import { withTempDir } from './support.mjs';

test('a red gate in the middle of 3 is reported red — each exit status read independently, never &&-chained', async () => {
  await withTempDir(async (dir) => {
    const gates = {
      test: ['node', '-e', 'process.exit(0)'],
      lint: ['node', '-e', 'process.exit(1)'],
      types: ['node', '-e', 'process.exit(0)'],
      format: null,
    };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, false);
    const byGate = Object.fromEntries(results.map((r) => [r.gate, r]));
    // All three configured gates actually RAN (their own exit status read individually) — proves
    // the middle failure did not short-circuit the run.
    assert.equal(byGate.test.ok, true);
    assert.equal(byGate.test.skipped, false);
    assert.equal(byGate.lint.ok, false);
    assert.equal(byGate.lint.code, 1);
    assert.equal(byGate.types.ok, true);
    assert.equal(byGate.types.skipped, false);
    assert.equal(byGate.format.skipped, true);
    assert.equal(byGate.format.ok, true);
  });
});

test('all green ⇒ allOk true', async () => {
  await withTempDir(async (dir) => {
    const gates = { test: ['node', '-e', 'process.exit(0)'], lint: null, types: null, format: null };
    const { allOk, results } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, true);
    assert.equal(results.length, 4);
  });
});

test('runGates refuses a missing cwd or a non-object gates argument', async () => {
  await assert.rejects(() => runGates(null, { cwd: '/tmp' }), TypeError);
  await assert.rejects(() => runGates({}, /** @type {any} */ ({})), TypeError);
});

// ── failOnStdout descriptor (e.g. detect.mjs's Go `gofmt -l` gate, which always exits 0) ──────

test('a failOnStdout gate that exits 0 but prints something to stdout is reported red', async () => {
  await withTempDir(async (dir) => {
    /** @type {{test: null, lint: null, types: null, format: {argv: string[], failOnStdout: true}}} */
    const gates = {
      test: null,
      lint: null,
      types: null,
      // Simulates `gofmt -l` finding an unformatted file: exits 0, but prints its name.
      format: { argv: ['node', '-e', 'process.stdout.write("unformatted.go\\n"); process.exit(0);'], failOnStdout: true },
    };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    const format = results.find((r) => r.gate === 'format');
    assert.equal(allOk, false);
    assert.equal(format.ok, false);
    assert.equal(format.code, 0); // the process itself still exited 0 — ok is false anyway
    assert.match(format.stdout, /unformatted\.go/);
  });
});

test('a failOnStdout gate that exits 0 with EMPTY stdout is reported green (the boundary the flag exists for)', async () => {
  await withTempDir(async (dir) => {
    /** @type {{test: null, lint: null, types: null, format: {argv: string[], failOnStdout: true}}} */
    const gates = { test: null, lint: null, types: null, format: { argv: ['node', '-e', 'process.exit(0);'], failOnStdout: true } };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    const format = results.find((r) => r.gate === 'format');
    assert.equal(allOk, true);
    assert.equal(format.ok, true);
    assert.equal(format.stdout, '');
  });
});

test('a PLAIN argv gate (no failOnStdout) with non-empty stdout still passes on exit 0 — the flag only applies when set', async () => {
  await withTempDir(async (dir) => {
    const gates = { test: ['node', '-e', 'console.log("noisy but fine"); process.exit(0);'], lint: null, types: null, format: null };
    const { results, allOk } = await runGates(gates, { cwd: dir });
    assert.equal(allOk, true);
    assert.equal(results[0].ok, true);
  });
});
