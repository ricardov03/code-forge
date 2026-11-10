import { baseEnv, freshDir, makeProject, runWizard, scriptedUi } from './helpers.mjs';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { parse as parseYAML } from 'yaml';

/** @param {string[]} args @param {any} [ui] */
async function run(args, ui) {
  const home = freshDir('home');
  const cwd = await makeProject(async () => {});
  const result = await runWizard(args, { cwd, home, env: baseEnv(home), isTTY: Boolean(ui), ui });
  return { ...result, file: path.join(cwd, '.code-forge.yml') };
}

/** @param {ReturnType<typeof scriptedUi>} ui */
function levelCalls(ui) {
  return ui.calls.filter((c) => /^L[0-3] model/.test(c.message));
}

/** @param {ReturnType<typeof scriptedUi>} ui @param {string} message */
function messageCount(ui, message) {
  return ui.calls.filter((c) => c.message.includes(message)).length;
}

test('reporter: duplicate Luna levels are immediately re-asked, corrected config is written; flags still refuse duplicates', async () => {
  const duplicate = 'gpt-6-luna:high@openai';
  const ui = scriptedUi({
    'Install the recommended tools': 'current',
    'Install the skill into:': [['claude']],
    Scope: 'project',
    Method: 'copy',
    'Default provider': 'openai',
    'Keep the level matrix?': false,
    'L0 model': [duplicate],
    'L1 model': [duplicate, 'gpt-6-luna:low@openai'],
    'L2 model': [duplicate, 'gpt-6-sol:high@openai'],
    'L3 model': [duplicate, 'gpt-6-astra:high@openai'],
    'Multimodel review': false,
    Engine: 'harness',
    'May a coder push': false,
    'Gates and proof settings:': 'use',
  });
  const r = await run(['--no-jev'], ui);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(existsSync(r.file), true, 'the corrected project config was written');
  assert.doesNotMatch(r.stdout + r.stderr, /nothing written/i);
  const calls = levelCalls(ui);
  assert.deepEqual(calls.map((c) => c.message.slice(0, 2)), ['L0', 'L1', 'L1', 'L2', 'L2', 'L3', 'L3']);
  const first = ui.calls.indexOf(calls[0]);
  assert.deepEqual(ui.calls.slice(first, first + 7), calls, 'retries happen before the next level or any other question');
  for (const level of ['L1', 'L2', 'L3']) {
    assert.equal(messageCount(ui, `${level} is the same as L0`), 1, `${level} names L0 exactly once`);
  }
  assert.equal(messageCount(ui, 'Each level must be a different model or effort.'), 3);
  assert.deepEqual(parseYAML(readFileSync(r.file, 'utf8')).levels, {
    L0: { model: 'gpt-6-luna', effort: 'high', provider: 'openai' },
    L1: { model: 'gpt-6-luna', effort: 'low', provider: 'openai' },
    L2: { model: 'gpt-6-sol', effort: 'high', provider: 'openai' },
    L3: { model: 'gpt-6-astra', effort: 'high', provider: 'openai' },
  });

  const flagged = await run([
    '--no-jev', '--no-interaction', '--provider', 'openai', '--engine', 'harness', '-p',
    ...['L0', 'L1', 'L2', 'L3'].flatMap((level) => ['--level', `${level}=${duplicate}`]),
  ]);
  assert.equal(flagged.code, 1, 'duplicate --level flags still fail final validation');
  assert.equal(existsSync(flagged.file), false, 'duplicate flags write no project config');
  assert.equal((flagged.stderr.match(/duplicate-level-tuple:/g) ?? []).length, 3, flagged.stderr);
  for (const level of ['L1', 'L2', 'L3']) {
    assert.match(flagged.stderr, new RegExp(`levels.L0 and levels.${level} resolve to the same`));
  }
});

