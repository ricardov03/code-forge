import assert from 'node:assert/strict';
import { lstat, mkdir, readFile, readdir, readlink, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { tempDir, writeMarker } from './helpers.mjs';
import {
  install,
  installsPath,
  isSymlink,
  linkSkill,
  readInstalls,
  removeRecords,
  targetResolves,
  upgradeAll,
  writeInstallsAtomically,
} from '../../src/install/link.mjs';

/** @returns {Promise<string>} a fresh installs.json path under its own temp "home". */
async function freshInstallsFile() {
  const home = await tempDir('cf-home-');
  return installsPath(home);
}

// ── Acceptance: "symlink and copy modes on a temp HOME (2 tests)" ────────────

test('install() with method "symlink": the target is a real symlink pointing at source, and reads through to source live', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source, 'v1');
  const target = path.join(await tempDir('cf-tgt-parent-'), 'code-forge');

  const records = await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target });

  assert.equal(records.length, 1);
  assert.equal((await lstat(target)).isSymbolicLink(), true);
  assert.equal(await readlink(target), source);
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'v1');

  // A symlink reads through LIVE — proves it is a real link, not a one-time copy.
  await writeMarker(source, 'v2');
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'v2');

  const onDisk = await readInstalls(installsFile);
  assert.deepEqual(onDisk, records);
});

test('install() with method "copy": the target is a real directory (not a symlink) with an independent snapshot of source', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source, 'v1');
  const target = path.join(await tempDir('cf-tgt-parent-'), 'code-forge');

  const records = await install({ installsFile, harness: 'codex', scope: 'project', method: 'copy', source, target });

  assert.equal(records.length, 1);
  assert.equal((await lstat(target)).isSymbolicLink(), false);
  assert.equal((await lstat(target)).isDirectory(), true);
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'v1');

  // Changing source AFTER the copy must NOT be visible in target — proves independence.
  await writeMarker(source, 'v2');
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'v1');
});

test('linkSkill refuses an unknown method', async () => {
  const source = await tempDir('cf-src-');
  const target = path.join(await tempDir('cf-tgt-'), 'x');
  await assert.rejects(() => linkSkill({ source, target, method: /** @type {any} */ ('hardlink') }), TypeError);
});

// ── linkSkill source validation, checked BEFORE target is ever touched ───────

test('linkSkill rejects a MISSING source without creating anything at target', async () => {
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  const missingSource = path.join(await tempDir('cf-missing-'), 'nope');
  await assert.rejects(() => linkSkill({ source: missingSource, target, method: 'symlink' }), /does not exist/);
  assert.equal(await targetResolves(target), false, 'nothing must have been created at target');
});

test('linkSkill rejects a source that is a FILE, not a directory', async () => {
  const parent = await tempDir('cf-src-file-');
  const filePath = path.join(parent, 'not-a-dir');
  await writeFile(filePath, 'x', 'utf8');
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await assert.rejects(() => linkSkill({ source: filePath, target, method: 'copy' }), /not a directory/);
});

test('linkSkill rejects source === target', async () => {
  const same = path.join(await tempDir('cf-same-'), 'code-forge');
  await writeMarker(same);
  await assert.rejects(() => linkSkill({ source: same, target: same, method: 'symlink' }), /same path/);
});

test('linkSkill rejects a target that is INSIDE source (clearing target would delete part of source)', async () => {
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const target = path.join(source, 'nested-target');
  await assert.rejects(() => linkSkill({ source, target, method: 'copy' }), /inside source/);
  assert.equal(await readFile(path.join(source, 'MARKER'), 'utf8'), 'marker', 'source must be untouched');
});

test('linkSkill rejects a source that is INSIDE target', async () => {
  const target = await tempDir('cf-tgt-');
  const source = path.join(target, 'nested-source');
  await writeMarker(source);
  await assert.rejects(() => linkSkill({ source, target, method: 'copy' }), /inside target/);
});

test('install() replacing an existing target: a stale copy directory does not survive a re-install as a symlink', async () => {
  const installsFile = await freshInstallsFile();
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'copied');
  const target = path.join(await tempDir('cf-tgt-parent-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'copy', source: sourceA, target });
  assert.equal((await lstat(target)).isSymbolicLink(), false);

  const sourceB = await tempDir('cf-src-b-');
  await writeMarker(sourceB, 'linked');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceB, target });

  assert.equal((await lstat(target)).isSymbolicLink(), true);
  assert.equal(await readlink(target), sourceB);
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 1, 're-installing the same (harness, scope) must replace, not duplicate, the record');
  assert.equal(records[0].method, 'symlink');
});

