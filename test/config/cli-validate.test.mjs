import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { stringify as toYAML } from 'yaml';
import { SECRET_LEAK_CASES } from './secret-leak-cases.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..', '..');
const BIN = path.join(ROOT, 'bin', 'code-forge.mjs');
const FIXTURES = path.join(__dirname, '..', 'fixtures', 'config');

/** @type {string} */
let tmpRoot;
/** @type {string} */
let tmpHome;
/** @type {string} */
let emptyPathDir;
/** @type {string} */
let stubPathDir;

before(() => {
  // HOME is isolated so a developer's real `~/.code-forge/cache/models.json` can't change these
  // tests. PATH is isolated too: the `validate` verb's `hasCliOnPath` scans PATH for
  // `claude`/`codex`/`grok`, so the real PATH would make the exact counts below depend on which
  // CLIs happen to be installed on the machine running the test.
  tmpRoot = mkdtempSync(path.join(os.tmpdir(), 'code-forge-cli-validate-test-'));
  tmpHome = path.join(tmpRoot, 'home');
  emptyPathDir = path.join(tmpRoot, 'empty-bin');
  stubPathDir = path.join(tmpRoot, 'stub-bin');
  mkdirSync(tmpHome);
  mkdirSync(emptyPathDir);
  mkdirSync(stubPathDir);
  // `hasCliOnPath` only checks that the file exists — a stub is enough to make `claude` "present".
  writeFileSync(path.join(stubPathDir, 'claude'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
});

after(() => {
  rmSync(tmpRoot, { recursive: true, force: true });
});

/**
 * Spawns the real verb with a MINIMAL env. The binary is launched through `process.execPath`
 * (an absolute path), so node needs no PATH lookup at all — PATH here only feeds `hasCliOnPath`,
 * and defaults to an EMPTY directory (no provider CLI anywhere).
 * @param {string[]} args
 * @param {{pathDir?: string}} [opts]
 */
function runCli(args, opts = {}) {
  return spawnSync(process.execPath, [BIN, ...args], {
    encoding: 'utf8',
    env: { PATH: opts.pathDir ?? emptyPathDir, HOME: tmpHome, USERPROFILE: tmpHome },
  });
}

/**
 * @param {string} text
 * @param {string} needle
 * @returns {number}
 */
function occurrences(text, needle) {
  return text.split(needle).length - 1;
}

/**
 * @param {string} name
 * @param {Record<string, any>} cfg
 * @returns {string} the written file's path.
 */
function writeConfig(name, cfg) {
  const file = path.join(tmpRoot, name);
  writeFileSync(file, toYAML(cfg), 'utf8');
  return file;
}

// ── Exact-output assertions ─────────────────────────────────────────────────

test('code-forge validate --file <valid config> exits 0 and prints an EXACT "<path>: valid (0 warnings)" line, nowhere containing "invalid"', () => {
  const file = path.join(FIXTURES, 'valid-full.code-forge.yml');
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 0, stderr);
  assert.match(stdout, new RegExp(`^\\[code-forge:info\\] ${file.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}: valid \\(0 warnings\\)$`, 'm'));
  assert.equal(stdout.includes('invalid'), false, `stdout must never contain the substring "invalid" for a valid config: ${stdout}`);
  assert.equal(stderr, '', 'a fully valid config with 0 warnings must print nothing to stderr');
});

test('code-forge validate --file <config missing L3> exits 1, reports EXACTLY 1 level-missing refusal naming L3, and 0 other rule refusals', () => {
  const file = path.join(FIXTURES, 'invalid-missing-level.code-forge.yml');
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);

  const levelMissingLines = stderr.split('\n').filter((line) => line.includes('[level-missing]'));
  assert.equal(levelMissingLines.length, 1, `expected exactly 1 level-missing line, got:\n${stderr}`);
  assert.match(levelMissingLines[0], /\bL3\b/, `expected the level-missing line to name L3, got: ${levelMissingLines[0]}`);
  assert.equal(levelMissingLines[0].includes('L2'), false, 'L2 IS present in the fixture — must not be named as missing');

  // Exactly 2 error lines total: the domain rule AND the schema layer's own "required property"
  // complaint about the same missing L3 (both independently correct — see validate.test.mjs).
  const errorLines = stderr.split('\n').filter((line) => line.startsWith('[code-forge:error]'));
  assert.equal(errorLines.length, 2, `expected exactly 2 error lines (level-missing + schema), got:\n${stderr}`);

  assert.match(stdout, /invalid \(2 errors\)$/m);
});

