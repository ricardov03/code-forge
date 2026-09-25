import assert from 'node:assert/strict';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { DEFAULT_CONFIG_FILENAME, loadConfigFile, loadProjectConfig } from '../../src/config/load.mjs';

/** @type {string} */
let tmpDir;

before(async () => {
  tmpDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-load-test-'));
});

after(async () => {
  await rm(tmpDir, { recursive: true, force: true });
});

test('loadConfigFile returns ok:false, error:"not-found" for a missing file', async () => {
  const result = await loadConfigFile(path.join(tmpDir, 'does-not-exist.yml'));
  assert.equal(result.ok, false);
  assert.equal(result.error, 'not-found');
});

test('loadConfigFile parses a valid YAML file and migrates it (version filled in)', async () => {
  const file = path.join(tmpDir, 'ok.yml');
  await writeFile(
    file,
    'provider: anthropic\nlevels:\n  L0:\n    model: claude-haiku-4-5-20251001\n',
    'utf8',
  );
  const result = await loadConfigFile(file);
  assert.equal(result.ok, true);
  assert.equal(result.config.version, 1);
  assert.equal(result.config.provider, 'anthropic');
  assert.equal(result.config.levels.L0.model, 'claude-haiku-4-5-20251001');
});

// ── Parse errors and the secret-leak guard (MAJOR fix round 1) ──────────────

test('loadConfigFile returns ok:false, error:"parse-error" for invalid YAML, with a line/column, no source excerpt', async () => {
  const file = path.join(tmpDir, 'broken.yml');
  await writeFile(file, 'provider: [unterminated\n', 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'parse-error');
  assert.match(result.message, /^[A-Z_]+: .+\(line \d+, column \d+\)$/, `expected a "CODE: text (line N, column N)" shape, got: ${result.message}`);
});

test('a malformed line holding a secret-looking value does NOT put that value in the parse-error message', async () => {
  const file = path.join(tmpDir, 'broken-with-secret.yml');
  const fakeSecret = 'sk-ant-fake1234567890abcdef1234567890';
  await writeFile(file, `api_key: ${fakeSecret}\nfoo: [unterminated\n`, 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'parse-error');
  assert.equal(result.message.includes(fakeSecret), false, `secret leaked into parse-error message: ${result.message}`);
  // Prove the message still carries SOMETHING useful (a code and a location), not an empty string.
  assert.match(result.message, /line \d+, column \d+/);
});

test('a config that parses to a top-level scalar (not a mapping) is refused as parse-error, naming the actual kind', async () => {
  const file = path.join(tmpDir, 'scalar.yml');
  await writeFile(file, 'just-a-string\n', 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'parse-error');
  assert.match(result.message, /got string/);
});

test('a config that parses to a top-level LIST is refused as parse-error, naming "a list"', async () => {
  const file = path.join(tmpDir, 'list.yml');
  await writeFile(file, '- a\n- b\n', 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'parse-error');
  assert.match(result.message, /got a list/);
});

test('an EMPTY file (parses to null/undefined) is refused as parse-error, not silently accepted', async () => {
  const file = path.join(tmpDir, 'empty.yml');
  await writeFile(file, '', 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'parse-error');
  assert.match(result.message, /empty document/);
});

// ── migrate-error passthrough ─────────────────────────────────────────────

test('loadConfigFile returns ok:false, error:"migrate-error" for a config whose version is newer than supported', async () => {
  const file = path.join(tmpDir, 'future.yml');
  await writeFile(file, 'version: 99\nprovider: anthropic\n', 'utf8');
  const result = await loadConfigFile(file);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'migrate-error');
  assert.ok(result.message.includes('99'));
});

// ── Non-ENOENT read failures are distinguished from "not-found" (MINOR §119/120) ─

test('loadConfigFile on a DIRECTORY path returns ok:false, error:"read-error" (never "not-found")', async () => {
  const dirAsFile = path.join(tmpDir, 'a-directory-not-a-file');
  await mkdir(dirAsFile, { recursive: true });
  const result = await loadConfigFile(dirAsFile);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'read-error');
  assert.notEqual(result.error, 'not-found');
});

// ── loadProjectConfig ─────────────────────────────────────────────────────

test('loadProjectConfig reads DEFAULT_CONFIG_FILENAME from the given cwd', async () => {
  const projectDir = path.join(tmpDir, 'project-a');
  await mkdir(projectDir, { recursive: true });
  await writeFile(
    path.join(projectDir, DEFAULT_CONFIG_FILENAME),
    'provider: xai\nlevels:\n  L0:\n    model: grok-4.7\n',
    'utf8',
  );
  const result = await loadProjectConfig(projectDir);
  assert.equal(result.ok, true);
  assert.equal(result.config.provider, 'xai');
  assert.equal(result.path, path.join(projectDir, DEFAULT_CONFIG_FILENAME));
});

test('loadProjectConfig on a directory with NO .code-forge.yml returns ok:false, error:"not-found", with the exact expected path', async () => {
  const projectDir = path.join(tmpDir, 'project-empty');
  await mkdir(projectDir, { recursive: true });
  const result = await loadProjectConfig(projectDir);
  assert.equal(result.ok, false);
  assert.equal(result.error, 'not-found');
  assert.equal(result.path, path.join(projectDir, DEFAULT_CONFIG_FILENAME));
});

test('DEFAULT_CONFIG_FILENAME is exactly ".code-forge.yml"', () => {
  assert.equal(DEFAULT_CONFIG_FILENAME, '.code-forge.yml');
});
