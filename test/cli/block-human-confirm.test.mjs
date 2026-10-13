import { commitAll, makeRepo, writeFile, freshDir, PARENT } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

// B80 (D1 = a): `block waive` and `block reopen` are the human's — asked at a terminal through the
// same confirm as `block close --no-require-reviews`. Every scenario runs the real `block` verb (in
// process with injected TTY flags and prompt, or the real bin as a child process, which never has a
// terminal) on a real run in a temp HOME and a temp repo: block B1 owns `src/a.mjs`, a stub worker
// stopped its review (`l3_patch_exhausted`) with finding C1 open, and the stop of the current
// content is cached as a done marker — what a `reopen` clears.

const { loadKey, verifyRow } = await import('../../src/state/signer.mjs');
const { startRun } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow, readAllRows } = await import('../../src/ledger/write.mjs');
const { runBlock } = await import('../../src/cli/block.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { announceWorker, queueDir } = await import('../../src/worker/queue.mjs');
const { createWorker } = await import('../../src/worker/loop.mjs');
const { createKeyStore } = await import('../../src/keys/store.mjs');
const { startGrant } = await import('../../src/autopilot/grant.mjs');

const BIN = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');
const SRC = 'src/a.mjs';
const T0 = new Date('2026-10-12T10:00:00.000Z');
const V = (/** @type {number} */ n) => `export const a = ${n};\nexport function add(x, y) {\n  return x - y;\n}\n`;
const C1 = { id: 'C1', file: SRC, line_start: 3, line_end: 3, severity: 'warning', category: 'correctness', claim: 'add subtracts', evidence: 'return x - y', fix: 'return x + y' };
const VERBS = /** @type {const} */ (['waive', 'reopen']);
const NO_TTY = (/** @type {string} */ verb) => `block ${verb} needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n`;

// The rows today's code writes, captured from the BASE code (c4c310b) in a scratch worktree outside
// the repo, on this same fixture (run `r-b80-human`): the row without `mac`, `ts`, `tokens_source`
// and `cost_source`, in key order, and the full key list.
const BASE_RUN = 'r-b80-human';
const BASE_ROW = {
  waive: '{"run":"r-b80-human","event":"review.waived","block":"B1","file":"src/a.mjs","finding":"C1","reason":"owner accepted it","by":"human"}',
  reopen: '{"run":"r-b80-human","event":"review.reopened","block":"B1","file":"src/a.mjs","from":"stopped","stop_reason":"l3_patch_exhausted","at_seq":2,"from_hash":"de9655d3cb2fe8e8b3678d55af3a65872148f370a0881948db53e56f470a47b3","to_hash":"1567cc893737153ddb5b8158a05149f4fc80f9eb4aace9b354006eeac83215e6","reason":"the owner split the fix","by":"human"}',
};
const BASE_KEYS = {
  waive: ['run', 'event', 'block', 'file', 'finding', 'reason', 'by', 'ts', 'mac', 'tokens_source', 'cost_source'],
  reopen: ['run', 'event', 'block', 'file', 'from', 'stop_reason', 'at_seq', 'from_hash', 'to_hash', 'reason', 'by', 'ts', 'mac', 'tokens_source', 'cost_source'],
};
const BASE_OUT = {
  waive: 'block B1 waived C1 in src/a.mjs (by: human)\n',
  reopen: 'block B1 reopened src/a.mjs (from stopped l3_patch_exhausted; by: human)\n',
};

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

/** @param {string} text @returns {string[]} the packet's hunk list. */
function hunksOf(text) {
  const at = text.indexOf('\nhunks:\n');
  if (at < 0) return [];
  const listed = text.slice(at + 8).split('\n');
  const end = listed.findIndex((l) => !l.startsWith('- @@'));
  return listed.slice(0, end < 0 ? listed.length : end).map((l) => l.slice(2));
}

/**
 * Every entry under the per-file temp parent (HOME, the repos, the run roots): a file maps to its
 * bytes, a directory to the marker `dir:` (so a new empty directory is a change too).
 */
function snapshot() {
  /** @type {Map<string, Buffer>} */
  const files = new Map();
  const walk = (/** @type {string} */ dir) => {
    for (const name of readdirSync(dir).sort()) {
      const full = path.join(dir, name);
      if (statSync(full).isDirectory()) {
        files.set(`${path.relative(PARENT, full)}/`, Buffer.from('dir:'));
        walk(full);
      } else files.set(path.relative(PARENT, full), readFileSync(full));
    }
  };
  walk(PARENT);
  return files;
}

/** @param {Map<string, Buffer>} before @param {Map<string, Buffer>} after */
function assertUnchanged(before, after) {
  assert.deepEqual([...after.keys()], [...before.keys()]);
  for (const [name, bytes] of before) assert.equal(after.get(name)?.equals(bytes), true, `${name} changed`);
}

/**
 * The run: B1 owns SRC; round 1 on V(2) finds C1 (the L3 rung), the patch check on V(3) finds it
 * again (stopped `l3_patch_exhausted`); after an attempt, V(4) is reviewed and the stop is cached
 * (its done marker is on disk).
 * @param {string} slug
 */
async function fixture(slug) {
  const runId = `r-${slug}`;
  const repo = await makeRepo();
  writeFile(repo, SRC, V(1));
  writeFile(repo, 'coder.log', '$ node --test\n');
  await commitAll(repo);
  const probe = async (/** @type {number} */ pid) => (pid === 4242 ? 'start-A' : null);
  const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug });
  await startRun({ workspace: repo, project: slug, runId, workerPid: 4242, writeRow, probe });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [SRC], acceptance: [{ clause: 'c', tests: ['t'] }], writeRow });
  writeFile(repo, SRC, V(2));
  const key = await loadKey(runId);
  const spawn = async (/** @type {Record<string, any>} */ opts) => {
    const text = readFileSync(opts.promptPath, 'utf8');
    const resolved = [...text.slice(text.indexOf('## open findings\n')).matchAll(/^- (\S+) \(/gm)].map((m) => ({ id: m[1], resolved: false, why: 'still open' }));
    return { status: 'ok', exit_code: 0, answer: { passed: false, summary: 's', reviewed_hunks: hunksOf(text), findings: [C1], resolved, needs_file: [] }, usage: { tokens_in: 900, tokens_out: 300 } };
  };
  const jev = async (/** @type {{questions: Record<string, any>}} */ req) => {
    const [id] = Object.keys(req.questions);
    return { ok: true, answers: { [id]: { type: 'noul', noul: id === 'defect' ? 0.95 : 0.2 } } };
  };
  const worker = await createWorker(
    { runId, repoRoot: repo, cfg: { review: { max_rounds_per_file: 1 } }, runRootDir: freshDir('runroot'), slug, key },
    { store: await createKeyStore({ backends: [], dir: freshDir('store') }), env: {}, writeRow, readRows: () => readAllRows(slug), spawn: /** @type {any} */ (spawn), jev },
  );
  announceWorker(repo, { pid: process.pid, run: runId });
  const review = async () => {
    const stdout = capture();
    assert.equal(await runReviewFile([SRC, '--block', 'B1'], { cwd: repo, stdout, stderr: capture() }), 0);
    assert.equal(await worker.drain(), 1);
    return /** @type {string} */ (JSON.parse(stdout.text).ticket);
  };
  await review();
  writeFile(repo, SRC, V(3));
  await review();
  /** @type {{asked: Array<Record<string, any>>, answer: unknown}} */
  const ui = { asked: [], answer: true };
  /** @param {string[]} args @param {Record<string, any>} [tty] */
  const block = async (args, tty = { isTTY: true }) => {
    const stdout = capture();
    const stderr = capture();
    const prompt = {
      confirm: async (/** @type {Record<string, any>} */ opts) => {
        ui.asked.push(opts);
        return ui.answer;
      },
      isCancel: (/** @type {unknown} */ v) => v === CANCEL,
    };
    const code = await runBlock(args, { stdout, stderr, probe, now: () => T0, ui: prompt, ...tty });
    return { code, out: stdout.text, err: stderr.text };
  };
  assert.equal((await block(['attempt', 'B1', '--run', runId])).code, 0);
  writeFile(repo, SRC, V(4));
  const ticket = await review();
  const doneMarker = path.join(queueDir(repo), `${ticket}.done`);
  assert.equal(existsSync(doneMarker), true);
  /** @type {Record<string, string[]>} */
  const argsOf = {
    waive: ['waive', 'B1', 'C1', '--run', runId, '--file', SRC, '--reason', 'owner accepted it'],
    reopen: ['reopen', 'B1', '--run', runId, '--file', SRC, '--reason', 'the owner split the fix'],
  };
  const humanRows = async () => (await readAllRows(slug)).filter((r) => r.run === runId && (r.event === 'review.waived' || r.event === 'review.reopened'));
  const prompts = {
    waive: `Waive finding C1 in ${SRC} of block B1 (run ${runId})? Only the owner may say yes.`,
    reopen: `Reopen the review of ${SRC} in block B1 (run ${runId})? Only the owner may say yes.`,
  };
  return { runId, slug, repo, key, ui, block, argsOf, humanRows, doneMarker, prompts, writeRow };
}

