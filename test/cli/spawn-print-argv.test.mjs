import { BINS, fakeDeps, freshDir, readRecords, sink, writeIn } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

const { runSpawn } = await import('../../src/cli/spawn.mjs');
const { readAllRows } = await import('../../src/ledger/write.mjs');

function fixture(provider = 'anthropic', briefName = 'brief.md') {
  const ws = freshDir('print-argv');
  const model = provider === 'openai' ? 'gpt-6-luna' : 'claude-sonnet-5';
  writeIn(ws, '.code-forge.yml', `version: 1\nprovider: ${provider}\nlevels:\n${['L0', 'L1', 'L2', 'L3'].map((level) => `  ${level}:\n    model: ${model}\n    effort: high\n`).join('')}`);
  const brief = writeIn(ws, briefName, 'Implement the task.\n');
  const args = ['--level', 'L1', '--role', 'coder', '--brief', brief];
  return { ws, args, slug: path.basename(ws), model };
}

async function invoke(f, args, deps) {
  const before = process.cwd();
  const stdout = sink();
  process.chdir(f.ws);
  try {
    const code = await runSpawn(args, { ...deps, stdout });
    return { code, stdout: stdout.text() };
  } finally {
    process.chdir(before);
  }
}

test('customer: L1 coder --print-argv prints exactly one JSON line with the resolved argv and metadata', async () => {
  const f = fixture();
  const { deps, stderr } = fakeDeps();
  const result = await invoke(f, [...f.args, '--print-argv'], deps);
  assert.equal(result.code, 0, stderr.text());
  assert.equal(result.stdout.endsWith('\n'), true, 'JSON line ends with a newline');
  const lines = result.stdout.trimEnd().split('\n');
  assert.equal(lines.length, 1, 'exactly one stdout line');
  const shown = JSON.parse(lines[0]);
  assert.equal(Array.isArray(shown.argv) && shown.argv.length > 1, true, 'argv contains a binary and arguments');
  assert.equal(shown.argv[0], BINS.claude, 'argv uses the injected fake binary');
  assert.deepEqual(Object.keys(shown).sort(), ['argv', 'cwd', 'effort', 'fallback_step', 'model', 'provider']);
  assert.deepEqual([shown.cwd, shown.provider, shown.model, shown.effort, shown.fallback_step], [f.ws, 'anthropic', f.model, 'high', 0]);
});

test('preview starts zero provider processes and writes zero ledger session or cost rows', async () => {
  const f = fixture();
  const { deps, records, stderr } = fakeDeps();
  assert.deepEqual(await readAllRows(f.slug), [], 'fixture ledger starts empty');
  const result = await invoke(f, [...f.args, '--print-argv'], deps);
  assert.equal(result.code, 0, stderr.text());
  assert.deepEqual(readRecords(records), [], 'zero fake CLI record rows');
  assert.deepEqual(await readAllRows(f.slug), [], 'zero ledger rows, including sessions and costs');
});

test('printed Codex argv equals the real spawn argv, replacing only session directory prefixes', async () => {
  const f = fixture('openai');
  const { deps, records, stderr } = fakeDeps();
  const preview = await invoke(f, [...f.args, '--print-argv'], deps);
  assert.equal(preview.code, 0, stderr.text());
  const shown = JSON.parse(preview.stdout);
  assert.equal(shown.argv[0], BINS.codex);
  assert.deepEqual(readRecords(records), [], 'preview starts zero fake CLIs');
  const real = await invoke(f, f.args, deps);
  assert.equal(real.code, 0, stderr.text());
  const rows = readRecords(records);
  assert.equal(rows.length, 1, 'real spawn starts exactly one fake CLI');
  const actual = rows[0].argv;
  const session = path.dirname(actual[actual.indexOf('-o') + 1]);
  const normalized = actual.map((arg) => arg.replaceAll(session, '<session>'));
  assert.deepEqual(shown.argv.slice(1), normalized, 'every argument matches the executed argv');
  assert.equal(shown.cwd, rows[0].cwd, 'preview and real spawn use the same cwd');
  assert.equal(shown.argv[shown.argv.indexOf('-o') + 1], '<session>/out.json');
});

test('--print-argv with --background is a usage error and starts nothing', async () => {
  const f = fixture();
  const { deps, records, stderr } = fakeDeps();
  const result = await invoke(f, [...f.args, '--print-argv', '--background'], deps);
  assert.equal(result.code, 2);
  assert.equal(result.stdout, '', 'usage error prints no JSON');
  assert.match(stderr.text(), /usage: code-forge spawn .*\[--print-argv\]/);
  assert.deepEqual(readRecords(records), [], 'zero processes after incompatible flags');
  assert.deepEqual(await readAllRows(f.slug), [], 'zero ledger rows after incompatible flags');
});

test('preview refuses a forbidden coder argv before printing or starting it', async () => {
  const f = fixture('anthropic', 'x--no-require-reviews.md');
  const { deps, records, stderr } = fakeDeps();
  const result = await invoke(f, [...f.args, '--print-argv'], deps);
  assert.equal(result.code, 1, stderr.text());
  assert.equal(result.stdout, '', 'a forbidden argv is never printed');
  assert.equal(stderr.text(), 'spawn: coder argv refused before spawn: forbidden entry code-forge-no-require-reviews\n');
  assert.deepEqual(readRecords(records), [], 'zero processes for forbidden argv');
  assert.deepEqual(await readAllRows(f.slug), [], 'zero ledger rows for forbidden argv');
});