test('bad text is re-asked; unchanged prefill still checks default provider and omitted effort', async () => {
  const ui = scriptedUi({
    'Keep the level matrix?': false,
    'L0 model': ['gpt-6-luna:@openai', 'claude-sonnet-5@anthropic'],
    // The first answer is Enter on the unchanged prefilled L1.
    'L1 model': ['claude-sonnet-5', 'claude-haiku-4-5-20251001'],
    Engine: 'harness',
  });
  const r = await run(['--no-jev'], ui);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(existsSync(r.file), true, 'valid corrections write the config');
  assert.doesNotMatch(r.stdout + r.stderr, /nothing written/i);
  const calls = levelCalls(ui);
  assert.deepEqual(calls.map((c) => c.message.slice(0, 2)), ['L0', 'L0', 'L1', 'L1', 'L2', 'L3']);
  assert.equal(messageCount(ui, '--level: empty effort'), 1, 'parseLevel usage message is shown once');
  assert.equal(messageCount(ui, 'L1 is the same as L0'), 1, 'inherited provider equals explicit provider');
  assert.equal(messageCount(ui, 'Each level must be a different model or effort.'), 1);
  assert.equal(calls[2].initialValue, 'claude-sonnet-5', 'duplicate answer was the unchanged prefill');
  assert.deepEqual(parseYAML(readFileSync(r.file, 'utf8')).levels, {
    L0: { model: 'claude-sonnet-5', provider: 'anthropic' },
    L1: { model: 'claude-haiku-4-5-20251001' },
    L2: { model: 'claude-opus-5-5' },
    L3: { model: 'claude-fable-5-2' },
  });
});

test('judge duplicate is immediately re-asked with the same level error, then writes the correction', async () => {
  const ui = scriptedUi({
    'Multimodel review': true,
    'Second provider': 'openai',
    'L3 judge': ['grok-4.7:low@xai', 'grok-4.7:high@xai'],
    Engine: 'harness',
  });
  const r = await run(['--no-jev', '--level', 'L0=grok-4.7:low@xai'], ui);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(existsSync(r.file), true, 'corrected judge writes the config');
  assert.doesNotMatch(r.stdout + r.stderr, /nothing written/i);
  const calls = ui.calls.filter((c) => c.message.startsWith('L3 judge'));
  assert.equal(calls.length, 2);
  assert.equal(ui.calls.indexOf(calls[1]), ui.calls.indexOf(calls[0]) + 1);
  assert.equal(messageCount(ui, 'L3 is the same as L0. Each level must be a different model or effort.'), 1);
  assert.equal(calls[1].initialValue, 'grok-4.7:low@xai');
  const written = parseYAML(readFileSync(r.file, 'utf8'));
  assert.deepEqual(written.levels.L0, { model: 'grok-4.7', effort: 'low', provider: 'xai' });
  assert.deepEqual(written.levels.L3, { model: 'grok-4.7', effort: 'high', provider: 'xai' });
});

test('judge bad text is immediately re-asked with the usage error, then writes the correction', async () => {
  const ui = scriptedUi({
    'Multimodel review': true,
    'Second provider': 'openai',
    'L3 judge': ['grok-4.7:@xai', 'grok-4.7:high@xai'],
    Engine: 'harness',
  });
  const r = await run(['--no-jev'], ui);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(existsSync(r.file), true, 'corrected judge writes the config');
  assert.doesNotMatch(r.stdout + r.stderr, /nothing written/i);
  const calls = ui.calls.filter((c) => c.message.startsWith('L3 judge'));
  assert.equal(calls.length, 2);
  assert.equal(ui.calls.indexOf(calls[1]), ui.calls.indexOf(calls[0]) + 1);
  assert.equal(messageCount(ui, '--level: empty effort'), 1);
  assert.equal(calls[1].initialValue, 'grok-4.7:@xai');
  assert.deepEqual(parseYAML(readFileSync(r.file, 'utf8')).levels.L3,
    { model: 'grok-4.7', effort: 'high', provider: 'xai' });
});

test('a cleared level answer returning undefined is re-asked as empty, then writes the correction', async () => {
  const ui = scriptedUi({
    'Keep the level matrix?': false,
    'L0 model': [undefined, 'claude-haiku-4-5-20251001'],
    Engine: 'harness',
  });
  const r = await run(['--no-jev'], ui);
  assert.equal(r.code, 0, r.stderr);
  assert.equal(existsSync(r.file), true, 'corrected level writes the config');
  assert.doesNotMatch(r.stdout + r.stderr, /nothing written/i);
  const calls = levelCalls(ui);
  assert.deepEqual(calls.map((c) => c.message.slice(0, 2)), ['L0', 'L0', 'L1', 'L2', 'L3']);
  assert.equal(ui.calls.indexOf(calls[1]), ui.calls.indexOf(calls[0]) + 1);
  assert.equal(messageCount(ui, '--level: empty model'), 1);
  assert.equal(calls[1].initialValue, '', 'the cleared field stays empty on retry');
  assert.deepEqual(parseYAML(readFileSync(r.file, 'utf8')).levels.L0,
    { model: 'claude-haiku-4-5-20251001' });
});