const CANCEL = Symbol('clack-cancel');

/**
 * Run the real bin as a child process (no terminal: stdin and stdout are pipes).
 * @param {string[]} args @param {string} cwd @returns {Promise<{code: number, stdout: string, stderr: string}>}
 */
function cli(args, cwd) {
  return new Promise((resolve) => {
    execFile(process.execPath, [BIN, ...args], { cwd, timeout: 30000, env: { ...process.env, CODE_FORGE_NO_ERROR_LOG: '1' } }, (error, stdout, stderr) =>
      resolve({ code: error ? Number(error.code ?? 1) : 0, stdout, stderr }),
    );
  });
}

test('B80 (1): no terminal on either side ⇒ exit 2, the exact message, no prompt, nothing on disk changed, the done marker kept', async () => {
  const f = await fixture('b80-notty');
  const sides = [{ isTTY: false }, { stdinIsTTY: false, stdoutIsTTY: true }, { stdinIsTTY: true, stdoutIsTTY: false }];
  for (const verb of VERBS) {
    for (const tty of sides) {
      for (const json of [[], ['--json']]) {
        const before = snapshot();
        const r = await f.block([...f.argsOf[verb], ...json], tty);
        assert.deepEqual([r.code, r.out, r.err], [2, '', NO_TTY(verb)], `${verb} ${JSON.stringify(tty)} ${json}`);
        assertUnchanged(before, snapshot());
      }
    }
  }
  assert.equal(f.ui.asked.length, 0);
  assert.deepEqual(await f.humanRows(), []);
  assert.equal(existsSync(f.doneMarker), true);
});