test('install() with a BAD new source throws and leaves the PREVIOUS working install untouched', async () => {
  const installsFile = await freshInstallsFile();
  const goodSource = await tempDir('cf-src-good-');
  await writeMarker(goodSource, 'working');
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'copy', source: goodSource, target });
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'working');

  const missingSource = path.join(await tempDir('cf-missing-'), 'typo');
  await assert.rejects(
    () => install({ installsFile, harness: 'claude', scope: 'project', method: 'copy', source: missingSource, target }),
    /does not exist/,
  );

  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'working', 'the old working install must survive a bad re-install attempt');
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 1);
  assert.equal(records[0].source, goodSource, 'the ledger must still point at the OLD, working source');
});

// ── Acceptance: "`upgrade` re-points all 3 recorded links" ───────────────────

test('upgradeAll re-points ALL 3 recorded installs (mixed symlink/copy) at a new source', async () => {
  const installsFile = await freshInstallsFile();
  const sourceA = await tempDir('cf-src-a-');
  await writeMarker(sourceA, 'A');

  const target1 = path.join(await tempDir('cf-tgt1-'), 'code-forge');
  const target2 = path.join(await tempDir('cf-tgt2-'), 'code-forge');
  const target3 = path.join(await tempDir('cf-tgt3-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceA, target: target1 });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source: sourceA, target: target2 });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'copy', source: sourceA, target: target3 });

  const sourceB = await tempDir('cf-src-b-');
  await writeMarker(sourceB, 'B');

  const updated = await upgradeAll({ installsFile, source: sourceB });

  assert.equal(updated.length, 3, 'upgradeAll must return all 3 records');
  assert.equal(await readlink(target1), sourceB);
  assert.equal(await readlink(target2), sourceB);
  assert.equal((await lstat(target3)).isSymbolicLink(), false);
  assert.equal(await readFile(path.join(target3, 'MARKER'), 'utf8'), 'B');

  for (const record of updated) {
    assert.equal(record.source, sourceB);
  }
  const onDisk = await readInstalls(installsFile);
  assert.equal(onDisk.length, 3, 'upgrade must not lose or duplicate records');
});

test('upgradeAll with NO source re-applies each record\'s own previously-recorded source unchanged', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source, 'stable');
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target });

  const updated = await upgradeAll({ installsFile });
  assert.equal(updated.length, 1);
  assert.equal(updated[0].source, source);
  assert.equal(await readlink(target), source);
});

test('upgradeAll on an empty installs.json is a no-op that returns []', async () => {
  const installsFile = await freshInstallsFile();
  const updated = await upgradeAll({ installsFile, source: await tempDir('cf-src-') });
  assert.deepEqual(updated, []);
});

