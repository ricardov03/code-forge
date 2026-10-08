// B49a autopilot binnacle and full log: exact JSON for a fixture ledger covering every event kind,
// exact Markdown for a small one (8 parts in order), newest-first ordering, the scrub, the CLI
// (`autopilot binnacle|log`, `--json|--markdown`, `--link`) and the rewrite after every decision.
// `../session/helpers.mjs` is imported FIRST: HOME and the cwd point at one per-file temp parent,
// removed in `after()`. The clock is fake; sessions run against the fake CLIs.
import { fakeDeps } from '../session/helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdirSync, readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { askDelegate } = await import('../../src/autopilot/delegate.mjs');
const { startGrant, stopGrant } = await import('../../src/autopilot/grant.mjs');
const { buildBinnacle, buildFullLog, renderBinnacleMarkdown, renderLogMarkdown, runFilesDir, writeAutopilotFiles } = await import('../../src/autopilot/binnacle.mjs');
const { runAutopilot } = await import('../../src/cli/autopilot.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, startRun } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');

const SC = { home: '/home/fake', cwd: '/work/fake' };
const T = (/** @type {number} */ m) => new Date(Date.UTC(2026, 9, 6, 20, 0, 0) + m * 60000).toISOString();
const NOW = new Date('2026-10-06T21:30:00.000Z');

const RECORD = {
  run_id: 'r1',
  project: 'proj',
  status: 'active',
  blocks: {
    b1: { block: 'b1', status: 'closed', base_sha: 'aaaaaaa1111111', opened_at: T(1), level: 'L1' },
    b2: { block: 'b2', status: 'open', base_sha: 'aaaaaaa1111111', opened_at: T(2), level: 'L2' },
  },
  autopilot: { grant_id: 'ap-1', status: 'active', scopes: ['waive:nit', 'round:extra'], deny: [], delegate: 'L2', until: T(120), caps: { coding: 20 }, stop_at: 0.9, started_at: T(0), stopped_at: null, expired_at: null, link: 'https://claude.ai/doc/x', paused: { coding: { at: T(50), spent_usd: 18, reserved_usd: 0, cap_usd: 20 } } },
};

/** One row of every event kind the binnacle reads (plus another run's row, which must not show). */
const ROWS = [
  { run: 'r1', event: 'autopilot.grant', grant_id: 'ap-1', scopes: ['waive:nit', 'round:extra'], deny: [], delegate: 'L2', until: T(120), caps: { coding: 20 }, stop_at: 0.9, ts: T(0) },
  { run: 'r1', event: 'autopilot.decision', decision_id: 'ad-1', grant_id: 'ap-1', scope: 'waive:nit', subject: { block: 'b1', file: 'src/a.mjs', finding: 'f1' }, question: 'Waive the nit?', options: ['waive', 'fix'], decision: 'waive', confidence: 0.82, reason: 'style only', acted: true, to_owner: false, owner_reason: null, failure: null, ts: T(10) },
  { run: 'r1', event: 'review.waived', block: 'b1', file: 'src/a.mjs', finding: 'f1', reason: 'style only', by: 'autopilot', grant_id: 'ap-1', decision_id: 'ad-1', severity: 'nit', issue: 'https://github.com/o/r/issues/7', ts: T(11) },
  { run: 'r1', event: 'autopilot.decision', decision_id: 'ad-2', grant_id: 'ap-1', scope: 'waive:warning', subject: { block: 'b2', file: 'src/b.mjs' }, question: 'Waive the warning?', options: ['waive', 'fix'], decision: 'waive', confidence: 0.4, reason: 'unsure', acted: false, to_owner: true, owner_reason: 'low-confidence', failure: null, ts: T(20) },
  { run: 'r1', event: 'autopilot.extra_round', block: 'b1', file: 'src/a.mjs', grant_id: 'ap-1', decision_id: 'ad-3', ts: T(25) },
  { run: 'r1', event: 'autopilot.level', block: 'b2', level: 'L2', lane: 'L1', plan_level: 'L2', grant_id: 'ap-1', decision_id: 'ad-4', ts: T(30) },
  { run: 'r1', event: 'autopilot.approve', approval_id: 'aa-1', key: 'review.max_rounds_per_file', until: T(60), ts: T(35) },
  { run: 'r1', event: 'autopilot.restore_skipped', approval_id: 'aa-1', key: 'review.max_rounds_per_file', reason: 'changed-by-hand', ts: T(40) },
  { run: 'r1', event: 'autopilot.issue_failed', block: 'b2', file: 'src/c.mjs', finding: 'f2', grant_id: 'ap-1', reason: 'gh missing', ts: T(45) },
  { run: 'r1', event: 'autopilot.pause', grant_id: 'ap-1', category: 'coding', spent_usd: 18, reserved_usd: 0, cap_usd: 20, stop_at: 0.9, role: 'coder', block: 'b2', ts: T(50) },
  { run: 'r1', event: 'review.session_timeout', lens: 'correctness', role: 'reviewer', attempt: 1, retried: true, ts: T(52) },
  { run: 'r1', event: 'worker.down', reason: 'pinned worker pid 7 is not running', ts: T(53) },
  { run: 'r1', event: 'budget.refused', role: 'coder', block: 'b2', budget_usd: 20, spent_usd: 19, ts: T(55) },
  { run: 'r1', event: 'review.done', block: 'b2', file: 'src/c.mjs', findings_by_severity: { critical: 1 }, round: 1, ts: T(56) },
  { run: 'r1', event: 'review.result', block: 'b1', file: 'src/a.mjs', status: 'ok', ts: T(57) },
  { run: 'r1', event: 'review.approved', block: 'b1', file: 'src/a.mjs', ts: T(57.5) },
  { run: 'r1', event: 'block.close', block: 'b1', status: 'complete', commit: 'abcdef1234567', pr_url: 'https://github.com/o/r/pull/9', ts: T(58) },
  { run: 'r1', event: 'session', role: 'delegate', level: 'L2', provider: 'anthropic', status: 'ok', ts: T(59) },
  { run: 'r1', event: 'autopilot.link', grant_id: 'ap-1', link: 'https://claude.ai/doc/x', ts: T(60) },
  { run: 'r1', event: 'autopilot.restore', approval_id: 'aa-2', key: 'review.x', ts: T(61) },
  { run: 'r1', event: 'autopilot.stop', grant_id: 'ap-1', ts: T(62) },
  { run: 'r1', event: 'autopilot.expire', grant_id: 'ap-1', until: T(120), ts: T(63) },
  { run: 'other', event: 'autopilot.stop', grant_id: 'x', ts: T(64) },
];