// ── hasCliOnPath is really wired into the verb (PATH decides the outcome) ───

/** An all-anthropic `engine: subprocess` config with one fallback — every CLI it needs is `claude`. */
function writeSubprocessConfig() {
  return writeConfig('subprocess.code-forge.yml', {
    version: 1,
    provider: 'anthropic',
    engine: 'subprocess',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'claude-opus-5-5' },
      L3: { model: 'claude-fable-5-1', fallback: [{ provider: 'anthropic', model: 'claude-sonnet-5' }] },
    },
  });
}

test('engine: subprocess with NO provider CLI on PATH: EXACTLY 1 engine-subprocess-no-cli refusal naming "anthropic", exit 1, "invalid (1 error)"', () => {
  const file = writeSubprocessConfig();
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);
  const refusals = stderr.split('\n').filter((l) => l.includes('[engine-subprocess-no-cli]'));
  assert.deepEqual(refusals, [
    '[code-forge:error] [engine-subprocess-no-cli] engine is "subprocess" but no CLI was found on PATH for provider(s): "anthropic"',
  ]);
  const fallbackWarnings = stderr.split('\n').filter((l) => l.includes('[fallback-unknown-or-cli-absent]'));
  assert.deepEqual(fallbackWarnings, [
    '[code-forge:warn] [fallback-unknown-or-cli-absent] levels.L3.fallback[0] (provider "anthropic") has no CLI on PATH',
  ]);
  assert.match(stdout, /invalid \(1 error\)$/m);
});

test('the SAME config with a `claude` stub on PATH: 0 refusals, 0 warnings, exit 0 — proves the verb passes a real PATH-backed hasCliOnPath', () => {
  const file = writeSubprocessConfig();
  const { status, stdout, stderr } = runCli(['validate', '--file', file], { pathDir: stubPathDir });
  assert.equal(status, 0, stderr);
  assert.equal(stderr, '');
  assert.match(stdout, /: valid \(0 warnings\)$/m);
});

// ── not-found: exact path named, both streams checked ───────────────────────

test('code-forge validate --file <nonexistent path> exits 1, names the EXACT missing path in stderr, and prints nothing to stdout', () => {
  const file = path.join(FIXTURES, 'does-not-exist.code-forge.yml');
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);
  assert.match(stderr, /^\[code-forge:error\] not-found: /m);
  assert.ok(stderr.includes(file), `expected the exact missing path in stderr, got: ${stderr}`);
  assert.equal(stdout, '', 'a load failure (never reaching the validator) must print nothing to stdout');
});

// ── Usage errors (exit 2) ────────────────────────────────────────────────────

test('code-forge validate with an unknown flag exits 2 (usage error, distinct from a failed validation)', () => {
  const { status, stderr } = runCli(['validate', '--bogus-flag']);
  assert.equal(status, 2);
  assert.match(stderr, /unknown argument: --bogus-flag/);
});

test('code-forge validate --file with no path argument exits 2', () => {
  const { status, stderr } = runCli(['validate', '--file']);
  assert.equal(status, 2);
  assert.match(stderr, /--file requires a path/);
});

test('code-forge validate --file --foo exits 2 (the next flag is NOT treated as the path)', () => {
  const { status, stderr } = runCli(['validate', '--file', '--foo']);
  assert.equal(status, 2);
  assert.match(stderr, /--file requires a path/);
});

// ── Secret safety: never echo a secret-looking value ────────────────────────

