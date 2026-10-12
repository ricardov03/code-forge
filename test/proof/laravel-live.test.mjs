/**
 * B61 live check: a real Laravel skeleton measured through code-forge's own export isolation.
 *
 * Runs ONLY with `CODE_FORGE_LIVE_PHP=1` and `php` + `composer` on PATH (it needs the network for
 * `composer create-project`); otherwise it is skipped with the reason. Re-run it with:
 *
 *   CODE_FORGE_LIVE_PHP=1 node --import ./test/helpers/isolate.mjs --test test/proof/laravel-live.test.mjs
 *
 * What it does, once on PHPUnit (the skeleton as shipped) and once on Pest, with the same code
 * `proof export` and the gate run use:
 *   1. `composer create-project laravel/laravel` in a temp dir (argv only; for Pest then
 *      `composer require --dev pestphp/pest pestphp/pest-plugin-laravel`), `.code-forge/` ignored,
 *      everything committed: the block's base;
 *   2. the block's owned work in the main tree: a new class `app/Support/Greeting.php`, a unit
 *      test for it and a feature test that writes a user to the database;
 *   3. another block's half-written file in the main tree: `AppServiceProvider::boot()` throws —
 *      the main tree is red (checked), the export must not see it;
 *   4. `buildExport` with the default `link_dirs` / `copy_untracked` (`vendor` copied as a
 *      Composer vendor dir, `.env` from create-project carried along), then the detected test
 *      gate `php artisan test --compact` with the gate env `DB_CONNECTION=sqlite`,
 *      `DB_DATABASE=:memory:` through `runGatesInExport`: exit 0, 4 tests passed — the skeleton's
 *      2 `ExampleTest`s (unit and feature) plus the block's 2 (the Greeting unit test and the
 *      user-count feature test).
 * The versions (PHP, Composer, Laravel) and the duration are printed as test diagnostics.
 */

