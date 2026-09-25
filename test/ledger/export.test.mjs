import assert from 'node:assert/strict';
import { access, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { runReport } from '../../src/cli/report.mjs';
import { exportReportSections } from '../../src/ledger/export.mjs';
import { buildReport, SECTION_NAMES } from '../../src/ledger/report.mjs';
import { appendRow } from '../../src/ledger/write.mjs';
import { captureStream, withTempHome } from './helpers.mjs';

test('exportReportSections writes exactly the SECTION_NAMES files (13), each byte-for-byte round-tripping its section data', async () => {
  await withTempHome(async () => {
    const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-export-'));
    try {
      const { sections } = buildReport([{ event: 'dispatch', block: 'b1', level: 'L1', lane: 'L1' }]);
      assert.equal(Object.keys(sections).length, 13);

      const paths = await exportReportSections(dir, sections);

      const expectedFiles = SECTION_NAMES.map((k) => `${k}.json`).sort();
      const actualFiles = (await readdir(dir)).sort();
      assert.deepEqual(actualFiles, expectedFiles);

      const expectedPaths = SECTION_NAMES.map((k) => path.join(dir, `${k}.json`)).sort();
      assert.deepEqual([...paths].sort(), expectedPaths);

      for (const key of SECTION_NAMES) {
        const parsed = JSON.parse(await readFile(path.join(dir, `${key}.json`), 'utf8'));
        assert.deepEqual(parsed, sections[key], `section "${key}" did not round-trip`);
      }
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
});

test('exportReportSections rejects an unsafe section key (path traversal) and writes NOTHING — no evil.json escapes dir, and dir itself stays empty', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-export-bad-'));
  try {
    await assert.rejects(() => exportReportSections(dir, { '../evil': [] }), /unsafe section key/);
    await assert.rejects(() => access(path.join(path.dirname(dir), 'evil.json')), 'the path-traversal write must never have happened');
    assert.deepEqual(await readdir(dir), []);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test('exportReportSections rejects keys that collide case-insensitively and writes NOTHING (the collision check runs before any write)', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-export-collide-'));
  try {
    await assert.rejects(() => exportReportSections(dir, { foo_bar: [], Foo_Bar: [] }), /collides/);
    assert.deepEqual(await readdir(dir), [], 'no file should have been written before the collision was detected');
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test('exportReportSections rejects a section whose data is undefined and writes NOTHING — not even the valid sections before it (atomic against validation failures)', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-export-undef-'));
  try {
    await assert.rejects(() => exportReportSections(dir, { a: [1], b: undefined, c: [3] }), /undefined data/);
    assert.deepEqual(await readdir(dir), [], 'a.json must NOT have been written before the loop reached the undefined b — validation runs fully up front now');
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test('CLI-level: report --export <dir> actually wires up exportReportSections and writes EXACTLY the SECTION_NAMES filenames — nothing extra, nothing renamed', async () => {
  await withTempHome(async () => {
    await appendRow({ event: 'dispatch', block: 'b1', level: 'L1', lane: 'L0' }, { slug: 'export-cli-proj' });
    const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-export-cli-'));
    try {
      const stdout = captureStream();
      const code = await runReport(['--slug', 'export-cli-proj', '--export', dir], { stdout });
      assert.equal(code, 0);
      const expected = SECTION_NAMES.map((k) => `${k}.json`).sort();
      assert.deepEqual((await readdir(dir)).sort(), expected);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  });
});