test('a config with a secret-looking value exits 1 with EXACTLY 1 refusal naming project.name, 1 error line total, "invalid (1 error)", and 0 occurrences of the secret', () => {
  const fakeSecret = 'sk-fake-9f2c7e1a4b6d80351234567890abcdef';
  const file = path.join(tmpRoot, 'secret.code-forge.yml');
  writeFileSync(
    file,
    [
      'version: 1',
      'provider: anthropic',
      'levels:',
      '  L0:',
      '    model: claude-haiku-4-5-20251001',
      '  L1:',
      '    model: claude-sonnet-5',
      '  L2:',
      '    model: claude-opus-5-5',
      '  L3:',
      '    model: claude-fable-5-1',
      'project:',
      `  name: ${fakeSecret}`,
      '',
    ].join('\n'),
    'utf8',
  );
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);
  const refusals = stderr.split('\n').filter((l) => l.includes('[secret-looking-value]'));
  assert.equal(refusals.length, 1, stderr);
  assert.match(refusals[0], /\[secret-looking-value\] project\.name\b/);
  assert.equal(stderr.split('\n').filter((l) => l.startsWith('[code-forge:error]')).length, 1, stderr);
  assert.match(stdout, /invalid \(1 error\)$/m);
  assert.equal(occurrences(stdout + stderr, fakeSecret), 0);
});

for (const leakCase of SECRET_LEAK_CASES) {
  test(`validate verb no-leak [${leakCase.name}]: EXACTLY ${leakCase.count} [${leakCase.rule}] line(s), exit 1, and the planted secret appears 0 times in stdout+stderr`, () => {
    const file = writeConfig(`leak-${leakCase.rule}-${leakCase.secret.slice(-3)}.code-forge.yml`, leakCase.build(leakCase.secret));
    const { status, stdout, stderr } = runCli(['validate', '--file', file]);
    assert.equal(status, 1, `${leakCase.name}: every case also carries a secret refusal, so it must be invalid`);
    const ruleLines = stderr.split('\n').filter((l) => l.includes(`] [${leakCase.rule}] `));
    assert.equal(ruleLines.length, leakCase.count, stderr);
    assert.equal(occurrences(stdout + stderr, leakCase.secret), 0, `leaked:\n${stdout}\n${stderr}`);
  });
}

// ── Secret safety: a malformed YAML line near a secret must not leak it in the parse error ──

test('a MALFORMED config with a secret near the bad line exits 1, names the location (line 3, column 1), and never echoes the secret', () => {
  const fakeSecret = 'sk-ant-fakeCONFIGPARSEERR0R1234567890abcdef';
  const file = path.join(tmpRoot, 'broken-secret.code-forge.yml');
  writeFileSync(file, `api_key: ${fakeSecret}\nfoo: [unterminated\n`, 'utf8');
  const { status, stdout, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);
  assert.match(stderr, /^\[code-forge:error\] parse-error: [A-Z_]+: .+ \(line 3, column 1\)$/m);
  assert.equal(occurrences(stdout + stderr, fakeSecret), 0, `leaked:\n${stdout}\n${stderr}`);
});

// ── B29 (issue #2): a bad effort is refused at config time, not at spawn time ──

test('validate verb: an openai L2 with effort "xhigh" exits 1 with EXACTLY 1 effort line carrying the exact message', () => {
  const file = writeConfig('b29-effort.code-forge.yml', {
    version: 1,
    provider: 'anthropic',
    levels: {
      L0: { model: 'claude-haiku-4-5-20251001' },
      L1: { model: 'claude-sonnet-5' },
      L2: { model: 'gpt-6-sol', provider: 'openai', effort: 'xhigh' },
      L3: { model: 'claude-fable-5-1' },
    },
  });
  const { status, stderr } = runCli(['validate', '--file', file]);
  assert.equal(status, 1);
  const lines = stderr.split('\n').filter((l) => l.includes('[effort-not-valid-for-provider]'));
  assert.deepEqual(lines, [
    '[code-forge:error] [effort-not-valid-for-provider] levels.L2.effort "xhigh" is not valid for provider openai; use one of: minimal, low, medium, high',
  ]);
});
