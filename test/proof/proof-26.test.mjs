import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { validateConfig } from '../../src/config/validate.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { deleteAssertions, journalPath } from '../../src/proof/red-green.mjs';
import { defaultPestRunner, runnerHook } from '../../src/proof/runners.mjs';
import { openBlock } from '../../src/state/block.mjs';
import { proofRunners } from '../../src/state/config-snapshot.mjs';
import { readRun, reloadRun, startRun } from '../../src/state/run.mjs';
import { TEMP_PARENT, captureStream, git } from './helpers.mjs';

const BASE = { version: 1, project: { slug: 'b171' }, provider: 'anthropic', engine: 'harness',
  levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } } };
let serial = 0;

async function fixture(proof = {}, artisan = true) {
  const name = `b171-${++serial}`;
  const dir = path.join(TEMP_PARENT, name);
  const config = { ...BASE, project: { slug: name }, proof };
  const write = (rel, text) => {
    mkdirSync(path.dirname(path.join(dir, rel)), { recursive: true });
    writeFileSync(path.join(dir, rel), text);
  };
  write('.gitignore', '.code-forge/\nvendor/\n');
  write('.code-forge.yml', JSON.stringify(config));
  write('src/value.php', '<?php // BASE\n');
  if (artisan) write('artisan', '<?php\n');
  else write('vendor/bin/pest', '<?php\n');
  git(['init', '-q'], dir);
  git(['add', '-A'], dir);
  git(['-c', 'commit.gpgsign=false', 'commit', '-qm', 'base'], dir);
  write('src/value.php', '<?php // FIXED\n');
  write('tests/ValueTest.php', "<?php\nit('value', function () {\n  expect($x)\n    ->toBe(1)\n    ->not->toBeNull();\n});\n");
  const writeRow = (row) => appendRow(row, { slug: name });
  await startRun({ workspace: dir, project: name, runId: name, writeRow, config });
  await openBlock({ runId: name, id: 'A', level: 'L1', owned: ['src/value.php', 'tests/**'], acceptance: [{ clause: 'value', tests: ['tests/ValueTest.php'] }], writeRow });
  return { name, dir, config, write, writeRow };
}

async function prove(fx, deps = {}) {
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runProof(['red-green', 'A', '--run', fx.name, '--test', 'tests/ValueTest.php::value'], { ...deps, stdout, stderr });
  return { code, stderr: stderr.text, data: stdout.text ? JSON.parse(stdout.text) : null };
}

test('B171: default Pest JUnit runners prefer artisan, otherwise vendor/bin/pest, and filter the case', async () => {
  for (const artisan of [true, false]) {
    const fx = await fixture({}, artisan);
    const fake = path.join(TEMP_PARENT, `${fx.name}-runner.mjs`);
    writeFileSync(fake, `import {readFileSync,writeFileSync} from 'node:fs';
const report = process.argv[process.argv.indexOf('--log-junit') + 1];
const fixed = readFileSync('src/value.php','utf8').includes('FIXED');
writeFileSync(report, '<testsuite><testcase classname="ValueTest" name="value">' + (fixed ? '' : '<failure type="PHPUnit\\\\Framework\\\\ExpectationFailedException" message="Failed asserting that 2 is identical to 1."/>') + '</testcase></testsuite>');
process.exit(fixed ? 0 : 1);`);
    let selected;
    const result = await prove(fx, { hook: (entry, test) => {
      selected = entry;
      return runnerHook({ ...entry, argv: [process.execPath, fake, ...entry.argv.slice(1)] }, test);
    } });
    assert.equal(result.code, 0, result.stderr);
    assert.deepEqual(selected.argv, [...(artisan ? ['php', 'artisan', 'test'] : ['vendor/bin/pest']), '{test_file}', '--log-junit', '{report}', '--filter', '{test_filter}']);
    assert.deepEqual([result.data.runner, result.data.red, result.data.green], ['junit', 'RED', 'GREEN']);
  }
});

test('B171: a Pest project can select its default before setup installs vendor; unrelated PHP projects have no default', async () => {
  const dir = path.join(TEMP_PARENT, 'pest-without-vendor');
  mkdirSync(dir);
  writeFileSync(path.join(dir, 'composer.json'), JSON.stringify({ 'require-dev': { 'pestphp/pest': '^4.0' } }));
  const entry = await defaultPestRunner(dir, 'tests/ExampleTest.php', undefined);
  assert.deepEqual(entry?.argv, ['vendor/bin/pest', '{test_file}', '--log-junit', '{report}']);
  assert.equal(await defaultPestRunner(dir, 'test/example.test.mjs', undefined), null);
  writeFileSync(path.join(dir, 'composer.json'), '{}');
  assert.equal(await defaultPestRunner(dir, 'tests/ExampleTest.php', undefined), null);
});

