/**
 * B16 acceptance (plan §10.4 row B16, C14 "marketing corrections"):
 *   marketing file contains `npm install -g @codedology/code-forge` (>= 1) and `npx code-forge init` (0),
 *   contains `8 hours` and not `a few hours`, and no `%` followed by `saving`/`cheaper` (4 asserts).
 *
 * Plus (same row): "README commands run (1)". `README.md` is B16's other owned file, and every
 * command it documents must really execute — not merely parse as a shell line. There is no
 * `test/docs-readme.test.mjs` in B16's owned-files list, so that check lives here, in the one
 * `test/docs-*.test.mjs` file this block is required to ship.
 */

import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, after } from 'node:test';
import { spawnSync } from 'node:child_process';
import { liveWorker } from '../src/worker/queue.mjs';

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

test('marketing file: the global install `npm install -g @codedology/code-forge` appears at least once', () => {
  assert.ok(occurrences(marketing, 'npm install -g @codedology/code-forge') >= 1);
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
test('README: installs globally with the scoped name, never the unscoped form', () => {
  assert.ok(occurrences(readme, 'npm install -g @codedology/code-forge') >= 1);
  assert.equal(occurrences(readme, 'npm install -g code-forge'), 0);
  assert.equal(occurrences(readme, 'npx code-forge init'), 0);
});

test('README guides a new user in order, under 250 lines, with one workflow diagram and resolving documentation links', () => {
  const lines = readme.trimEnd().split('\n');
  assert.ok(lines.length < 250, `README has ${lines.length} lines; maximum is 249`);
  assert.deepEqual(lines.filter((line) => line.startsWith('## ')), [
    '## The problem', '## Install', '## 5-minute quickstart', '## How it works',
    '## Key commands', '## Supported agents', '## Security model', '## Links',
  ]);
  assert.match(lines[2], /^A CLI .*Agent Skill.*delivery\.$/, 'one-line pitch');
  const problem = readme.split('## The problem\n\n')[1]?.split('\n\n')[0].split('\n') ?? [];
  assert.ok(problem.length >= 3 && problem.length <= 5, `problem has ${problem.length} lines; expected 3–5`);
  const diagrams = [...readme.matchAll(/```(?:text|mermaid)\n([\s\S]*?)```/g)];
  assert.equal(diagrams.length, 1, 'exactly one workflow diagram');
  assert.match(diagrams[0][1], /plan -> code -> review -> proof -> land/);
  const commands = readme.split('## Key commands\n')[1]?.split('\n## ')[0] ?? '';
  for (const verb of ['init', 'doctor', 'review', 'run', 'block', 'review-file', 'proof', 'gates', 'keys', 'report', 'logs', 'upgrade', '--help']) {
    const prefix = `| \`code-forge ${verb}`;
    assert.equal(commands.split('\n').filter((line) => line.startsWith(prefix) && [' ', '`'].includes(line[prefix.length])).length, 1, `one key-command row for ${verb}`);
  }
  const agents = readme.split('## Supported agents\n')[1]?.split('\n## ')[0] ?? '';
  for (const agent of ['Claude Code', 'Codex', 'Grok', 'Solo']) assert.equal(occurrences(agents, agent), 1, agent);
  for (const heading of ['### What ships', '### Review only', '### Configuration reference', '### Development']) {
    assert.equal(lines.filter((line) => line === heading).length, 1, heading);
  }
  const links = [...readme.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)].map((m) => m[1]);
  for (const target of ['docs/', 'CHANGELOG.md', 'docs/reference/config.md', 'docs/releasing.md', 'docs/review-only.md']) {
    assert.ok(links.includes(target), `README links to ${target}`);
  }
  for (const target of links.filter((link) => !/^https?:/.test(link))) {
    assert.ok(existsSync(path.resolve(REPO_ROOT, target.split('#')[0])), `README link resolves: ${target}`);
  }
  assert.equal(readme.split('\n').filter((line) => line === '- Roadmap: [public feature ideas](https://claude.ai/artifact/BmeWyqRxZYsWr7eH5vBtdF)').length, 1, 'one exact public Roadmap link');
});

// --- "README commands run": every command shown in README.md is spawned for real ------------

/**
 * Every non-comment line in a bash fence is a command, including unfamiliar executables.
 * Exact membership below prevents a newly documented command from silently escaping execution.
 * @param {string} readmeText
 * @returns {string[]}
 */
export function extractReadmeCommands(readmeText) {
  const fencedBashBlocks = [...readmeText.matchAll(/```bash\n([\s\S]*?)```/g)].map((m) => m[1]);
  return fencedBashBlocks
    .flatMap((block) => block.split('\n'))
    .map((line) => line.replace(/\s+#.*$/, '').trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'));
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
    line: 'code-forge init',
    check: (r) => {
      // no --no-jev/--jev-ref/--jev-env and no TTY: a real, deterministic refusal, not a hang.
      assert.equal(r.code, 2, r.stderr);
      assert.equal(JSON.parse(r.stdout).ok, false);
    },
  },
  {
    line: 'code-forge init --no-interaction --no-jev',
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
  {
    line: 'code-forge review',
    check: (r) => {
      // the temp repo's only changes are what `init` wrote: its config and the skill link, which
      // `review` skips by name — so a real base/file discovery ends in a deterministic `nothing to review`
      assert.equal(r.code, 0, r.stderr);
      assert.equal(r.stdout, 'nothing to review\n');
      assert.match(r.stderr, /^review: skipped \.code-forge\.yml \(code-forge config\)$/m);
    },
  },
];

test('README_COMMANDS is exactly the fenced ```bash command set in README.md (count and members)', () => {
  const parsed = extractReadmeCommands(readme);
  const declared = README_COMMANDS.map((c) => c.line);
  assert.deepEqual(parsed, declared, 'all bash commands execute in README order');
  assert.equal(parsed.length, 11);
});

const PARENT = mkdtempSync(path.join(os.tmpdir(), 'cf-docs-readme-'));
after(() => {
  try {
    // A failed start assertion or a timed-out test may never assign workerPid in its body.
    const worker = liveWorker(CWD);
    if (worker && Number.isInteger(worker.pid) && worker.pid > 1 && worker.pid !== process.pid) {
      process.kill(worker.pid, 'SIGTERM');
    }
  } catch {
    // A missing/corrupt announcement or an already-gone worker cannot prevent cleanup.
  } finally {
    rmSync(PARENT, { recursive: true, force: true });
  }
});

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

test('README verb reference: run/block/worker/review-file/proof/gates run for real, with no leaked worker process', async () => {
  const acceptancePath = path.join(CWD, 'acc.json');
  writeFileSync(acceptancePath, JSON.stringify([{ clause: 'demo clause', tests: ['t1'] }]));

  let workerPid;
  try {
    let r = cf(['run', 'start', '--run', 'readme-demo']);
    const pidMatch = /worker pid (\d+)/.exec(r.stdout);
    if (pidMatch) workerPid = Number(pidMatch[1]);
    assert.equal(r.code, 0, r.stderr);
    assert.ok(pidMatch, r.stdout);

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

    r = cf(['ledger', 'tail', '--slug', 'cwd']);
    assert.equal(r.code, 0, r.stderr);

    r = cf(['report', '--slug', 'cwd']);
    assert.equal(r.code, 0, r.stderr);
    assert.match(r.stdout, /== cost_per_block ==/);

    r = cf(['block', 'stop', 'B1', '--run', 'readme-demo', '--reason', 'docs smoke test']);
    assert.equal(r.code, 0, r.stderr);

    r = cf(['run', 'end', '--run', 'readme-demo']);
    assert.equal(r.code, 0, r.stderr);

    // `run end` signals the worker and returns; on a slow machine it can take a moment to exit.
    const pid = workerPid;
    const gone = () => {
      try {
        process.kill(pid, 0);
        return false;
      } catch {
        return true;
      }
    };
    for (let until = Date.now() + 10000; !gone() && Date.now() < until; ) await new Promise((r) => setTimeout(r, 50));
    assert.throws(() => process.kill(pid, 0), 'the worker process must be gone after `run end`');
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
