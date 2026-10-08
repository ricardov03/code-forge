/**
 * B57: `pathKind`/`ownedKinds` — what each exact owned entry is on disk. Only a regular file (by
 * `lstat`, never through a symlink) is `file`; everything else stays directory-like. No process.
 */
// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync, realpathSync, symlinkSync, writeFileSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { ownedKinds, pathKind } from '../../src/state/owned-kinds.mjs';

let ROOT = '';

before(async () => {
  ROOT = realpathSync(await mkdtemp(path.join(os.tmpdir(), 'code-forge-kinds-')));
  mkdirSync(path.join(ROOT, 'src', 'feature'), { recursive: true });
  writeFileSync(path.join(ROOT, 'src', 'README'), 'r\n');
  writeFileSync(path.join(ROOT, 'src', 'feature', 'a.swift'), 'a\n');
  symlinkSync(path.join(ROOT, 'src', 'feature'), path.join(ROOT, 'src', 'link-dir'));
  symlinkSync(path.join(ROOT, 'src', 'README'), path.join(ROOT, 'src', 'link-file'));
});

after(async () => {
  if (ROOT) await rm(ROOT, { recursive: true, force: true });
});

test('pathKind: a regular file is `file`, a directory `dir`, a missing path (or one below a file) `absent`, a symlink `other`', () => {
  const kinds = ['src/README', 'src/feature/a.swift', 'src/feature', 'src', 'src/new.txt', 'src/README/x', 'src/link-dir', 'src/link-file'].map((p) => pathKind(ROOT, p));
  assert.deepEqual(kinds, ['file', 'file', 'dir', 'dir', 'absent', 'absent', 'other', 'other']);
});

test('ownedKinds: one kind per exact entry, none for a glob or a brace entry; an entry named `__proto__` is an ordinary key', () => {
  const kinds = ownedKinds(ROOT, ['src/README', 'src/feature', 'src/**/*.mjs', 'src/{a,b}', 'missing', '__proto__']);
  assert.deepEqual({ ...kinds }, { 'src/README': 'file', 'src/feature': 'dir', missing: 'absent', ['__proto__']: 'absent' });
  assert.deepEqual(Object.keys(kinds), ['src/README', 'src/feature', 'missing', '__proto__']);
});