const GH = 'https://github.com/o/r';
/** The full log of ROWS (r1), newest first: also the timeline's source, entry by entry. */
const EXPECTED_LOG = [
  { time: T(63), event: 'Grant expired', block: null, file: null, detail: `grant ap-1 · window ended ${T(120)}`, link: null },
  { time: T(62), event: 'Grant stopped', block: null, file: null, detail: 'grant ap-1', link: null },
  { time: T(61), event: 'Approval restored', block: null, file: null, detail: 'review.x', link: null },
  { time: T(60), event: 'Log link stored', block: null, file: null, detail: 'grant ap-1', link: 'https://claude.ai/doc/x' },
  { time: T(59), event: 'Delegate session', block: null, file: null, detail: 'L2 · anthropic · status ok', link: null },
  { time: T(58), event: 'Block closed', block: 'b1', file: null, detail: 'status complete · commit abcdef1', link: `${GH}/pull/9` },
  { time: T(55), event: 'Budget refused a session', block: 'b2', file: null, detail: 'coder · spent 19.00 of 20.00 USD', link: null },
  { time: T(53), event: 'Worker down', block: null, file: null, detail: 'the pinned worker is not running', link: null },
  { time: T(52), event: 'Review session timed out', block: null, file: null, detail: 'correctness · attempt 1 · retried', link: null },
  { time: T(50), event: 'Budget category paused', block: 'b2', file: null, detail: 'coding · spent 18.00 of 20.00 USD', link: null },
  { time: T(45), event: 'Waiver issue failed', block: 'b2', file: 'src/c.mjs', detail: 'finding f2 · no issue opened', link: null },
  { time: T(40), event: 'Restore skipped', block: null, file: null, detail: 'review.max_rounds_per_file · changed by hand', link: null },
  { time: T(35), event: 'Owner approval', block: null, file: null, detail: `review.max_rounds_per_file · until ${T(60)} · aa-1`, link: null },
  { time: T(30), event: 'Coder level chosen', block: 'b2', file: null, detail: 'L2 · lane L1 · plan L2 · decision ad-4', link: null },
  { time: T(25), event: 'Extra fix round', block: 'b1', file: 'src/a.mjs', detail: 'grant ap-1 · decision ad-3', link: null },
  { time: T(20), event: 'Question to the owner', block: null, file: null, detail: 'waive:warning · to owner (low-confidence) · confidence 0.4 · ad-2', link: null },
  { time: T(11), event: 'Waiver by autopilot', block: 'b1', file: 'src/a.mjs', detail: 'finding f1 · nit · issue https://github.com/o/r/issues/7 · decision ad-1', link: `${GH}/issues/7` },
  { time: T(10), event: 'Delegate decided', block: null, file: null, detail: 'waive:nit · acted · decision waive · confidence 0.82 · ad-1', link: null },
  { time: T(0), event: 'Grant started', block: null, file: null, detail: `grant ap-1 · delegate L2 · allow waive:nit, round:extra · until ${T(120)}`, link: null },
];

