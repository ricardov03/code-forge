/**
 * B16 acceptance: "the config doc regenerates byte-identical (`--check`, 1)". `scripts/gen-config-doc.mjs`
 * is the single generator for `docs/reference/config.md`; this proves the two stay in sync both
 * through its `--check` CLI flag (the form CI runs) and through the exported pure function (the
 * form a future regeneration would call), so a divergence between the two call paths cannot hide.
 */

import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { after, test } from 'node:test';
import { spawnSync } from 'node:child_process';
import { renderConfigDoc } from '../scripts/gen-config-doc.mjs';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const CONFIG_DOC_PATH = path.join(REPO_ROOT, 'docs', 'reference', 'config.md');
const GEN_SCRIPT = path.join(REPO_ROOT, 'scripts', 'gen-config-doc.mjs');
const SCHEMA_PATH = path.join(REPO_ROOT, 'schema', 'code-forge.schema.json');

/** One per-file temp parent (coder rules §8), removed in after(). */
const TMP_PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-docs-config-'));
after(() => rmSync(TMP_PARENT, { recursive: true, force: true }));

test('`node scripts/gen-config-doc.mjs --check` exits 0 against the committed docs/reference/config.md', () => {
  const res = spawnSync(process.execPath, [GEN_SCRIPT, '--check'], { cwd: REPO_ROOT, encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  assert.match(res.stdout, /up to date/);
});

test('the exported generator is byte-identical to the committed file (boundary: same fact, two call paths)', () => {
  const onDisk = readFileSync(CONFIG_DOC_PATH, 'utf8');
  assert.equal(renderConfigDoc(), onDisk);
});

/**
 * Copies the script, the schema and the doc into a temp repo layout (the script finds both from
 * its own directory), changes one byte of the doc copy when `stale`, and runs `--check` through a
 * symlink to the copied script. Node resolves the entry symlink for `import.meta.url` but not for
 * `argv[1]`, which is the case the old `path.resolve` entry-point check silently skipped.
 * @param {string} name @param {boolean} stale
 */
function runCheckViaSymlink(name, stale) {
  const root = path.join(TMP_PARENT, name);
  mkdirSync(path.join(root, 'scripts'), { recursive: true });
  mkdirSync(path.join(root, 'schema'), { recursive: true });
  mkdirSync(path.join(root, 'docs', 'reference'), { recursive: true });
  copyFileSync(GEN_SCRIPT, path.join(root, 'scripts', 'gen-config-doc.mjs'));
  copyFileSync(SCHEMA_PATH, path.join(root, 'schema', 'code-forge.schema.json'));
  const doc = Buffer.from(readFileSync(CONFIG_DOC_PATH));
  if (stale) doc[0] = doc[0] === 0x23 ? 0x2a : 0x23; // one byte changed: '#' <-> '*'
  writeFileSync(path.join(root, 'docs', 'reference', 'config.md'), doc);
  const link = path.join(root, 'gen-link.mjs');
  symlinkSync(path.join(root, 'scripts', 'gen-config-doc.mjs'), link);
  return spawnSync(process.execPath, [link, '--check'], { cwd: root, encoding: 'utf8' });
}

test('`--check` invoked through a symlink exits 1 on a doc with one byte changed (entry-point check follows symlinks)', () => {
  const res = runCheckViaSymlink('stale', true);
  assert.equal(res.status, 1, res.stdout);
  assert.match(res.stderr, /is stale \(first difference at line 1\)/);
  assert.equal(res.stdout, '');
});

test('`--check` invoked through a symlink exits 0 and reports up to date on an unchanged copy', () => {
  const res = runCheckViaSymlink('fresh', false);
  assert.equal(res.status, 0, res.stderr);
  assert.equal(res.stdout, 'gen-config-doc --check: docs/reference/config.md is up to date\n');
});