test('upgradeAll with a NONEXISTENT source throws and leaves all 3 existing targets resolving at their OLD source', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const target1 = path.join(await tempDir('cf-tgt1-'), 'code-forge');
  const target2 = path.join(await tempDir('cf-tgt2-'), 'code-forge');
  const target3 = path.join(await tempDir('cf-tgt3-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: target1 });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source, target: target2 });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'symlink', source, target: target3 });

  const missingSource = path.join(await tempDir('cf-missing-'), 'typo');
  await assert.rejects(() => upgradeAll({ installsFile, source: missingSource }), /does not exist/);

  assert.equal(await readlink(target1), source);
  assert.equal(await readlink(target2), source);
  assert.equal(await readlink(target3), source);
  const records = await readInstalls(installsFile);
  assert.equal(records.length, 3, 'no record may be lost or altered by a failed upgrade');
  assert.ok(records.every((r) => r.source === source));
});

// ── Acceptance: "`remove` deletes only the 3 recorded paths and leaves a 4th unrecorded file" ─

test('removeRecords deletes exactly the 3 recorded targets and leaves a 4th, UNRECORDED, path (a SIBLING of a recorded target) untouched — and never touches the shared source', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source, 'x');

  const target1Parent = await tempDir('cf-tgt1-');
  const target1 = path.join(target1Parent, 'code-forge');
  const target2 = path.join(await tempDir('cf-tgt2-'), 'code-forge');
  const target3 = path.join(await tempDir('cf-tgt3-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: target1 });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source, target: target2 });
  await install({ installsFile, harness: 'grok', scope: 'global', method: 'copy', source, target: target3 });

  // The 4th path: a real skill-shaped directory that was never recorded (e.g. someone dropped it
  // by hand, or a harness this test never installed through code-forge at all) — placed as a
  // SIBLING of target1, inside the SAME parent directory, so a bug that deleted target1's whole
  // parent (over-deletion, not just target1 itself) would also destroy it and be caught here.
  const unrecordedTarget = path.join(target1Parent, 'other-skill');
  await writeMarker(unrecordedTarget, 'manual');

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 3);
  assert.equal(await targetResolves(target1), false);
  assert.equal(await targetResolves(target2), false);
  assert.equal(await targetResolves(target3), false);
  assert.equal(await targetResolves(unrecordedTarget), true, 'the unrecorded 4th path (sibling of target1) must survive');
  assert.equal(await readFile(path.join(unrecordedTarget, 'MARKER'), 'utf8'), 'manual');

  // Two of the three targets are SYMLINKS to `source` — a bug that deleted what the link points
  // to (instead of the link itself) would still pass every assertion above.
  assert.equal(await targetResolves(source), true, 'the shared source directory must survive — only the LINKS were recorded for removal');
  assert.equal(await readFile(path.join(source, 'MARKER'), 'utf8'), 'x');

  assert.deepEqual(await readInstalls(installsFile), []);
});

test('removeRecords with a harness filter removes only that harness\'s record, leaving the other harness installed', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source, 'x');
  const targetClaude = path.join(await tempDir('cf-tgt-c-'), 'code-forge');
  const targetCodex = path.join(await tempDir('cf-tgt-x-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: targetClaude });
  await install({ installsFile, harness: 'codex', scope: 'project', method: 'symlink', source, target: targetCodex });

  const removed = await removeRecords({ installsFile, harness: 'claude' });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].harness, 'claude');
  assert.equal(await targetResolves(targetClaude), false);
  assert.equal(await targetResolves(targetCodex), true, 'codex must survive a claude-only remove');
  const remaining = await readInstalls(installsFile);
  assert.equal(remaining.length, 1);
  assert.equal(remaining[0].harness, 'codex');
});

test('removeRecords on an empty installs.json returns [] and touches nothing', async () => {
  const installsFile = await freshInstallsFile();
  assert.deepEqual(await removeRecords({ installsFile }), []);
});

// ── removeRecords refuses a catastrophic delete from a hand-tampered ledger ──

test('removeRecords refuses a tampered record whose target IS $HOME — $HOME survives, the record is KEPT (not silently dropped), and the other safe record still gets removed', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const safeTarget = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: safeTarget });

  // Hand-tamper the ledger: append a record whose target is the process's own $HOME (an absolute
  // path, so isValidRecord alone cannot catch it — only the deletion-safety guard can).
  const before = await readInstalls(installsFile);
  const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
    harness: 'codex',
    scope: 'global',
    method: 'symlink',
    source,
    target: os.homedir(),
    linked_at: 'x',
  });
  await writeInstallsAtomically(installsFile, [...before, dangerousRecord]);

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].harness, 'claude');
  assert.equal(await targetResolves(os.homedir()), true, '$HOME must survive');
  const remaining = await readInstalls(installsFile);
  assert.deepEqual(remaining, [dangerousRecord], 'the refused record must stay in the ledger, not be dropped');
});

test('removeRecords refuses a tampered record whose target is an ANCESTOR of its own source', async () => {
  const installsFile = await freshInstallsFile();
  const parent = await tempDir('cf-parent-');
  const nestedSource = path.join(parent, 'nested-source');
  await writeMarker(nestedSource);
  const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
    harness: 'grok',
    scope: 'global',
    method: 'copy',
    source: nestedSource,
    target: parent,
    linked_at: 'x',
  });
  await writeInstallsAtomically(installsFile, [dangerousRecord]);

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 0);
  assert.equal(await targetResolves(parent), true);
  assert.equal(await targetResolves(nestedSource), true, 'source nested under target must survive');
  assert.deepEqual(await readInstalls(installsFile), [dangerousRecord]);
});

