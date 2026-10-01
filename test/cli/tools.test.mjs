// `code-forge tools` (B26): status and install, against a fake PATH (temp dirs holding empty
// executable files), a fake platform, a fake Solo app check and a fake `exec` — nothing is ever
// installed and no real tool is ever run. One per-file temp parent, removed in `after()`; HOME
// points into it before any `src` module loads.
import assert from 'node:assert/strict';
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, test } from 'node:test';

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-tools-'));
process.env.HOME = path.join(PARENT, 'home');
mkdirSync(process.env.HOME, { recursive: true });
after(() => rmSync(PARENT, { recursive: true, force: true }));

const { runTools } = await import('../../src/cli/tools.mjs');
const { checkTools } = await import('../../src/doctor/local.mjs');

let n = 0;
/** @param {string[]} cmds @returns {string} a PATH holding one empty executable per command */
function fakePath(cmds) {
  n += 1;
  const dir = path.join(PARENT, `bin-${n}`);
  mkdirSync(dir, { recursive: true });
  for (const c of cmds) {
    const f = path.join(dir, c);
    writeFileSync(f, '');
    chmodSync(f, 0o755);
  }
  return dir;
}

function sink() {
  const chunks = [];
  return { write: (s) => (chunks.push(String(s)), true), text: () => chunks.join('') };
}

/**
 * A fake exec: records argv; `--version` answers from `versions` (or fails), installs answer
 * from `installs` keyed by package (an exit code, or a partial ExecResult such as a signal kill;
 * default exit 0). `onInstall` runs after each install call.
 * @param {{versions?: Record<string, string>, installs?: Record<string, number|Record<string, any>>, onInstall?: () => void}} [o]
 * @returns {any}
 */
function fakeExec({ versions = {}, installs = {}, onInstall = () => {} } = {}) {
  /** @type {Array<{argv: string[], opts: any}>} */
  const calls = [];
  /** @type {any} */
  const fn = async (/** @type {string[]} */ argv, /** @type {any} */ opts) => {
    calls.push({ argv, opts });
    if (argv[1] === '--version') {
      const v = versions[argv[0]];
      if (v === 'TIMEOUT') return { result: 'failed', code: null, signal: 'SIGTERM', stdout: '', stderr: '', timedOut: true };
      if (v === undefined) return { result: 'failed', code: 1, signal: null, stdout: '', stderr: 'no', timedOut: false };
      return { result: 'ok', code: 0, signal: null, stdout: v, stderr: '', timedOut: false };
    }
    const pkg = argv[argv.length - 1];
    const spec = installs[pkg] ?? 0;
    onInstall();
    if (typeof spec === 'object') return { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false, ...spec };
    return { result: spec === 0 ? 'ok' : 'failed', code: spec, signal: null, stdout: `installing ${pkg}\n`, stderr: '', timedOut: false };
  };
  fn.calls = calls;
  fn.installs = () => calls.filter((c) => c.argv[1] !== '--version').map((c) => c.argv);
  return fn;
}

/** Every exec call (versions and installs) is an argv array of strings with no shell option. */
function assertArgvOnly(/** @type {any} */ exec) {
  for (const c of exec.calls) {
    assert.equal(Array.isArray(c.argv), true);
    assert.equal(c.argv.every((/** @type {unknown} */ a) => typeof a === 'string'), true);
    assert.equal(c.opts?.shell, undefined);
  }
}

/**
 * @param {string[]} args
 * @param {{pathCmds?: string[], platform?: NodeJS.Platform, exists?: (p: string) => boolean, exec?: any, isTTY?: boolean, ui?: any}} [o]
 */
async function run(args, { pathCmds = [], platform = 'darwin', exists = () => false, exec = fakeExec(), isTTY = false, ui = undefined } = {}) {
  const stdout = sink();
  const stderr = sink();
  const env = { HOME: process.env.HOME, PATH: fakePath(pathCmds) };
  const code = await runTools(args, { env, stdout, stderr, platform, exists, exec, isTTY, ui });
  return { code, out: stdout.text(), err: stderr.text(), exec };
}

/** A fake ui that records each confirm's options and answers `answer`. */
function fakeUi(/** @type {unknown} */ answer) {
  /** @type {{asked: number, options: any[], confirm: (o: any) => Promise<unknown>, isCancel: () => boolean}} */
  const ui = { asked: 0, options: [], confirm: async (o) => ((ui.asked += 1), ui.options.push(o), answer), isCancel: () => false };
  return ui;
}