test('B80 (1): the real bin from a child process (no terminal) ⇒ exit 2, the exact message, nothing on disk changed', async () => {
  const f = await fixture('b80-bin');
  for (const verb of VERBS) {
    const before = snapshot();
    const r = await cli(['block', ...f.argsOf[verb]], f.repo);
    assert.deepEqual([r.code, r.stdout, r.stderr], [2, '', NO_TTY(verb)]);
    assertUnchanged(before, snapshot());
  }
  assert.deepEqual(await f.humanRows(), []);
  assert.equal(existsSync(f.doneMarker), true);
});

test('B80 (2): at a terminal, "no" and a cancel ⇒ exit 1, `cancelled`, the prompt asked once each, nothing on disk changed', async () => {
  const f = await fixture('b80-no');
  for (const verb of VERBS) {
    for (const answer of [false, CANCEL]) {
      f.ui.asked = [];
      f.ui.answer = answer;
      const before = snapshot();
      const r = await f.block(f.argsOf[verb]);
      assert.deepEqual([r.code, r.out, r.err], [1, '', `block B1 ${verb}: cancelled — nothing was recorded\n`]);
      assert.deepEqual(f.ui.asked, [{ message: f.prompts[verb], initialValue: false }]);
      assertUnchanged(before, snapshot());
    }
  }
  assert.deepEqual(await f.humanRows(), []);
  assert.equal(existsSync(f.doneMarker), true);
});

test('B80 (2): at a terminal, "yes" ⇒ the prompt asked once, then the same signed row and output as the base code', async () => {
  const f = await fixture('b80-yes');
  for (const verb of VERBS) {
    f.ui.asked = [];
    const r = await f.block(f.argsOf[verb]);
    assert.deepEqual([r.code, r.out, r.err], [0, BASE_OUT[verb], '']);
    assert.deepEqual(f.ui.asked, [{ message: f.prompts[verb], initialValue: false }]);
  }
  const rows = await f.humanRows();
  assert.deepEqual(rows.map((row) => row.event), ['review.waived', 'review.reopened']);
  for (const [i, verb] of VERBS.entries()) {
    const row = rows[i];
    assert.equal(verifyRow(row, f.key).ok, true);
    assert.deepEqual(Object.keys(row), BASE_KEYS[verb]);
    assert.equal(row.run, f.runId);
    const { mac: _m, ts: _t, tokens_source: _ts, cost_source: _cs, ...rest } = row;
    assert.equal(JSON.stringify({ ...rest, run: BASE_RUN }), BASE_ROW[verb]);
  }
  // the reopen cleared the cached stop, as before
  assert.equal(existsSync(f.doneMarker), false);
});