test('buildBinnacle: exact JSON for a fixture with every event kind (8 parts, other runs left out)', () => {
  const b = buildBinnacle({ runId: 'r1', rows: ROWS, record: RECORD, now: NOW, scrubCtx: SC });
  assert.deepEqual(b.title, 'Autopilot run r1');
  assert.deepEqual(b.byline, { date: '2026-10-06', owner: null });
  assert.deepEqual(b.status, [
    { item: 'Result', value: '1 of 2 blocks closed' },
    { item: 'Stack/PRs', value: 'https://github.com/o/r/pull/9' },
    { item: 'Full suite', value: 'not recorded' },
    { item: 'Reviews', value: '1 result · 1 file approved · 1 waived by autopilot' },
    { item: 'Files to read', value: 'src/a.mjs, src/b.mjs' },
    { item: 'Base', value: 'aaaaaaa' },
    { item: 'Spend', value: 'coding 0.00 of 20.00 USD, review 0.00 USD (no cap)' },
    { item: 'Autopilot window', value: 'active · grant ap-1 · 2026-10-06T20:00:00.000Z to 2026-10-06T22:00:00.000Z · delegate L2 · allow waive:nit, round:extra' },
    { item: 'Log link', value: 'https://claude.ai/doc/x' },
  ]);
  assert.deepEqual(b.decisions, [
    { time: T(10), decision: 'waive:nit: the delegate chose "waive" · b1 · src/a.mjs', why: 'style only (confidence 0.82)', reverse: 'Overrule it: undo the action it allowed (see its own entry) and, to stop further ones, run code-forge autopilot stop --run r1' },
    { time: T(11), decision: 'Waived f1 (nit) in src/a.mjs, block b1', why: 'style only (decision ad-1)', reverse: 'Reopen the finding: fix it in src/a.mjs, then delete the waiver issue (https://github.com/o/r/issues/7)' },
    { time: T(20), decision: 'Asked the owner (waive:warning): Waive the warning?', why: 'Not acted on: low-confidence', reverse: 'Nothing was done; answer it yourself (see Open questions).' },
    { time: T(25), decision: 'One extra fix round for src/a.mjs, block b1', why: 'The delegate allowed it (decision ad-3)', reverse: 'The round is spent; refuse further ones by stopping the grant or denying round:extra.' },
    { time: T(30), decision: 'Block b2 codes at L2', why: 'Lane L1, plan level L2 (decision ad-4)', reverse: 'Open the block again with code-forge block open --level <other level>.' },
    { time: T(35), decision: `You approved review.max_rounds_per_file until ${T(60)}`, why: 'Owner approval aa-1', reverse: `It restores itself at ${T(60)}; or set the old value back in .code-forge.yml and run code-forge run reload.` },
  ]);
  assert.deepEqual(b.blocks, [
    { block: 'b1', branch: 'commit abcdef1 · https://github.com/o/r/pull/9', review: '1 result · 1 approved · 1 waived', tests: 'not recorded', gates: 'passed at close', state: 'Closed, ready for you to mark ready and merge' },
    { block: 'b2', branch: 'not recorded', review: 'none yet', tests: 'not recorded', gates: 'none yet', state: 'Open' },
  ]);
  assert.deepEqual(b.open_questions, ['Waive the warning? (waive:warning, low-confidence)', 'Budget coding stopped at 18.00 of 20.00 USD: raise the cap or leave it?']);
  assert.deepEqual(b.owner_actions, [
    { text: 'Answer: Waive the warning? (waive:warning)', done: false },
    { text: 'Decide the critical finding in src/c.mjs (block b2): critical findings are never delegated', done: false },
    { text: 'Budget coding is paused: raise the cap or leave it (only you can raise a budget)', done: false },
    { text: 'Check review.max_rounds_per_file: it was changed by hand, so it was not restored', done: false },
    { text: 'Mark block b1 ready and merge it (https://github.com/o/r/pull/9)', done: false },
  ]);
  assert.deepEqual(b.incidents, [
    { time: T(45), what: 'The waiver issue for f2 in src/c.mjs could not be opened', effect: 'The waiver stands without an issue.', fix: 'Open the tracking issue by hand.' },
    { time: T(50), what: 'Budget category coding reached its stop at 18.00 of 20.00 USD', effect: 'Delegated actions in coding stopped.', fix: 'Raise the cap or leave it paused.' },
    { time: T(52), what: 'A correctness session timed out (attempt 1)', effect: 'It was retried.', fix: 'None needed unless it repeats.' },
    { time: T(53), what: 'The pinned worker was down', effect: 'Reviews wait until the worker is back.', fix: 'Restart the worker for this run.' },
    { time: T(55), what: 'A coder session was refused: budget 19.00 of 20.00 USD', effect: 'The session did not run.', fix: 'Raise the budget (only you can) or finish by hand.' },
  ]);
  assert.deepEqual(b.timeline, EXPECTED_LOG.map((e) => ({ time: e.time, event: `${e.event}${e.block ? ` · ${e.block}` : ''}${e.file ? ` · ${e.file}` : ''} — ${e.detail}` })));
  assert.deepEqual(Object.keys(b), ['title', 'byline', 'status', 'decisions', 'blocks', 'open_questions', 'owner_actions', 'incidents', 'timeline']);
});

test('buildFullLog: every autopilot row of the run newest first, with the fixed detail; no packet text, no other run', () => {
  const log = buildFullLog({ runId: 'r1', rows: ROWS, scrubCtx: SC });
  assert.deepEqual(log, EXPECTED_LOG);
  const text = JSON.stringify(log);
  assert.equal(text.includes('Waive the nit?') || text.includes('style only') || text.includes('gh missing'), false); // question, answer reason and failure text stay out of the log
});

test('newest first also when rows arrive out of order, a tie keeps the later row first, and a row without a time takes the one before it (clock note)', () => {
  const rows = [
    { run: 'r1', event: 'autopilot.stop', grant_id: 'a', ts: T(5) },
    { run: 'r1', event: 'autopilot.grant', grant_id: 'b', scopes: [], deny: [], delegate: 'L2', until: T(9), ts: T(1) },
    { run: 'r1', event: 'autopilot.expire', grant_id: 'c', until: T(9), ts: T(5) },
    { run: 'r1', event: 'autopilot.link', grant_id: 'd', link: 'https://claude.ai/x' }, // no ts: takes T(5) from the row before it
  ];
  const log = buildFullLog({ runId: 'r1', rows, scrubCtx: SC });
  assert.deepEqual(log.map((e) => [e.event, e.time]), [['Log link stored', T(5)], ['Grant expired', T(5)], ['Grant stopped', T(5)], ['Grant started', T(1)]]);
  const b = buildBinnacle({ runId: 'r1', rows, record: { run_id: 'r1', status: 'active', blocks: {} }, now: NOW, scrubCtx: SC });
  assert.deepEqual(b.status.at(-1), { item: 'Clock note', value: '1 row carries no valid timestamp and takes the time of the row before it' });
  const two = buildBinnacle({ runId: 'r1', rows: [...rows, { run: 'r1', event: 'autopilot.stop', grant_id: 'e' }], record: { run_id: 'r1', status: 'active', blocks: {} }, now: NOW, scrubCtx: SC });
  assert.deepEqual(two.status.at(-1), { item: 'Clock note', value: '2 rows carry no valid timestamp and take the time of the rows before them' });
  assert.equal(buildBinnacle({ runId: 'r1', rows: ROWS, record: RECORD, now: NOW, scrubCtx: SC }).status.some((s) => s.item === 'Clock note'), false);
});

