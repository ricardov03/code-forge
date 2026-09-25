import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { detectGates } from '../../src/gates/detect.mjs';
import { withTempDir } from './support.mjs';
import { build as buildLaravelVue } from '../fixtures/repos/laravel-vue/build.mjs';
import { build as buildNode } from '../fixtures/repos/node/build.mjs';
import { build as buildRust } from '../fixtures/repos/rust/build.mjs';
import { build as buildPython } from '../fixtures/repos/python/build.mjs';
import { build as buildGo } from '../fixtures/repos/go/build.mjs';
import { build as buildUnknown } from '../fixtures/repos/unknown/build.mjs';

test('laravel-vue: composer.json + vendor/bin/pest + artisan ⇒ php artisan test, phpstan, pint --test', async () => {
  await withTempDir(async (dir) => {
    await buildLaravelVue(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'php-pest');
    assert.deepEqual(gates.test, ['php', 'artisan', 'test', '--compact']);
    assert.equal(gates.lint, null);
    assert.deepEqual(gates.types, ['vendor/bin/phpstan']);
    assert.deepEqual(gates.format, ['vendor/bin/pint', '--test']);
  });
});

test('node: package.json scripts ⇒ pm from packageManager, lint script, tsconfig fallback, prettier fallback', async () => {
  await withTempDir(async (dir) => {
    await buildNode(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'node');
    assert.deepEqual(gates.test, ['pnpm', 'test']);
    assert.deepEqual(gates.lint, ['pnpm', 'run', 'lint']);
    assert.deepEqual(gates.types, ['npx', 'tsc', '--noEmit']);
    assert.deepEqual(gates.format, ['npx', 'prettier', '--check', '.']);
  });
});

test('rust: Cargo.toml ⇒ cargo test/clippy/fmt --check, no types gate', async () => {
  await withTempDir(async (dir) => {
    await buildRust(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'rust');
    assert.deepEqual(gates.test, ['cargo', 'test']);
    assert.deepEqual(gates.lint, ['cargo', 'clippy', '--', '-D', 'warnings']);
    assert.equal(gates.types, null);
    assert.deepEqual(gates.format, ['cargo', 'fmt', '--check']);
  });
});

test('python: pyproject.toml with [tool.mypy] ⇒ pytest/ruff check/mypy/ruff format --check', async () => {
  await withTempDir(async (dir) => {
    await buildPython(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'python');
    assert.deepEqual(gates.test, ['pytest']);
    assert.deepEqual(gates.lint, ['ruff', 'check']);
    assert.deepEqual(gates.types, ['mypy']);
    assert.deepEqual(gates.format, ['ruff', 'format', '--check']);
  });
});

test('go: go.mod ⇒ go test/vet, gofmt -l . as a failOnStdout descriptor (its exit code is always 0), no types gate', async () => {
  await withTempDir(async (dir) => {
    await buildGo(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'go');
    assert.deepEqual(gates.test, ['go', 'test', './...']);
    assert.deepEqual(gates.lint, ['go', 'vet', './...']);
    assert.equal(gates.types, null);
    // `gofmt -l` exits 0 whether or not it printed anything — the descriptor tells run.mjs to
    // also treat non-empty stdout as red, since a bare exit-code check can never fail here.
    assert.deepEqual(gates.format, { argv: ['gofmt', '-l', '.'], failOnStdout: true });
  });
});

test('unknown: no recognized marker file ⇒ every gate null', async () => {
  await withTempDir(async (dir) => {
    await buildUnknown(dir);
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'unknown');
    assert.equal(gates.test, null);
    assert.equal(gates.lint, null);
    assert.equal(gates.types, null);
    assert.equal(gates.format, null);
  });
});

// ── Negative/boundary cases (the failure path next to the happy path, per coder rule 5) ──────

test('python types is null when no [tool.mypy] section is configured', async () => {
  await withTempDir(async (dir) => {
    const { writeFile } = await import('node:fs/promises');
    const path = await import('node:path');
    await writeFile(path.join(dir, 'pyproject.toml'), '[project]\nname = "fixture"\n');
    assert.equal(detectGates(dir).types, null);
  });
});

test('php artisan absent ⇒ falls back to bare vendor/bin/pest', async () => {
  await withTempDir(async (dir) => {
    const { mkdir, writeFile } = await import('node:fs/promises');
    const path = await import('node:path');
    await writeFile(path.join(dir, 'composer.json'), '{}');
    await mkdir(path.join(dir, 'vendor', 'bin'), { recursive: true });
    await writeFile(path.join(dir, 'vendor', 'bin', 'pest'), '#!/usr/bin/env php\n');
    assert.deepEqual(detectGates(dir).test, ['vendor/bin/pest']);
  });
});

test('detectGates refuses a non-string root', () => {
  assert.throws(() => detectGates(undefined), TypeError);
  assert.throws(() => detectGates(''), TypeError);
});

test('node with packageManager "bun@..." ⇒ test is ["bun","run","test"], NOT ["bun","test"] (bun test ignores the package.json script entirely)', async () => {
  await withTempDir(async (dir) => {
    const { writeFile } = await import('node:fs/promises');
    const path = await import('node:path');
    await writeFile(
      path.join(dir, 'package.json'),
      JSON.stringify({ name: 'fixture-bun', packageManager: 'bun@1.1.0', scripts: { test: 'bun test' } }, null, 2),
    );
    const gates = detectGates(dir);
    assert.equal(gates.stack, 'node');
    assert.deepEqual(gates.test, ['bun', 'run', 'test']);
  });
});
