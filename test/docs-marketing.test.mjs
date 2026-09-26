/**
 * B16 acceptance (plan §10.4 row B16, C14 "marketing corrections"):
 *   marketing file contains `npx @codedology/code-forge init` (>= 1) and `npx code-forge init` (0),
 *   contains `8 hours` and not `a few hours`, and no `%` followed by `saving`/`cheaper` (4 asserts).
 *
 * Plus (same row): "README commands run (1)". `README.md` is B16's other owned file, and every
 * command it documents must really execute — not merely parse as a shell line. There is no
 * `test/docs-readme.test.mjs` in B16's owned-files list, so that check lives here, in the one
 * `test/docs-*.test.mjs` file this block is required to ship.
 */

import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, after } from 'node:test';
import { spawnSync } from 'node:child_process';

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const MARKETING_PATH = path.join(REPO_ROOT, 'docs', 'marketing', 'code-forge-story.md');
const README_PATH = path.join(REPO_ROOT, 'README.md');
const BIN_PATH = path.join(REPO_ROOT, 'bin', 'code-forge.mjs');

const marketing = readFileSync(MARKETING_PATH, 'utf8');
const readme = readFileSync(README_PATH, 'utf8');

/** @param {string} haystack @param {string} needle @returns {number} */
function occurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}

test('marketing file: `npx @codedology/code-forge init` appears at least once', () => {
  assert.ok(occurrences(marketing, 'npx @codedology/code-forge init') >= 1);
});

test('marketing file: the unscoped `npx code-forge init` never appears', () => {
  assert.equal(occurrences(marketing, 'npx code-forge init'), 0);
});

test('marketing file: says "8 hours" (the real key-cache default) and never "a few hours"', () => {
  assert.ok(marketing.includes('8 hours'));
  assert.equal(occurrences(marketing, 'a few hours'), 0);
});

test('marketing file: no `%` savings/cheaper claim (no ledger-backed number exists yet)', () => {
  assert.equal(/%[^.\n]{0,60}\b(saving|savings|cheaper)\b/i.test(marketing), false);
});

/** README uses the real package scope too, for the same reason as the marketing file. */
test('README: uses the scoped `npx @codedology/code-forge init`, never the unscoped form', () => {
  assert.ok(occurrences(readme, 'npx @codedology/code-forge init') >= 1);
  assert.equal(occurrences(readme, 'npx code-forge init'), 0);
});

// --- "README commands run": every command shown in README.md is spawned for real ------------

/**
 * Fix round 1: this used to be a hand-written list of commands that only LOOKED like it came
 * from the README. It now parses the file for real: every ```bash fenced block, split into
 * lines, a trailing ` # comment` stripped, kept only when the line is one of the two forms
 * README.md documents — `npx @codedology/code-forge …` (the one-shot install form) or
 * `code-forge …` (the installed-binary form). A README edit that adds, removes or rewords one of
 * these lines changes what this function returns, and the exact-match test right below the
 * `README_COMMANDS` array below will fail until the test is updated to match.
 * @param {string} readmeText
 * @returns {string[]}
 */
export function extractReadmeCommands(readmeText) {
  const fencedBashBlocks = [...readmeText.matchAll(/```bash\n([\s\S]*?)```/g)].map((m) => m[1]);
  return fencedBashBlocks
    .flatMap((block) => block.split('\n'))
    .map((line) => line.replace(/\s+#.*$/, '').trim())
    .filter((line) => line.startsWith('npx @codedology/code-forge') || line.startsWith('code-forge'));
}

/** `npx @codedology/code-forge init …` / `code-forge …` -> the argv the real CLI receives. */
function argvForReadmeCommand(line) {
  const prefix = line.startsWith('npx @codedology/code-forge') ? 'npx @codedology/code-forge' : 'code-forge';
  return line.slice(prefix.length).trim().split(/\s+/).filter(Boolean);
}

/**
 * One entry per command line this test spawns, in README order, each with the check that proves
 * it reached real code (not just "a defined exit code"). `.line` values here must be exactly the
 * set `extractReadmeCommands(readme)` returns — asserted immediately below the array — so this
 * array cannot silently drift out of sync with what README.md actually shows.
 */
const README_COMMANDS = [
  {
    line: 'npx @codedology/code-forge init',
    check: (r) => {
      // no --no-jev/--jev-ref/--jev-env and no TTY: a real, deterministic refusal, not a hang.
      assert.equal(r.code, 2, r.stderr);
      assert.equal(JSON.parse(r.stdout).ok, false);
    },
  },
  {
    line: 'npx @codedology/code-forge init --no-interaction --no-jev',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.equal(JSON.parse(r.stdout).ok, true);
    },
  },
  {
    line: 'code-forge validate',
    check: (r) => assert.equal(r.code, 0, r.stderr),
  },
  {
    line: 'code-forge resolve L1',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.deepEqual(Object.keys(JSON.parse(r.stdout)), ['provider', 'model', 'effort', 'fallback', 'cli']);
    },
  },
  {
    line: 'code-forge doctor --quick',
    check: (r) => assert.equal(r.code, 0, r.stderr),
  },
  {
    line: 'code-forge list',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.match(r.stdout, /^INSTALLED\n/);
    },
  },
  {
    line: 'code-forge keys list',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.match(r.stdout, /^NAME\tSOURCE\tBACKEND\tEXPIRES\n/);
    },
  },
  {
    line: 'code-forge models',
    check: (r) => assert.equal(r.code, 0, r.stderr),
  },
  {
    line: 'code-forge --help',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.match(r.stdout, /^ {2}init$/m);
      assert.match(r.stdout, /^ {2}doctor$/m);
    },
  },
  {
    line: 'code-forge version',
    check: (r) => {
      assert.equal(r.code, 0, r.stderr);
      assert.match(r.stdout, /^@codedology\/code-forge \d+\.\d+\.\d+\n$/);
    },
  },
];

