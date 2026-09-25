import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, before, test } from 'node:test';
import { stringify as toYAML } from 'yaml';
import { resolveLevel } from '../../src/config/known-ids.mjs';
import { migrateConfig } from '../../src/config/migrate.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(__dirname, '..', '..');
const BIN = path.join(ROOT, 'bin', 'code-forge.mjs');

/** @type {string} */
let projectDir;
/** @type {string} */
let tmpHome;

/** @type {Record<string, any>} */
const CONFIG = {
  version: 1,
  provider: 'anthropic',
  levels: {
    L0: { model: 'claude-haiku-4-5-20251001' },
    L1: { model: 'claude-sonnet-5' },
    // per-level provider override (R5): L2 runs on openai even though the top-level provider is
    // anthropic — model id is an openai-family id too, so an alias-map/family check elsewhere
    // could never trip on this fixture for an unrelated reason.
    L2: { model: 'gpt-6-astra', effort: 'high', provider: 'openai', fallback: [{ provider: 'openai', model: 'gpt-6-sol' }] },
    L3: { model: 'claude-fable-5-1' },
  },
};

before(() => {
  projectDir = mkdtempSync(path.join(os.tmpdir(), 'code-forge-resolve-verb-test-'));
  tmpHome = mkdtempSync(path.join(os.tmpdir(), 'code-forge-resolve-verb-home-'));
  writeFileSync(path.join(projectDir, '.code-forge.yml'), toYAML(CONFIG), 'utf8');
});

after(() => {
  rmSync(projectDir, { recursive: true, force: true });
  rmSync(tmpHome, { recursive: true, force: true });
});

/**
 * @param {string[]} args
 * @param {{cwd?: string}} [opts]
 */
function runCli(args, opts = {}) {
  return spawnSync(process.execPath, [BIN, ...args], { encoding: 'utf8', cwd: opts.cwd ?? projectDir, env: { HOME: tmpHome, USERPROFILE: tmpHome } });
}

test('resolve L2 prints exactly {provider, model, effort, fallback, cli} on stdout, RAW (no log prefix), honouring the per-level provider override (R5), and matches B1\'s resolveLevel exactly', () => {
  const { status, stdout, stderr } = runCli(['resolve', 'L2']);
  assert.equal(status, 0, stderr);
  // fix round 1 (MINOR): the output is now written via redact.writeSafe, not log.info — no
  // "[code-forge:info] " prefix, so this is EXACTLY one clean JSON line.
  assert.equal(stdout.startsWith('[code-forge:info]'), false, 'stdout must be RAW JSON, not log-prefixed');
  assert.equal(stderr, '');
  const lines = stdout.trimEnd().split('\n');
  assert.equal(lines.length, 1, `expected exactly 1 output line, got:\n${stdout}`);
  const printed = JSON.parse(lines[0]);
  assert.deepEqual(Object.keys(printed).sort(), ['cli', 'effort', 'fallback', 'model', 'provider'].sort());
  assert.equal(printed.provider, 'openai');
  assert.equal(printed.model, 'gpt-6-astra');
  assert.equal(printed.effort, 'high');
  assert.deepEqual(printed.fallback, [{ provider: 'openai', model: 'gpt-6-sol' }]);
  assert.equal(printed.cli, 'codex');

  // Cross-check against B1's OWN resolveLevel, called independently here on the SAME migrated
  // config, so a separate resolver hidden inside the verb (rather than actually using B1's) would
  // fail this — the acceptance clause says "via B1's resolveLevel" (fix round 1, MINOR).
  const resolved = resolveLevel(migrateConfig(CONFIG), 'L2');
  assert.equal(printed.provider, resolved.provider);
  assert.equal(printed.model, resolved.model);
  assert.equal(printed.effort, resolved.effort ?? null);
  assert.deepEqual(printed.fallback, resolved.fallback);
});

test('resolve L1 (no per-level provider, no effort, no fallback): top-level provider wins, effort null, fallback [], and the exact 5-key contract still holds', () => {
  const { status, stdout, stderr } = runCli(['resolve', 'L1']);
  assert.equal(status, 0, stderr);
  const printed = JSON.parse(stdout.trimEnd());
  // fix round 1 (MINOR): explicitly re-asserts the exact key SET for the no-fallback case too —
  // resolveLevel (B1) always returns a (possibly empty) array for fallback, never `undefined`,
  // so `JSON.stringify` can never silently drop this key; this test locks that in directly rather
  // than only checking it for the WITH-fallback (L2) case above.
  assert.deepEqual(Object.keys(printed).sort(), ['cli', 'effort', 'fallback', 'model', 'provider'].sort());
  assert.equal(printed.provider, 'anthropic');
  assert.equal(printed.model, 'claude-sonnet-5');
  assert.equal(printed.effort, null);
  assert.deepEqual(printed.fallback, []);
  assert.equal(printed.cli, 'claude');
});

test('resolve with an unknown level exits 2, prints nothing to stdout, and stderr names the level — run where NO CONFIG FILE EXISTS AT ALL', () => {
  const emptyDir = mkdtempSync(path.join(os.tmpdir(), 'code-forge-resolve-verb-no-config-'));
  try {
    // fix round 1 (MAJOR): run in a dir with NO .code-forge.yml — the old test ran this exact
    // case inside projectDir, which DOES have a valid config, so it could never actually prove
    // "refuses before ever loading the config" (a load-then-check bug would have passed it too).
    const { status, stdout, stderr } = runCli(['resolve', 'L9'], { cwd: emptyDir });
    assert.equal(status, 2);
    assert.equal(stdout, '');
    assert.match(stderr, /unknown level "L9"/);
  } finally {
    rmSync(emptyDir, { recursive: true, force: true });
  }
});

test('resolve with no argument, or more than one, exits 2 (usage error), with the usage message on stderr and nothing on stdout', () => {
  for (const args of [['resolve'], ['resolve', 'L1', 'L2']]) {
    const { status, stdout, stderr } = runCli(args);
    assert.equal(status, 2, `args ${JSON.stringify(args)}`);
    assert.equal(stdout, '');
    assert.match(stderr, /usage: resolve/);
  }
});

test('resolve exits 1 with a clear "not-found" message when no .code-forge.yml exists (a config-load level check, distinct from the L9 usage-level check above)', () => {
  const emptyDir = mkdtempSync(path.join(os.tmpdir(), 'code-forge-resolve-verb-empty-'));
  try {
    const { status, stdout, stderr } = runCli(['resolve', 'L1'], { cwd: emptyDir });
    assert.equal(status, 1);
    assert.equal(stdout, '');
    assert.match(stderr, /not-found/);
  } finally {
    rmSync(emptyDir, { recursive: true, force: true });
  }
});

test('resolve exits 1 (never 0 with "cli": null) when the resolved provider has no known CLI mapping', () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'code-forge-resolve-verb-unknown-provider-'));
  try {
    writeFileSync(
      path.join(dir, '.code-forge.yml'),
      toYAML({
        version: 1,
        provider: 'unknown-vendor', // deliberately outside the anthropic/openai/xai enum — resolve
        //                             does not itself call validateConfig, so this can reach here.
        levels: {
          L0: { model: 'x' },
          L1: { model: 'x' },
          L2: { model: 'x' },
          L3: { model: 'x' },
        },
      }),
      'utf8',
    );
    const { status, stdout, stderr } = runCli(['resolve', 'L1'], { cwd: dir });
    assert.equal(status, 1);
    assert.equal(stdout, '');
    assert.match(stderr, /no known CLI mapping/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