// ── a record whose target IS its own source (canonically) is refused ──────────────────────────

/**
 * Writes one tampered record whose `target` names `source` through `targetSpelling`, runs
 * `removeRecords`, and returns what it removed plus the ledger afterwards.
 * @param {string} source
 * @param {string} targetSpelling
 */
async function removeTamperedSelfTarget(source, targetSpelling) {
  const installsFile = await freshInstallsFile();
  const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
    harness: 'gemini',
    scope: 'global',
    method: 'copy',
    source,
    target: targetSpelling,
    linked_at: 'x',
  });
  await writeInstallsAtomically(installsFile, [dangerousRecord]);
  const removed = await removeRecords({ installsFile });
  return { removed, remaining: await readInstalls(installsFile), dangerousRecord };
}

/** Whether the temp filesystem is case-insensitive (macOS APFS default) — probed, not assumed. */
async function tempFsIsCaseInsensitive() {
  const probe = await tempDir('cf-caseprobe-');
  return targetResolves(probe.toUpperCase());
}

test('removeRecords refuses a tampered record whose target IS its own source (same spelling) — the source and its MARKER survive, the record is kept', async () => {
  const source = path.join(await tempDir('cf-self-'), 'skill');
  const marker = await writeMarker(source, 'keep-me');

  const { removed, remaining, dangerousRecord } = await removeTamperedSelfTarget(source, source);

  assert.equal(removed.length, 0);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.deepEqual(remaining, [dangerousRecord]);
});

test('removeRecords refuses a tampered record whose target is its own source written in a DIFFERENT CASE — the source survives', async (t) => {
  if (!(await tempFsIsCaseInsensitive())) {
    t.skip('temp filesystem is case-sensitive: a differently-cased path is a different entry');
    return;
  }
  const source = path.join(await tempDir('cf-self-'), 'skill');
  const marker = await writeMarker(source, 'keep-me');
  const differentCase = source.toUpperCase();
  assert.notEqual(differentCase, source);

  const { removed, remaining, dangerousRecord } = await removeTamperedSelfTarget(source, differentCase);

  assert.equal(removed.length, 0);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.deepEqual(remaining, [dangerousRecord]);
});

test('removeRecords refuses a tampered target reached through a SYMLINKED PARENT that resolves to its own source — the source survives', async () => {
  const sourceParent = await tempDir('cf-self-');
  const source = path.join(sourceParent, 'skill');
  const marker = await writeMarker(source, 'keep-me');
  const alias = path.join(await tempDir('cf-alias-'), 'alias-to-source-parent');
  await symlink(sourceParent, alias, 'dir');

  const { removed, remaining, dangerousRecord } = await removeTamperedSelfTarget(source, path.join(alias, 'skill'));

  assert.equal(removed.length, 0);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.deepEqual(remaining, [dangerousRecord]);
});

test('linkSkill rejects a target that is the source written in a DIFFERENT CASE, before touching anything — the source survives', async (t) => {
  if (!(await tempFsIsCaseInsensitive())) {
    t.skip('temp filesystem is case-sensitive: a differently-cased path is a different entry');
    return;
  }
  const source = path.join(await tempDir('cf-self-'), 'skill');
  const marker = await writeMarker(source, 'keep-me');

  await assert.rejects(linkSkill({ source, target: source.toUpperCase(), method: 'copy' }), /must not be the same path/);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.equal((await lstat(source)).isDirectory(), true);
});

test('linkSkill rejects a target reached through a SYMLINKED PARENT that resolves to the source — the source survives', async () => {
  const sourceParent = await tempDir('cf-self-');
  const source = path.join(sourceParent, 'skill');
  const marker = await writeMarker(source, 'keep-me');
  const alias = path.join(await tempDir('cf-alias-'), 'alias-to-source-parent');
  await symlink(sourceParent, alias, 'dir');

  await assert.rejects(linkSkill({ source, target: path.join(alias, 'skill'), method: 'symlink' }), /must not be the same path/);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.equal((await lstat(source)).isDirectory(), true);
});

// ── the SOURCE itself is a symlink (npm global installs): both its forms are protected ─────────

/**
 * A real skill directory (with a MARKER) and a symlink pointing at it — the shape an npm global
 * install leaves behind, where the path handed to `install` is the LINK, not the real directory.
 * @returns {Promise<{realDir: string, marker: string, sourceLink: string}>}
 */