describe('tools status', () => {
  test('claude + op on PATH, others missing: exact rows, versions from the first line, op note', async () => {
    const exec = fakeExec({ versions: { claude: '2.1.0 (Claude Code)\nextra line\n', op: '2.30.0\n' } });
    const r = await run([], { pathCmds: ['claude', 'op', 'npm', 'brew'], exec });
    assert.equal(r.code, 0);
    assert.deepEqual(r.out.split('\n'), [
      'claude  installed  2.1.0 (Claude Code)',
      'codex   missing    install: npm install -g @openai/codex',
      'gemini  missing    install: npm install -g @google/gemini-cli',
      'grok    missing    install: brew install --cask grok-build',
      'op      installed  2.30.0 — turn on 1Password app → Settings → Developer → "Integrate with 1Password CLI"',
      'solo    missing    manual: https://soloterm.com (desktop app, then add its MCP entry)',
      '',
    ]);
    // version asked of the installed tools only, with the 10 s timeout
    const versionCalls = exec.calls.filter((c) => c.argv[1] === '--version');
    assert.deepEqual(versionCalls.map((c) => c.argv), [['claude', '--version'], ['op', '--version']]);
    assert.deepEqual(versionCalls.map((c) => c.opts.timeoutMs), [10000, 10000]);
    assert.equal(exec.installs().length, 0);
  });

  test('a version timeout or failure reads "version unknown"; --json is one object', async () => {
    const exec = fakeExec({ versions: { claude: 'TIMEOUT' } });
    const r = await run([], { pathCmds: ['claude', 'op'], exec });
    assert.equal(r.code, 0);
    const lines = r.out.split('\n');
    assert.equal(lines[0], 'claude  installed  version unknown');
    assert.equal(lines[4], 'op      installed  version unknown — turn on 1Password app → Settings → Developer → "Integrate with 1Password CLI"');

    const j = await run(['--json'], { pathCmds: ['claude'], exec: fakeExec({ versions: { claude: '2.1.0' } }), platform: 'linux' });
    assert.equal(j.code, 0);
    assert.equal(j.out.trim().split('\n').length, 1);
    const doc = JSON.parse(j.out);
    assert.deepEqual(doc.tools.map((t) => [t.id, t.status]), [['claude', 'installed'], ['codex', 'missing'], ['gemini', 'missing'], ['grok', 'missing'], ['op', 'missing'], ['solo', 'missing']]);
    assert.equal(doc.tools[0].version, '2.1.0');
    assert.deepEqual(doc.tools[1].install, null); // no npm on this PATH
    assert.equal(doc.tools[1].hint, 'install Node.js/npm first, then: npm install -g @openai/codex');
    assert.equal(doc.tools[3].hint, 'https://x.ai/build');
  });

  test('solo is installed when /Applications/Solo.app exists (macOS only)', async () => {
    const exists = (p) => p === '/Applications/Solo.app';
    const mac = await run(['--json'], { exists });
    assert.equal(JSON.parse(mac.out).tools[5].status, 'installed');
    const linux = await run(['--json'], { exists, platform: 'linux' });
    assert.equal(JSON.parse(linux.out).tools[5].status, 'missing');
  });

  test('solo on PATH counts as installed on darwin and linux (no app bundle)', async () => {
    for (const platform of /** @type {NodeJS.Platform[]} */ (['darwin', 'linux'])) {
      const r = await run(['--json'], { pathCmds: ['solo'], platform, exists: () => false });
      assert.equal(JSON.parse(r.out).tools[5].status, 'installed');
    }
  });

  test('win32: npm tools are manual ("run: npm install -g <pkg>"), never spawned; version falls back to stderr', async () => {
    const r = await run(['install', 'codex', '--yes'], { pathCmds: [], platform: 'win32' });
    assert.equal(r.exec.installs().length, 0);
    assert.equal(r.out.split('\n').filter((l) => l === 'codex   skipped: manual — run: npm install -g @openai/codex').length, 1);
    const { toolVersion, TOOLS } = await import('../../src/install/tools.mjs');
    const v = await toolVersion(TOOLS[0], { exec: /** @type {any} */ (async () => ({ result: 'ok', code: 0, signal: null, stdout: '\n', stderr: '\n  op 9.9.9  \n', timedOut: false })) });
    assert.equal(v, 'op 9.9.9');
  });
});

