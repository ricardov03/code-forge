/**
 * B38: `.github/workflows/error-triage.yml` — the exact triggers, permissions and job conditions,
 * no shell step and no expression inside a script, and both github-script scripts run here
 * against a fake `github`/`core`/`require` (never the network): fingerprint labels, the known-fix
 * comment written once (its own marker), the body treated as capped data, and the weekly job that
 * only reads issues and writes the job summary.
 */

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';
import { parse } from 'yaml';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const RAW = readFileSync(path.join(REPO, '.github', 'workflows', 'error-triage.yml'), 'utf8');
const WF = parse(RAW);
const MARKER = '<!-- code-forge-triage: known-fix -->';
const BOT = 'github-actions[bot]';
const AsyncFunction = /** @type {any} */ (async () => {}).constructor;

/** @param {string} job @returns {string} the job's github-script script */
function scriptOf(job) {
  const steps = WF.jobs[job].steps.filter((s) => s.uses === 'actions/github-script@v7');
  assert.equal(steps.length, 1);
  return steps[0].with.script;
}

/** @param {{labels?: string[], comments?: any[], issues?: any[][]}} [o] */
function fakeGithub(o = {}) {
  const existing = new Set(o.labels ?? []);
  const comments = o.comments ?? [];
  /** @type {any[]} */
  const calls = [];
  const issues = {
    getLabel: async (/** @type {any} */ p) => {
      calls.push(['getLabel', p.name]);
      if (!existing.has(p.name)) throw Object.assign(new Error('Not Found'), { status: 404 });
      return {};
    },
    createLabel: async (/** @type {any} */ p) => (calls.push(['createLabel', p.name]), existing.add(p.name), {}),
    addLabels: async (/** @type {any} */ p) => (calls.push(['addLabels', p.issue_number, p.labels]), {}),
    listComments: async () => ({}),
    createComment: async (/** @type {any} */ p) => {
      calls.push(['createComment', p.issue_number, p.body]);
      comments.push({ user: { login: BOT, type: 'Bot' }, body: p.body });
      return {};
    },
    listForRepo: async () => ({}),
  };
  const paginate = async (/** @type {any} */ fn) => {
    calls.push(['paginate', fn === issues.listComments ? 'listComments' : 'other']);
    return fn === issues.listComments ? [...comments] : [];
  };
  paginate.iterator = async function* (/** @type {any} */ fn, /** @type {any} */ params) {
    calls.push(['iterator', fn === issues.listForRepo ? 'listForRepo' : 'other', params]);
    for (const page of o.issues ?? []) yield { data: page };
  };
  return { github: { rest: { issues }, paginate }, calls, comments };
}

function fakeCore() {
  /** @type {any[]} */
  const summary = [];
  const s = {
    addHeading: (/** @type {string} */ t, /** @type {number} */ l) => (summary.push(['heading', t, l]), s),
    addRaw: (/** @type {string} */ t, /** @type {boolean} */ eol) => (summary.push(['raw', t, eol]), s),
    addTable: (/** @type {any[]} */ rows) => (summary.push(['table', rows]), s),
    write: async () => (summary.push(['write']), s),
  };
  /** @type {string[]} */
  const warnings = [];
  return { core: { info: () => {}, warning: (/** @type {string} */ m) => warnings.push(m), summary: s }, summary, warnings };
}

/** @param {string} tableJson @returns {(m: string) => any} */
const fakeRequire = (tableJson) => (m) => {
  if (m !== 'fs') throw new Error(`unexpected require ${m}`);
  return { readFileSync: (/** @type {string} */ f) => (f === 'src/util/known-fixes.json' ? tableJson : assert.fail(`read ${f}`)) };
};

const CONTEXT = (/** @type {any} */ issue) => ({ repo: { owner: 'ricardov03', repo: 'code-forge' }, payload: { issue } });
const TABLE = JSON.stringify([{ fp: '0123456789ab', fixed_in: '0.4.0', summary: 'review no longer hangs on an empty diff.', issue: 42 }]);

/** @param {any} issue @param {{gh: any, table?: string}} o */
async function triage(issue, o) {
  const { core, warnings } = fakeCore();
  await new AsyncFunction('github', 'context', 'core', 'require', scriptOf('triage'))(o.gh.github, CONTEXT(issue), core, fakeRequire(o.table ?? TABLE));
  return { warnings };
}

const BODY = 'details\n\n### Fingerprint\n\nFingerprint: 0123456789ab\n\n<!-- code-forge-fp: 0123456789ab -->\n<!-- code-forge-fp: ffffffffffff -->\n<!-- code-forge-fp: 0123456789AB -->\n<!-- code-forge-fp: 0123456789ab -->\n';

