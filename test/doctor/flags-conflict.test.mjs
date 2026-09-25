/**
 * Doctor's static Codex coder flag-conflict check (B4.2): Codex 0.155.1 refuses `-s/--sandbox`
 * together with `--approve-for-me`, so the `flags.codex` row FAILs when the built coder argv holds
 * both. The CLI answering `--help` is the doctor fake-bin wrapper (pinned fixture), never a real Codex.
 */

import { fakeBin, testPath } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { coderFlagArgv } from '../../src/engines/builders/codex.mjs';
import { codexCoderFlagConflict, probeCli } from '../../src/doctor/probes.mjs';
import { CLAUDE_HELP } from './helpers.mjs';

const CONFLICT = '-s/--sandbox cannot be used with --approve-for-me';

test('codexCoderFlagConflict: the real coder argv is clear; -s or --sandbox[=] next to --approve-for-me is the conflict', () => {
  const real = coderFlagArgv({ model: 'm', cwd: '/w', outPath: '/o.json', promptPath: '/b.md' });
  assert.equal(real.filter((a) => a === '-s').length, 1);
  assert.equal(real.includes('--approve-for-me'), false);
  assert.equal(codexCoderFlagConflict(real), null);
  assert.equal(codexCoderFlagConflict(['codex', 'exec', '-s', 'workspace-write', '--approve-for-me']), CONFLICT);
  assert.equal(codexCoderFlagConflict(['codex', 'exec', '--sandbox', 'workspace-write', '--approve-for-me']), CONFLICT);
  assert.equal(codexCoderFlagConflict(['codex', 'exec', '--sandbox=workspace-write', '--approve-for-me']), CONFLICT);
  assert.equal(codexCoderFlagConflict(['codex', 'exec', '--approve-for-me']), null);
  assert.equal(codexCoderFlagConflict(['codex', 'exec', '-s', 'workspace-write']), null);
});

test('probeCli(codex): flags.codex is OK for the real coder argv and FAILs naming the conflict for a -s + --approve-for-me argv', async () => {
  const bin = fakeBin(CLAUDE_HELP);
  const ctx = /** @type {any} */ ({ env: { PATH: testPath(bin) } });
  const ok = await probeCli(ctx, 'codex');
  assert.equal(ok.present, true);
  assert.deepEqual(ok.rows.map((r) => [r.id, r.status]), [['cli.codex', 'OK'], ['flags.codex', 'OK']]);
  const bad = await probeCli(ctx, 'codex', { codexCoderArgv: () => ['codex', 'exec', '-s', 'workspace-write', '--approve-for-me', '-C', '/w', '/b.md'] });
  assert.deepEqual(bad.rows.map((r) => [r.id, r.status, r.detail]), [
    ['cli.codex', 'OK', 'present, codex-cli 0.155.1'],
    ['flags.codex', 'FAIL', `coder argv conflict: ${CONFLICT}`],
  ]);
});