describe('tools install', () => {
  test('no ids: only the missing tools, exact argv on darwin, --yes asks 0 times, final table', async () => {
    const ui = fakeUi(true);
    const r = await run(['install', '--yes'], { pathCmds: ['claude', 'op', 'npm', 'brew'], ui });
    assert.equal(r.code, 0, r.err);
    assert.equal(ui.asked, 0);
    assert.deepEqual(r.exec.installs(), [
      ['npm', 'install', '-g', '@openai/codex'],
      ['npm', 'install', '-g', '@google/gemini-cli'],
      ['brew', 'install', '--cask', 'grok-build'],
    ]);
    const table = r.out.split('\n\n').at(-1).split('\n');
    assert.deepEqual(table, [
      'codex   installed (not on PATH yet — open a new shell)',
      'gemini  installed (not on PATH yet — open a new shell)',
      'grok    installed (not on PATH yet — open a new shell)',
      'solo    skipped: manual — https://soloterm.com (desktop app, then add its MCP entry)',
      '',
    ]);
  });

  test('re-check after install: a tool that appears on PATH reads "installed"', async () => {
    const bin = fakePath(['npm']);
    const exec = fakeExec({ onInstall: () => { const f = path.join(bin, 'codex'); writeFileSync(f, ''); chmodSync(f, 0o755); } });
    const stdout = sink();
    const code = await runTools(['install', 'codex', '--yes'], { env: { HOME: process.env.HOME, PATH: bin }, stdout, stderr: sink(), platform: 'darwin', exists: () => false, exec, isTTY: false });
    assert.equal(code, 0);
    assert.deepEqual(exec.installs(), [['npm', 'install', '-g', '@openai/codex']]);
    assert.equal(stdout.text().split('\n').filter((l) => l === 'codex   installed').length, 1);
  });

  test('linux: npm tools run, grok and op are manual with their links, no brew needed', async () => {
    const r = await run(['install', '--yes'], { pathCmds: ['npm'], platform: 'linux' });
    assert.equal(r.code, 0, r.err);
    assert.deepEqual(r.exec.installs(), [
      ['npm', 'install', '-g', '@anthropic-ai/claude-code'],
      ['npm', 'install', '-g', '@openai/codex'],
      ['npm', 'install', '-g', '@google/gemini-cli'],
    ]);
    const lines = r.out.split('\n');
    assert.equal(lines.filter((l) => l === 'grok    skipped: manual — https://x.ai/build').length, 1);
    assert.equal(lines.filter((l) => l === 'op      skipped: manual — https://developer.1password.com/docs/cli/get-started/').length, 1);
  });

  test('darwin without brew: grok and op are manual "install Homebrew first"', async () => {
    const r = await run(['install', 'grok', 'op', '--yes'], { pathCmds: ['npm'] });
    assert.equal(r.code, 0);
    assert.equal(r.exec.installs().length, 0);
    const lines = r.out.split('\n');
    assert.equal(lines.filter((l) => l === 'grok    skipped: manual — install Homebrew first: https://brew.sh').length, 1);
    assert.equal(lines.filter((l) => l === 'op      skipped: manual — install Homebrew first: https://brew.sh').length, 1);
  });

  test('a TTY asks exactly one yes (default no); a "no" runs nothing and exits 1', async () => {
    const yes = fakeUi(true);
    const r = await run(['install'], { pathCmds: ['npm', 'brew'], isTTY: true, ui: yes });
    assert.equal(yes.asked, 1);
    assert.equal(yes.options[0].initialValue, false);
    assert.equal(r.exec.installs().length, 5);
    assertArgvOnly(r.exec);
    assert.equal(r.code, 0);
    const no = fakeUi(false);
    const r2 = await run(['install'], { pathCmds: ['npm', 'brew'], isTTY: true, ui: no });
    assert.equal(no.asked, 1);
    assert.equal(r2.exec.installs().length, 0);
    assert.equal(r2.code, 1);
  });

  test('non-TTY without --yes: prints the plan, runs 0 commands, exits 2 with the --yes hint', async () => {
    const r = await run(['install'], { pathCmds: ['npm', 'brew'] });
    assert.equal(r.code, 2);
    assert.equal(r.exec.installs().length, 0);
    assert.equal(r.out.split('\n').filter((l) => l === '  claude: npm install -g @anthropic-ai/claude-code').length, 1);
    assert.equal(r.err, 'tools: no terminal to confirm; nothing installed — pass --yes to install the plan above\n');
  });

  test('one failing install: the others still run, exit 1, the table says failed (exit 3)', async () => {
    const exec = fakeExec({
      installs: {
        '@openai/codex': 3,
        '@google/gemini-cli': { signal: 'SIGKILL' },
        'grok-build': { timedOut: true, signal: 'SIGTERM' },
        '1password-cli': { error: 'spawn brew ENOENT-FAKE-detail' },
      },
    });
    const r = await run(['install', '--yes'], { pathCmds: ['npm', 'brew'], exec });
    assert.equal(r.code, 1);
    assert.equal(r.exec.installs().length, 5);
    assertArgvOnly(r.exec);
    const lines = r.out.split('\n');
    assert.equal(lines.filter((l) => l === 'codex   failed (exit 3)').length, 1);
    assert.equal(lines.filter((l) => l === 'gemini  failed (signal SIGKILL)').length, 1);
    assert.equal(lines.filter((l) => l === 'grok    failed (timed out)').length, 1);
    assert.equal(lines.filter((l) => l === 'op      failed (did not start)').length, 1);
    assert.equal(lines.filter((l) => l.startsWith('claude  installed')).length, 1);
    assert.equal((r.out + r.err).split('ENOENT-FAKE-detail').length - 1, 0);
  });

  test('no ids: present tools are not listed in the final table', async () => {
    const r = await run(['install', '--yes'], { pathCmds: ['claude', 'codex', 'gemini', 'op', 'npm', 'brew'] });
    assert.equal(r.code, 0);
    const table = r.out.split('\n\n').at(-1).split('\n');
    assert.deepEqual(table, ['grok    installed (not on PATH yet — open a new shell)', 'solo    skipped: manual — https://soloterm.com (desktop app, then add its MCP entry)', '']);
  });

  test('--yes with no npm on PATH: 0 runs, one "skipped: manual" row per npm tool', async () => {
    const r = await run(['install', '--yes'], { pathCmds: [], platform: 'linux' });
    assert.equal(r.code, 0);
    assert.equal(r.exec.installs().length, 0);
    const lines = r.out.split('\n');
    for (const [id, pkg] of [['claude', '@anthropic-ai/claude-code'], ['codex', '@openai/codex'], ['gemini', '@google/gemini-cli']]) {
      assert.equal(lines.filter((l) => l === `${id.padEnd(7)} skipped: manual — install Node.js/npm first, then: npm install -g ${pkg}`).length, 1);
    }
    assert.equal(lines.filter((l) => l.includes('skipped: manual')).length, 6);
  });

  test('already installed id: "already installed", 0 runs; unknown id: exit 2 listing valid ids; --dry-run: 0 runs', async () => {
    const a = await run(['install', 'claude', '--yes'], { pathCmds: ['claude', 'npm'] });
    assert.equal(a.code, 0);
    assert.equal(a.exec.installs().length, 0);
    assert.equal(a.out, 'claude  already installed\n');

    const u = await run(['install', 'nope', '--yes'], { pathCmds: ['npm'] });
    assert.equal(u.code, 2);
    assert.equal(u.exec.calls.length, 0);
    assert.match(u.err, /^tools: unknown tool id\(s\): nope — valid ids: claude, codex, gemini, grok, op, solo\n/);

    const d = await run(['install', '--dry-run'], { pathCmds: ['npm', 'brew'], isTTY: true, ui: fakeUi(true) });
    assert.equal(d.code, 0);
    assert.equal(d.exec.installs().length, 0);
    assert.equal(d.out.split('\n').filter((l) => l === 'dry run: nothing installed').length, 1);
  });
});