async function symlinkedSource() {
  const realDir = path.join(await tempDir('cf-real-'), 'skill');
  const marker = await writeMarker(realDir, 'keep-me');
  const sourceLink = path.join(await tempDir('cf-link-'), 'skill');
  await symlink(realDir, sourceLink, 'dir');
  return { realDir, marker, sourceLink };
}

test('install() refuses a target spelled EXACTLY like a symlinked source — the source link, the real directory and its MARKER all survive, and nothing is recorded', async () => {
  const installsFile = await freshInstallsFile();
  const { realDir, marker, sourceLink } = await symlinkedSource();

  await assert.rejects(
    install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceLink, target: sourceLink }),
    /must not be the same path/,
  );

  assert.equal((await lstat(sourceLink)).isSymbolicLink(), true, 'the source link must not have been removed');
  assert.equal(await readlink(sourceLink), realDir);
  assert.equal((await lstat(realDir)).isDirectory(), true);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.deepEqual(await readInstalls(installsFile), [], 'a refused install must not be recorded');
});

test('linkSkill refuses a target that is the REAL directory a symlinked source resolves to — the real directory and its MARKER survive', async () => {
  const { realDir, marker, sourceLink } = await symlinkedSource();

  await assert.rejects(linkSkill({ source: sourceLink, target: realDir, method: 'copy' }), /must not be the same path/);

  assert.equal((await lstat(realDir)).isDirectory(), true);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.equal((await lstat(sourceLink)).isSymbolicLink(), true);
});

test('removeRecords refuses a tampered record whose target is spelled exactly like its own SYMLINKED source — the link and the real directory survive, the record is kept', async () => {
  const { realDir, marker, sourceLink } = await symlinkedSource();

  const { removed, remaining, dangerousRecord } = await removeTamperedSelfTarget(sourceLink, sourceLink);

  assert.equal(removed.length, 0);
  assert.equal((await lstat(sourceLink)).isSymbolicLink(), true, 'the source link must not have been removed');
  assert.equal((await lstat(realDir)).isDirectory(), true);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me');
  assert.deepEqual(remaining, [dangerousRecord]);
});

test('a symlinked source still installs to a NORMAL target and removes cleanly — protecting both source forms must not over-refuse the npm-global case', async () => {
  const installsFile = await freshInstallsFile();
  const { realDir, marker, sourceLink } = await symlinkedSource();
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');

  const records = await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source: sourceLink, target });

  assert.equal(records.length, 1);
  assert.equal(await readlink(target), sourceLink, 'the install links to the source AS GIVEN, not to what it resolves to');
  assert.equal(await readFile(path.join(target, 'MARKER'), 'utf8'), 'keep-me');

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].target, target);
  assert.equal(await isSymlink(target), false, 'the target link must be gone');
  assert.equal((await lstat(sourceLink)).isSymbolicLink(), true, 'removing the target must not touch the source link');
  assert.equal(await readlink(sourceLink), realDir);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me', 'removing the target must not touch the real directory');
  assert.deepEqual(await readInstalls(installsFile), []);
});

// ── removeRecords canonicalizes paths before comparing (case-insensitive FS, symlinked parents) ─

test('removeRecords refuses a tampered target that is $HOME written in a DIFFERENT CASE — macOS APFS is case-insensitive (same on-disk entry, different string); $HOME survives and the record is kept', async (t) => {
  if (!(await tempFsIsCaseInsensitive())) {
    t.skip('filesystem is case-sensitive (e.g. Linux CI): a differently-cased $HOME is a different, absent path');
    return;
  }
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const safeTarget = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: safeTarget });

  const differentCaseHome = os.homedir().toUpperCase();
  assert.notEqual(differentCaseHome, os.homedir(), 'precondition: the fixture $HOME must contain a lowercase letter for this test to prove anything');

  const before = await readInstalls(installsFile);
  const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
    harness: 'codex',
    scope: 'global',
    method: 'symlink',
    source,
    target: differentCaseHome,
    linked_at: 'x',
  });
  await writeInstallsAtomically(installsFile, [...before, dangerousRecord]);

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].harness, 'claude');
  assert.equal(await targetResolves(os.homedir()), true, '$HOME must survive a differently-cased tampered target');
  const remaining = await readInstalls(installsFile);
  assert.deepEqual(remaining, [dangerousRecord], 'the refused record must stay in the ledger, not be dropped');
});

