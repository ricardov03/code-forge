import { commitAll, freshDir, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import path from 'node:path';
import { test } from 'node:test';

// B58 (issue #11): `review.external.required: true`, set by the owner before `run start`, is a
// non-interactive way to close a block without the local per-file review when an outside reviewer
// reviews it. These tests pin the rows, exit codes and messages of every rule of the block.

const { runRun } = await import('../../src/cli/run.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { readRun, reloadRun, saveRun, writeSigned } = await import('../../src/state/run.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');
const { contentHash, ticketId } = await import('../../src/worker/ticket.mjs');
/** B96: the line a passing close prints for `file` of block B1 at its current content */
const rv = (/** @type {string} */ root, /** @type {string} */ run, /** @type {string} */ file) => `reviewed ${file} ticket ${ticketId({ run, block: 'B1', file, content_hash: contentHash(root, file) })}\n`;
const { validateConfig } = await import('../../src/config/validate.mjs');
const { initialConfigState } = await import('../../src/state/config-snapshot.mjs');

const T0 = new Date('2026-10-07T10:00:00.000Z');
const PROBE = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
const EXTERNAL = 'review:\n  external: {required: true, name: Grok}\n';

/** @param {string} slug @param {string} [extra] - YAML appended to the base config. */
const configYaml = (slug, extra = '') =>
  [
    'version: 1',
    'project:',
    `  slug: ${slug}`,
    'provider: anthropic',
    'engine: harness',
    'levels:',
    '  L0: {model: claude-haiku-4-5-20251001}',
    '  L1: {model: claude-sonnet-5}',
    '  L2: {model: claude-opus-5-5}',
    '  L3: {model: claude-fable-5-1}',
    '',
  ].join('\n') + extra;

/** @returns {{write: (s: string) => boolean, text: string}} */
function capture() {
  let text = '';
  return {
    write(s) {
      text += s;
      return true;
    },
    get text() {
      return text;
    },
  };
}

/** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string]>} */
async function cli(fn, args, deps = {}) {
  const stream = capture();
  const code = await fn(args, { stdout: stream, stderr: stream, probe: PROBE, now: () => T0, ...deps });
  return [code, stream.text];
}

const ACCEPTANCE = freshDir('acceptance');
writeFile(ACCEPTANCE, 'acc.json', JSON.stringify([{ clause: 'c', tests: ['t'] }]));
const ACC = path.join(ACCEPTANCE, 'acc.json');

/**
 * A repo with `.code-forge.yml` (`extra` appended), `src/a.mjs`, a transcript and a README, all
 * committed; `run start` through the CLI with a pinned worker. `ws` is a subdirectory of the
 * repository when given (the config lives there).
 * @param {string} slug @param {string} runId @param {{extra?: string, ws?: string}} [opts]
 */
async function fixture(slug, runId, { extra = EXTERNAL, ws = '' } = {}) {
  const repo = await makeRepo();
  const root = path.join(repo, ws);
  writeFile(root, '.code-forge.yml', configYaml(slug, extra));
  writeFile(root, 'src/a.mjs', 'export const a = 1;\n');
  writeFile(root, 'coder.log', '$ node --test\n');
  writeFile(root, 'README.md', '# demo\n');
  await commitAll(repo);
  assert.deepEqual(await cli(runRun, ['start', '--cwd', root, '--run', runId, '--worker-pid', '4242']), [0, `run ${runId} started · engine harness · worker pid 4242\n`]);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  /** @param {string} id @param {string[]} owned */
  const open = (id, owned) => cli(runBlock, ['open', id, '--run', runId, '--level', 'L2', '--owned', ...owned, '--acceptance', ACC]);
  /** @param {string[]} more @param {Record<string, any>} [deps] */
  const close = (more = [], deps = {}) => cli(runBlock, ['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', `${root}/coder.log`, ...more], deps);
  const rows = async () => readAllRows(slug);
  return { repo, root, writeRow, open, close, rows };
}

/** @param {Array<Record<string, any>>} rows */
const waived = (rows) => rows.filter((r) => r.event === 'gate.reviews_waived');
/** @param {Record<string, any>} row */
const strip = ({ mac: _m, ts: _t, ...rest }) => rest;

test('B58 (3): run start writes one signed review.external row naming the reviewer — only when required is true', async () => {
  const on = await fixture('b58-start-on', 'r-b58-start-on');
  const rows = await on.rows();
  assert.deepEqual(rows.map((r) => r.event), ['run.start', 'review.external']);
  assert.deepEqual(strip(rows[1]), { run: 'r-b58-start-on', event: 'review.external', required: true, reviewer: 'Grok', tokens_source: null, cost_source: null });
  assert.deepEqual(verifyRow(rows[1], await loadKey('r-b58-start-on')), { ok: true });

  const off = await fixture('b58-start-off', 'r-b58-start-off', { extra: 'review:\n  external: {required: false, name: Grok}\n' });
  assert.deepEqual((await off.rows()).map((r) => r.event), ['run.start']);
  const none = await fixture('b58-start-none', 'r-b58-start-none', { extra: '' });
  assert.deepEqual((await none.rows()).map((r) => r.event), ['run.start']);
});

test('B58 (1): the schema refuses an unknown key, a non-boolean required and an empty or blank name; required:false needs no name', () => {
  const levels = { L0: { model: 'claude-haiku-4-5-20251001' }, L1: { model: 'claude-sonnet-5' }, L2: { model: 'claude-opus-5-5' }, L3: { model: 'claude-fable-5-1' } };
  const base = { version: 1, project: { slug: 'b58-schema' }, provider: 'anthropic', engine: 'harness', levels };
  /** @param {Record<string, any>} external */
  const issues = (external) => validateConfig({ ...base, review: { external } }).errors.map((e) => [e.rule, e.path]);
  assert.deepEqual(issues({ required: true, name: 'Grok', by: 'me' }), [['schema', '/review/external']]);
  assert.deepEqual(issues({ required: 'yes', name: 'Grok' }), [['schema', '/review/external/required']]);
  assert.deepEqual(issues({ required: true, name: '' }), [['schema', '/review/external/name'], ['schema', '/review/external/name']]); // minLength and pattern
  assert.deepEqual(issues({ required: true, name: '   ' }), [['schema', '/review/external/name']]);
  assert.deepEqual(issues({ required: true, name: 'Grok' }), []);
  assert.deepEqual(issues({ name: 'Grok' }), []); // required defaults to false
  assert.deepEqual(issues({ required: false }), []);
  assert.deepEqual(issues({}), []);
});

test('B58 (1): run start refuses review.external.required true with no name, naming the key path only — nothing is written', async () => {
  for (const [slug, extra] of [
    ['b58-noname', 'review:\n  external: {required: true}\n'],
    ['b58-blank', "review:\n  external: {required: true, name: '  '}\n"],
  ]) {
    const repo = await makeRepo();
    writeFile(repo, '.code-forge.yml', configYaml(slug, extra));
    await commitAll(repo);
    assert.deepEqual(await cli(runRun, ['start', '--cwd', repo, '--run', `r-${slug}`, '--worker-pid', '4242']), [
      1,
      'run start: review.external.required is true but review.external.name is missing or blank\n',
    ]);
    await assert.rejects(readRun(`r-${slug}`), { code: 'no-run' });
    assert.deepEqual(await readAllRows(slug), []);
  }
});

const IMMUTABLE = 'run reload: review.external cannot change mid-run (the outside reviewer is fixed by the owner before the run starts) — put it back, or end the run and start a new one\n';

test('B58 (3): run reload refuses to add, remove or change review.external, naming the key path only', async () => {
  const on = await fixture('b58-reload', 'r-b58-reload');
  const before = await readRun('r-b58-reload');
  for (const extra of ['review:\n  external: {required: true, name: Codex}\n', 'review:\n  external: {required: false, name: Grok}\n', '']) {
    writeFile(on.root, '.code-forge.yml', configYaml('b58-reload', extra));
    assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b58-reload']), [1, IMMUTABLE]);
  }
  assert.deepEqual((await readRun('r-b58-reload')).config, before.config);
  // another key may still change
  writeFile(on.root, '.code-forge.yml', configYaml('b58-reload', `${EXTERNAL}  session_timeout_s: 77\n`));
  const [code, text] = await cli(runRun, ['reload', '--run', 'r-b58-reload']);
  assert.equal(code, 0, text);
  assert.ok(text.startsWith('run r-b58-reload: config reloaded · 1 key changed\n  review.session_timeout_s\n'), text);
  const { config: after } = await readRun('r-b58-reload');
  assert.deepEqual(after.snapshots[after.hash].review.external, { required: true, name: 'Grok' });

  const none = await fixture('b58-reload-add', 'r-b58-reload-add', { extra: '' });
  writeFile(none.root, '.code-forge.yml', configYaml('b58-reload-add', EXTERNAL));
  assert.deepEqual(await cli(runRun, ['reload', '--run', 'r-b58-reload-add']), [1, IMMUTABLE]);
  assert.deepEqual((await none.rows()).map((r) => r.event), ['run.start']);
});

test('B58 (3): a run recorded before snapshots cannot gain review.external at its first reload', async () => {
  const none = await fixture('b58-legacy', 'r-b58-legacy', { extra: '' });
  const record = await readRun('r-b58-legacy');
  delete record.config;
  await saveRun(record);
  const config = { version: 1, provider: 'anthropic', engine: 'harness', review: { external: { required: true, name: 'Grok' } } };
  await assert.rejects(reloadRun({ runId: 'r-b58-legacy', config, writeRow: none.writeRow }), {
    code: 'immutable-key',
    message: 'review.external cannot change mid-run (the outside reviewer is fixed by the owner before the run starts) — put it back, or end the run and start a new one',
  });
  assert.equal((await readRun('r-b58-legacy')).config, undefined);
  // a key that leaves the switch off (`required` false or absent, the schema default) is no change
  for (const external of [{ required: false, name: 'Grok' }, { name: 'Grok' }]) {
    const legacy = await readRun('r-b58-legacy');
    delete legacy.config;
    await saveRun(legacy);
    const off = { version: 1, provider: 'anthropic', engine: 'harness', review: { external } };
    const reloaded = await reloadRun({ runId: 'r-b58-legacy', config: off, writeRow: none.writeRow });
    assert.equal(reloaded.changed, null);
    const { config: state } = await readRun('r-b58-legacy');
    assert.deepEqual(state.snapshots[state.hash].review.external, external);
  }
});

/** @param {[number, string]} result */
const opened = ([code, text]) => {
  assert.equal(code, 0, text);
  assert.match(text, /^block B1 open · L2 · code · attempt 1 · base [0-9a-f]{12}\nbrief: none given \(--brief <file>\)\n$/);
};

test('B58 (1): with review.external required, close skips the per-file review and the --report ticket check — no TTY — and writes one signed gate.reviews_waived {by: config} row before the close', async () => {
  const fx = await fixture('b58-close', 'r-b58-close');
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n'); // changed, never reviewed
  const report = path.join(freshDir('report'), 'report.md');
  writeFile(path.dirname(report), 'report.md', 'done, no tickets\n');
  assert.deepEqual(await fx.close(['--report', report], { isTTY: false }), [0, 'block B1 closed\n']);
  const rows = await fx.rows();
  assert.deepEqual(waived(rows).map(strip), [{ run: 'r-b58-close', event: 'gate.reviews_waived', block: 'B1', by: 'config', reviewer: 'Grok', tokens_source: null, cost_source: null }]);
  assert.deepEqual(verifyRow(waived(rows)[0], await loadKey('r-b58-close')), { ok: true });
  const events = rows.map((r) => r.event);
  assert.ok(events.indexOf('gate.reviews_waived') < events.indexOf('block.close'), events.join(','));
  assert.equal((await readRun('r-b58-close')).blocks.B1.status, 'closed');
});

test('B58 (6): the same close without the key is refused as before (unreviewed) and writes no waiver; once approved, a report with no ticket lines closes (B96)', async () => {
  const fx = await fixture('b58-control', 'r-b58-control', { extra: 'review:\n  external: {required: false, name: Grok}\n' });
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close([], { isTTY: false }), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  await writeSigned('r-b58-control', fx.writeRow, { event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: contentHash(fx.root, 'src/a.mjs'), round: 1 });
  const report = path.join(freshDir('report'), 'report.md');
  writeFile(path.dirname(report), 'report.md', 'done, no tickets\n');
  assert.deepEqual(await fx.close(['--report', report]), [0, `${rv(fx.root, 'r-b58-control', 'src/a.mjs')}block B1 closed\n`]);
  assert.deepEqual(waived(await fx.rows()), []);
});

test('B58 (1): the config way out works under an active autopilot grant', async () => {
  const fx = await fixture('b58-autopilot', 'r-b58-autopilot');
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  await startGrant({ runId: 'r-b58-autopilot', input: { until: '2026-10-07T12:00:00Z', delegate: 'L2', allow: 'waive:nit' }, now: T0, writeRow: fx.writeRow, grantId: 'ap-b58' });
  assert.deepEqual(await fx.close([]), [0, 'block B1 closed\n']);
  assert.deepEqual(waived(await fx.rows()).map((r) => [r.by, r.reviewer]), [['config', 'Grok']]);
});

test('B58 (2): unowned changes, no_changes and the transcript grep still refuse under review.external', async () => {
  const fx = await fixture('b58-still', 'r-b58-still');
  opened(await fx.open('B1', ['src/a.mjs', 'coder.log']));
  // nothing changed and nothing reviewed is never a pass
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: no_changes\n']);
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  writeFile(fx.root, 'README.md', '# demo, edited\n');
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: unowned_change README.md\n']);
  writeFile(fx.root, 'README.md', '# demo\n');
  writeFile(fx.root, 'coder.log', '$ node --test\n$ forge block close B1 --no-require-reviews\n');
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: rule_break transcript line 2: code-forge-no-require-reviews\n']);
  assert.equal((await readRun('r-b58-still')).blocks.B1.status, 'open');
  const rows = await fx.rows();
  assert.equal(rows.filter((r) => r.event === 'block.close').length, 0);
  // a refused close leaves no config waiver behind — after each of the three refusals above
  assert.deepEqual(waived(rows), []);
});

test('B58 (1): a review that did not approve the file (not_approved) refuses without the key and is skipped with the per-file review under it', async () => {
  /** @param {string} slug @param {string} extra */
  const staleApproval = async (slug, extra) => {
    const fx = await fixture(slug, `r-${slug}`, { extra });
    opened(await fx.open('B1', ['src/a.mjs']));
    writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
    const hash = contentHash(fx.root, 'src/a.mjs');
    await writeSigned(`r-${slug}`, fx.writeRow, { event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: hash, round: 1 });
    await writeSigned(`r-${slug}`, fx.writeRow, { event: 'review.round', block: 'B1', file: 'src/a.mjs', content_hash: hash, round: 2, kind: 'full', open_after: 1 });
    await writeSigned(`r-${slug}`, fx.writeRow, { event: 'review.result', block: 'B1', file: 'src/a.mjs', content_hash: hash, ticket: 't', status: 'reviewed', approved: false, open: [{ id: 'W1', severity: 'warning' }] });
    return fx;
  };
  const control = await staleApproval('b58-notappr-off', '');
  assert.deepEqual(await control.close([]), [1, 'block B1 open: not_approved src/a.mjs W1\n']);
  assert.deepEqual(waived(await control.rows()), []);

  const fx = await staleApproval('b58-notappr', EXTERNAL);
  assert.deepEqual(await fx.close([]), [0, 'block B1 closed\n']);
  assert.deepEqual(waived(await fx.rows()).map((r) => [r.by, r.reviewer]), [['config', 'Grok']]);
});

test('B58 (1): the reviewer name is trimmed in the review.external and gate.reviews_waived rows', async () => {
  const fx = await fixture('b58-trim', 'r-b58-trim', { extra: "review:\n  external: {required: true, name: '  Ana  '}\n" });
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close([]), [0, 'block B1 closed\n']);
  const rows = await fx.rows();
  assert.deepEqual(rows.filter((r) => r.event === 'review.external').map((r) => r.reviewer), ['Ana']);
  assert.deepEqual(waived(rows).map((r) => r.reviewer), ['Ana']);
});

test('B58: a run whose config snapshot does not match its hash refuses block open, claim and close with config-snapshot — with or without the key', async () => {
  const fx = await fixture('b58-tamper', 'r-b58-tamper', { extra: '' });
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  const record = await readRun('r-b58-tamper');
  record.config.snapshots[record.config.hash].review = { session_timeout_s: 1 };
  await saveRun(record);
  const damaged = 'the config snapshot in force does not match its hash';
  assert.deepEqual(await fx.close([]), [1, `block close: run r-b58-tamper: ${damaged}\n`]);
  assert.deepEqual(await cli(runBlock, ['claim', 'B1', 'README.md', '--run', 'r-b58-tamper']), [1, `block claim: run r-b58-tamper: ${damaged}\n`]);
  assert.deepEqual(await cli(runBlock, ['open', 'B2', '--run', 'r-b58-tamper', '--level', 'L2', '--owned', 'README.md', '--acceptance', ACC]), [1, `block open: run r-b58-tamper: ${damaged}\n`]);
  const after = await readRun('r-b58-tamper');
  assert.equal(after.blocks.B1.status, 'open');
  assert.deepEqual(after.blocks.B1.owned_files, ['src/a.mjs']);
  assert.equal(after.blocks.B2, undefined);
});

test('B58 (2): closeBlock\'s own refusal (orphans in the registry) still refuses under the key, and no waiver row is left', async () => {
  const fx = await fixture('b58-orphans', 'r-b58-orphans');
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  const record = await readRun('r-b58-orphans');
  record.orphans = ['src/zz.mjs'];
  await saveRun(record);
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: orphans: src/zz.mjs\n']);
  const rows = await fx.rows();
  assert.deepEqual(waived(rows), []);
  assert.equal(rows.filter((r) => r.event === 'block.close').length, 0);
  // the orphan resolved, the same close passes and writes the waiver row right before block.close
  const fixed = await readRun('r-b58-orphans');
  fixed.orphans = [];
  await saveRun(fixed);
  assert.deepEqual(await fx.close([]), [0, 'block B1 closed\n']);
  const events = (await fx.rows()).map((r) => r.event);
  assert.deepEqual(events.slice(-2), ['gate.reviews_waived', 'block.close']);
});

test('B58 (6): with no review key at all, a full close works as today — approved and ticketed — and writes no waiver', async () => {
  const fx = await fixture('b58-absent', 'r-b58-absent', { extra: '' });
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  await writeSigned('r-b58-absent', fx.writeRow, { event: 'review.approved', block: 'B1', file: 'src/a.mjs', content_hash: contentHash(fx.root, 'src/a.mjs'), round: 1 });
  const report = path.join(freshDir('report'), 'report.md');
  const line = rv(fx.root, 'r-b58-absent', 'src/a.mjs');
  writeFile(path.dirname(report), 'report.md', line);
  assert.deepEqual(await fx.close(['--report', report]), [0, `${line}block B1 closed\n`]);
  const rows = await fx.rows();
  assert.deepEqual(waived(rows), []);
  assert.deepEqual(rows.filter((r) => r.event === 'review.external'), []);
});

test('B58 (2): a high-tier file still needs its proven red→green row', async () => {
  const fx = await fixture('b58-proof', 'r-b58-proof', { extra: `${EXTERNAL}proof:\n  tiers:\n    high:\n      paths: [src/a.mjs]\n` });
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: unproven src/a.mjs\n']);
  assert.equal((await readRun('r-b58-proof')).blocks.B1.status, 'open');
  assert.deepEqual(waived(await fx.rows()), []);
});

const NO_TTY = 'block close: --no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)\n';

test('B58 (5): --no-require-reviews is unchanged with the key set — no TTY refused, under autopilot refused, by: human when confirmed', async () => {
  const fx = await fixture('b58-human', 'r-b58-human');
  opened(await fx.open('B1', ['src/a.mjs']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close(['--no-require-reviews'], { isTTY: false }), [2, NO_TTY]);
  const yes = { confirm: async () => true, isCancel: () => false };
  assert.deepEqual(await fx.close(['--no-require-reviews'], { isTTY: true, ui: yes }), [0, 'block B1 closed\n']);
  assert.deepEqual(waived(await fx.rows()).map((r) => [r.by, r.reviewer]), [['human', undefined]]);

  const ap = await fixture('b58-human-ap', 'r-b58-human-ap');
  opened(await ap.open('B1', ['src/a.mjs']));
  writeFile(ap.root, 'src/a.mjs', 'export const a = 2;\n');
  await startGrant({ runId: 'r-b58-human-ap', input: { until: '2026-10-07T12:00:00Z', delegate: 'L2', allow: 'waive:nit' }, now: T0, writeRow: ap.writeRow, grantId: 'ap-b58h' });
  assert.deepEqual(await ap.close(['--no-require-reviews'], { isTTY: true, ui: yes }), [
    1,
    'block close: --no-require-reviews is refused while an autopilot grant is active on run r-b58-human-ap (stop it first: code-forge autopilot stop --run r-b58-human-ap)\n',
  ]);
  assert.deepEqual(waived(await ap.rows()), []);
});

/** @param {string} entry */
const ownsConfig = (entry) => `block open: owned entry ${JSON.stringify(entry)} covers the project's .code-forge.yml; no block may own it while review.external.required is true\n`;

test('B58 (4): with the key set, block open refuses an entry that owns .code-forge.yml — the exact path or a glob matching it', async () => {
  const fx = await fixture('b58-open', 'r-b58-open');
  for (const entry of ['.code-forge.yml', '*.yml', '**', '{src/a.mjs,.code-forge.yml}', '.code-forge.*']) {
    assert.deepEqual(await fx.open('B1', ['src/a.mjs', entry]), [1, ownsConfig(entry)]);
  }
  // the whole workspace as "." or "./" never reaches the B58 check: the owned-path check refuses it first
  for (const entry of ['.', './']) {
    assert.deepEqual(await fx.open('B1', ['src/a.mjs', entry]), [1, `block open: owned files: invalid path "${entry}": no ".", ".." or empty segments\n`]);
  }
  assert.deepEqual((await readRun('r-b58-open')).blocks, {});
  // an entry that does not cover it opens
  opened(await fx.open('B1', ['src/a.mjs', 'src/**', '.code-forge.yml.bak']));
});

test('B58 (4): for a workspace in a subdirectory, the directory above the config (repo-relative) is refused too', async () => {
  const fx = await fixture('b58-sub', 'r-b58-sub', { ws: 'pkg' });
  assert.deepEqual(await fx.open('B1', ['pkg']), [1, ownsConfig('pkg')]);
  assert.deepEqual(await fx.open('B1', ['pkg/.code-forge.yml']), [1, ownsConfig('pkg/.code-forge.yml')]);
  assert.deepEqual(await fx.open('B1', ['.code-forge.yml']), [1, ownsConfig('.code-forge.yml')]);
  opened(await fx.open('B1', ['src/a.mjs']));
});

test('B58 (4): with the key set, block claim refuses the config path; another path is claimed', async () => {
  const fx = await fixture('b58-claim', 'r-b58-claim');
  opened(await fx.open('B1', ['src/a.mjs']));
  const claim = (/** @type {string} */ file) => cli(runBlock, ['claim', 'B1', file, '--run', 'r-b58-claim']);
  assert.deepEqual(await claim('.code-forge.yml'), [1, `block claim: owned entry ".code-forge.yml" covers the project's .code-forge.yml; no block may own it while review.external.required is true\n`]);
  assert.deepEqual((await readRun('r-b58-claim')).blocks.B1.owned_files, ['src/a.mjs']);
  assert.deepEqual(await claim('README.md'), [0, 'block B1 claimed README.md\n']);
  // a glob is never a claim (one exact path only), so it cannot reach the config either
  assert.deepEqual(await claim('*.yml'), [1, 'block claim: block claim takes one exact path, not a glob ("*.yml")\n']);
  assert.deepEqual((await readRun('r-b58-claim')).blocks.B1.owned_files, ['src/a.mjs', 'README.md']);
});

test('B58 (4): in a subdirectory workspace, block claim refuses the directory that holds .code-forge.yml', async () => {
  const fx = await fixture('b58-claim-dir', 'r-b58-claim-dir', { ws: 'pkg' });
  opened(await fx.open('B1', ['src/a.mjs']));
  const claim = (/** @type {string} */ file) => cli(runBlock, ['claim', 'B1', file, '--run', 'r-b58-claim-dir']);
  assert.deepEqual(await claim('pkg'), [1, `block claim: owned entry "pkg" covers the project's .code-forge.yml; no block may own it while review.external.required is true\n`]);
  assert.deepEqual((await readRun('r-b58-claim-dir')).blocks.B1.owned_files, ['src/a.mjs']);
});

test('B58 (6): without the key a block may own and claim .code-forge.yml as before', async () => {
  const fx = await fixture('b58-nokey', 'r-b58-nokey', { extra: '' });
  opened(await fx.open('B1', ['src/a.mjs', '.code-forge.yml']));
  const sub = await fixture('b58-nokey-claim', 'r-b58-nokey-claim', { extra: 'review:\n  external: {required: false, name: Grok}\n' });
  opened(await sub.open('B1', ['src/a.mjs']));
  assert.deepEqual(await cli(runBlock, ['claim', 'B1', '.code-forge.yml', '--run', 'r-b58-nokey-claim']), [0, 'block B1 claimed .code-forge.yml\n']);
});

test('B58 (1): a snapshot with required true but a blank name is no switch — the per-file review still applies', async () => {
  const fx = await fixture('b58-blankname', 'r-b58-blankname', { extra: '' });
  const record = await readRun('r-b58-blankname');
  record.config = initialConfigState({ version: 1, engine: 'harness', review: { external: { required: true, name: '   ' } } });
  await saveRun(record);
  opened(await fx.open('B1', ['src/a.mjs', '.code-forge.yml']));
  writeFile(fx.root, 'src/a.mjs', 'export const a = 2;\n');
  assert.deepEqual(await fx.close([]), [1, 'block B1 open: unreviewed src/a.mjs\n']);
  assert.deepEqual(waived(await fx.rows()), []);
});
