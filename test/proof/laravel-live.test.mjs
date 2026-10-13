/**
 * B61 + B89 live check: a real Laravel skeleton measured through code-forge's own export
 * isolation, then proven red→green through the `junit` adapter.
 *
 * Runs ONLY with `CODE_FORGE_LIVE_PHP=1` and `php` + `composer` on PATH (it needs the network for
 * `composer create-project`); otherwise it is skipped with the reason. Re-run it with:
 *
 *   CODE_FORGE_LIVE_PHP=1 node --import ./test/helpers/isolate.mjs --test test/proof/laravel-live.test.mjs
 *
 * With `CODE_FORGE_PHP_CAPTURE=<dir>` set too, every red→green step's report dir is copied there
 * (`<runner>/<n>-<label>/`, with `step.json` and `console.txt`; `<dir>/<runner>` is removed first, so
 * a capture never merges with an older one), also when the check fails; that is
 * how the PHP fixtures in `test/fixtures/junit/` were made (see `junit.test.mjs`).
 *
 * What it does, once on PHPUnit (the skeleton as shipped) and once on Pest, in one `mkdtemp` dir
 * each (removed afterwards):
 *   1. `composer create-project laravel/laravel` (argv only; for Pest then
 *      `composer require --dev pestphp/pest pestphp/pest-plugin-laravel`), a broken
 *      `app/Support/Clamp.php` (it returns its value), `.code-forge/` ignored, a `.code-forge.yml`
 *      with the `junit` runner below, everything committed: the block's base;
 *   2. the block's owned work in the main tree: `Clamp` fixed and its unit test (one case that
 *      fails on the base, one that passes on it), a new class `app/Support/Greeting.php`, a unit
 *      test for it and a feature test that writes a user to the database;
 *   3. another block's half-written file in the main tree: `AppServiceProvider::boot()` throws —
 *      the main tree is red (checked), the export must not see it;
 *   4. (B61, the gate part) `buildExport` with the default `link_dirs` / `copy_untracked`
 *      (`vendor` copied as a Composer vendor dir, `.env` from create-project carried along), then
 *      the detected test gate `php artisan test --compact` with the gate env
 *      `DB_CONNECTION=sqlite`, `DB_DATABASE=:memory:` through `runGatesInExport`: exit 0, 6 tests
 *      passed — the skeleton's 2 `ExampleTest`s (unit and feature) plus the block's 4 (2 Clamp
 *      cases, the Greeting unit test and the user-count feature test);
 *   5. (B89) `proof red-green` through `runProof` (the CLI verb, export isolation) with the runner
 *      `[php, artisan, test, "{test_file}", --filter, "{test_filter}", --log-junit, "{report}"]`:
 *      the Clamp test's failing case ⇒ RED `assertion`, then GREEN, exit 0, one signed row that
 *      covers both reverted sources, with exactly that test in `failed_tests`; the Greeting test
 *      (its class is new in the block, so it is gone at RED) ⇒ `RED_INVALID` `import`, exit 1, proven
 *      twice, once in each console form whatever the caller's env: with `CLAUDECODE=1` (the one-line
 *      JSON summary Laravel prints under an AI agent, the message JSON-escaped) and with every agent
 *      variable removed (the plain `Class "App\Support\Greeting" not found`); each form is checked;
 *   6. the main tree is left exactly as the block and the other block left it;
 *   7. a full-config run in the main tree (`php artisan test --log-junit <report>`, no file, no
 *      `--filter`; the base's Clamp, the clean provider): exit 1, the nested report (a root suite
 *      named after the `phpunit.xml` path > `Unit` / `Feature` > class) with the 6 ids, only the
 *      Clamp case failed. It is captured as `<n>-full/` (the source of `phpunit-full.xml`).
 * The versions (PHP, Composer, Laravel, PHPUnit or Pest), every step's duration and the total are
 * printed as test diagnostics.
 *
 * The runner argv is wrapped in a pass-through recorder (Node, argv only): it runs the real
 * `php artisan test …` argv, forwards its stdout, stderr and exit code unchanged, and keeps a copy
 * of the report dir before code-forge reads and removes it.
 */

