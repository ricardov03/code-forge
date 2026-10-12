import './support.mjs';
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
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

// ── B61: PHPUnit without Pest ────────────────────────────────────────────────

/**
 * Write fixture files into `dir`: `package.json` gets the given `scripts`; every other path (an
 * `artisan` file, `vendor/bin/*` binaries, a `phpstan.neon*` config) gets a PHP stub.
 * @param {string} dir @param {string[]} files @param {Record<string, string>} [scripts]
 */
async function writeFiles(dir, files, scripts = { dev: 'vite', build: 'vite build' }) {
  for (const rel of files) {
    await mkdir(path.dirname(path.join(dir, rel)), { recursive: true });
    await writeFile(path.join(dir, rel), rel === 'package.json' ? JSON.stringify({ private: true, scripts }) : '#!/usr/bin/env php\n<?php\n');
  }
}

/**
 * Write a PHP project: a `composer.json`, then `files` through `writeFiles`.
 * @param {string} dir @param {string[]} files @param {Record<string, string>} [scripts]
 */
async function writePhpProject(dir, files, scripts) {
  await writeFile(path.join(dir, 'composer.json'), JSON.stringify({ name: 'fixture/php', require: { php: '^8.2' } }, null, 2));
  await writeFiles(dir, files, scripts);
}

test('B61 Laravel+PHPUnit: composer.json + artisan + vendor/bin/phpunit, no pest ⇒ php-phpunit, php artisan test --compact, phpstan, pint --test', async () => {
  await withTempDir(async (dir) => {
    await writePhpProject(dir, ['artisan', 'vendor/bin/phpunit', 'vendor/bin/pint', 'phpstan.neon.dist', 'package.json']);
    assert.deepEqual(detectGates(dir), {
      stack: 'php-phpunit',
      test: ['php', 'artisan', 'test', '--compact'],
      lint: null,
      types: ['vendor/bin/phpstan'],
      format: ['vendor/bin/pint', '--test'],
    });
  });
});

test('B61 plain PHPUnit: no artisan ⇒ php-phpunit runs vendor/bin/phpunit; no phpstan config and no pint ⇒ types and format null', async () => {
  await withTempDir(async (dir) => {
    await writePhpProject(dir, ['vendor/bin/phpunit']);
    assert.deepEqual(detectGates(dir), { stack: 'php-phpunit', test: ['vendor/bin/phpunit'], lint: null, types: null, format: null });
  });
});

test('B61 Laravel+Pest is unchanged: with both vendor/bin/pest and vendor/bin/phpunit the stack is php-pest with the same argv', async () => {
  await withTempDir(async (dir) => {
    await buildLaravelVue(dir);
    await writeFiles(dir, ['vendor/bin/phpunit']); // the fixture's own composer.json is kept
    assert.deepEqual(detectGates(dir), {
      stack: 'php-pest',
      test: ['php', 'artisan', 'test', '--compact'],
      lint: null,
      types: ['vendor/bin/phpstan'],
      format: ['vendor/bin/pint', '--test'],
    });
  });
});

test('B61 PHP before Node: composer.json + vendor/bin/phpunit + a package.json with a test script ⇒ php-phpunit, not node', async () => {
  await withTempDir(async (dir) => {
    await writePhpProject(dir, ['vendor/bin/phpunit', 'package.json'], { test: 'vitest run' });
    assert.deepEqual(detectGates(dir), { stack: 'php-phpunit', test: ['vendor/bin/phpunit'], lint: null, types: null, format: null });
  });
});

test('B61 no vendor: composer.json + artisan without an installed runner is not a PHP stack (unknown, every gate null)', async () => {
  await withTempDir(async (dir) => {
    await writePhpProject(dir, ['artisan', 'phpstan.neon', 'package.json']);
    assert.deepEqual(detectGates(dir), { stack: 'unknown', test: null, lint: null, types: null, format: null });
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
