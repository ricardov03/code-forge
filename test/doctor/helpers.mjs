/**
 * Test helpers for `test/doctor/**`. Importing `../session/helpers.mjs` first pins `$HOME` and the
 * cwd to one per-test-file temp parent (removed in `after()`).
 *
 * Every doctor run gets a PATH of exactly two entries: a fresh fake bin (first) and the directory
 * of the running `node` (for the fakes' `#!/usr/bin/env node`). The caller's PATH is NEVER
 * forwarded — a developer's real `claude`/`codex`/`grok` must be unreachable however doctor looks a
 * CLI up (`commandOnPath`, `exec`, `spawnSession`, the worker). The fake bin holds one wrapper per
 * provider CLI (logs the call, answers `--version`/`--help` from the pinned help fixtures or a
 * mutilated copy, otherwise runs B9a's fake) and a `git` wrapper that execs the real git by
 * absolute path. No `bins` seam: doctor finds each CLI the production way, through PATH.
 * No real CLI, no real key, no network.
 */

import { BINS, freshDir, PARENT, readRecords, sink } from '../session/helpers.mjs';
import { accessSync, chmodSync, constants, existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export { freshDir, PARENT, readRecords, sink };

const HERE = path.dirname(fileURLToPath(import.meta.url));
const HELP_DIR = path.resolve(HERE, '..', 'fixtures', 'help');
export const CLAUDE_HELP = readFileSync(path.join(HELP_DIR, 'claude-2.1.282.txt'), 'utf8');
const CODEX_HELP = readFileSync(path.join(HELP_DIR, 'codex-0.155.1.txt'), 'utf8');
const GROK_HELP = readFileSync(path.join(HELP_DIR, 'grok-1.0.34.txt'), 'utf8');

/** The provider CLI names doctor may spawn, each with its fake's `--version` line. */
export const CLI_NAMES = /** @type {const} */ (['claude', 'codex', 'grok']);
export const CLI_VERSION = Object.freeze({ claude: '2.1.282 (Claude Code)', codex: 'codex-cli 0.155.1', grok: 'grok 1.0.34' });

/** The directory of the running `node`: the only entry of the test PATH besides the fake bin. */
export const NODE_DIR = path.dirname(process.execPath);

/** First line of the fixture project's CLAUDE.md: what a leaking reviewer would echo. */
export const CLAUDE_MD_LINE = '# canary-project-rules-FAKE-b13b';
/** Fake Jev key (contains FAKE so scanners allow it). */
export const FAKE_JEV_KEY = 'FAKE-jev-key-b13b-4c1d9e2a7f';

process.env.CODE_FORGE_KEY_BACKEND = 'file';

/**
 * The real `git`, located ONCE on the caller's PATH (the only use the tests make of it): doctor's
 * `git init` and the worker's `git rev-parse` reach it through the fake bin's `git` wrapper.
 * @returns {string} absolute path.
 */
function realGit() {
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    if (dir.length === 0) continue;
    const candidate = path.join(dir, 'git');
    try {
      accessSync(candidate, constants.X_OK);
      if (statSync(candidate).isFile()) return candidate;
    } catch {
      // not here
    }
  }
  throw new Error('test/doctor: no git on PATH (doctor needs one)');
}
const REAL_GIT = realGit();

/**
 * A provider CLI wrapper: logs `<name>\t<own path>\t<argv[0..1]>` to `$FAKE_RECORD/wrapper-calls.tsv`,
 * answers `--version` / `--help` (codex: `exec --help`) from `helpFile`, anything else runs the fake.
 * @param {'claude'|'codex'|'grok'} name @param {string} helpFile @returns {string}
 */
function wrapperSource(name, helpFile) {
  return `#!/usr/bin/env node
// test/doctor fake-bin wrapper for \`${name}\`: logs the call, answers --version/--help, else runs B9a's fake.
import { appendFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const argv = process.argv.slice(2);
if (process.env.FAKE_RECORD) {
  appendFileSync(path.join(process.env.FAKE_RECORD, 'wrapper-calls.tsv'), \`${name}\\t\${fileURLToPath(import.meta.url)}\\t\${argv.slice(0, 2).join(' ')}\\n\`);
}
if (argv[0] === '--version') { process.stdout.write(${JSON.stringify(`${CLI_VERSION[name]}\n`)}); process.exit(0); }
if (argv.includes('--help')) { process.stdout.write(readFileSync(${JSON.stringify(helpFile)})); process.exit(0); }
await import(${JSON.stringify(pathToFileURL(BINS[name]).href)});
`;
}

/**
 * A fresh fake bin: `claude`, `codex`, `grok` wrappers (the claude one answering `--help` with
 * `claudeHelp`) and a `git` wrapper that execs the real git by absolute path.
 * @param {string} claudeHelp @returns {string} the directory.
 */
export function fakeBin(claudeHelp) {
  const dir = freshDir('bin');
  writeFileSync(path.join(dir, 'package.json'), '{"type":"module"}\n'); // the wrappers are ESM (extensionless)
  const help = { claude: claudeHelp, codex: CODEX_HELP, grok: GROK_HELP };
  for (const name of CLI_NAMES) {
    const helpFile = path.join(dir, `help-${name}.txt`);
    writeFileSync(helpFile, help[name]);
    const file = path.join(dir, name);
    writeFileSync(file, wrapperSource(name, helpFile));
    chmodSync(file, 0o755);
  }
  const git = path.join(dir, 'git');
  writeFileSync(git, `#!/bin/sh\n# test/doctor fake-bin git: the one outside binary, reached by absolute path.\nexec ${JSON.stringify(REAL_GIT)} "$@"\n`);
  chmodSync(git, 0o755);
  return dir;
}

