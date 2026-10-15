import { cfgFor, commitAll, freshDir, makeRepo, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { test } from 'node:test';

const { runBlock } = await import('../../src/cli/block.mjs');
const { startRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { contentHash } = await import('../../src/worker/ticket.mjs');
const { isForbidden, mergeForbidden, scanTranscript } = await import('../../src/util/forbidden.mjs');

const FILE = 'src/a.mjs';
const RULE = 'code-forge-block-waive-from-coder';
const LINE = '  doc text: forge block waive B1 F1  \r';
const REASON = 'This writes documentation text.';
const NO_HIT = 'block accept-hit: accept-hit-no-such-hit: the named transcript hit was not found; nothing was recorded\n';
const hash = (text) => createHash('sha256').update(text).digest('hex');
let seq = 0;

async function fixture() {
  const slug = `accept-hit-${++seq}`;
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  const logs = freshDir('transcripts');
  const transcript = `${logs}/coder.log`;
  writeFile(repo, FILE, 'export const a = 1;\n');
  writeFile(logs, 'coder.log', `safe\n${LINE}\n`);
  await commitAll(repo);
  const probe = async (pid) => pid === 4242 ? 'start-A' : null;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe, config: cfgFor() });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [FILE], acceptance: [{ clause: 'accept false hits', tests: ['t'] }], writeRow });
  writeFile(repo, FILE, 'export const a = 2;\n');
  const key = await loadKey(runId);
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B1', ...row });
  await sign({ event: 'review.approved', file: FILE, content_hash: contentHash(repo, FILE) });
  const rows = async () => (await readAllRows(slug)).filter((r) => r.run === runId);
  const asked = [];
  /** @param {string[]} args @param {Record<string, any>} [deps] @returns {Promise<[number, string, string]>} */
  const invoke = async (args, deps = {}) => {
    let out = '', err = '';
    const code = await runBlock(args, { probe, isTTY: true,
      stdout: { write(s) { out += s; } }, stderr: { write(s) { err += s; } },
      ui: { confirm: async (o) => { asked.push(o); return true; }, isCancel: () => false }, ...deps });
    return [code, out, err];
  };
  const args = ['accept-hit', 'B1', '--run', runId, '--rule', RULE, '--line', '2', '--transcript', transcript, '--reason', REASON];
  const accept = (deps = {}) => invoke(args, deps);
  const close = (extra = []) => invoke(['close', 'B1', '--run', runId, '--worker-pid', '4242', '--transcript', transcript, ...extra]);
  const log = (text) => writeFile(logs, 'coder.log', text);
  return { runId, key, args, accept, close, invoke, rows, asked, sign, writeRow, log, transcript };
}

test('B127 clause 1: terminal yes asks once and writes exactly one signed human acceptance of the exact line', async () => {
  const f = await fixture();
  const before = (await f.rows()).length;
  assert.deepEqual(await f.accept(), [0, `block B1 accepted ${RULE} transcript line 2 (by: human)\n`, '']);
  assert.deepEqual(f.asked, [{ message: `Accept false transcript hit ${RULE} at line 2 in ${f.transcript} of block B1 (run ${f.runId})? Only the owner may say yes.`, initialValue: false }]);
  const rows = (await f.rows()).slice(before);
  assert.equal(rows.length, 1, 'yes writes exactly one ledger row');
  const { mac, ts, tokens_source, cost_source, ...row } = rows[0];
  assert.deepEqual(row, { run: f.runId, event: 'rule_break.accepted', block: 'B1', rule: RULE, line: 2, line_sha256: hash(LINE), reason: REASON, by: 'human' });
  assert.deepEqual(verifyRow(rows[0], f.key), { ok: true });
});

test('B127 clause 1 boundary: no, cancel, either missing terminal and --yes write exactly zero rows', async () => {
  const f = await fixture();
  const before = (await f.rows()).length;
  for (const answer of [false, Symbol('cancel')]) {
    let calls = 0;
    const r = await f.accept({ ui: { confirm: async () => { calls++; return answer; }, isCancel: (v) => typeof v === 'symbol' } });
    assert.deepEqual(r, [1, '', 'block B1 accept-hit: cancelled — nothing was recorded\n']);
    assert.equal(calls, 1);
    assert.equal((await f.rows()).length - before, 0);
  }
  for (const deps of [{ isTTY: false }, { stdinIsTTY: false }, { stdoutIsTTY: false }]) {
    assert.deepEqual(await f.accept(deps), [2, '', 'block accept-hit needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n']);
    assert.equal((await f.rows()).length - before, 0);
  }
  assert.deepEqual(await f.invoke([...f.args, '--yes']), [2, '', 'block accept-hit: unknown flag "--yes"\n']);
  assert.equal(f.asked.length, 0);
  assert.equal((await f.rows()).length - before, 0);
});

test('B127 clause 2: unknown block or run, unreadable log, out-of-range line and non-hit pairs refuse before any prompt', async () => {
  const f = await fixture();
  const before = (await f.rows()).length;
  for (const [from, to] of [['B1', 'B9'], [f.runId, 'r-absent'], [f.transcript, `${f.transcript}/missing`], ['2', '99'], ['2', '0'], ['2', '-1'], ['2', '1'], [RULE, 'git-clean']]) {
    assert.deepEqual(await f.invoke(f.args.map((v) => v === from ? to : v)), [1, '', NO_HIT], `${from} → ${to}`);
    assert.equal(f.asked.length, 0, 'refusals ask zero questions');
    assert.equal((await f.rows()).length - before, 0, 'refusals write zero rows');
  }
});

