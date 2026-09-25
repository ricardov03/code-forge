import { fakeDeps, freshDir, readRecords, sink } from './helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, copyFileSync, existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const { runAuthorLoop } = await import('../../src/session/author.mjs');
const { runAuthorVerb } = await import('../../src/cli/author.mjs');

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const BRIEFS = path.join(REPO, 'test', 'fixtures', 'briefs');
const CFG = { provider: 'anthropic', levels: Object.fromEntries(['L0', 'L1', 'L2', 'L3'].map((l) => [l, { model: `fake-${l}` }])) };

const ONE_QUESTION = { draft: '# Draft 1\n', questions: [{ id: 'q1', question: 'Split B2 in two?', options: ['yes', 'no'], why: 'B2 mixes two concerns', blocking: true }] };
const NO_QUESTIONS = { draft: '# Draft 2\n', questions: [] };

/** A workspace with the config, the brief and its facts sheet (built from this exact brief). */
function workspace(name) {
  const ws = freshDir(name);
  copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
  copyFileSync(path.join(BRIEFS, 'tool-brief.md'), path.join(ws, 'tool-brief.md'));
  copyFileSync(path.join(BRIEFS, 'tool-brief.facts.md'), path.join(ws, 'tool-brief.facts.md'));
  return ws;
}

test('the author loop runs 2 rounds: questions 1 then 0; round 2 gets the draft and the answers', async () => {
  const ws = workspace('loop');
  const { deps, records, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(ONE_QUESTION) });
  const asked = [];
  const result = await runAuthorLoop(
    {
      cfg: CFG,
      job: 'harden',
      briefPath: path.join(ws, 'tool-brief.md'),
      factsPath: path.join(ws, 'tool-brief.facts.md'),
      draftOut: path.join(ws, 'plans', 'tool.draft.md'),
      ask: async (questions, round) => {
        asked.push([round, questions.map((q) => q.id)]);
        deps.env.FAKE_ANSWER = JSON.stringify(NO_QUESTIONS); // the human answered; the next round has nothing left to ask
        return { answers: { q1: 'yes, split it' } };
      },
    },
    deps,
  );
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.rounds.map((r) => [r.round, r.questions]), [[1, 1], [2, 0]]);
  assert.deepEqual(asked, [[1, ['q1']]]);
  assert.equal(readFileSync(path.join(ws, 'plans', 'tool.draft.md'), 'utf8'), '# Draft 2\n');
  const spawned = readRecords(records);
  assert.equal(spawned.length, 2);
  const second = Buffer.from(spawned[1].stdin_b64, 'base64').toString('utf8');
  assert.deepEqual(
    [second.includes('## Prior draft\n\n# Draft 1\n'), second.includes('"question": "Split B2 in two?"'), second.includes('"answer": "yes, split it"'), second.includes('## Facts sheet\n\n# Facts sheet\n')],
    [true, true, true, true],
  );
  assert.equal(stderr.text().split('\n').filter((l) => l.startsWith('author round ')).length, 2);
});

test('author --job plan without --facts exits 2 and spawns nothing; with --facts it exits 0', async () => {
  const ws = workspace('plan-job');
  const without = fakeDeps({ FAKE_ANSWER: JSON.stringify(NO_QUESTIONS) });
  assert.equal(await runAuthorVerb(['--job', 'plan', '--brief', 'tool-brief.md'], { ...without.deps, stdout: sink(), cwd: ws }), 2);
  assert.deepEqual(without.stderr.text().split('\n').filter(Boolean), ['author: facts sheet required: run forge facts first']);
  assert.equal(readRecords(without.records).length, 0);

  const withFacts = fakeDeps({ FAKE_ANSWER: JSON.stringify(NO_QUESTIONS) });
  const stdout = sink();
  const code = await runAuthorVerb(['--job', 'plan', '--brief', 'tool-brief.md', '--facts', 'tool-brief.facts.md'], { ...withFacts.deps, stdout, cwd: ws });
  assert.equal(code, 0);
  const printed = JSON.parse(stdout.text());
  assert.deepEqual([printed.draft, printed.questions], [path.join(ws, 'plans', 'tool-brief.plan.md'), []]);
  assert.equal(readFileSync(printed.draft, 'utf8'), '# Draft 2\n');
  assert.equal(readRecords(withFacts.records).length, 1);
});

test('a stale facts sheet is refused before any session runs', async () => {
  const ws = workspace('stale');
  appendFileSync(path.join(ws, 'tool-brief.md'), 'It also reads `--verbose`.\n');
  const { deps, records, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify(NO_QUESTIONS) });
  assert.equal(await runAuthorVerb(['--job', 'plan', '--brief', 'tool-brief.md', '--facts', 'tool-brief.facts.md'], { ...deps, stdout: sink(), cwd: ws }), 2);
  assert.deepEqual(stderr.text().split('\n').filter(Boolean), ['author: facts sheet is stale: brief changed after it was built']);
  assert.equal(readRecords(records).length, 0);
  assert.equal(existsSync(path.join(ws, 'plans')), false);
});