test('removeRecords refuses a target reached through a SYMLINKED PARENT that resolves to $HOME — a plain string comparison would miss this', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  await writeMarker(source);
  const safeTarget = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: safeTarget });

  // A symlink whose destination is $HOME's own PARENT directory, combined with a basename equal
  // to $HOME's own folder name, reconstructs $HOME exactly once the parent is resolved — even
  // though the literal target string never mentions $HOME's real path.
  const aliasParentDir = await tempDir('cf-alias-');
  const homeParentAlias = path.join(aliasParentDir, 'alias-to-home-parent');
  await symlink(path.dirname(os.homedir()), homeParentAlias, 'dir');
  const dangerousTarget = path.join(homeParentAlias, path.basename(os.homedir()));

  const before = await readInstalls(installsFile);
  const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
    harness: 'grok',
    scope: 'global',
    method: 'symlink',
    source,
    target: dangerousTarget,
    linked_at: 'x',
  });
  await writeInstallsAtomically(installsFile, [...before, dangerousRecord]);

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].harness, 'claude');
  assert.equal(await targetResolves(os.homedir()), true, '$HOME must survive a target reached through a symlinked parent');
  const remaining = await readInstalls(installsFile);
  assert.deepEqual(remaining, [dangerousRecord]);
});

test('removeRecords refuses a tampered target spelled exactly like a $HOME that is itself a SYMLINK — the link and the real home directory survive, the record is kept, and a normal remove still works', async () => {
  // A fake $HOME that is a symlink to a real directory, both under the helper's temp root — the
  // process's $HOME is swapped to the LINK for this test only and restored whatever happens.
  const realHome = await tempDir('cf-realhome-');
  const homeMarker = await writeMarker(realHome, 'home-keep-me');
  const homeLink = path.join(await tempDir('cf-homelink-'), 'home');
  await symlink(realHome, homeLink, 'dir');
  const previousHome = process.env.HOME;
  process.env.HOME = homeLink;
  try {
    assert.equal(os.homedir(), homeLink, 'precondition: the process must see the LINK as $HOME');

    const installsFile = await freshInstallsFile();
    const source = await tempDir('cf-src-');
    await writeMarker(source);
    const safeTarget = path.join(await tempDir('cf-tgt-'), 'code-forge');
    await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target: safeTarget });

    const before = await readInstalls(installsFile);
    const dangerousRecord = /** @type {import('../../src/install/link.mjs').InstallRecord} */ ({
      harness: 'codex',
      scope: 'global',
      method: 'symlink',
      source,
      target: homeLink,
      linked_at: 'x',
    });
    await writeInstallsAtomically(installsFile, [...before, dangerousRecord]);

    const removed = await removeRecords({ installsFile });

    assert.equal(removed.length, 1);
    assert.equal(removed[0].harness, 'claude');
    assert.equal(await targetResolves(safeTarget), false, 'the normal record must still be removed');
    assert.equal((await lstat(homeLink)).isSymbolicLink(), true, 'the $HOME link must not have been removed');
    assert.equal(await readlink(homeLink), realHome);
    assert.equal(await readFile(homeMarker, 'utf8'), 'home-keep-me', 'the real home directory must survive');
    assert.deepEqual(await readInstalls(installsFile), [dangerousRecord], 'the refused record must stay in the ledger');
  } finally {
    process.env.HOME = previousHome;
  }
});

test('a NORMAL, lowercase-named skill directory still removes cleanly — the canonicalization fix must not over-refuse a legitimate install', async () => {
  const installsFile = await freshInstallsFile();
  const source = await tempDir('cf-src-');
  const marker = await writeMarker(source, 'keep-me');
  const target = path.join(await tempDir('cf-tgt-'), 'code-forge');
  await install({ installsFile, harness: 'claude', scope: 'project', method: 'symlink', source, target });

  const removed = await removeRecords({ installsFile });

  assert.equal(removed.length, 1);
  assert.equal(removed[0].target, target);
  assert.equal(await targetResolves(target), false);
  assert.equal(await readFile(marker, 'utf8'), 'keep-me', 'removing the link must not touch its source');
  assert.deepEqual(await readInstalls(installsFile), []);
});

// ── readInstalls: tolerant read ───────────────────────────────────────────────