test('renderBinnacleMarkdown: exact Markdown for a small fixture, 8 parts in the doc order with its table headers', () => {
  const record = { run_id: 'r2', project: 'proj', status: 'active', blocks: { b1: { block: 'b1', status: 'closed', base_sha: 'bbbbbbb2222222', opened_at: T(1) } } };
  const rows = [
    { run: 'r2', event: 'autopilot.grant', grant_id: 'ap-2', scopes: ['waive:nit'], deny: [], delegate: 'L2', until: T(120), caps: {}, stop_at: 0.9, ts: T(0) },
    { run: 'r2', event: 'autopilot.decision', decision_id: 'ad-9', grant_id: 'ap-2', scope: 'waive:nit', subject: null, question: 'Waive a | pipe?', options: ['waive', 'fix'], decision: 'fix', confidence: 0.9, reason: 'worth fixing', acted: true, to_owner: false, owner_reason: null, failure: null, ts: T(10) },
    { run: 'r2', event: 'block.close', block: 'b1', status: 'complete', ts: T(20) },
  ];
  const md = renderBinnacleMarkdown(buildBinnacle({ runId: 'r2', rows, record: { ...record, autopilot: { grant_id: 'ap-2', status: 'active', scopes: ['waive:nit'], deny: [], delegate: 'L2', until: T(120), caps: {}, stop_at: 0.9, started_at: T(0), link: null } }, now: NOW, scrubCtx: SC }), SC);
  assert.equal(
    md,
    [
      '# Autopilot run r2',
      '',
      '2026-10-06 · owner not set',
      '',
      '## Status at a glance',
      '',
      '| Item | Value |',
      '| --- | --- |',
      '| Result | 1 of 1 block closed |',
      '| Stack/PRs | none recorded |',
      '| Full suite | not recorded |',
      '| Reviews | 0 results · 0 files approved · 0 waived by autopilot |',
      '| Files to read | none |',
      '| Base | bbbbbbb |',
      '| Spend | coding 0.00 USD (no cap), review 0.00 USD (no cap) |',
      `| Autopilot window | active · grant ap-2 · ${T(0)} to ${T(120)} · delegate L2 · allow waive:nit |`,
      '| Log link | none |',
      '',
      '## Decisions',
      '',
      '| Time | Decision | Why | What would reverse it |',
      '| --- | --- | --- | --- |',
      `| ${T(10)} | waive:nit: the delegate chose "fix" | worth fixing (confidence 0.9) | Overrule it: undo the action it allowed (see its own entry) and, to stop further ones, run code-forge autopilot stop --run r2 |`,
      '',
      '## Blocks',
      '',
      '| Block | Branch / PR | Review | Tests | Gates | State |',
      '| --- | --- | --- | --- | --- | --- |',
      '| b1 | not recorded | none yet | not recorded | passed at close | Closed, ready for you to mark ready and merge |',
      '',
      '## Open questions',
      '',
      'none yet',
      '',
      '## Actions only you can take',
      '',
      '- [ ] Mark block b1 ready and merge it',
      '',
      '## Incidents',
      '',
      'none yet',
      '',
      '## Timeline',
      '',
      `- ${T(20)} — Block closed · b1 — status complete`,
      `- ${T(10)} — Delegate decided — waive:nit · acted · decision fix · confidence 0.9 · ad-9`,
      `- ${T(0)} — Grant started — grant ap-2 · delegate L2 · allow waive:nit · until ${T(120)}`,
      '',
    ].join('\n'),
  );
  const log = renderLogMarkdown({ runId: 'r2', entries: buildFullLog({ runId: 'r2', rows, scrubCtx: SC }) }, SC);
  assert.equal(log.split('\n')[4], '| Time | Event | Block | File | Detail | Link |');
  assert.equal(log.split('\n').length, 10); // title, blank, note, blank, header, rule, 3 rows, the empty tail after the final newline
});

test('an empty run: the 8 parts are all there, the lists say "none yet"', () => {
  const record = { run_id: 'r3', project: 'proj', status: 'active', blocks: {} };
  const md = renderBinnacleMarkdown(buildBinnacle({ runId: 'r3', rows: [], record, now: NOW, scrubCtx: SC }), SC);
  const heads = md.split('\n').filter((l) => l.startsWith('#'));
  assert.deepEqual(heads, ['# Autopilot run r3', '## Status at a glance', '## Decisions', '## Blocks', '## Open questions', '## Actions only you can take', '## Incidents', '## Timeline']);
  assert.equal(md.split('\n').filter((l) => l === 'none yet').length, 6);
  assert.equal(md.includes('| Result | no blocks yet |'), true);
  assert.equal(renderLogMarkdown({ runId: 'r3', entries: [] }, SC).includes('\nnone yet\n'), true);
  assert.deepEqual(buildFullLog({ runId: 'r3', rows: [], scrubCtx: SC }), []);
});

test('scrub: home path, working dir, a fake secret and an op reference appear 0 times in the JSON and both Markdown files', () => {
  const secret = 'sk-ant-FAKE0123456789abcdefghij';
  const rows = [
    { run: 'r4', event: 'autopilot.decision', decision_id: 'ad-1', grant_id: 'g', scope: 'waive:nit', subject: { block: 'b1', file: 'src/a.mjs' }, question: `Look at /home/fake/proj/x.mjs and /work/fake/y.mjs with ${secret} and op://vault/item/field`, options: ['a', 'b'], decision: 'a', confidence: 0.2, reason: `key ${secret} in /home/fake/.env`, acted: false, to_owner: true, owner_reason: 'low-confidence', failure: null, ts: T(1) },
    { run: 'r4', event: 'worker.down', reason: `cannot start /work/fake/bin with ${secret}`, ts: T(2) },
  ];
  const record = { run_id: 'r4', project: 'proj', status: 'active', blocks: {} };
  const b = buildBinnacle({ runId: 'r4', rows, record, now: NOW, scrubCtx: SC });
  const all = [JSON.stringify(b), renderBinnacleMarkdown(b, SC), JSON.stringify(buildFullLog({ runId: 'r4', rows, scrubCtx: SC })), renderLogMarkdown({ runId: 'r4', entries: buildFullLog({ runId: 'r4', rows, scrubCtx: SC }) }, SC)].join('\n');
  for (const bad of ['/home/fake', '/work/fake', 'FAKE0123456789', 'op://vault']) assert.equal(all.includes(bad), false, bad);
  assert.equal(all.includes('~/proj/x.mjs'), true);
  assert.equal(all.includes('<project>/y.mjs'), true);
  assert.equal(all.includes('op://<ref>'), true);
});