test('B80 (3): there is no --yes — a usage error for both verbs, no prompt, nothing on disk changed', async () => {
  const f = await fixture('b80-flag');
  for (const verb of VERBS) {
    for (const tty of [{ isTTY: true }, { isTTY: false }]) {
      const before = snapshot();
      const r = await f.block([...f.argsOf[verb], '--yes'], tty);
      assert.deepEqual([r.code, r.out, r.err], [2, '', `block ${verb}: unknown flag "--yes"\n`]);
      assertUnchanged(before, snapshot());
    }
  }
  assert.equal(f.ui.asked.length, 0);
  assert.deepEqual(await f.humanRows(), []);
});

test('B80: an unknown block at a terminal is refused before the question; nothing is asked', async () => {
  const f = await fixture('b80-unknown');
  for (const verb of VERBS) {
    const before = snapshot();
    const r = await f.block(f.argsOf[verb].map((a) => (a === 'B1' ? 'B9' : a)));
    assert.deepEqual([r.code, r.out, r.err], [1, '', `block ${verb}: block B9 is not in run ${f.runId}\n`]);
    assertUnchanged(before, snapshot());
  }
  assert.equal(f.ui.asked.length, 0);
});

test('B80 (4): an active autopilot grant changes nothing — no terminal is still refused, at a terminal the same prompt and a `by: human` row', async () => {
  const f = await fixture('b80-grant');
  await startGrant({ runId: f.runId, input: { until: '2026-10-12T12:00:00.000Z', delegate: 'L2', allow: 'waive:warning,waive:nit' }, now: T0, writeRow: f.writeRow, grantId: 'ap-b80' });
  const grants = (await readAllRows(f.slug)).filter((r) => r.run === f.runId && r.event === 'autopilot.grant');
  assert.equal(grants.length, 1);
  for (const verb of VERBS) {
    const before = snapshot();
    const refused = await f.block(f.argsOf[verb], { isTTY: false });
    assert.deepEqual([refused.code, refused.out, refused.err], [2, '', NO_TTY(verb)]);
    assertUnchanged(before, snapshot());
  }
  for (const verb of VERBS) {
    f.ui.asked = [];
    const r = await f.block(f.argsOf[verb]);
    assert.deepEqual([r.code, r.out, r.err], [0, BASE_OUT[verb], '']);
    assert.deepEqual(f.ui.asked, [{ message: f.prompts[verb], initialValue: false }]);
  }
  const rows = await f.humanRows();
  assert.deepEqual(rows.map((row) => [row.event, row.by]), [['review.waived', 'human'], ['review.reopened', 'human']]);
});

test('B80 (2): "yes" with stdin and stdout each a TTY (no isTTY given) ⇒ asked once, the base row and output', async () => {
  const f = await fixture('b80-yes-sides');
  for (const verb of VERBS) {
    f.ui.asked = [];
    const r = await f.block(f.argsOf[verb], { stdinIsTTY: true, stdoutIsTTY: true });
    assert.deepEqual([r.code, r.out, r.err], [0, BASE_OUT[verb], '']);
    assert.deepEqual(f.ui.asked, [{ message: f.prompts[verb], initialValue: false }]);
  }
  const rows = await f.humanRows();
  assert.deepEqual(rows.map((row) => row.event), ['review.waived', 'review.reopened']);
  for (const [i, verb] of VERBS.entries()) {
    const { mac: _m, ts: _t, tokens_source: _ts, cost_source: _cs, ...rest } = rows[i];
    assert.equal(verifyRow(rows[i], f.key).ok, true);
    assert.equal(JSON.stringify({ ...rest, run: BASE_RUN }), BASE_ROW[verb]);
  }
});

test('B80: a waive the verb would refuse is refused at a terminal BEFORE the question — an unknown finding id, a file the block does not own, a malformed id or path; nothing is asked or written', async () => {
  const f = await fixture('b80-precheck');
  /** @param {string} finding @param {string} file */
  const waive = (finding, file) => ['waive', 'B1', finding, '--run', f.runId, '--file', file, '--reason', 'owner accepted it'];
  /** @type {Array<[string[], number, string]>} */
  const cases = [
    [waive('Z9', SRC), 1, `block waive: waive-unknown-finding: no review row of block B1 names finding Z9 in ${SRC}; nothing was recorded\n`],
    [waive('C1', 'src/other.mjs'), 1, 'block waive: waive-not-owned: block B1 does not own src/other.mjs; nothing was recorded\n'],
    [waive('Z9', 'src/other.mjs'), 1, 'block waive: waive-not-owned: block B1 does not own src/other.mjs; nothing was recorded\n'],
    [waive('bad id', SRC), 2, 'block waive: a finding id is required (letters, digits, . _ : -)\n'],
    [waive('C1', '../a.mjs'), 2, 'block waive: --file must be repo-root-relative\n'],
  ];
  for (const [args, code, err] of cases) {
    const before = snapshot();
    const r = await f.block(args);
    assert.deepEqual([r.code, r.out, r.err], [code, '', err], args.join(' '));
    assertUnchanged(before, snapshot());
  }
  assert.equal(f.ui.asked.length, 0);
  assert.deepEqual(await f.humanRows(), []);
  // the same finding on the owned file is asked about (the check finds C1 in the review rows)
  const ok = await f.block(f.argsOf.waive);
  assert.deepEqual([ok.code, ok.err, f.ui.asked.length], [0, '', 1]);
});

