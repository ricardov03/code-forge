import { freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { runBlock } from '../../src/cli/block.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { runRecordPath } from '../../src/state/paths.mjs';
import { readRun, startRun } from '../../src/state/run.mjs';

const NOW = new Date('2026-10-31T20:00:00Z');
const CODE = [{ clause: 'automated behavior', tests: ['behavior-test'] }];
const HUMAN = [CODE[0], { clause: 'Check phone layout', kind: 'human_check' }, { clause: 'Approve device colors', kind: 'human_check' }];

async function fixture(slug) {
  const repo = realpathSync(freshDir(slug));
  await git(['init', '-q', '-b', 'main'], repo);
  writeFile(repo, 'a.mjs', 'export const a = 1;\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-q', '-m', 'base'], repo);
  const files = freshDir('acceptance');
  writeFile(files, 'code.json', JSON.stringify(CODE));
  writeFile(files, 'human.yml', '- clause: automated behavior\n  tests: [behavior-test]\n- clause: Check phone layout\n  kind: human_check\n- clause: Approve device colors\n  kind: human_check\n');
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, writeRow, now: NOW });
  let questions = 0;
  const cli = async (args, isTTY = true) => {
    let out = ''; let err = '';
    const code = await runBlock([...args, '--run', runId, '--json'], {
      now: () => NOW, isTTY,
      stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } },
      ui: { confirm: async () => { questions += 1; return true; }, isCancel: () => false },
    });
    return { code, out, err };
  };
  const open = (file, isTTY = true) => cli(['open', 'B1', '--level', 'L1', '--owned', 'a.mjs', '--acceptance', path.join(files, file)], isTTY);
  const amend = (isTTY = true) => cli(['amend-acceptance', 'B1', '--acceptance', path.join(files, 'human.yml'), '--reason', 'add device checks'], isTTY);
  const snapshot = async () => ({ record: readFileSync(runRecordPath(runId), 'utf8'), rows: await readAllRows(slug) });
  const grant = () => startGrant({ runId, input: { until: '2026-10-31T21:00:00Z', delegate: 'L2', allow: 'waive:nit' }, now: NOW, writeRow });
  return { runId, open, amend, grant, snapshot, questions: () => questions };
}

test('active grant refuses open and amend with every human clause named and zero writes', async (t) => {
  for (const verb of ['open', 'amend']) {
    await t.test(verb, async () => {
      const f = await fixture(`human-active-${verb}`);
      if (verb === 'amend') assert.equal((await f.open('code.json')).code, 0);
      await f.grant();
      const before = await f.snapshot();
      for (const isTTY of [true, false]) {
        const result = verb === 'open' ? await f.open('human.yml', isTTY) : await f.amend(isTTY);
        assert.equal(result.code, 1, `${verb} must refuse an active grant: ${result.err}`);
        assert.match(result.err, /autopilot-human-check/, `${verb} refusal code`);
        for (const clause of HUMAN.slice(1)) assert.equal(result.err.includes(clause.clause), true, `${verb} names ${clause.clause}`);
        assert.match(result.err, /clause with tests.*ask the owner/i, `${verb} explains both remedies`);
        assert.equal(result.out, '', `${verb} emits no success`);
        assert.deepEqual(await f.snapshot(), before, `${verb} changes zero record bytes and zero ledger rows`);
        assert.equal(f.questions(), 0, `${verb} asks zero human questions`);
      }
      const blocks = (await readRun(f.runId)).blocks;
      assert.deepEqual(Object.keys(blocks), verb === 'open' ? [] : ['B1']);
      if (verb === 'amend') assert.deepEqual(blocks.B1.acceptance, CODE);
    });
  }
});

test('boundary: without a grant both open and amend still accept human clauses', async () => {
  for (const verb of ['open', 'amend']) {
    const f = await fixture(`human-no-grant-${verb}`);
    if (verb === 'amend') assert.equal((await f.open('code.json')).code, 0);
    const before = await f.snapshot();
    const result = verb === 'open' ? await f.open('human.yml') : await f.amend();
    assert.equal(result.code, 0, `${verb} without a grant: ${result.err}`);
    assert.equal(result.err, '');
    const blocks = (await readRun(f.runId)).blocks;
    assert.deepEqual(Object.keys(blocks), ['B1']);
    assert.deepEqual(blocks.B1.acceptance, HUMAN);
    assert.deepEqual(blocks.B1.human_checks, HUMAN.slice(1).map((c) => c.clause));
    const added = (await f.snapshot()).rows.slice(before.rows.length);
    assert.deepEqual(added.map((row) => row.event), [verb === 'open' ? 'dispatch' : 'block.acceptance_amended']);
    assert.equal(f.questions(), verb === 'open' ? 0 : 1);
  }
});