// ---- the CLI, the link and the rewrite after a decision (real ledger and run records in the temp HOME)

const WORK = process.cwd();
const T0 = new Date('2026-10-06T20:00:00.000Z');
const at = (/** @type {number} */ m) => new Date(T0.getTime() + m * 60000);
const UNTIL = '2026-10-06T22:00:00Z';
const ANSWER = { decision: 'waive', within_scope: true, confidence: 0.82, reason: 'style only; no behaviour change', escalate: false };
const LEVELS = {
  L0: { provider: 'anthropic', model: 'claude-haiku-4-5-20251001' },
  L1: { provider: 'anthropic', model: 'claude-sonnet-5' },
  L2: { provider: 'anthropic', model: 'claude-opus-5-5' },
  L3: { provider: 'anthropic', model: 'claude-fable-5-1' },
};

/** A run whose ledger slug is `proj`, run id `runId`, with an active grant. @param {string} runId */
async function newRun(runId) {
  const workspace = path.join(WORK, `ws-${runId}`);
  mkdirSync(workspace, { recursive: true });
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: 'proj' });
  await startRun({ workspace, project: 'proj', runId, writeRow, now: T0, config: { provider: 'anthropic', levels: LEVELS } });
  await startGrant({ runId, input: { until: UNTIL, delegate: 'L2', allow: 'waive:nit' }, now: T0, writeRow, grantId: `ap-${runId}` });
}

/** @param {string[]} args @param {{now?: Date}} [o] */
async function cli(args, { now = at(10) } = {}) {
  let out = '';
  let err = '';
  const code = await runAutopilot(args, { stdout: { write: (s) => ((out += s), true) }, stderr: { write: (s) => ((err += s), true) }, now: () => now });
  return { code, out, err };
}

test('autopilot binnacle --json and autopilot log --json print the data; the default is Markdown on stdout', async () => {
  await newRun('c-json');
  const json = await cli(['binnacle', '--run', 'c-json', '--json']);
  assert.equal(json.code, 0);
  const data = JSON.parse(json.out);
  assert.equal(data.title, 'Autopilot run c-json');
  assert.deepEqual(data.status.find((/** @type {any} */ s) => s.item === 'Log link'), { item: 'Log link', value: 'none' });
  assert.deepEqual(data.timeline.map((/** @type {any} */ t) => t.event.split(' — ')[0]), ['Grant started']);
  const log = JSON.parse((await cli(['log', '--run', 'c-json', '--json'])).out);
  assert.deepEqual(log.map((/** @type {any} */ e) => e.event), ['Grant started']);
  const md = await cli(['binnacle', '--run', 'c-json']);
  assert.equal(md.out.startsWith('# Autopilot run c-json\n\n2026-10-06 · owner not set\n\n## Status at a glance\n'), true);
  assert.equal((await cli(['log', '--run', 'c-json'])).out.startsWith('# Autopilot full log c-json\n'), true);
  const both = await cli(['binnacle', '--run', 'c-json', '--json', '--markdown']);
  assert.deepEqual([both.code, both.err], [2, 'autopilot binnacle: give --json or --markdown, not both\n']);
});

test('--markdown writes autopilot-binnacle.md and autopilot-log.md (0600) in the run dir and prints the path', async () => {
  await newRun('c-md');
  const res = await cli(['binnacle', '--run', 'c-md', '--markdown']);
  const dir = runFilesDir('c-md');
  assert.deepEqual([res.code, res.out], [0, `${path.join(dir, 'autopilot-binnacle.md')}\n`]);
  const logRes = await cli(['log', '--run', 'c-md', '--markdown']);
  assert.equal(logRes.out, `${path.join(dir, 'autopilot-log.md')}\n`);
  for (const name of ['autopilot-binnacle.md', 'autopilot-log.md']) assert.equal(statSync(path.join(dir, name)).mode & 0o777, 0o600);
  assert.equal(readFileSync(path.join(dir, 'autopilot-binnacle.md'), 'utf8').includes('## Timeline\n\n- 2026-10-06T20:00:00.000Z — Grant started'), true);
  assert.equal(readFileSync(path.join(dir, 'autopilot-log.md'), 'utf8').includes('| Grant started |'), true);
});

test('--link: http, a non-URL, credentials and a secret-shaped URL are refused (exit 2, nothing stored); https is stored in the grant under a signed autopilot.link row', async () => {
  await newRun('c-link');
  const HTTPS = '--link must be an https URL; http and other schemes are refused';
  const cases = [
    ['http://claude.ai/doc/x', HTTPS],
    ['not a url', '--link must be an https URL, e.g. https://claude.ai/…'],
    ['https://user:pw@claude.ai/x', '--link must not carry credentials'],
    ['ftp://claude.ai/x', HTTPS],
    ['https://claude.ai/x?k=sk-ant-FAKE0123456789abcdefghij', '--link looks like it carries a secret; give a plain URL'],
  ];
  const refused = [];
  for (const [bad] of cases) {
    const r = await cli(['binnacle', '--run', 'c-link', '--link', bad]);
    refused.push([r.code, r.out, r.err]);
  }
  assert.deepEqual(refused, cases.map(([, msg]) => [2, '', `autopilot binnacle: ${msg}\n`]));
  assert.equal(refused.flat().join('').includes('FAKE0123456789'), false);
  assert.equal((await readRun('c-link')).autopilot.link, null);
  assert.equal((await readAllRows('proj')).filter((r) => r.event === 'autopilot.link').length, 0);

  const ok = await cli(['binnacle', '--run', 'c-link', '--link', 'https://claude.ai/code/artifact/abc', '--json']);
  assert.equal(ok.code, 0);
  assert.equal((await readRun('c-link')).autopilot.link, 'https://claude.ai/code/artifact/abc');
  assert.deepEqual(JSON.parse(ok.out).status.find((/** @type {any} */ s) => s.item === 'Log link'), { item: 'Log link', value: 'https://claude.ai/code/artifact/abc' });
  const rows = (await readAllRows('proj')).filter((r) => r.event === 'autopilot.link' && r.run === 'c-link');
  assert.equal(rows.length, 1);
  assert.deepEqual([rows[0].grant_id, rows[0].link], ['ap-c-link', 'https://claude.ai/code/artifact/abc']);
  assert.deepEqual(verifyRow(rows[0], await loadKey('c-link')), { ok: true });
  // status shows it too, and a stopped grant takes no link
  assert.equal((await cli(['status', '--run', 'c-link'])).out.includes('  link      https://claude.ai/code/artifact/abc\n'), true);
  await stopGrant({ runId: 'c-link', now: at(11), writeRow: (row) => appendRow(row, { slug: 'proj' }) });
  const late = await cli(['binnacle', '--run', 'c-link', '--link', 'https://claude.ai/code/artifact/zzz'], { now: at(12) });
  assert.equal(late.code, 1);
  assert.equal((await readRun('c-link')).autopilot.link, 'https://claude.ai/code/artifact/abc');
  assert.equal((await readAllRows('proj')).filter((r) => r.event === 'autopilot.link' && r.run === 'c-link').length, 1); // the stopped grant got none
});

