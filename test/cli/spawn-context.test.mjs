import { cli, freshDir, makeRepo, useFake } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const fake = path.join(freshDir('coder'), 'claude.mjs');
writeFileSync(fake, `#!/usr/bin/env node
import { readFileSync, writeFileSync } from 'node:fs';
const args = process.argv.slice(2);
const prompt = readFileSync(args[args.indexOf('--') + 1], 'utf8');
writeFileSync(process.env.CODER_CAPTURE, prompt);
process.stdout.write(JSON.stringify({type:'result', result:'done', is_error:false}));
`, { mode: 0o700 });
useFake(fake);

async function fixture() {
  const { repo, runId } = await makeRepo({ start: false });
  await startRun({ workspace: repo, project: 'worker-test', runId, writeRow: async () => {} });
  const brief = '# The block task\nImplement coder context delivery.\n';
  writeFileSync(path.join(repo, 'block.md'), brief);
  await openBlock({ runId, id: 'B172', level: 'L2', owned: ['src/**'], acceptance: [{ clause: 'the coder receives the task', tests: ['coder-context'] }], brief: { path: 'block.md', content: brief }, writeRow: async () => {} });
  return { repo, runId, brief };
}

test('B172: spawn coder appends missing block brief and acceptance, with TESTS and exact final sentinel instructions', async () => {
  const { repo, runId, brief } = await fixture();
  const prompt = path.join(repo, 'prompt.md');
  const capture = path.join(repo, 'capture.md');
  writeFileSync(prompt, 'Do the approved work.\n');
  const res = await cli(['spawn', '--level', 'L2', '--role', 'coder', '--brief', prompt, '--run', runId, '--block', 'B172', '--cwd', repo], repo, { CODER_CAPTURE: capture });
  assert.equal(res.code, 0, res.stderr);
  const sent = readFileSync(capture, 'utf8');
  assert.equal(sent.startsWith('Do the approved work.\n'), true);
  assert.equal(sent.split(brief.trimEnd()).length - 1, 1);
  assert.equal(sent.split('the coder receives the task').length - 1, 1);
  assert.match(sent, /coder-context/);
  assert.match(sent, /TESTS <command> exit=<n>/);
  assert.match(sent, /last line/);
  assert.equal(sent.trimEnd().split('\n').at(-1), '===BLOCK B172 COMPLETE===');
  assert.equal(readFileSync(prompt, 'utf8'), 'Do the approved work.\n');
});

test('B172: spawn preserves existing brief and acceptance without duplicating either', async () => {
  const { repo, runId, brief } = await fixture();
  const text = `${brief}\nAcceptance: the coder receives the task (coder-context).\n`;
  const prompt = path.join(repo, 'prompt.md');
  const capture = path.join(repo, 'capture.md');
  writeFileSync(prompt, text);
  const res = await cli(['spawn', '--level', 'L2', '--role', 'coder', '--brief', prompt, '--run', runId, '--block', 'B172'], repo, { CODER_CAPTURE: capture });
  assert.equal(res.code, 0, res.stderr);
  const sent = readFileSync(capture, 'utf8');
  assert.equal(sent.split(brief.trimEnd()).length - 1, 1);
  assert.equal(sent.split('the coder receives the task').length - 1, 1);
});

test('B172: spawn coder without a recorded run still asks for the exact block completion contract', async () => {
  const { repo } = await makeRepo({ start: false });
  const prompt = path.join(repo, 'prompt.md');
  const capture = path.join(repo, 'capture.md');
  writeFileSync(prompt, 'Standalone task.\n');
  const res = await cli(['spawn', '--level', 'L2', '--role', 'coder', '--brief', prompt, '--block', 'B172'], repo, { CODER_CAPTURE: capture });
  assert.equal(res.code, 0, res.stderr);
  const sent = readFileSync(capture, 'utf8');
  assert.match(sent, /TESTS <command> exit=<n>/);
  assert.equal(sent.trimEnd().split('\n').at(-1), '===BLOCK B172 COMPLETE===');
});
