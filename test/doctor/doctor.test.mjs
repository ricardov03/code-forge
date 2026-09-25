/**
 * `code-forge doctor` (plan §2.3; B13b acceptance, §10.4). Every run resolves its CLIs through a
 * two-entry PATH (a fake bin of wrappers that answer `--help` from the pinned fixture, then node's
 * dir — never the caller's PATH), a mocked Jev client and a temp HOME; the worker probe starts a
 * real `code-forge worker` and stops it (asserted gone after each run). The last suite guards the
 * PATH itself: no real `claude`/`codex`/`grok` is reachable.
 */

import { CLAUDE_HELP, CLAUDE_MD_LINE, CLI_NAMES, CLI_VERSION, doctorDeps, FAKE_JEV_KEY, freshDir, goneCode, isolationRuns, makeProject, NODE_DIR, plantStaleRoot, readRecords, rowById, wrapperCalls } from './helpers.mjs';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';
import { runDoctorCli } from '../../src/cli/doctor.mjs';
import { runsDir } from '../../src/state/paths.mjs';

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'src');
const MODELS = 'claude-haiku-4-5-20251001,claude-sonnet-5,claude-opus-5-5,claude-fable-5-1';

/** @param {string[]} args @param {ReturnType<typeof doctorDeps>} d */
async function runJSON(args, d) {
  const code = await runDoctorCli(['--json', ...args], /** @type {any} */ (d.deps));
  const text = d.stdout.text();
  return { code, text, doc: JSON.parse(text) };
}

/** Doctor leaves no run behind: the runs dir holds nothing after a full run. */
function assertNoRunLeft() {
  const left = existsSync(runsDir()) ? readdirSync(runsDir()) : [];
  assert.deepEqual(left, []);
}

describe('doctor full run', () => {
  test('baseline: one JSON document; flags, isolation, path deny, worker, signer rows; tmp counts a planted stale root', async () => {
    const { proj, tmpRoot } = makeProject();
    plantStaleRoot(tmpRoot);
    const d = doctorDeps({ env: { CODE_FORGE_KEY_JEV: FAKE_JEV_KEY } });
    const { text, doc } = await runJSON(['--cwd', proj], d);

    assert.equal(text.trim().split('\n').length, 1); // --json: one document, one line
    assert.equal(rowById(doc.rows, 'flags.claude').status, 'OK');
    assert.equal(rowById(doc.rows, 'isolation').status, 'OK');
    assert.equal(rowById(doc.rows, 'path-deny').status, 'OK');
    const tmp = rowById(doc.rows, 'tmp');
    assert.equal(tmp.status, 'WARN');
    assert.match(tmp.detail, /^1 stale roots, 1 stale pids /);
    assert.equal(rowById(doc.rows, 'signer-key').status, 'OK');
    const worker = rowById(doc.rows, 'worker');
    assert.equal(worker.status, 'OK', worker.detail);
    const workerPid = Number(/^pid (\d+) /.exec(worker.detail)?.[1]);
    assert.ok(workerPid > 1, worker.detail);
    assert.equal(await goneCode(workerPid), 'ESRCH'); // the worker really stopped
    const iso = isolationRuns(d.records);
    assert.equal(iso.length, 1);
    assert.equal(iso[0].cwd, proj); // the isolation session ran IN the project dir (worst case)
    assert.equal(rowById(doc.rows, 'jev').status, 'OK');
    assert.equal(d.jevCalls.length, 1);
    assert.equal(d.jevCalls[0].key, FAKE_JEV_KEY);
    assert.equal(text.split(FAKE_JEV_KEY).length - 1, 0); // the key is never printed
    assert.deepEqual(doc.rows.filter((/** @type {any} */ r) => r.id.startsWith('ping.')).map((/** @type {any} */ r) => `${r.id}=${r.status}`), [
      'ping.facts=OK',
      'ping.coder=OK',
      'ping.reviewer=OK',
      'ping.s2=OK',
    ]);
    assertNoRunLeft();
    assert.deepEqual(readdirSync(tmpRoot), ['r-stale-fake']); // the doctor's own roots are gone
  });

  test('a --help that lacks a builder flag FAILs the flags row and the run (exit 1)', async () => {
    const { proj } = makeProject();
    const help = CLAUDE_HELP.split('\n').filter((l) => !l.includes('--safe-mode')).join('\n');
    const d = doctorDeps({ help });
    const { code, doc } = await runJSON(['--cwd', proj], d);
    const flags = rowById(doc.rows, 'flags.claude');
    assert.equal(flags.status, 'FAIL');
    assert.equal(flags.detail, 'missing from --help: --safe-mode');
    assert.equal(code, 1);
  });

  test('the isolation probe FAILs when the reviewer echoes the CLAUDE.md line', async () => {
    const { proj } = makeProject();
    const d = doctorDeps({ env: { FAKE_ANSWER: JSON.stringify(CLAUDE_MD_LINE) } });
    const { code, doc } = await runJSON(['--cwd', proj], d);
    const iso = rowById(doc.rows, 'isolation');
    assert.equal(iso.status, 'FAIL');
    assert.equal(iso.detail, "the reviewer saw the project's CLAUDE.md");
    assert.equal(code, 1);
    assert.deepEqual(isolationRuns(d.records).map((r) => r.cwd), [proj]);
  });

  test('the path-deny probe WARNs when the coder prints the denied file', async () => {
    const { proj } = makeProject();
    const canary = 'CANARY-FAKE-b13b-path-deny';
    const d = doctorDeps({ canary, env: { FAKE_ANSWER: JSON.stringify(canary) } });
    const { doc } = await runJSON(['--cwd', proj], d);
    const deny = rowById(doc.rows, 'path-deny');
    assert.equal(deny.status, 'WARN');
    assert.equal(deny.detail, 'claude: path deny rules not honoured');
    assertNoRunLeft(); // the canary file is removed too
  });

  test('a 402 provider is ping=skipped(402) WARN, never FAIL; the signer line prints once', async () => {
    const { proj } = makeProject();
    const d = doctorDeps({ env: { FAKE_402_MODELS: MODELS } });
    const code = await runDoctorCli(['--cwd', proj], /** @type {any} */ (d.deps));
    const lines = d.stdout.text().trim().split('\n');
    const pings = lines.filter((l) => /^\S+\s+ping /.test(l));
    assert.equal(pings.length, 4);
    assert.equal(pings.filter((l) => l.startsWith('WARN') && l.endsWith(': ping=skipped(402)')).length, 4);
    assert.deepEqual(lines.filter((l) => l.startsWith('FAIL')), []);
    assert.equal(code, 0);
    assert.equal(lines.filter((l) => l.includes('signer: same-user boundary only')).length, 1);
    assert.equal(lines.filter((l) => l.includes('tmp: 0 stale roots, 0 stale pids')).length, 1);
  });
});