test('README_COMMANDS is exactly the fenced ```bash command set in README.md (count and members)', () => {
  const parsed = extractReadmeCommands(readme);
  const declared = README_COMMANDS.map((c) => c.line);
  assert.deepEqual([...parsed].sort(), [...declared].sort());
  assert.equal(parsed.length, 10);
});

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-docs-readme-'));
after(() => rmSync(PARENT, { recursive: true, force: true }));

const HOME = path.join(PARENT, 'home');
const CWD = path.join(PARENT, 'cwd');
mkdirSync(HOME, { recursive: true });
mkdirSync(CWD, { recursive: true });

const GIT_ENV = { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' };

/** @param {string[]} argv */
function git(argv) {
  const res = spawnSync('git', argv, { cwd: CWD, env: { ...process.env, ...GIT_ENV }, encoding: 'utf8' });
  assert.equal(res.status, 0, `git ${argv.join(' ')} failed: ${res.stderr}`);
}
git(['init', '-q']);
git(['-c', 'user.email=docs@example.invalid', '-c', 'user.name=docs', 'commit', '--allow-empty', '-q', '-m', 'init']);

/**
 * Spawns the real CLI (`bin/code-forge.mjs`) with a temp HOME and temp cwd — never the real
 * `~`, never the repo. `PATH` is inherited (this is an end-to-end CLI smoke test: it needs the
 * real `node`/`git` the shell would use to run these commands, per README).
 * @param {string[]} argv
 */
function cf(argv) {
  const res = spawnSync(process.execPath, [BIN_PATH, ...argv], {
    cwd: CWD,
    env: { ...process.env, HOME, ...GIT_ENV },
    encoding: 'utf8',
    timeout: 15_000,
  });
  return { code: res.status, stdout: res.stdout ?? '', stderr: res.stderr ?? '' };
}

test('README Quickstart: every parsed command runs for real, in README order, against a temp HOME/cwd', () => {
  for (const { line, check } of README_COMMANDS) {
    const r = cf(argvForReadmeCommand(line));
    check(r);
  }
});

test('README verb reference: run/block/worker/review-file/proof/gates run for real, with no leaked worker process', () => {
  const acceptancePath = path.join(CWD, 'acc.json');
  writeFileSync(acceptancePath, JSON.stringify([{ clause: 'demo clause', tests: ['t1'] }]));

  let workerPid;
  try {
    let r = cf(['run', 'start', '--run', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);
    const pidMatch = /worker pid (\d+)/.exec(r.stdout);
    assert.ok(pidMatch, r.stdout);
    workerPid = Number(pidMatch[1]);

    r = cf(['run', 'status', '--run', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(JSON.parse(r.stdout).run_id, 'readme-demo');

    r = cf(['block', 'open', 'B1', '--run', 'readme-demo', '--level', 'L1', '--owned', 'src/x.mjs', '--acceptance', 'acc.json']);
    assert.equal(r.code, 0, r.stderr);

    // a second worker refuses to double-serve the same queue — a real, documented refusal.
    r = cf(['worker', '--run', 'readme-demo', '--once']);
    assert.equal(r.code, 1);
    assert.match(r.stderr, /already serves this queue/);

    r = cf(['review-file', 'src/x.mjs', '--block', 'B1', '--run', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(JSON.parse(r.stdout).status, 'queued');

    r = cf(['proof', 'tier', '--file', 'src/x.mjs', '--risk', '0']);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(JSON.parse(r.stdout).tier, 'light');

    r = cf(['gates', 'detect', '--cwd', CWD]);
    assert.equal(r.code, 0, r.stderr);

    r = cf(['gates', 'run', '--cwd', CWD]);
    assert.equal(r.code, 0, r.stderr);
    assert.equal(JSON.parse(r.stdout).allOk, true);

    r = cf(['ledger', 'tail', '--slug', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);

    r = cf(['report', '--slug', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);
    assert.match(r.stdout, /== cost_per_block ==/);

    r = cf(['block', 'stop', 'B1', '--run', 'readme-demo', '--reason', 'docs smoke test']);
    assert.equal(r.code, 0, r.stderr);

    r = cf(['run', 'end', '--run', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);

    assert.throws(() => process.kill(workerPid, 0), 'the worker process must be gone after `run end`');
    workerPid = undefined;
  } finally {
    // Hygiene (coder-rules #8): never leave a detached child running.
    if (workerPid !== undefined) {
      try {
        process.kill(workerPid, 'SIGKILL');
      } catch {
        // already gone
      }
    }
  }
});