import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { accessSync, appendFileSync, constants, cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { loadConfigFile } from '../../src/config/load.mjs';
import { detectGates } from '../../src/gates/detect.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { DEFAULT_COPY_UNTRACKED, DEFAULT_LINK_DIRS, buildExport, runGatesInExport } from '../../src/proof/export.mjs';
import { readJUnit } from '../../src/proof/junit.mjs';
import { runnerHook } from '../../src/proof/runners.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { startRun } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
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

/**
 * The console form of a failed `php artisan test` run: `json` when a line is Laravel's JSON summary
 * (`{"tool":"phpunit"|"pest",…,"result":"failed",…}`), else `text`.
 * @param {string} console @returns {'json' | 'text'}
 */
function consoleForm(console) {
  for (const line of console.split('\n')) {
    if (!line.trim().startsWith('{')) continue;
    try {
      const summary = JSON.parse(line);
      if ((summary.tool === 'phpunit' || summary.tool === 'pest') && summary.result === 'failed') return 'json';
    } catch {
      // not the summary line
    }
  }
  return 'text';
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

/** The child env: this process's, without any inherited `GIT_*` variable. */
function childEnv() {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_')) env[key] = value;
  return { ...env, COMPOSER_NO_INTERACTION: '1' };
}

const NODE = process.execPath;
const CLAMP = 'app/Support/Clamp.php';
const CLAMP_TEST = 'tests/Unit/ClampTest.php';
const GREETING_SRC = 'app/Support/Greeting.php';
const GREETING_TEST = 'tests/Unit/GreetingTest.php';
const OWNED = Object.freeze([GREETING_SRC, GREETING_TEST, 'tests/Feature/UserCountTest.php', CLAMP, CLAMP_TEST]);
/** The Laravel runner the docs give (proof.md §2), wrapped by the recorder below. */
const PHP_ARGV = Object.freeze(['php', 'artisan', 'test', '{test_file}', '--filter', '{test_filter}', '--log-junit', '{report}']);

/** @param {string} body @returns {string} the Clamp class with this `between` body */
const clampSource = (body) => `<?php

namespace App\\Support;

class Clamp
{
    public static function between(int $value, int $low, int $high): int
    {
        ${body}
    }
}
`;
const CLAMP_BROKEN = clampSource('return $value;');
const CLAMP_FIXED = clampSource('return min(max($value, $low), $high);');

/** The Clamp unit test, PHPUnit style: one case fails on the base, one passes on it. */
const CLAMP_PHPUNIT_TEST = `<?php

namespace Tests\\Unit;

use App\\Support\\Clamp;
use PHPUnit\\Framework\\TestCase;

class ClampTest extends TestCase
{
    public function test_caps_high(): void
    {
        $this->assertSame(10, Clamp::between(12, 0, 10));
    }

    public function test_keeps_inside(): void
    {
        $this->assertSame(5, Clamp::between(5, 0, 10));
    }
}
`;

/** The Clamp unit test, Pest style (closures, no class). */
const CLAMP_PEST_TEST = `<?php

use App\\Support\\Clamp;

test('caps high', function () {
    expect(Clamp::between(12, 0, 10))->toBe(10);
});

test('keeps inside', function () {
    expect(Clamp::between(5, 0, 10))->toBe(5);
});
`;

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

/** Per runner: the case names `--test <file>::<case>` gives, and the report ids they get. */
const CASES = Object.freeze({
  phpunit: {
    clampCase: 'test_caps_high',
    clampId: 'Tests.Unit.ClampTest.test_caps_high',
    greetingCase: 'test_greeting_comes_from_the_owned_class',
    greetingId: 'Tests.Unit.GreetingTest.test_greeting_comes_from_the_owned_class',
    fullIds: [
      'Tests.Unit.ClampTest.test_caps_high',
      'Tests.Unit.ClampTest.test_keeps_inside',
      'Tests.Unit.ExampleTest.test_that_true_is_true',
      'Tests.Unit.GreetingTest.test_greeting_comes_from_the_owned_class',
      'Tests.Feature.ExampleTest.test_the_application_returns_a_successful_response',
      'Tests.Feature.UserCountTest.test_a_user_is_written_to_the_in_memory_database',
    ],
  },
  pest: {
    clampCase: 'caps high',
    clampId: 'Tests.Unit.ClampTest.caps high',
    greetingCase: 'greeting comes from the owned class',
    greetingId: 'Tests.Unit.GreetingTest.greeting comes from the owned class',
    fullIds: [
      'Tests.Unit.ClampTest.caps high',
      'Tests.Unit.ClampTest.keeps inside',
      'Tests.Unit.ExampleTest.That true is true',
      'Tests.Unit.GreetingTest.greeting comes from the owned class',
      'Tests.Feature.ExampleTest.The application returns a successful response',
      'Tests.Feature.UserCountTest.A user is written to the in memory database',
    ],
  },
});

/**
 * Every variable `laravel/agent-detector` (Laravel 13's `AgentDetector`) reads: `AI_AGENT` and its
 * `AGENT_ENV_VARS` list, as shipped with Laravel Framework 13.35.0. Any one of them set makes
 * `php artisan test` print its one-line JSON summary. (It also checks for `/opt/.devin`, a file.)
 */
const AGENT_DETECTOR_VARS = Object.freeze([
  'AI_AGENT', 'CURSOR_AGENT', 'GROK_PLUGIN_ROOT', 'GROK_PLUGIN_DATA', 'GEMINI_CLI', 'CODEX_SANDBOX', 'CODEX_CI', 'CODEX_THREAD_ID',
  'CODEX_SANDBOX_NETWORK_DISABLED', 'AUGMENT_AGENT', 'OPENCODE_CLIENT', 'OPENCODE', 'AMP_CURRENT_THREAD_ID', 'CLAUDECODE', 'CLAUDE_CODE',
  'REPL_ID', 'COPILOT_MODEL', 'COPILOT_ALLOW_ALL', 'COPILOT_GITHUB_TOKEN', 'COPILOT_CLI', 'ANTIGRAVITY_AGENT', 'ANTIGRAVITY_CLI_ALIAS',
  'PI_CODING_AGENT', 'MATTERHORN_SESSION_ID', 'JUNIE_DATA', 'JUNIE_SHIM_PATH', 'KIRO_AGENT_PATH', 'KIMI_PLUGIN_ROOT', 'CLINE_ACTIVE',
  'GOOSE_TERMINAL', 'OPENCLAW_SHELL',
]);

/**
 * The pass-through recorder: argv `<capture dir> <label file> <argv…>`. It runs `argv` (no shell),
 * forwards stdout, stderr and the exit code, and copies the `--log-junit` report's dir into
 * `<capture dir>/<n>-<label>/` with a `step.json` (exit code, duration, file names) and the console.
 * A `form.txt` next to the label file forces Laravel's console form for the run: `json` adds
 * `CLAUDECODE=1` to the child env, `text` removes every variable Laravel's agent detector reads
 * (`AGENT_DETECTOR_VARS`); without it the env is passed as given.
 */
const RECORDER = `
import { spawnSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
const [captureDir, labelFile, ...argv] = process.argv.slice(2);
const formFile = path.join(path.dirname(labelFile), 'form.txt');
const form = existsSync(formFile) ? readFileSync(formFile, 'utf8').trim() : 'inherit';
const env = { ...process.env };
if (form === 'json') env.CLAUDECODE = '1';
if (form === 'text') for (const key of ${JSON.stringify(AGENT_DETECTOR_VARS)}) delete env[key];
const started = Date.now();
const r = spawnSync(argv[0], argv.slice(1), { stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024, env });
const ms = Date.now() - started;
// never process.exit(): it can cut a pipe write short; the exit code is set, and the process ends
// once both streams are flushed (each write's callback)
process.exitCode = 1;
if (r.error) {
  process.stderr.write('recorder: could not run ' + argv[0] + ': ' + r.error.message + '\\n');
} else {
  // the capture never decides the outcome: a missing report dir (or any copy error) is recorded,
  // and the runner's output and exit code are still forwarded unchanged below
  try {
    const reportDir = path.dirname(argv[argv.indexOf('--log-junit') + 1]);
    let files = [];
    let missing = null;
    try {
      files = readdirSync(reportDir).sort();
    } catch (err) {
      missing = String(err && err.code || err);
    }
    const dest = path.join(captureDir, String(readdirSync(captureDir).length + 1).padStart(2, '0') + '-' + readFileSync(labelFile, 'utf8').trim());
    mkdirSync(dest);
    for (const f of files) copyFileSync(path.join(reportDir, f), path.join(dest, f));
    writeFileSync(path.join(dest, 'step.json'), JSON.stringify({ code: r.status, signal: r.signal, ms, files, ...(missing ? { report_dir: missing } : {}) }));
    writeFileSync(path.join(dest, 'console.txt'), String(r.stdout ?? '') + '\\n--- stderr\\n' + String(r.stderr ?? ''));
  } catch (err) {
    process.stderr.write('recorder: capture failed: ' + String(err && err.message || err) + '\\n');
  }
  process.stdout.write(r.stdout, () =>
    process.stderr.write(r.stderr, () => {
      if (r.signal) process.kill(process.pid, r.signal);
      else process.exitCode = r.status ?? 1;
    }),
  );
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
  const tools = path.join(parent, 'tools');
  const captureDir = path.join(tools, 'capture');
  const labelFile = path.join(tools, 'label.txt');
  mkdirSync(captureDir, { recursive: true });
  writeFileSync(path.join(tools, 'recorder.mjs'), RECORDER);
  const run = (/** @type {string} */ cmd, /** @type {string[]} */ args, /** @type {string} */ cwd) =>
    execFileSync(cmd, args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], env: childEnv() });
  const slug = `laravel-live-${runner}`;
  const runId = `r-b89-laravel-${runner}`;
  const cases = CASES[runner];
  try {
    run('composer', ['create-project', 'laravel/laravel', app, '--no-interaction', '--prefer-dist', '--no-progress'], parent);
    if (runner === 'pest') {
      run('composer', ['require', '--dev', 'pestphp/pest', 'pestphp/pest-plugin-laravel', '--with-all-dependencies', '--no-interaction', '--no-progress'], app);
    }
    t.diagnostic(`php: ${run('php', ['-r', 'echo PHP_VERSION;'], app)}`);
    t.diagnostic(`composer: ${run('composer', ['--version', '--no-ansi'], app).trim()}`);
    t.diagnostic(`laravel: ${run('php', ['artisan', '--version', '--no-ansi'], app).trim()}`);
    t.diagnostic(`phpunit: ${stripAnsi(run('php', ['vendor/bin/phpunit', '--version'], app)).trim()}`);
    if (runner === 'pest') t.diagnostic(`pest: ${stripAnsi(run('php', ['vendor/bin/pest', '--version'], app)).trim()}`);

    // 1. the base: the skeleton as composer left it, a broken Clamp, the runner config, the export dir ignored
    write(app, CLAMP, CLAMP_BROKEN);
    appendFileSync(path.join(app, '.gitignore'), '\n/.code-forge/\n');
    const runners = [
      {
        match: ['tests/**/*Test.php'],
        adapter: 'junit',
        argv: [NODE, path.join(tools, 'recorder.mjs'), captureDir, labelFile, ...PHP_ARGV],
        env: { DB_CONNECTION: 'sqlite', DB_DATABASE: ':memory:' },
        timeout_s: 900,
      },
    ];
    write(
      app,
      '.code-forge.yml',
      [
        'version: 1',
        'project:',
        `  slug: ${slug}`,
        'provider: anthropic',
        'engine: harness',
        'levels:',
        '  L0: {model: claude-haiku-4-5-20251001}',
        '  L1: {model: claude-sonnet-5}',
        '  L2: {model: claude-opus-5-5}',
        '  L3: {model: claude-fable-5-1}',
        'proof:',
        `  runners: ${JSON.stringify(runners)}`,
        '',
      ].join('\n'),
    );
    git(['init', '-q'], app);
    git(['symbolic-ref', 'HEAD', 'refs/heads/main'], app);
    git(['add', '-A'], app);
    git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', 'base'], app);
    const baseSha = git(['rev-parse', 'HEAD'], app);

    // 2. the block's owned work; 3. another block's half-written provider
    write(app, CLAMP, CLAMP_FIXED);
    write(app, CLAMP_TEST, runner === 'pest' ? CLAMP_PEST_TEST : CLAMP_PHPUNIT_TEST);
    write(app, GREETING_SRC, GREETING);
    write(app, GREETING_TEST, runner === 'pest' ? GREETING_PEST_TEST : GREETING_PHPUNIT_TEST);
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
    assert.equal(summary?.passed, 6, result.stdout);
    t.diagnostic(`gate summary form: ${summary?.form}; gate part: ${Math.round((Date.now() - started) / 1000)} s`);

    // 5. red→green through the junit adapter, in a fresh export of the same block
    const statusBefore = git(['status', '--porcelain', '--untracked-files=all'], app);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
    const loaded = await loadConfigFile(path.join(app, '.code-forge.yml'));
    assert.equal(loaded.ok, true);
    await startRun({ workspace: app, project: slug, runId, writeRow, config: /** @type {Record<string, any>} */ (loaded.config) });
    await openBlock({ runId, id: 'B1', level: 'L1', owned: [...OWNED], acceptance: [{ clause: 'clamp caps at high', tests: [CLAMP_TEST, GREETING_TEST] }], writeRow });
    const key = await loadKey(runId);

    /**
     * One `proof red-green` through the CLI verb (the real `runnerHook`).
     * @param {string} label @param {string} testArg
     */
    const prove = async (label, testArg) => {
      writeFileSync(labelFile, label);
      const stepsBefore = readdirSync(captureDir).length;
      const rowsBefore = (await readAllRows(slug)).length;
      let stdout = '';
      let stderr = '';
      const proofStarted = Date.now();
      const code = await runProof(['red-green', 'B1', '--run', runId, '--test', testArg], {
        hook: runnerHook,
        stdout: { write: (s) => (stdout += s) },
        stderr: { write: (s) => (stderr += s) },
      });
      const steps = readdirSync(captureDir)
        .sort()
        .slice(stepsBefore)
        .map((name) => ({ name, console: readFileSync(path.join(captureDir, name, 'console.txt'), 'utf8'), ...JSON.parse(readFileSync(path.join(captureDir, name, 'step.json'), 'utf8')) }));
      t.diagnostic(`${label}: exit ${code} in ${Date.now() - proofStarted} ms; steps ${steps.map((s) => `${s.name} exit ${s.code} ${s.ms} ms [${s.files.join(', ')}]`).join('; ')}`);
      return { code, stdout, stderr, steps, added: (await readAllRows(slug)).slice(rowsBefore) };
    };

    // the Clamp test's failing case: RED assertion, GREEN, one signed row covering both reverted sources
    const clampArg = `${CLAMP_TEST}::${cases.clampCase}`;
    const clamp = await prove('clamp', clampArg);
    assert.deepEqual([clamp.code, clamp.stderr], [0, `RED ${clampArg}\nGREEN ${clampArg}\n`], clamp.stdout);
    assert.deepEqual(
      clamp.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed ?? null, r.failed_tests ?? null, r.env_keys ?? null, r.covers ?? null]),
      [
        ['export', null, null, null, null, null, null, null, null, null],
        ['red-green', 'RED', 'assertion', 'GREEN', true, 'junit', 1, [cases.clampId], ['DB_CONNECTION', 'DB_DATABASE'], [CLAMP, GREETING_SRC]],
      ],
    );
    assert.equal(verifyRow(clamp.added[1], key).ok, true);
    // RED exits 1 (one assertion failure), GREEN 0; each step writes exactly the one report
    assert.deepEqual(clamp.steps.map((s) => [s.code, s.files]), [[1, ['report.xml']], [0, ['report.xml']]]);
    t.diagnostic(`clamp RED console form: ${consoleForm(clamp.steps[0].console)}`);

    // a test of a class the block added: the class is gone at RED, so the run proves nothing. It runs
    // once in each console form, whatever the caller's env: `json` (CLAUDECODE=1, Laravel's one-line
    // summary, the class name JSON-escaped) and `text` (no agent variable, the plain `Class "…" not found`)
    const greetingArg = `${GREETING_TEST}::${cases.greetingCase}`;
    const formFile = path.join(tools, 'form.txt');
    for (const form of /** @type {const} */ (['json', 'text'])) {
      writeFileSync(formFile, form);
      const greeting = await prove(`new-class-${form}`, greetingArg);
      rmSync(formFile);
      assert.equal(greeting.steps.length, 1, form);
      const seen = consoleForm(greeting.steps[0].console);
      const kind = greeting.added[1]?.red_kind;
      t.diagnostic(`new-class RED forced form: ${form}; console form: ${seen}; red_kind: ${kind}`);
      assert.equal(seen, form);
      assert.deepEqual([greeting.code, greeting.stderr], [1, `RED_INVALID ${greetingArg} (import)\n`], greeting.stdout);
      assert.deepEqual(
        greeting.added.map((r) => [r.step, r.red ?? null, r.red_kind ?? null, r.green ?? null, r.proven ?? null, r.runner ?? null, r.failed ?? null, r.failed_tests ?? null]),
        [
          ['export', null, null, null, null, null, null, null],
          ['red-green', 'RED_INVALID', 'import', null, false, 'junit', 1, [cases.greetingId]],
        ],
        form,
      );
      // PHPUnit exits 2 on an error; the report holds the error, not a failure
      assert.deepEqual(greeting.steps.map((s) => [s.code, s.files]), [[2, ['report.xml']]], form);
    }

    // 6. the main tree is untouched: the proofs ran in the export
    assert.equal(git(['status', '--porcelain', '--untracked-files=all'], app), statusBefore);
    assert.deepEqual([readFileSync(path.join(app, CLAMP), 'utf8'), readFileSync(provider, 'utf8')], [CLAMP_FIXED, dirty]);

    // 7. a full-config run (no file, no --filter) in the main tree with the base's Clamp and the clean
    //    provider: the nested report (config suite > Unit / Feature > class) with a pass and a fail mixed
    writeFileSync(path.join(app, CLAMP), CLAMP_BROKEN);
    writeFileSync(provider, clean);
    const fullDir = path.join(captureDir, `${String(readdirSync(captureDir).length + 1).padStart(2, '0')}-full`);
    mkdirSync(fullDir);
    const fullReport = path.join(fullDir, 'report.xml');
    const fullStarted = Date.now();
    const full = spawnSync('php', ['artisan', 'test', '--log-junit', fullReport], {
      cwd: app,
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
      env: { ...childEnv(), DB_CONNECTION: 'sqlite', DB_DATABASE: ':memory:' },
    });
    const fullMs = Date.now() - fullStarted;
    writeFileSync(path.join(fullDir, 'step.json'), JSON.stringify({ code: full.status, signal: full.signal, ms: fullMs, files: readdirSync(fullDir).sort() }));
    writeFileSync(path.join(fullDir, 'console.txt'), `${full.stdout ?? ''}\n--- stderr\n${full.stderr ?? ''}`);
    const fullXml = readFileSync(fullReport, 'utf8');
    const nesting = /^<\?xml[^\n]*\n<testsuites>\n {2}<testsuite name="([^"]+)"[^\n]*>\n {4}<testsuite name="([^"]+)"[^\n]*>\n {6}<testsuite name="([^"]+)"/.exec(fullXml);
    t.diagnostic(`full run: exit ${full.status} in ${fullMs} ms; suites ${JSON.stringify(nesting?.slice(1) ?? null)}`);
    assert.ok(nesting, fullXml);
    assert.equal(path.basename(nesting[1]), 'phpunit.xml', 'the root suite is named after the config file');
    assert.deepEqual([nesting[2], nesting[3]], ['Unit', 'Tests\\Unit\\ClampTest']);
    const fullTests = readJUnit(fullXml).tests;
    t.diagnostic(`full run ids: ${JSON.stringify(fullTests.map((x) => [x.id, x.status]))}`);
    assert.equal(full.status, 1);
    assert.deepEqual(
      fullTests.map((x) => [x.id, x.status]),
      cases.fullIds.map((id) => [id, id === cases.clampId ? 'failed' : 'passed']),
    );
    assert.deepEqual(fullTests.filter((x) => x.status !== 'passed'), [{ id: cases.clampId, status: 'failed', kind: 'assertion' }]);
  } finally {
    // kept on failure too: the capture is what explains a failed live run; copied into a fresh dir,
    // so it never merges with an older run's capture
    const keep = process.env.CODE_FORGE_PHP_CAPTURE;
    if (keep && existsSync(captureDir)) {
      rmSync(path.join(keep, runner), { recursive: true, force: true });
      cpSync(captureDir, path.join(keep, runner), { recursive: true });
      t.diagnostic(`reports copied to ${path.join(keep, runner)}`);
    }
    rmSync(parent, { recursive: true, force: true });
    t.diagnostic(`duration: ${Math.round((Date.now() - started) / 1000)} s`);
  }
}

const LIVE = { skip: reason ?? false, timeout: 20 * 60 * 1000 };

test('B61 + B89 live: a Laravel skeleton on PHPUnit runs green through the export, unaffected by a dirty main tree, and proves red→green through the junit runner; a test of a new class is RED_INVALID', LIVE, (t) =>
  liveCheck(t, 'phpunit'),
);

test('B61 + B89 live: the same skeleton on Pest runs green through the export and proves red→green through the junit runner; a test of a new class is RED_INVALID', LIVE, (t) => liveCheck(t, 'pest'));

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

test('B89 live: the console form of a failed run is json only for Laravel\'s JSON summary line', () => {
  assert.equal(consoleForm('{"tool":"phpunit","result":"failed","tests":1}\n'), 'json');
  assert.equal(consoleForm('noise\n{"tool":"pest","result":"failed","tests":1}\n--- stderr\n'), 'json');
  assert.equal(consoleForm('{"tool":"phpunit","result":"passed","tests":1}\n'), 'text');
  assert.equal(consoleForm('{"tool":"jest","result":"failed"}\n'), 'text');
  assert.equal(consoleForm('  FAILED  Tests\\Unit\\GreetingTest\n  Error: Class "App\\Support\\Greeting" not found\n'), 'text');
  assert.equal(consoleForm('{not json\n'), 'text');
});

test('B89 live: the recorder forwards stdout, stderr and the exit code when the report dir is missing, and records why', () => {
  const dir = tempDir('laravel-live-recorder');
  const captureDir = path.join(dir, 'capture');
  mkdirSync(captureDir);
  writeFileSync(path.join(dir, 'label.txt'), 'gone\n');
  writeFileSync(path.join(dir, 'recorder.mjs'), RECORDER);
  // a stand-in runner: prints on both streams and exits 3, never writes the report
  const fake = path.join(dir, 'fake-runner.mjs');
  writeFileSync(fake, "process.stdout.write('OUT-FAKE\\n'); process.stderr.write('ERR-FAKE\\n'); process.exitCode = 3;\n");
  const missing = path.join(dir, 'no-such-dir', 'report.xml');
  const r = spawnSync(process.execPath, [path.join(dir, 'recorder.mjs'), captureDir, path.join(dir, 'label.txt'), process.execPath, fake, '--log-junit', missing], {
    encoding: 'utf8',
    env: childEnv(),
  });
  assert.deepEqual([r.status, r.stdout, r.stderr], [3, 'OUT-FAKE\n', 'ERR-FAKE\n']);
  assert.deepEqual(readdirSync(captureDir), ['01-gone']);
  const step = JSON.parse(readFileSync(path.join(captureDir, '01-gone', 'step.json'), 'utf8'));
  assert.deepEqual([step.code, step.signal, step.files, step.report_dir], [3, null, [], 'ENOENT']);
  assert.deepEqual(readdirSync(path.join(captureDir, '01-gone')).sort(), ['console.txt', 'step.json']);
  rmSync(dir, { recursive: true, force: true });
});

test('B89 live: the recorder forces the console form: json sets CLAUDECODE=1, text removes every agent detector variable', () => {
  const dir = tempDir('laravel-live-recorder-form');
  const captureDir = path.join(dir, 'capture');
  mkdirSync(captureDir);
  writeFileSync(path.join(dir, 'label.txt'), 'form\n');
  writeFileSync(path.join(dir, 'recorder.mjs'), RECORDER);
  // a stand-in runner: prints the agent detector variables it sees
  const fake = path.join(dir, 'fake-runner.mjs');
  writeFileSync(fake, `process.stdout.write(JSON.stringify(${JSON.stringify(AGENT_DETECTOR_VARS)}.filter((k) => k in process.env)));\n`);
  // the base env has no agent detector variable, so each case sees only what the recorder adds
  /** @type {NodeJS.ProcessEnv} */
  const baseEnv = { ...childEnv() };
  for (const k of AGENT_DETECTOR_VARS) delete baseEnv[k];
  const run = (/** @type {NodeJS.ProcessEnv} */ env) =>
    spawnSync(process.execPath, [path.join(dir, 'recorder.mjs'), captureDir, path.join(dir, 'label.txt'), process.execPath, fake, '--log-junit', path.join(dir, 'r', 'report.xml')], {
      encoding: 'utf8',
      env: { ...baseEnv, ...env },
    }).stdout;
  const agentEnv = { AI_AGENT: 'FAKE-agent', CODEX_SANDBOX: '1', OPENCLAW_SHELL: '1' };
  writeFileSync(path.join(dir, 'form.txt'), 'text');
  assert.equal(run(agentEnv), '[]');
  writeFileSync(path.join(dir, 'form.txt'), 'json');
  assert.deepEqual(JSON.parse(run({})), ['CLAUDECODE']);
  rmSync(path.join(dir, 'form.txt'));
  assert.deepEqual(JSON.parse(run(agentEnv)), ['AI_AGENT', 'CODEX_SANDBOX', 'OPENCLAW_SHELL']);
  assert.equal(run({}), '[]');
  rmSync(dir, { recursive: true, force: true });
});