describe('doctor recommended-tools row', () => {
  test('codex + op missing: exactly 1 INFO row with the exact text; a throwing check never rejects', async () => {
    const rows = await checkTools({ pathEnv: fakePath(['claude', 'gemini', 'grok', 'solo']), platform: 'linux', home: process.env.HOME, exists: () => false });
    assert.deepEqual(rows, [{ id: 'tools', status: 'INFO', label: 'tools', detail: 'missing recommended tools: codex, op — run code-forge tools install' }]);
    const thrown = await checkTools({ pathEnv: '', platform: 'darwin', home: process.env.HOME, exists: () => { throw new Error('boom'); } });
    assert.deepEqual(thrown, [{ id: 'tools', status: 'INFO', label: 'tools', detail: 'missing recommended tools: claude, codex, gemini, grok, op, solo — run code-forge tools install' }]);
  });

  test('one INFO row listing exactly the missing ids; none when all are present', async () => {
    const rows = await checkTools({ pathEnv: fakePath(['claude', 'codex', 'op']), platform: 'darwin', home: process.env.HOME, exists: () => false });
    assert.deepEqual(rows, [{ id: 'tools', status: 'INFO', label: 'tools', detail: 'missing recommended tools: gemini, grok, solo — run code-forge tools install' }]);
    const none = await checkTools({ pathEnv: fakePath(['claude', 'codex', 'gemini', 'grok', 'op']), platform: 'darwin', home: process.env.HOME, exists: (p) => p === path.join(process.env.HOME, 'Applications', 'Solo.app') });
    assert.deepEqual(none, []);
  });
});