test('both files are rewritten after every autopilot decision; a hook that throws never fails the decision', async () => {
  await newRun('c-hook');
  const dir = runFilesDir('c-hook');
  const f = fakeDeps({ FAKE_ANSWER: JSON.stringify(ANSWER) });
  const ask = { runId: 'c-hook', scope: 'waive:nit', question: 'Waive the nit "rename tmp"?', options: ['waive', 'fix'], subject: { block: 'b1', file: 'src/a.mjs', finding: 'f1' } };
  const first = await askDelegate(ask, { ...f.deps, now: () => at(10) });
  assert.equal(first.acted, true);
  const binnacle = () => readFileSync(path.join(dir, 'autopilot-binnacle.md'), 'utf8');
  const log = () => readFileSync(path.join(dir, 'autopilot-log.md'), 'utf8');
  assert.equal((binnacle().match(/Delegate decided/g) ?? []).length, 1);
  assert.equal(binnacle().includes(`the delegate chose "waive" · b1 · src/a.mjs`), true);
  assert.equal((log().match(/\| Delegate decided \|/g) ?? []).length, 1);
  assert.equal((log().match(/\| Delegate session \|/g) ?? []).length, 1);
  for (const name of ['autopilot-binnacle.md', 'autopilot-log.md']) assert.equal(statSync(path.join(dir, name)).mode & 0o777, 0o600);

  const second = await askDelegate({ ...ask, scope: 'waive:nit' }, { ...f.deps, now: () => at(20) });
  assert.equal(second.acted, true);
  assert.equal((binnacle().match(/Delegate decided/g) ?? []).length, 2);
  assert.equal((log().match(/\| Delegate decided \|/g) ?? []).length, 2);

  const calls = [];
  const third = await askDelegate(ask, { ...f.deps, now: () => at(30), afterDecision: async (/** @type {string} */ id) => { calls.push(id); throw new Error('disk full'); } });
  assert.deepEqual([third.acted, calls], [true, ['c-hook']]);
  assert.equal((await readAllRows('proj')).filter((r) => r.event === 'autopilot.decision' && r.run === 'c-hook').length, 3);
});

test('--link on an expired grant: 0 autopilot.link rows, exit 1, the expire row is written once', async () => {
  await newRun('c-exp');
  const res = await cli(['binnacle', '--run', 'c-exp', '--link', 'https://claude.ai/code/artifact/abc'], { now: at(150) });
  assert.equal(res.code, 1);
  assert.equal(res.err, `autopilot binnacle: run c-exp's autopilot grant already expired at ${UNTIL.replace('Z', '.000Z')}\n`);
  const rows = (await readAllRows('proj')).filter((r) => r.run === 'c-exp');
  assert.equal(rows.filter((r) => r.event === 'autopilot.link').length, 0);
  assert.equal(rows.filter((r) => r.event === 'autopilot.expire').length, 1);
  assert.equal((await readRun('c-exp')).autopilot.link, null);
});

test('paused categories come from the active grant when there is one (ledger pause rows only when there is no grant)', () => {
  const pauseRow = { run: 'r5', event: 'autopilot.pause', category: 'coding', spent_usd: 18, cap_usd: 20, ts: T(1) };
  const grant = { grant_id: 'g', status: 'active', scopes: [], deny: [], delegate: 'L2', until: T(120), caps: {}, started_at: T(0), link: null };
  const withGrant = buildBinnacle({ runId: 'r5', rows: [pauseRow], record: { run_id: 'r5', status: 'active', blocks: {}, autopilot: grant }, now: NOW, scrubCtx: SC });
  assert.deepEqual([withGrant.open_questions, withGrant.owner_actions], [[], []]);
  const paused = buildBinnacle({ runId: 'r5', rows: [pauseRow], record: { run_id: 'r5', status: 'active', blocks: {}, autopilot: { ...grant, paused: { review: { at: T(2), spent_usd: 4, cap_usd: 5 } } } }, now: NOW, scrubCtx: SC });
  assert.deepEqual(paused.open_questions, ['Budget review stopped at 4.00 of 5.00 USD: raise the cap or leave it?']);
  assert.deepEqual(paused.owner_actions.map((a) => a.text), ['Budget review is paused: raise the cap or leave it (only you can raise a budget)']);
  const noGrant = buildBinnacle({ runId: 'r5', rows: [pauseRow], record: { run_id: 'r5', status: 'active', blocks: {} }, now: NOW, scrubCtx: SC });
  assert.deepEqual(noGrant.owner_actions.map((a) => a.text), ['Budget coding is paused: raise the cap or leave it (only you can raise a budget)']);
});