import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { accessSync, appendFileSync, constants, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { detectGates } from '../../src/gates/detect.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { DEFAULT_COPY_UNTRACKED, DEFAULT_LINK_DIRS, buildExport, runGatesInExport } from '../../src/proof/export.mjs';
import { git, tempDir } from './helpers.mjs';

/**
 * @param {string} cmd @param {string} searchPath - a PATH value
 * @returns {boolean} an executable `cmd` is in one of the `searchPath` directories
 */
function onPath(cmd, searchPath) {
  for (const dir of searchPath.split(path.delimiter)) {
    if (dir === '') continue;
    try {
      accessSync(path.join(dir, cmd), constants.X_OK);
      return true;
    } catch {
      // not in this directory
    }
  }
  return false;
}

/**
 * @param {{env?: Record<string, string | undefined>, path?: string}} [where] - the environment and
 *   PATH to judge (default: this process's)
 * @returns {string | null} why the live test cannot run there, or null when it can
 */
function skipReason({ env = process.env, path: searchPath = env.PATH ?? '' } = {}) {
  if (env.CODE_FORGE_LIVE_PHP !== '1') return 'live PHP check: set CODE_FORGE_LIVE_PHP=1 to run it (needs php, composer and the network)';
  const missing = ['php', 'composer'].filter((cmd) => !onPath(cmd, searchPath));
  if (missing.length > 0) return `live PHP check: ${missing.join(' and ')} not on PATH`;
  return null;
}

/**
 * The passed-test count `php artisan test --compact` reports, and the form it used: a JSON summary
 * line (seen in real runs of this check under Claude Code: Laravel 13 prints it when it detects an AI
 * agent, e.g. `CLAUDECODE` set) or the `Tests: N passed` line (a plain terminal).
 * @param {string} stdout @returns {{form: 'json' | 'text', passed: number} | null}
 */
function passedCount(stdout) {
  for (const line of stdout.split('\n')) {
    if (!line.trim().startsWith('{')) continue;
    try {
      const summary = JSON.parse(line);
      if (summary.result === 'passed' && Number.isInteger(summary.passed)) return { form: 'json', passed: summary.passed };
    } catch {
      // not the summary line
    }
  }
  const plain = stripAnsi(stdout).match(/Tests:\s+(\d+) passed/);
  return plain ? { form: 'text', passed: Number(plain[1]) } : null;
}

/** @param {string} text @returns {string} `text` without ANSI colour codes */
function stripAnsi(text) {
  // eslint-disable-next-line no-control-regex
  return text.replace(/\u001b\[[0-9;]*m/g, '');
}

/** @param {string} root @param {string} rel @param {string} text */
function write(root, rel, text) {
  mkdirSync(path.dirname(path.join(root, rel)), { recursive: true });
  writeFileSync(path.join(root, rel), text);
}

const OWNED = Object.freeze(['app/Support/Greeting.php', 'tests/Unit/GreetingTest.php', 'tests/Feature/UserCountTest.php']);

const GREETING = `<?php

namespace App\\Support;

class Greeting
{
    public static function text(): string
    {
        return 'measured in the export';
    }
}
`;

/** The owned unit test, Pest style (a closure, no class). */
const GREETING_PEST_TEST = `<?php

use App\\Support\\Greeting;

test('greeting comes from the owned class', function () {
    expect(Greeting::text())->toBe('measured in the export');
});
`;

/** The owned unit test, PHPUnit style. */
const GREETING_PHPUNIT_TEST = `<?php

namespace Tests\\Unit;

use App\\Support\\Greeting;
use PHPUnit\\Framework\\TestCase;

class GreetingTest extends TestCase
{
    public function test_greeting_comes_from_the_owned_class(): void
    {
        $this->assertSame('measured in the export', Greeting::text());
    }
}
`;

const USER_COUNT_TEST = `<?php

namespace Tests\\Feature;

use App\\Models\\User;
use Illuminate\\Foundation\\Testing\\RefreshDatabase;
use Tests\\TestCase;

class UserCountTest extends TestCase
{
    use RefreshDatabase;

    public function test_a_user_is_written_to_the_in_memory_database(): void
    {
        User::factory()->create();
        $this->assertSame(1, User::count());
    }
}
`;

const reason = skipReason();

/**
 * The whole live check for one runner.
 * @param {import('node:test').TestContext} t @param {'phpunit' | 'pest'} runner
 */
async function liveCheck(t, runner) {
  const started = Date.now();
  const parent = tempDir(`laravel-live-${runner}`);
  const app = path.join(parent, 'app');
  const run = (/** @type {string} */ cmd, /** @type {string[]} */ args, /** @type {string} */ cwd) =>
    execFileSync(cmd, args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, COMPOSER_NO_INTERACTION: '1' } });

  run('composer', ['create-project', 'laravel/laravel', app, '--no-interaction', '--prefer-dist', '--no-progress'], parent);
  if (runner === 'pest') {
    run('composer', ['require', '--dev', 'pestphp/pest', 'pestphp/pest-plugin-laravel', '--with-all-dependencies', '--no-interaction', '--no-progress'], app);
  }
  t.diagnostic(`php: ${run('php', ['-r', 'echo PHP_VERSION;'], app)}`);
  t.diagnostic(`composer: ${run('composer', ['--version', '--no-ansi'], app).trim()}`);
  t.diagnostic(`laravel: ${run('php', ['artisan', '--version', '--no-ansi'], app).trim()}`);
  t.diagnostic(`${runner}: ${stripAnsi(run('php', [`vendor/bin/${runner}`, '--version'], app)).trim()}`);

  // 1. the base: the skeleton as composer left it, the export dir ignored
  appendFileSync(path.join(app, '.gitignore'), '\n/.code-forge/\n');
  git(['init', '-q'], app);
  git(['add', '-A'], app);
  git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], app);
  const baseSha = git(['rev-parse', 'HEAD'], app);

  // 2. the block's owned work; 3. another block's half-written provider
  write(app, OWNED[0], GREETING);
  write(app, OWNED[1], runner === 'pest' ? GREETING_PEST_TEST : GREETING_PHPUNIT_TEST);
  write(app, OWNED[2], USER_COUNT_TEST);
  const provider = path.join(app, 'app/Providers/AppServiceProvider.php');
  // a throw as the first statement of boot(), whatever the signature's spacing or return type
  const clean = readFileSync(provider, 'utf8');
  const boot = clean.indexOf('function boot(');
  const body = boot === -1 ? -1 : clean.indexOf('{', boot) + 1;
  assert.ok(body > 0, 'the skeleton provider has no boot() method: the dirty edit cannot apply');
  const dirty = `${clean.slice(0, body)}\n        throw new \\RuntimeException('another block is half-way through this file');${clean.slice(body)}`;
  writeFileSync(provider, dirty);
  assert.match(readFileSync(provider, 'utf8'), /function boot\([^)]*\)[^{]*\{\s*throw new \\RuntimeException\('another block is half-way through this file'\);/);

  const detected = detectGates(app);
  const argv = ['php', 'artisan', 'test', '--compact'];
  assert.deepEqual([detected.stack, detected.test], [`php-${runner}`, argv]);
  const gates = { test: { argv, env: { DB_CONNECTION: 'sqlite', DB_DATABASE: ':memory:' } }, lint: null, types: null, format: null };

  // the main tree is red: the export below is green only because it does not see the dirty file
  const inMain = await runGates(gates, { cwd: app });
  const red = inMain.results[0];
  assert.deepEqual([red.ok, red.code], [false, 1], `the dirty provider must make the main tree red\n${red.stdout}\n${red.stderr}`);
  assert.match(`${red.stdout}\n${red.stderr}`, /another block is half-way through this file/);

  // 4. the export, with the defaults `proof export` uses
  const exp = await buildExport({ cwd: app, blockId: 'B1', baseSha, owned: OWNED, ownedKinds: {}, linkDirs: DEFAULT_LINK_DIRS, copyUntracked: DEFAULT_COPY_UNTRACKED });
  assert.deepEqual([exp.copied, exp.linked, exp.untracked, exp.owned], [['vendor'], [], ['.env'], [...OWNED].sort()]);

  const measured = await runGatesInExport({ gates, exportDir: exp.dir, mainTree: app });
  const result = measured.results[0];
  assert.deepEqual([result.result, result.code, measured.incomplete], ['ok', 0, []], `${result.stdout}\n${result.stderr}`);
  const summary = passedCount(result.stdout);
  assert.equal(summary?.passed, 4, result.stdout);
  t.diagnostic(`summary form: ${summary?.form}`);
  t.diagnostic(`duration: ${Math.round((Date.now() - started) / 1000)}s`);
}

