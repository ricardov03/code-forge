/**
 * Doctor's Codex rules row (B4.1): OK when the Codex coder build — run exactly as the session
 * runner runs it — wires the forbidden list as execpolicy rules under a per-session CODEX_HOME;
 * WARN `codex: forbidden list is prose-only` when it does not. No CLI is spawned, and nothing is
 * written under `os.homedir()`: the "real" Codex home is a fake one under this file's temp dir.
 */

import { freshDir } from './helpers.mjs';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { buildCodexArgv } from '../../src/engines/builders/codex.mjs';
import { removeCodexHome, renderCodexRules } from '../../src/engines/codex-home.mjs';
import { probeCodexRules } from '../../src/doctor/probes.mjs';
import { FORBIDDEN, renderForCodex } from '../../src/util/forbidden.mjs';

const PROSE_ONLY = { id: 'codex-rules', status: 'WARN', label: 'codex rules', detail: 'codex: forbidden list is prose-only' };

/** The exact rules file the builder writes for the default forbidden list (fix round 4). */
const FULL_RULES = renderCodexRules(renderForCodex(FORBIDDEN));

/** Write the FULL rendered rules file, read-only, into `<home>/rules/`, as the builder would. */
function writeFullRules(home) {
  mkdirSync(path.join(home, 'rules'), { recursive: true });
  const file = path.join(home, 'rules', 'code-forge.rules');
  writeFileSync(file, FULL_RULES.content, { mode: 0o600 });
  chmodSync(file, 0o444);
  return file;
}

/** @param {any} r */
const shape = (r) => ({ id: r.id, status: r.status, label: r.label, detail: r.detail });

/** A fake user home under the test temp dir, with its own `.codex`. */
function fakeUser(name) {
  const home = freshDir(name);
  const realCodex = path.join(home, '.codex');
  mkdirSync(path.join(realCodex, 'rules'), { recursive: true });
  return { home, realCodex, env: { HOME: home, PATH: '' } };
}

test('the Codex rules row is OK for the runner-default build: 46 rules under a per-session CODEX_HOME; the probe home is removed', () => {
  const { env } = fakeUser('codex-rules-ok-user');
  /** @type {string[]} */
  const homes = [];
  const spy = (/** @type {any} */ p) => {
    assert.equal('codexHome' in p, false); // the runner's own default path, never a probe-chosen home
    const built = /** @type {any} */ (buildCodexArgv(p));
    homes.push(built.env.CODEX_HOME);
    return built;
  };
  const rows = probeCodexRules(/** @type {any} */ ({ workDir: freshDir('codex-rules-ok'), env }), { build: /** @type {any} */ (spy) });
  assert.equal(rows.length, 1);
  assert.deepEqual([rows[0].id, rows[0].status, rows[0].detail], ['codex-rules', 'OK', '46 execpolicy rules via CODEX_HOME=<session>/rules/code-forge.rules']);
  assert.equal(homes.length, 1);
  assert.equal(existsSync(homes[0]), false);
});

test('the row WARNs prose-only when the build has no CODEX_HOME, throws, has an empty rules file, or lacks the tmp exclusions', () => {
  const { env } = fakeUser('codex-rules-warn-user');
  const workDir = freshDir('codex-rules-warn');
  const emptyHome = path.join(workDir, 'empty-home');
  mkdirSync(path.join(emptyHome, 'rules'), { recursive: true });
  writeFileSync(path.join(emptyHome, 'rules', 'code-forge.rules'), '# nothing\n');
  const builds = [
    () => ({ argv: ['codex', 'exec'] }),
    () => {
      throw new Error('boom');
    },
    () => ({ argv: [], env: { CODEX_HOME: emptyHome } }),
    (/** @type {any} */ p) => ({ ...buildCodexArgv(p), argv: ['codex', 'exec', '-s', 'workspace-write'] }),
  ];
  const rows = builds.flatMap((build) => probeCodexRules(/** @type {any} */ ({ workDir, env }), { build: /** @type {any} */ (build) }));
  assert.equal(rows.length, 4);
  for (const r of rows) assert.deepEqual(shape(r), PROSE_ONLY);
  // a home outside <run root>/codex-homes/ is never deleted (fix round 3)
  assert.equal(existsSync(path.join(emptyHome, 'rules', 'code-forge.rules')), true);
});