test('critical findings: one owner action per (block, file); the latest review.done decides and a later clean review clears it', () => {
  const done = (/** @type {string} */ file, /** @type {number} */ critical, /** @type {number} */ m, block = 'b1') => ({ run: 'r6', event: 'review.done', block, file, findings_by_severity: { critical }, round: 1, ts: T(m) });
  const rows = [done('a.mjs', 1, 1), done('a.mjs', 1, 2), done('b.mjs', 1, 3), done('b.mjs', 0, 4), done('c.mjs', 2, 5), { run: 'r6', event: 'review.approved', block: 'b1', file: 'c.mjs', ts: T(6) }, done('a.mjs', 1, 7, 'b2')];
  const b = buildBinnacle({ runId: 'r6', rows, record: { run_id: 'r6', status: 'active', blocks: {} }, now: NOW, scrubCtx: SC });
  assert.deepEqual(b.owner_actions.map((a) => a.text), [
    'Decide the critical finding in a.mjs (block b1): critical findings are never delegated',
    'Decide the critical finding in a.mjs (block b2): critical findings are never delegated',
  ]);
});

test('no text says undefined or null: rows missing their optional fields get a fallback', () => {
  const bare = ['autopilot.grant', 'autopilot.stop', 'autopilot.expire', 'autopilot.link', 'autopilot.pause', 'autopilot.approve', 'autopilot.restore', 'autopilot.restore_skipped', 'autopilot.extra_round', 'autopilot.level', 'autopilot.issue_failed', 'review.waived', 'block.close', 'review.session_timeout', 'review.schema_fallback', 'worker.down', 'ledger.tamper', 'budget.refused', 'gate.red', 'gate.done'].map((event, i) => ({ run: 'r7', event, ...(event === 'review.waived' ? { by: 'autopilot' } : {}), ts: T(i) }));
  const rows = [...bare, { run: 'r7', event: 'autopilot.decision', acted: true, ts: T(30) }, { run: 'r7', event: 'autopilot.decision', acted: false, ts: T(31) }, { run: 'r7', event: 'session', role: 'delegate', ts: T(32) }];
  const b = buildBinnacle({ runId: 'r7', rows, record: { run_id: 'r7', status: 'active', blocks: { x: {} } }, now: NOW, scrubCtx: SC });
  const all = [JSON.stringify(b), renderBinnacleMarkdown(b, SC), JSON.stringify(buildFullLog({ runId: 'r7', rows, scrubCtx: SC }).map((e) => ({ ...e, block: e.block ?? '', file: e.file ?? '', link: e.link ?? '' })))].join('\n');
  assert.equal(all.includes('undefined'), false);
  assert.equal(renderBinnacleMarkdown(b, SC).includes('null'), false);
  assert.equal(buildFullLog({ runId: 'r7', rows, scrubCtx: SC }).length, 23);
});