/** @param {string} bin @returns {string} the test PATH: the fake bin first, then node's dir, nothing else. */
export function testPath(bin) {
  return [bin, NODE_DIR].join(path.delimiter);
}

/**
 * The wrapper calls a run made (from `wrapper-calls.tsv`), oldest first.
 * @param {string} records @returns {Array<{name: string, file: string, argv: string}>}
 */
export function wrapperCalls(records) {
  const file = path.join(records, 'wrapper-calls.tsv');
  if (!existsSync(file)) return [];
  return readFileSync(file, 'utf8')
    .split('\n')
    .filter((l) => l.length > 0)
    .map((l) => {
      const [name, path_, argv] = l.split('\t');
      return { name, file: path_, argv: argv ?? '' };
    });
}

/** @returns {number} the pid of a process that has already exited. */
function deadPid() {
  const res = spawnSync(process.execPath, ['-e', '0']);
  return /** @type {number} */ (res.pid);
}

/**
 * A run root whose owner is dead, with one pid-registry entry: what a killed run leaves behind.
 * @param {string} tmpRoot
 */
export function plantStaleRoot(tmpRoot) {
  const pid = deadPid();
  const dir = path.join(tmpRoot, 'r-stale-fake');
  mkdirSync(path.join(dir, 'pids'), { recursive: true, mode: 0o700 });
  writeFileSync(path.join(dir, 'owner.json'), JSON.stringify({ pid, start_time: 'Mon Jan  1 00:00:00 2024', created_at: '2024-01-01T00:00:00.000Z' }));
  writeFileSync(path.join(dir, 'pids', `${pid}.json`), JSON.stringify({ pid, start_time: 'Mon Jan  1 00:00:00 2024', argv0: 'fake-claude' }));
}

/**
 * A project dir with `.code-forge.yml` (anthropic, `tmp.root` = a fresh trusted dir, one gate)
 * and a `CLAUDE.md`.
 * @returns {{proj: string, tmpRoot: string}}
 */
export function makeProject() {
  const proj = freshDir('proj');
  const tmpRoot = freshDir('tmproot');
  chmodSync(tmpRoot, 0o700);
  writeFileSync(
    path.join(proj, '.code-forge.yml'),
    `version: 1
project:
  slug: doctor-test
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-1
gates:
  test: ["node", "--test"]
tmp:
  root: ${JSON.stringify(tmpRoot)}
`,
  );
  writeFileSync(path.join(proj, 'CLAUDE.md'), `${CLAUDE_MD_LINE}\n\nNever do X.\n`);
  return { proj, tmpRoot };
}

/**
 * The deps a doctor run gets: a minimal env whose PATH is the fresh fake bin plus node's dir
 * (never the caller's PATH or full env), no `bins` seam (every CLI resolves through that PATH),
 * a mocked Jev client that counts its calls, stdout/stderr sinks.
 * @param {{help?: string, env?: Record<string, string>, canary?: string}} [opts]
 */
export function doctorDeps(opts = {}) {
  const records = freshDir('records');
  const bin = fakeBin(opts.help ?? CLAUDE_HELP);
  const jevCalls = /** @type {any[]} */ ([]);
  const stdout = sink();
  const stderr = sink();
  const env = {
    PATH: testPath(bin),
    HOME: process.env.HOME ?? '',
    TMPDIR: process.env.TMPDIR ?? '',
    CODE_FORGE_KEY_BACKEND: 'file',
    FAKE_RECORD: records,
    ...(opts.env ?? {}),
  };
  const deps = {
    env,
    stdout,
    stderr,
    ...(opts.canary ? { canary: opts.canary } : {}),
    // B26: the recommended-tools row never looks at this machine (no real /Applications/Solo.app)
    platform: 'darwin',
    toolExists: () => false,
    askJev: async (/** @type {any} */ args) => {
      jevCalls.push(args);
      return { ok: true, answers: { risk: { value: 0, confidence: 0.9 } }, attempts: 1, ms: 1 };
    },
  };
  return { deps, stdout, stderr, jevCalls, records, bin };
}

/**
 * @param {Array<{id: string, status: string, label: string, detail: string}>} rows @param {string} id
 * @returns {{id: string, status: string, label: string, detail: string}}
 */
export function rowById(rows, id) {
  const found = rows.filter((r) => r.id === id);
  if (found.length !== 1) throw new Error(`expected exactly one row ${id}, got ${found.length}`);
  return found[0];
}

/**
 * The fake CLI runs that received the isolation prompt on stdin (from the fakes' records).
 * @param {string} records @returns {Array<Record<string, any>>}
 */
export function isolationRuns(records) {
  return readRecords(records).filter((r) => Buffer.from(r.stdin_b64, 'base64').toString('utf8').includes('CLAUDE.md or AGENTS.md'));
}

/** @param {number} pid @param {number} [ms] @returns {Promise<string>} the `kill(pid, 0)` error code once the pid is gone ('alive' if it never went). */
export async function goneCode(pid, ms = 3000) {
  const until = Date.now() + ms;
  for (;;) {
    try {
      process.kill(pid, 0);
    } catch (err) {
      return /** @type {NodeJS.ErrnoException} */ (err).code ?? 'error';
    }
    if (Date.now() > until) return 'alive';
    await new Promise((r) => setTimeout(r, 25));
  }
}