test('a build pointing at the REAL Codex home, holding the full read-only rules file and the full argv, WARNs and the real home is untouched', () => {
  const { realCodex, env } = fakeUser('codex-rules-real-user');
  const workDir = freshDir('codex-rules-real');
  const realRules = writeFullRules(realCodex);
  assert.equal(statSync(realRules).mode & 0o777, 0o444);
  /** @type {string[]} */
  const defaults = [];
  // a real build (full argv, tmp exclusions) whose home is then the real home: that is the only thing wrong with it
  const build = (/** @type {any} */ p) => {
    const built = /** @type {any} */ (buildCodexArgv(p));
    defaults.push(built.env.CODEX_HOME);
    return { ...built, env: { CODEX_HOME: realCodex } };
  };
  const rows = probeCodexRules(/** @type {any} */ ({ workDir, env }), { build: /** @type {any} */ (build) });
  assert.equal(rows.length, 1);
  assert.deepEqual(shape(rows[0]), PROSE_ONLY);
  assert.equal(existsSync(realRules), true);
  assert.equal(statSync(realRules).size, FULL_RULES.content.length);
  assert.equal(existsSync(path.join(realCodex, 'rules')), true);
  assert.equal(defaults.length, 1);
  removeCodexHome(defaults[0]); // the probe deleted nothing; the test tidies its own confined home
});

test('a build whose CODEX_HOME is outside <run root>/codex-homes/ (the work dir, holding the full read-only rules file) WARNs and the dir is not deleted', () => {
  const { env } = fakeUser('codex-rules-outside-user');
  const workDir = freshDir('codex-rules-outside');
  const rules = writeFullRules(workDir);
  assert.equal(statSync(rules).mode & 0o777, 0o444);
  /** @type {string[]} */
  const defaults = [];
  // a real build (full argv, tmp exclusions) whose home is then pointed at the work dir: the only
  // thing wrong with it is where the home lives.
  const build = (/** @type {any} */ p) => {
    const built = /** @type {any} */ (buildCodexArgv(p));
    defaults.push(built.env.CODEX_HOME);
    return { ...built, env: { CODEX_HOME: p.cwd } };
  };
  const rows = probeCodexRules(/** @type {any} */ ({ workDir, env }), { build: /** @type {any} */ (build) });
  assert.equal(rows.length, 1);
  assert.deepEqual(shape(rows[0]), PROSE_ONLY);
  assert.equal(existsSync(workDir), true);
  assert.equal(existsSync(rules), true);
  assert.equal(statSync(rules).size, FULL_RULES.content.length);
  assert.equal(defaults.length, 1);
  assert.equal(existsSync(defaults[0]), true); // the probe deleted nothing at all
  removeCodexHome(defaults[0]);
});

test('a confined home whose rules file has the wrong count, or a write bit, WARNs (and is removed); the exact count with mode 0444 is OK', () => {
  const { env } = fakeUser('codex-rules-count-user');
  const workDir = freshDir('codex-rules-count');
  /** @type {string[]} */
  const homes = [];
  const oneShort = FULL_RULES.content.split('\n').filter((l) => l.startsWith('prefix_rule(')).slice(0, -1).join('\n') + '\n';
  const builds = [
    (/** @type {any} */ p) => {
      const built = /** @type {any} */ (buildCodexArgv(p));
      homes.push(built.env.CODEX_HOME);
      chmodSync(built.rulesFile.path, 0o600);
      writeFileSync(built.rulesFile.path, oneShort);
      chmodSync(built.rulesFile.path, 0o444);
      return built;
    },
    (/** @type {any} */ p) => {
      const built = /** @type {any} */ (buildCodexArgv(p));
      homes.push(built.env.CODEX_HOME);
      chmodSync(built.rulesFile.path, 0o644);
      return built;
    },
  ];
  const rows = builds.flatMap((build) => probeCodexRules(/** @type {any} */ ({ workDir, env }), { build: /** @type {any} */ (build) }));
  assert.equal(rows.length, 2);
  for (const r of rows) assert.deepEqual(shape(r), PROSE_ONLY);
  assert.equal(homes.length, 2);
  assert.equal(homes.filter((h) => existsSync(h)).length, 0);
  const ok = probeCodexRules(/** @type {any} */ ({ workDir, env }), {});
  assert.equal(ok.length, 1);
  assert.deepEqual([ok[0].status, ok[0].detail], ['OK', `${FULL_RULES.count} execpolicy rules via CODEX_HOME=<session>/rules/code-forge.rules`]);
});