describe('doctor wiring and modes', () => {
  test('the worker row uses B11 and the signer row uses B8, both statically imported', () => {
    const src = readFileSync(path.join(SRC, 'doctor', 'worker-probe.mjs'), 'utf8');
    assert.match(src, /^import \{[^}]*\blaunchWorker\b[^}]*\} from '\.\.\/worker\/loop\.mjs';$/m);
    assert.match(src, /^import \{[^}]*\bsignRow\b[^}]*\bverifyRow\b[^}]*\} from '\.\.\/state\/signer\.mjs';$/m);
  });

  test('--quick prints only the 3 quick rows', async () => {
    const { proj } = makeProject();
    const d = doctorDeps();
    const code = await runDoctorCli(['--quick', '--cwd', proj], /** @type {any} */ (d.deps));
    const lines = d.stdout.text().trim().split('\n');
    assert.deepEqual(lines.map((l) => l.split(':')[0].replace(/^\S+\s+/, '')), ['config', 'links', 'keys']);
    assert.equal(code, 0);
  });

  test('a missing config FAILs the config row and skips the probes', async () => {
    const d = doctorDeps();
    const { code, doc } = await runJSON(['--cwd', freshDir('empty')], d);
    assert.equal(rowById(doc.rows, 'config').status, 'FAIL');
    assert.equal(rowById(doc.rows, 'probes').status, 'FAIL');
    assert.equal(code, 1);
  });
});

describe('doctor never reaches a real CLI', () => {
  test("the test PATH is the fake bin then node's dir, nothing else; claude, codex, grok and git resolve inside the fake bin", () => {
    const d = doctorDeps();
    const PATH = d.deps.env.PATH;
    assert.deepEqual(PATH.split(path.delimiter), [d.bin, NODE_DIR]);
    for (const name of [...CLI_NAMES, 'git']) {
      const which = spawnSync('/usr/bin/which', [name], { env: { PATH }, encoding: 'utf8' });
      assert.equal(which.stdout.trim(), path.join(d.bin, name), name);
    }
    // what the OS spawns by name under that PATH is the wrapper, not a real CLI
    for (const name of CLI_NAMES) {
      const run = spawnSync(name, ['--version'], { env: { PATH, FAKE_RECORD: d.records }, encoding: 'utf8' });
      assert.equal(run.stdout, `${CLI_VERSION[name]}\n`, name);
    }
    assert.deepEqual(
      wrapperCalls(d.records),
      CLI_NAMES.map((name) => ({ name, file: path.join(d.bin, name), argv: '--version' })),
    );
    assert.deepEqual(readRecords(d.records), []); // --version never reaches a fake
  });

  test('a full run (no bins seam: every CLI found through PATH) makes 8 claude wrapper calls, 0 codex, 0 grok, none outside the fake bin', async () => {
    const { proj } = makeProject();
    const d = doctorDeps();
    const { doc } = await runJSON(['--cwd', proj], d);
    assert.equal(rowById(doc.rows, 'cli.claude').detail, 'present, 2.1.282 (Claude Code)');
    assert.equal(rowById(doc.rows, 'worker').status, 'OK');
    const calls = wrapperCalls(d.records);
    assert.deepEqual(calls.map((c) => c.name), Array(8).fill('claude')); // version, help, 4 pings, isolation, path deny
    assert.deepEqual(calls.slice(0, 2).map((c) => c.argv), ['--version', '--help']);
    assert.deepEqual(calls.map((c) => path.dirname(c.file)), Array(8).fill(d.bin));
    assert.deepEqual(readRecords(d.records).map((r) => r.name), Array(6).fill('claude')); // the 6 real calls, all the fake
  });
});