describe('.github/workflows/error-triage.yml (B38)', () => {
  test('triggers, permissions, job conditions; no shell step, no expression in a script', () => {
    assert.deepEqual(WF.on, { issues: { types: ['opened', 'edited'] }, schedule: [{ cron: '0 7 * * 1' }] });
    assert.deepEqual(WF.permissions, {});
    assert.deepEqual(Object.keys(WF.jobs), ['triage', 'weekly']);
    assert.deepEqual(WF.jobs.triage.permissions, { contents: 'read', issues: 'write' });
    assert.deepEqual(WF.jobs.weekly.permissions, { issues: 'read' });
    assert.equal(WF.jobs.triage.if, "github.event_name == 'issues' && contains(github.event.issue.labels.*.name, 'error-report')");
    assert.equal(WF.jobs.weekly.if, "github.event_name == 'schedule'");
    assert.deepEqual(WF.jobs.triage.steps[0], { uses: 'actions/checkout@v4', with: { 'persist-credentials': false, 'sparse-checkout': 'src/util/known-fixes.json', 'sparse-checkout-cone-mode': false } });
    for (const job of ['triage', 'weekly']) {
      assert.equal(WF.jobs[job].steps.filter((s) => s.run !== undefined).length, 0, job);
      assert.equal(scriptOf(job).includes('${{'), false, job);
    }
    for (const word of ['github.event.issue.body', 'github.event.issue.title', 'github.event.comment']) assert.equal(RAW.includes(word), false, word);
    assert.equal(scriptOf('triage').split(MARKER).length - 1, 1);
    // the weekly job never writes to an issue
    assert.deepEqual(scriptOf('weekly').match(/\brest\.\w+\.\w+/g), ['rest.issues.listForRepo']);
    assert.equal((scriptOf('weekly').match(/\b(?:create|add|update|remove|delete|lock|unlock)(?:Comment|Label|Labels|Issue|Assignees)\b/g) ?? []).length, 0);
  });

  test('triage: one fp label per distinct lowercase fingerprint, the known-fix comment once; a second run writes nothing', async () => {
    const gh = fakeGithub({ labels: ['fp:fffffff'] });
    const issue = { number: 9, body: BODY, labels: [{ name: 'error-report' }, { name: 'kind:op_timeout' }] };
    await triage(issue, { gh });
    const comment = [
      MARKER,
      'Known issue: fixed in 0.4.0 (#42): review no longer hangs on an empty diff.',
      '',
      'Upgrade with: `npm install -g @codedology/code-forge@latest`',
      '',
      'If this still happens on that version or newer, say so here: it may be a regression.',
    ].join('\n');
    assert.deepEqual(gh.calls, [
      ['getLabel', 'fp:0123456'],
      ['createLabel', 'fp:0123456'],
      ['getLabel', 'fp:fffffff'],
      ['addLabels', 9, ['fp:0123456', 'fp:fffffff']],
      ['paginate', 'listComments'],
      ['createComment', 9, comment],
    ]);
    gh.calls.length = 0;
    await triage({ ...issue, labels: [...issue.labels, { name: 'fp:0123456' }, { name: 'fp:fffffff' }] }, { gh });
    assert.deepEqual(gh.calls, [['paginate', 'listComments']]);
  });

  test('triage: no marker → 0 calls; an unknown fingerprint → label only; a marker past 64 KB is not read', async () => {
    let gh = fakeGithub();
    await triage({ number: 1, body: 'no fingerprint here', labels: [] }, { gh });
    assert.deepEqual(gh.calls, []);
    gh = fakeGithub({ labels: ['fp:aaaaaaa'] });
    await triage({ number: 2, body: '<!-- code-forge-fp: aaaaaaaaaaaa -->', labels: [] }, { gh });
    assert.deepEqual(gh.calls, [['getLabel', 'fp:aaaaaaa'], ['addLabels', 2, ['fp:aaaaaaa']]]);
    gh = fakeGithub();
    await triage({ number: 3, body: `${'x'.repeat(65536)}<!-- code-forge-fp: 0123456789ab -->`, labels: [] }, { gh });
    assert.deepEqual(gh.calls, []);
  });

  test('triage: the marker in a comment by someone else does not count; an unreadable table only warns', async () => {
    const gh = fakeGithub({ labels: ['fp:0123456'], comments: [{ user: { login: 'someone', type: 'User' }, body: MARKER }] });
    await triage({ number: 4, body: '<!-- code-forge-fp: 0123456789ab -->', labels: [{ name: 'fp:0123456' }] }, { gh });
    assert.deepEqual(gh.calls.map((c) => c[0]), ['paginate', 'createComment']);
    const quiet = fakeGithub({ labels: ['fp:0123456'] });
    const { warnings } = await triage({ number: 5, body: '<!-- code-forge-fp: 0123456789ab -->', labels: [{ name: 'fp:0123456' }] }, { gh: quiet, table: '[{' });
    assert.deepEqual([quiet.calls, warnings], [[], ['src/util/known-fixes.json could not be read; no known-fix check']]);
  });

  test('weekly: counts open error-report issues by kind and fingerprint label into the job summary; pull requests are skipped', async () => {
    const L = (/** @type {string[]} */ names) => names.map((name) => ({ name }));
    const gh = fakeGithub({
      issues: [
        [
          { number: 1, labels: L(['error-report', 'kind:op_timeout', 'fp:0123456']) },
          { number: 2, labels: L(['error-report', 'kind:op_timeout', 'fp:0123456']) },
          { number: 3, labels: L(['error-report', 'kind:crash', 'fp:fffffff']) },
        ],
        [{ number: 4, labels: L(['error-report']) }, { number: 5, pull_request: {}, labels: L(['error-report', 'kind:crash']) }],
      ],
    });
    const { core, summary } = fakeCore();
    await new AsyncFunction('github', 'context', 'core', 'require', scriptOf('weekly'))(gh.github, CONTEXT(undefined), core, fakeRequire('[]'));
    assert.deepEqual(gh.calls, [['iterator', 'listForRepo', { owner: 'ricardov03', repo: 'code-forge', state: 'open', labels: 'error-report', per_page: 100 }]]);
    const H = (/** @type {string} */ a) => [{ data: a, header: true }, { data: 'open issues', header: true }];
    assert.deepEqual(summary, [
      ['heading', 'Open error reports', 2],
      ['raw', '4 open issue(s) labelled error-report.', true],
      ['heading', 'By kind', 3],
      ['table', [H('kind label'), ['kind:op_timeout', '2'], ['(no kind label)', '1'], ['kind:crash', '1']]],
      ['heading', 'By fingerprint', 3],
      ['table', [H('fingerprint label'), ['fp:0123456', '2'], ['(no fingerprint label)', '1'], ['fp:fffffff', '1']]],
      ['write'],
    ]);
  });
});