test('readInstalls on a missing file returns []', async () => {
  const file = await freshInstallsFile();
  assert.deepEqual(await readInstalls(file), []);
});

test('readInstalls on a corrupt (non-JSON) file returns [] rather than throwing', async () => {
  const file = await freshInstallsFile();
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, 'not json{{{', 'utf8');
  assert.deepEqual(await readInstalls(file), []);
});

test('readInstalls drops a record missing a required field, but keeps a valid sibling record', async () => {
  const file = await freshInstallsFile();
  await mkdir(path.dirname(file), { recursive: true });
  const good = { harness: 'claude', scope: 'project', method: 'symlink', source: '/a', target: '/b', linked_at: '2026-01-01T00:00:00.000Z' };
  const bad = { harness: 'codex', scope: 'project', method: 'symlink', source: '/a', target: '/b' }; // missing linked_at
  await writeFile(file, JSON.stringify({ version: 1, installs: [good, bad] }), 'utf8');
  assert.deepEqual(await readInstalls(file), [good]);
});

test('readInstalls rejects an invalid "scope"/"method" value, dropping just that record', async () => {
  const file = await freshInstallsFile();
  await mkdir(path.dirname(file), { recursive: true });
  const good = { harness: 'claude', scope: 'project', method: 'copy', source: '/a', target: '/b', linked_at: 'x' };
  const badScope = { harness: 'grok', scope: 'nowhere', method: 'copy', source: '/a', target: '/b', linked_at: 'x' };
  const badMethod = { harness: 'gemini', scope: 'global', method: 'teleport', source: '/a', target: '/b', linked_at: 'x' };
  await writeFile(file, JSON.stringify({ version: 1, installs: [good, badScope, badMethod] }), 'utf8');
  assert.deepEqual(await readInstalls(file), [good]);
});

test('readInstalls drops a record whose "target" or "source" is a RELATIVE path — never trusted, never resolved against an unknown cwd', async () => {
  const file = await freshInstallsFile();
  await mkdir(path.dirname(file), { recursive: true });
  const good = { harness: 'claude', scope: 'project', method: 'copy', source: '/a', target: '/b', linked_at: 'x' };
  const relativeTarget = { harness: 'codex', scope: 'project', method: 'copy', source: '/a', target: 'relative/path', linked_at: 'x' };
  const relativeSource = { harness: 'grok', scope: 'global', method: 'copy', source: 'relative/path', target: '/c', linked_at: 'x' };
  await writeFile(file, JSON.stringify({ version: 1, installs: [good, relativeTarget, relativeSource] }), 'utf8');
  assert.deepEqual(await readInstalls(file), [good]);
});

test('readInstalls also accepts a bare top-level array (no {version, installs} wrapper)', async () => {
  const file = await freshInstallsFile();
  await mkdir(path.dirname(file), { recursive: true });
  const good = { harness: 'claude', scope: 'project', method: 'copy', source: '/a', target: '/b', linked_at: 'x' };
  await writeFile(file, JSON.stringify([good]), 'utf8');
  assert.deepEqual(await readInstalls(file), [good]);
});

// ── writeInstallsAtomically ───────────────────────────────────────────────────

test('a successful write leaves EXACTLY installs.json in its directory — no stray temp file', async () => {
  const file = await freshInstallsFile();
  await writeInstallsAtomically(file, []);
  assert.deepEqual(await readdir(path.dirname(file)), ['installs.json']);
});

// ── targetResolves / isSymlink ────────────────────────────────────────────────

test('targetResolves is true for an existing plain directory, false for a missing path', async () => {
  const dir = await tempDir('cf-exists-');
  assert.equal(await targetResolves(dir), true);
  assert.equal(await targetResolves(path.join(dir, 'nope')), false);
});

test('targetResolves is false for a BROKEN symlink (source removed) — a dangling link does not "resolve"', async () => {
  const parent = await tempDir('cf-broken-');
  const missingSource = path.join(parent, 'gone');
  const target = path.join(parent, 'link');
  await symlink(missingSource, target, 'dir');
  assert.equal(await isSymlink(target), true);
  assert.equal(await targetResolves(target), false);
});

test('isSymlink is false for a real directory and for a missing path', async () => {
  const dir = await tempDir('cf-real-');
  assert.equal(await isSymlink(dir), false);
  assert.equal(await isSymlink(path.join(dir, 'nope')), false);
});