test('B80: a reopen the verb would refuse is refused at a terminal BEFORE the question — a bad path, an unowned file; nothing is asked or written', async () => {
  const f = await fixture('b80-reopen-pre');
  /** @param {string} file */
  const reopen = (file) => ['reopen', 'B1', '--run', f.runId, '--file', file, '--reason', 'the owner split the fix'];
  /** @type {Array<[string[], number, string]>} */
  const cases = [
    [reopen('../a.mjs'), 2, 'block reopen: --file must be repo-root-relative\n'],
    [reopen('/abs/a.mjs'), 2, 'block reopen: --file must be repo-root-relative\n'],
    [reopen('src//a.mjs'), 2, 'block reopen: --file must be repo-root-relative\n'],
    [reopen('src/other.mjs'), 1, 'block reopen: reopen-not-owned: block B1 does not own src/other.mjs; nothing was recorded\n'],
  ];
  for (const [args, code, err] of cases) {
    const before = snapshot();
    const r = await f.block(args);
    assert.deepEqual([r.code, r.out, r.err], [code, '', err], args.join(' '));
    assertUnchanged(before, snapshot());
  }
  assert.equal(f.ui.asked.length, 0);
  assert.deepEqual(await f.humanRows(), []);
  assert.equal(existsSync(f.doneMarker), true);
});

test('B80: a --file or finding id with a control character is a usage error (exit 2) at a terminal, for both verbs; nothing is asked or written', async () => {
  const f = await fixture('b80-control');
  const waive = (/** @type {string} */ finding, /** @type {string} */ file) => ['waive', 'B1', finding, '--run', f.runId, '--file', file, '--reason', 'owner accepted it'];
  const reopen = (/** @type {string} */ file) => ['reopen', 'B1', '--run', f.runId, '--file', file, '--reason', 'the owner split the fix'];
  const FILE = (/** @type {string} */ verb) => `block ${verb}: --file must not contain control characters\n`;
  const ID = 'block waive: a finding id must not contain control characters\n';
  /** @type {Array<[string[], string]>} */
  const cases = [
    [reopen('src/a.mjs\x1b[2J'), FILE('reopen')],
    [reopen('src/\x7fa.mjs'), FILE('reopen')],
    [reopen('src/a.mjs\n'), FILE('reopen')],
    [waive('C1', 'src/a.mjs\x1b[2J'), FILE('waive')],
    [waive('C1', '\x00src/a.mjs'), FILE('waive')],
    [waive('C1\x1b[2J', SRC), ID],
    [waive('C1\x7f', SRC), ID],
    // an empty --file is refused before the prompt too (round 3: checkFileForm took '' as a path)
    [waive('C1', ''), 'block waive: --file is required\n'],
  ];
  for (const [args, err] of cases) {
    const before = snapshot();
    const r = await f.block(args);
    assert.deepEqual([r.code, r.out, r.err], [2, '', err], JSON.stringify(args));
    assertUnchanged(before, snapshot());
  }
  assert.equal(f.ui.asked.length, 0);
  assert.deepEqual(await f.humanRows(), []);
});

test('B80 helpers: snapshot() sees a new empty directory; humanBlock refuses a ui, stdout or stderr from the caller', async () => {
  const before = snapshot();
  const dir = freshDir('b80-empty-dir');
  assert.equal(readdirSync(dir).length, 0);
  assert.throws(() => assertUnchanged(before, snapshot()));
  const { humanBlock } = await import('../helpers/human-confirm.mjs');
  for (const own of ['ui', 'stdout', 'stderr']) {
    await assert.rejects(humanBlock(PARENT, ['waive', 'B1', 'C1', '--run', 'r-none'], { [own]: {} }), { name: 'TypeError', message: `humanBlock owns ${own}; do not pass it in deps` });
  }
});