const LIVE = { skip: reason ?? false, timeout: 15 * 60 * 1000 };

test('B61 live: a Laravel skeleton on PHPUnit runs green through the export (vendor copied, .env carried, sqlite in memory), unaffected by a dirty main tree', LIVE, (t) => liveCheck(t, 'phpunit'));

test('B61 live: the same skeleton on Pest runs green through the export, unaffected by a dirty main tree', LIVE, (t) => liveCheck(t, 'pest'));

test('B61 live: without CODE_FORGE_LIVE_PHP=1 the skip reason says how to opt in', () => {
  const optIn = 'live PHP check: set CODE_FORGE_LIVE_PHP=1 to run it (needs php, composer and the network)';
  assert.equal(skipReason({ env: {}, path: '' }), optIn);
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_PHP: 'yes' }, path: '' }), optIn);
});

test('B61 live: with the flag set but php and composer not on PATH the skip reason names both', () => {
  const emptyBin = tempDir('live-php-empty-bin');
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_PHP: '1' }, path: emptyBin }), 'live PHP check: php and composer not on PATH');
});

test('B61 live: with the flag set and both tools on PATH there is no skip reason', () => {
  const bin = tempDir('live-php-bin');
  for (const cmd of ['php', 'composer']) writeFileSync(path.join(bin, cmd), '#!/bin/sh\n', { mode: 0o755 });
  assert.equal(skipReason({ env: { CODE_FORGE_LIVE_PHP: '1' }, path: bin }), null);
});
