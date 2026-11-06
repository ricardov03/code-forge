import './support.mjs';
import assert from 'node:assert/strict';
import { chmod, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { changedScope, fileBaseline, testScope } from '../../src/gates/expand.mjs';
import { runGates } from '../../src/gates/run.mjs';
import { git, withTempDir } from './support.mjs';

const GO_TEST = 'engine/pkg/x_test.go';
const GO_SOURCE = 'engine/pkg/x.go';
const BRIDGE = 'tests/integration/native-suite.test.mjs';
const NODE_TEST = 'test/local.test.mjs';

/** @param {string} cwd @param {string} file @param {string} text */
async function put(cwd, file, text) {
  await mkdir(path.dirname(path.join(cwd, file)), { recursive: true });
  await writeFile(path.join(cwd, file), text);
}

/** A real shell wrapper and Node runner, with a deterministic Go executable double.
 * @param {string} cwd @param {boolean} bridge
 */
async function fixture(cwd, bridge) {
  await put(cwd, 'package.json', JSON.stringify({ type: 'module', scripts: { test: 'sh suite.sh' } }));
  await put(cwd, 'suite.sh', `#!/bin/sh\nunset NODE_TEST_CONTEXT CODE_FORGE_RUN_ID CODE_FORGE_OWNER_PID CODE_FORGE_OWNER_START_TIME CODE_FORGE_RUN CODE_FORGE_BLOCK\nif [ "$#" -eq 0 ]; then\n  echo FULL-SUITE\n  set -- '${NODE_TEST}' ${bridge ? `'${BRIDGE}'` : ''}\nfi\nexec '${process.execPath}' --test "$@"\n`);
  await put(cwd, NODE_TEST, "console.log('LOCAL-TEST');\n");
  await put(cwd, GO_TEST, 'package pkg\n// test fixture\n');
  await put(cwd, GO_SOURCE, 'package pkg\n');
  await put(cwd, 'engine/go.mod', 'module fixture\ngo 1.22\n');
  await put(cwd, 'fake-bin/go', `#!${process.execPath}
import assert from 'node:assert/strict';
assert.deepEqual(process.argv.slice(2), ['test', '-count=1', './...']);
console.log('GO-SUITE');
`);
  await chmod(path.join(cwd, 'fake-bin/go'), 0o755);
  if (bridge) await put(cwd, BRIDGE, `import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const result = spawnSync('go', ['test', '-count=1', './...'], {
  cwd: fileURLToPath(new URL('../../engine/', import.meta.url)), encoding: 'utf8',
});
assert.equal(result.status, 0, result.stderr);
console.log(result.stdout);
`);
  // Neither comments nor strings constitute a suite runner.
  await put(cwd, 'test/decoy.test.mjs', `// spawnSync('go', ['test', './...'], { cwd: new URL('../engine/', import.meta.url) });
const example = "spawnSync('go', ['test', './...'], { cwd: new URL('../engine/', import.meta.url) })";
`);
  git(['init', '-q'], cwd);
  git(['add', '-A'], cwd);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture'], cwd);
}

/** Exercise the same baseline → changed scope → gate runner path as gates run --changed.
 * @param {string} cwd @param {string[]} changed @param {boolean} [baselineKnown]
 */
async function changedGate(cwd, changed, baselineKnown = true) {
  const before = await fileBaseline(cwd, changed);
  for (const file of changed) await put(cwd, file, (await readFile(path.join(cwd, file), 'utf8')) + '\n// changed\n');
  const current = await fileBaseline(cwd, changed);
  const scope = changedScope({ baseline: baselineKnown ? before.files : null, current,
    fallback: testScope({ owned: changed }), owned: changed });
  return (await runGates({ test: { argv: ['sh', 'suite.sh', '{test_files}'],
    env: { PATH: `${path.join(cwd, 'fake-bin')}:${process.env.PATH}` } }, rerun_failed: 0 }, { cwd, scope })).results[0];
}

test('changed Node scope routes Go tests and sources through their suite wrapper and keeps Node selection', async () => {
  /** @type {[string[], string[], boolean][]} */
  const cases = [
    [[GO_TEST, GO_SOURCE], [BRIDGE], true],
    [[GO_TEST], [BRIDGE], true],
    [[GO_SOURCE], [BRIDGE], true],
    [[NODE_TEST], [NODE_TEST], true],
    [[GO_TEST, GO_SOURCE, NODE_TEST], [NODE_TEST, BRIDGE], true],
    [[GO_TEST, GO_SOURCE], [BRIDGE], false],
  ];
  for (const [changed, expected, baselineKnown] of cases) await withTempDir(async (cwd) => {
    await fixture(cwd, true);
    const result = await changedGate(cwd, changed, baselineKnown);
    assert.deepEqual(result.argv, ['sh', 'suite.sh', ...expected], `${changed}: runner receives exactly the covering Node tests`);
    assert.equal(result.ok, true, result.stderr);
    assert.equal(result.scope, 'changed');
    assert.equal(result.test_count, expected.length);
    assert.equal((result.stdout.match(/GO-SUITE/g) ?? []).length, expected.includes(BRIDGE) ? 1 : 0, 'Go suite invocation count');
    assert.equal((result.stdout.match(/LOCAL-TEST/g) ?? []).length, expected.includes(NODE_TEST) ? 1 : 0, 'Node test invocation count');
    assert.equal((result.stdout.match(/FULL-SUITE/g) ?? []).length, 0, 'a covered Go block must avoid the full npm suite');
  });
});

test('uncovered foreign tests force a full suite with a visible reason, including mixed changes', async () => {
  for (const foreign of [GO_TEST, 'other/UserTest.php', 'other/test_x.py']) {
    await withTempDir(async (cwd) => {
      await fixture(cwd, false);
      if (foreign !== GO_TEST) await put(cwd, foreign, '// fixture\n');
      const result = await changedGate(cwd, [foreign, NODE_TEST]);
      assert.deepEqual(result.argv, ['sh', 'suite.sh'], `${foreign}: no foreign path reaches Node, and mixed changes cannot hide missing coverage`);
      assert.equal(result.ok, true, result.stderr);
      assert.deepEqual([result.scope, result.reason, result.test_count], ['full', 'foreign-tests', undefined]);
      assert.equal((result.stdout.match(/FULL-SUITE/g) ?? []).length, 1, 'exactly one full run');
      assert.equal((result.stdout.match(/LOCAL-TEST/g) ?? []).length, 1, 'the fallback executes the Node suite');
    });
  }
});