test('B55: review.schema_fallback is an incident and a log entry, like review.session_timeout; a refused one says why and what to do', () => {
  const base = { run: 'r9', event: 'review.schema_fallback', block: 'b1', file: 'src/a.swift', lens: 'quick', role: 'reviewer', level: 'L2', packet_hash: 'ab', from_provider: 'anthropic', from_model: 'model-a', to: 'review.second_levels.L2', to_provider: 'xai', to_model: 'model-b', attempts: 2 };
  const rows = [
    { ...base, ts: T(1) },
    { ...base, to_provider: 'openai', to_model: 'model-c', refused: 'closed-book', ts: T(2) },
    { ...base, to_provider: null, to_model: null, refused: 'same-model', ts: T(3) },
    { ...base, refused: 'budget', ts: T(4) },
    { ...base, refused: 'other-reason', ts: T(5) },
    { ...base, refused: true, ts: T(6) },
  ];
  const b = buildBinnacle({ runId: 'r9', rows, record: { run_id: 'r9', status: 'active', blocks: { b1: {} } }, now: NOW, scrubCtx: SC });
  const what = 'A quick answer failed the schema twice on the same packet (model-a)';
  assert.deepEqual(b.incidents, [
    { time: T(1), what, effect: 'It was retried once on the second level (model-b).', fix: "Check the file's next review result." },
    { time: T(2), what, effect: 'The second-level try was refused (closed-book); the review stays unavailable.', fix: 'Set a second level that can run closed-book (not Codex), or review the file by hand.' },
    { time: T(3), what, effect: 'The second-level try was refused (same-model); the review stays unavailable.', fix: 'Set review.second_levels to a different model than the one that failed, or review the file by hand.' },
    { time: T(4), what, effect: 'The second-level try was refused (budget); the review stays unavailable.', fix: 'Raise the budget (only you can) or review the file by hand.' },
    { time: T(5), what, effect: 'The second-level try was refused (other-reason); the review stays unavailable.', fix: 'Review the file by hand.' },
    { time: T(6), what, effect: 'It was retried once on the second level (model-b).', fix: "Check the file's next review result." },
  ]);
  assert.deepEqual(buildFullLog({ runId: 'r9', rows, scrubCtx: SC }), [
    { time: T(6), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → model-b · retried once', link: null },
    { time: T(5), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → model-b · refused (other-reason)', link: null },
    { time: T(4), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → model-b · refused (budget)', link: null },
    { time: T(3), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → unknown model · refused (same-model)', link: null },
    { time: T(2), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → model-c · refused (closed-book)', link: null },
    { time: T(1), event: 'Review answer off-schema twice', block: 'b1', file: 'src/a.swift', detail: 'quick · model-a → model-b · retried once', link: null },
  ]);
});

test('a pipe in a shown text reaches its table cell escaped, so the row keeps its column count', () => {
  const rows = [{ run: 'r8', event: 'review.waived', block: 'b1', file: 'a|b.mjs', finding: 'f1', reason: 'keep a | b', by: 'autopilot', grant_id: 'g', decision_id: 'ad-1', severity: 'nit', issue: null, ts: T(1) }];
  const md = renderBinnacleMarkdown(buildBinnacle({ runId: 'r8', rows, record: { run_id: 'r8', status: 'active', blocks: {} }, now: NOW, scrubCtx: SC }), SC);
  const line = md.split('\n').find((l) => l.includes('Waived f1'));
  assert.equal(line, `| ${T(1)} | Waived f1 (nit) in a\\|b.mjs, block b1 | keep a \\| b (decision ad-1) | Reopen the finding: fix it in a\\|b.mjs, then delete the waiver issue (none was opened) |`);
  assert.equal(line.split(/(?<!\\)\|/).length - 2, 4); // 4 cells between the unescaped pipes
  const log = renderLogMarkdown({ runId: 'r8', entries: buildFullLog({ runId: 'r8', rows, scrubCtx: SC }) }, SC);
  assert.equal(log.includes('| Waiver by autopilot | b1 | a\\|b.mjs |'), true);
});

test('the byline owner is the workspace git user.name; no repo or no name gives "owner not set"', async () => {
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('GIT_')));
  await newRun('c-own');
  const workspace = path.join(WORK, 'ws-c-own');
  assert.equal(JSON.parse((await cli(['binnacle', '--run', 'c-own', '--json'])).out).byline.owner, null);
  assert.equal((await cli(['binnacle', '--run', 'c-own'])).out.split('\n')[2], '2026-10-06 · owner not set');
  execFileSync('git', ['init', '-q'], { cwd: workspace, env });
  execFileSync('git', ['config', 'user.name', 'Fixture Owner'], { cwd: workspace, env });
  assert.equal(JSON.parse((await cli(['binnacle', '--run', 'c-own', '--json'])).out).byline.owner, 'Fixture Owner');
  assert.equal((await cli(['binnacle', '--run', 'c-own'])).out.split('\n')[2], '2026-10-06 · Fixture Owner');
  await cli(['binnacle', '--run', 'c-own', '--markdown']);
  assert.equal(readFileSync(path.join(runFilesDir('c-own'), 'autopilot-binnacle.md'), 'utf8').split('\n')[2], '2026-10-06 · Fixture Owner');
});

test('a failed write removes its temp file and throws; the other file is untouched', async () => {
  await newRun('c-fail');
  const dir = runFilesDir('c-fail');
  mkdirSync(path.join(dir, 'autopilot-binnacle.md'), { recursive: true }); // a directory where the file goes: the rename fails
  const record = await readRun('c-fail');
  assert.throws(() => writeAutopilotFiles({ runId: 'c-fail', rows: [], record, now: NOW }));
  assert.deepEqual(readdirSync(dir).sort(), ['autopilot-binnacle.md']);
});

test('refreshAutopilotFiles scrubs the run workspace as <project>, from any cwd; a malformed scopes list and an empty pr_url do not throw or hide the PR', async () => {
  await newRun('c-ws');
  const workspace = path.join(WORK, 'ws-c-ws');
  const elsewhere = path.join(WORK, 'elsewhere');
  mkdirSync(elsewhere, { recursive: true });
  const rowsOf = (/** @type {Record<string, any>[]} */ extra) => [
    { run: 'c-ws', event: 'autopilot.grant', grant_id: 'g1', scopes: 'waive:nit', delegate: 'L2', until: UNTIL, ts: T0.toISOString() },
    { run: 'c-ws', event: 'worker.down', reason: `cannot start ${workspace}/bin`, ts: at(1).toISOString() },
    { run: 'c-ws', event: 'block.close', block: 'b1', status: 'complete', pr_url: '', pr: 'https://github.com/o/r/pull/5', commit: '', sha: 'abcdef1234567', ts: at(2).toISOString() },
    ...extra,
  ];
  const record = { ...(await readRun('c-ws')), autopilot: { ...(await readRun('c-ws')).autopilot, scopes: { not: 'a list' } }, blocks: { b1: { status: 'closed' } } };
  const back = process.cwd();
  process.chdir(elsewhere);
  try {
    const files = writeAutopilotFiles({ runId: 'c-ws', rows: rowsOf([{ run: 'c-ws', event: 'autopilot.decision', acted: false, scope: 'waive:nit', question: `look in ${workspace}/src`, owner_reason: 'low-confidence', ts: at(3).toISOString() }]), record, now: NOW });
    const md = readFileSync(files.binnacle, 'utf8') + readFileSync(files.log, 'utf8');
    assert.equal(md.includes(workspace), false);
    assert.equal(md.includes('<project>/src'), true);
  } finally {
    process.chdir(back);
  }
  const b = buildBinnacle({ runId: 'c-ws', rows: rowsOf([]), record, now: NOW, scrubCtx: SC });
  assert.deepEqual(b.status.find((s) => s.item === 'Stack/PRs'), { item: 'Stack/PRs', value: 'https://github.com/o/r/pull/5' });
  assert.equal(b.status.find((s) => s.item === 'Autopilot window')?.value.endsWith('allow '), true);
  assert.deepEqual(b.blocks[0].branch, 'commit abcdef1 · https://github.com/o/r/pull/5');
  assert.equal(b.owner_actions.at(-1)?.text, 'Mark block b1 ready and merge it (https://github.com/o/r/pull/5)');
  assert.deepEqual(buildFullLog({ runId: 'c-ws', rows: rowsOf([]), scrubCtx: SC })[0], { time: at(2).toISOString(), event: 'Block closed', block: 'b1', file: null, detail: 'status complete · commit abcdef1', link: 'https://github.com/o/r/pull/5' });
  assert.equal(buildFullLog({ runId: 'c-ws', rows: rowsOf([]), scrubCtx: SC }).at(-1)?.detail, `grant g1 · delegate L2 · allow  · until ${UNTIL}`);
  assert.equal(JSON.stringify(b).includes('cannot start'), false); // the worker row's reason text stays out
});
