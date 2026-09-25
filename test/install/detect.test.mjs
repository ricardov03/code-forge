import assert from 'node:assert/strict';
import { chmod, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { tempDir } from './helpers.mjs';
import { commandOnPath, detectAll, detectHarness, homeMarkerPresent } from '../../src/install/detect.mjs';
import { HARNESSES, getHarness } from '../../src/install/harnesses.mjs';

/**
 * @param {string} dir
 * @param {string} name
 * @param {number} mode
 */
async function writeFakeExecutable(dir, name, mode) {
  await mkdir(dir, { recursive: true });
  const filePath = path.join(dir, name);
  await writeFile(filePath, '#!/bin/sh\n', 'utf8');
  await chmod(filePath, mode);
  return filePath;
}

// ── commandOnPath ─────────────────────────────────────────────────────────────

test('commandOnPath finds an executable file in a PATH directory', async () => {
  const bin = await tempDir('cf-bin-');
  await writeFakeExecutable(bin, 'claude', 0o755);
  assert.equal(await commandOnPath('claude', { pathEnv: bin, platform: 'darwin' }), true);
});

test('commandOnPath is false for a NON-executable file of the same name (existence alone is not enough)', async () => {
  const bin = await tempDir('cf-bin-');
  await writeFakeExecutable(bin, 'claude', 0o644);
  assert.equal(await commandOnPath('claude', { pathEnv: bin, platform: 'darwin' }), false);
});

test('commandOnPath is false when the command is in no PATH directory', async () => {
  const bin = await tempDir('cf-bin-');
  await writeFakeExecutable(bin, 'codex', 0o755);
  assert.equal(await commandOnPath('claude', { pathEnv: bin, platform: 'darwin' }), false);
});

test('commandOnPath scans every directory in a multi-entry PATH, in order', async () => {
  const empty = await tempDir('cf-bin-empty-');
  const bin = await tempDir('cf-bin-');
  await writeFakeExecutable(bin, 'grok', 0o755);
  assert.equal(await commandOnPath('grok', { pathEnv: `${empty}:${bin}`, platform: 'darwin' }), true);
});

test('commandOnPath is false for an empty/undefined command', async () => {
  assert.equal(await commandOnPath('', { pathEnv: '/usr/bin' }), false);
  assert.equal(await commandOnPath(undefined, { pathEnv: '/usr/bin' }), false);
});

// ── homeMarkerPresent ─────────────────────────────────────────────────────────

test('homeMarkerPresent is true when <home>/<homeMarker> is a directory', async () => {
  const home = await tempDir('cf-home-');
  await mkdir(path.join(home, '.claude'), { recursive: true });
  assert.equal(await homeMarkerPresent(getHarness('claude'), { home }), true);
});

test('homeMarkerPresent is false when <home>/<homeMarker> is a FILE, not a directory', async () => {
  const home = await tempDir('cf-home-');
  await writeFile(path.join(home, '.claude'), 'not a dir', 'utf8');
  assert.equal(await homeMarkerPresent(getHarness('claude'), { home }), false);
});

test('homeMarkerPresent is false when nothing exists at that path', async () => {
  const home = await tempDir('cf-home-');
  assert.equal(await homeMarkerPresent(getHarness('gemini'), { home }), false);
});

// ── detectHarness / detectAll ─────────────────────────────────────────────────

test('detectHarness is detected via PATH alone (no home marker)', async () => {
  const home = await tempDir('cf-home-');
  const bin = await tempDir('cf-bin-');
  await writeFakeExecutable(bin, 'claude', 0o755);
  const result = await detectHarness(getHarness('claude'), { home, pathEnv: bin, platform: 'darwin' });
  assert.deepEqual(result, { id: 'claude', detected: true, onPath: true, homeDirPresent: false });
});

test('detectHarness is detected via the home marker alone (no PATH match)', async () => {
  const home = await tempDir('cf-home-');
  const bin = await tempDir('cf-bin-empty-');
  await mkdir(path.join(home, '.grok'), { recursive: true });
  const result = await detectHarness(getHarness('grok'), { home, pathEnv: bin, platform: 'darwin' });
  assert.deepEqual(result, { id: 'grok', detected: true, onPath: false, homeDirPresent: true });
});

test('detectHarness is NOT detected when neither PATH nor the home marker match', async () => {
  const home = await tempDir('cf-home-');
  const bin = await tempDir('cf-bin-empty-');
  const result = await detectHarness(getHarness('gemini'), { home, pathEnv: bin, platform: 'darwin' });
  assert.deepEqual(result, { id: 'gemini', detected: false, onPath: false, homeDirPresent: false });
});

test('a harness with command: null (cursor) is never marked onPath, even if a same-named, EXECUTABLE PATH binary genuinely exists', async () => {
  const home = await tempDir('cf-home-');
  const bin = await tempDir('cf-bin-');
  // A REAL, executable "cursor" binary sits on PATH — cursor's own `command` is null, so nothing
  // must ever look it up there (a bug that fell back to the harness id instead of `command` would
  // flip onPath to true here).
  await writeFakeExecutable(bin, 'cursor', 0o755);
  await mkdir(path.join(home, '.cursor'), { recursive: true });
  const result = await detectHarness(getHarness('cursor'), { home, pathEnv: bin, platform: 'darwin' });
  assert.deepEqual(result, { id: 'cursor', detected: true, onPath: false, homeDirPresent: true });
});

test('detectAll returns exactly 6 results, one per HARNESSES row, same order, same ids', async () => {
  const home = await tempDir('cf-home-');
  const bin = await tempDir('cf-bin-empty-');
  const results = await detectAll({ home, pathEnv: bin, platform: 'darwin' });
  assert.equal(results.length, 6);
  assert.deepEqual(
    results.map((r) => r.id),
    HARNESSES.map((h) => h.id),
  );
});