test('B171: reload appends runners, including first runners on a legacy run, but preserves existing entries and priority', async () => {
  const first = { match: ['tests/**/*.php'], adapter: 'junit', argv: ['php', '{test_file}', '{report}'] };
  const added = { ...first, match: ['checks/**/*.php'] };
  const fx = await fixture({ runners: [first] });
  await reloadRun({ runId: fx.name, config: { ...fx.config, proof: { runners: [first, added] } }, writeRow: fx.writeRow });
  assert.deepEqual(proofRunners(await readRun(fx.name)), [first, added]);
  for (const runners of [[added], [added, first], [{ ...first, argv: ['other', '{test_file}', '{report}'] }, added]]) {
    await assert.rejects(reloadRun({ runId: fx.name, config: { ...fx.config, proof: { runners } }, writeRow: fx.writeRow }), { code: 'immutable-key' });
  }
  const legacy = `${fx.name}-legacy`;
  await startRun({ workspace: fx.dir, project: fx.name, runId: legacy, writeRow: fx.writeRow });
  await reloadRun({ runId: legacy, config: { proof: { runners: [first] } }, writeRow: fx.writeRow });
  assert.deepEqual(proofRunners(await readRun(legacy)), [first]);
});

test('B171: setup schema accepts argv lists and refuses strings, empty argv and NUL', () => {
  assert.equal(validateConfig({ ...BASE, proof: { worktree: { setup: [[process.execPath, '-e', 'process.exit(0)']] } } }).valid, true);
  for (const setup of ['cp vendor', ['cp vendor'], [[]], [[42]], [['']], [['bad\0command']]]) {
    assert.equal(validateConfig({ ...BASE, proof: { worktree: { setup } } }).valid, false, JSON.stringify(setup));
  }
});

test('B171: setup runs sequentially in the export before RED; setup failure reports a tail without RED_INVALID', async () => {
  for (const fail of [false, true]) {
    const setup = [[process.execPath, '-e', "require('node:fs').writeFileSync('ready', process.cwd())"],
      [process.execPath, '-e', fail ? "console.error('setup-tail');process.exit(7)" : "require('node:fs').appendFileSync('ready', '\\nsecond')"]];
    const entry = { match: ['tests/**/*.php'], adapter: 'junit', argv: ['fake', '{test_file}', '{report}'] };
    const fx = await fixture({ worktree: { setup }, runners: [entry] });
    let calls = 0;
    const result = await prove(fx, { hook: () => ({ adapter: 'junit', env_keys: [], red: async ({ cwd }) => {
      calls++;
      assert.equal(readFileSync(path.join(cwd, 'ready'), 'utf8'), `${cwd}\nsecond`);
      assert.match(readFileSync(path.join(cwd, 'src/value.php'), 'utf8'), /BASE/);
      return { verdict: 'RED', red_kind: 'assertion', failed: 1, failed_tests: ['value'], failed_ids: ['value'], framework: 'junit', reason: 'assertion' };
    }, green: async () => 'GREEN' }) });
    assert.equal(result.code, fail ? 1 : 0, result.stderr);
    assert.equal(calls, fail ? 0 : 1);
    assert.equal(existsSync(path.join(fx.dir, 'ready')), false);
    assert.equal(existsSync(journalPath(path.join(fx.dir, '.code-forge/export/A'))), false);
    if (fail) {
      assert.deepEqual([result.data.reason, result.data.red, result.data.proven], ['setup', null, false]);
      assert.match(result.data.output_tail, /setup-tail/);
      assert.doesNotMatch(result.stderr, /RED_INVALID/);
      const row = (await readAllRows(fx.name)).at(-1);
      assert.deepEqual([row.reason, row.proven], ['setup', false]);
    }
  }
});

test('B171: multiline Pest chains are removed through the semicolon, preserving surrounding code and line numbers', () => {
  const source = "<?php\nit('value', function () {\n  $x = 1;\n  expect($x)\n    ->toBe(1)\n    ->not->toBeNull(); // chain\n  cleanup();\n});\n";
  const result = deleteAssertions('tests/ValueTest.php', Buffer.from(source));
  assert.equal(result.deleted, 1);
  assert.equal(result.bytes.toString(), source.replace('  expect($x)\n    ->toBe(1)\n    ->not->toBeNull(); // chain', "  $this->fail('code-forge: assertion deleted');\n\n"));
  for (const chain of ['  expect($x)\n    ->toBe(1); cleanup();', '  expect($x)\n    ->toBe(1) + 2;', '  expect($x)\n    ->toBe(1)\n  cleanup();']) {
    assert.throws(() => deleteAssertions('tests/ValueTest.php', Buffer.from(`<?php\nit('x', function () {\n${chain}\n});`)), { code: 'unclassified-assertion' });
  }
});