test('B127 clause 2 boundary: non-integer lines, empty reasons and controls are usage errors before any prompt', async () => {
  const f = await fixture();
  const before = (await f.rows()).length;
  const cases = [
    ['--line', '1.5', '--line must be an integer'],
    ['--line', '1e2', '--line must be an integer'],
    ['--line', '9007199254740992', '--line must be an integer'],
    ['--reason', '  ', '--reason is required'],
    ...['--rule', '--line', '--reason', '--transcript', '--run'].map((flag) => [flag, 'bad\x1b[2J', `${flag} must not contain control characters`]),
  ];
  for (const [flag, value, message] of cases) {
    const args = [...f.args];
    args[args.indexOf(flag) + 1] = value;
    assert.deepEqual(await f.invoke(args), [2, '', `block accept-hit: ${message}\n`], flag);
    assert.equal(f.asked.length, 0);
    assert.equal((await f.rows()).length - before, 0);
  }
});

test('B127 clause 3: close accepts signed content after lines move, but refuses a second hit of the same rule with different bytes', async () => {
  const f = await fixture();
  assert.equal((await f.accept())[0], 0);
  f.log(`joined prefix\nsafe\n${LINE}\n${LINE} different\n`);
  const refused = await f.close();
  assert.deepEqual(refused, [1, '', `block B1 open: rule_break transcript line 4: ${RULE}\n`]);
  f.log(`joined prefix\nsafe\n${LINE}\n`);
  assert.equal((await f.close())[0], 0, 'the accepted content still matches at line 3');
});

test('B127 clause 3 boundary: unsigned, tampered, other-block, other-run, wrong-rule and non-human rows never clear a hit; ledger rule_break stays refused', async () => {
  for (const kind of ['unsigned', 'tampered', 'other-block', 'other-run', 'wrong-rule', 'non-human', 'ledger']) {
    const f = await fixture();
    // First prove the real command can record this hit. The adversarial line has different bytes.
    assert.equal((await f.accept())[0], 0, kind);
    const other = `${LINE} other content`;
    f.log(`safe\n${other}\n`);
    const row = { event: 'rule_break.accepted', block: 'B1', rule: RULE, line: 2, line_sha256: hash(other), reason: REASON, by: 'human' };
    if (kind === 'unsigned' || kind === 'tampered') await f.writeRow({ run: f.runId, ...row, ...(kind === 'tampered' ? { mac: 'bad' } : {}) });
    else if (kind === 'ledger') {
      await f.sign(row);
      await f.sign({ event: 'rule_break', rule: RULE });
    } else await f.sign({ ...row, ...(kind === 'other-block' ? { block: 'B2' } : kind === 'other-run' ? { run: 'r-other' } : kind === 'wrong-rule' ? { rule: 'git-clean' } : { by: 'agent' }) });
    const before = (await f.rows()).length;
    const expected = kind === 'ledger' ? 'rule_break' : `rule_break transcript line 2: ${RULE}`;
    assert.deepEqual(await f.close(), [1, '', `block B1 open: ${expected}\n`], kind);
    assert.equal((await f.rows()).length - before, 0, `${kind}: refused close writes zero rows`);
  }
});

test('B127 clause 4: close prints each used acceptance and JSON gives exactly rule, current line and reason', async () => {
  for (const json of [false, true]) {
    const f = await fixture();
    assert.equal((await f.accept())[0], 0);
    f.log(`joined\nsafe\n${LINE}\n${LINE}\n`);
    const [code, out, err] = await f.close(json ? ['--json'] : []);
    assert.deepEqual([code, err], [0, '']);
    if (json) assert.deepEqual(JSON.parse(out).accepted_hits, [3, 4].map((line) => ({ rule: RULE, line, reason: REASON })));
    else assert.deepEqual(out.split('\n').filter((l) => l.startsWith('accepted ')), [3, 4].map((line) => `accepted rule_break ${RULE} transcript line ${line}: ${REASON} (by: human)`));
  }
});

test('B127 clause 4 boundary: unused acceptance prints no accepted line and JSON omits accepted_hits', async () => {
  for (const json of [false, true]) {
    const f = await fixture();
    assert.equal((await f.accept())[0], 0);
    f.log('safe\n');
    const [code, out, err] = await f.close(json ? ['--json'] : []);
    assert.deepEqual([code, err], [0, '']);
    if (json) assert.equal(Object.hasOwn(JSON.parse(out), 'accepted_hits'), false);
    else assert.equal(out.split('\n').filter((l) => l.startsWith('accepted ')).length, 0);
  }
});

test('B127 clause 5 and boundary: both command prefixes refuse close as rule_break; ordinary block status has zero hits', async () => {
  for (const bin of ['code-forge', 'forge']) {
    const argv = [bin, 'block', 'accept-hit', 'B1'];
    assert.equal(isForbidden(argv, mergeForbidden())?.id, 'code-forge-block-accept-hit-from-coder');
    assert.deepEqual(scanTranscript(argv.join(' '), mergeForbidden()), [{ id: 'code-forge-block-accept-hit-from-coder', line: 1 }]);
    assert.deepEqual(scanTranscript(`${bin} block status`, mergeForbidden()), []);
    const f = await fixture();
    f.log(`${argv.join(' ')}\n`);
    const before = (await f.rows()).length;
    assert.deepEqual(await f.close(), [1, '', 'block B1 open: rule_break transcript line 1: code-forge-block-accept-hit-from-coder\n']);
    assert.equal((await f.rows()).length - before, 0, `${bin}: refused close writes zero rows`);
  }
});
