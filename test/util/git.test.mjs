import assert from 'node:assert/strict';
import { mock, test } from 'node:test';
import * as git from '../../src/util/git.mjs';

// The executing-helper tests below intercept `exec` with `mock.module`, which only exists when
// node runs with `--experimental-test-module-mocks` (as `npm test` does). Fail the whole file
// loudly and legibly instead of letting those tests die on "mock.module is not a function".
if (typeof mock.module !== 'function') {
  throw new Error(
    'test/util/git.test.mjs requires `node --experimental-test-module-mocks --test …` ' +
      '(run it via `npm test`); mock.module is unavailable without that flag.',
  );
}

/** @param {{result: string, code: number, stderr?: string}} reply */
async function withMockedExec(reply, /** @type {(mockedGit: typeof git, calls: {argv: string[], opts: any}[]) => Promise<void>} */ body) {
  /** @type {{argv: string[], opts: any}[]} */
  const calls = [];
  const execUrl = new URL('../../src/util/exec.mjs', import.meta.url).href;
  const mockHandle = mock.module(execUrl, {
    namedExports: {
      exec: async (argv, opts) => {
        calls.push({ argv, opts });
        return { signal: null, stdout: '', stderr: '', timedOut: false, ...reply };
      },
    },
  });
  try {
    // Cache-busting query so this dynamic import re-resolves `./exec.mjs` under the active mock
    // instead of returning the already-cached, real-`exec`-bound instance.
    const mockedGit = await import(`../../src/util/git.mjs?exec-mock=${Date.now()}-${Math.random()}`);
    await body(mockedGit, calls);
  } finally {
    mockHandle.restore();
  }
}

// Independent of git.mjs's own READ_COMMANDS/WRITE_COMMANDS — if git.mjs put a writing verb into
// its own READ_COMMANDS (or dropped 'commit' from its own WRITE_COMMANDS), a test built from
// those same exports couldn't catch it (circular oracle). These lists are written by hand here.
const KNOWN_WRITE_VERBS = [
  'commit', 'push', 'reset', 'checkout', 'switch', 'restore', 'add', 'rm', 'mv', 'merge',
  'rebase', 'cherry-pick', 'revert', 'tag', 'branch', 'stash', 'clean', 'am', 'apply', 'init',
  'clone', 'pull', 'gc', 'update-ref', 'config', 'notes', 'worktree', 'submodule',
];
const KNOWN_READ_VERBS = ['status', 'diff', 'show', 'rev-parse', 'merge-base', 'fetch', 'archive', 'log', 'ls-files'];

/** Every `*Argv` builder in git.mjs, called with plausible dummy args, no process spawned. */
const ARGV_CALLS = {
  statusArgv: () => git.statusArgv(),
  diffNameOnlyArgv: () => git.diffNameOnlyArgv('HEAD'),
  diffArgv: () => git.diffArgv('HEAD', 'foo.txt'),
  diffNoIndexArgv: () => git.diffNoIndexArgv('foo.txt'),
  showArgv: () => git.showArgv('HEAD', 'foo.txt'),
  revParseArgv: () => git.revParseArgv('HEAD'),
  isAncestorArgv: () => git.isAncestorArgv('HEAD~1', 'HEAD'),
  fetchArgv: () => git.fetchArgv(),
  archiveArgv: () => git.archiveArgv('HEAD'),
  logArgv: () => git.logArgv(),
  lsFilesArgv: () => git.lsFilesArgv(),
  commitArgv: () => git.commitArgv('a message'),
  pushArgv: () => git.pushArgv({ branch: 'feature/x' }),
};

/** The full, exact set of names git.mjs is expected to export. A new export must update this. */
const EXPECTED_EXPORTS = [
  'READ_COMMANDS',
  'WRITE_COMMANDS',
  'statusArgv',
  'diffNameOnlyArgv',
  'diffArgv',
  'diffNoIndexArgv',
  'showArgv',
  'revParseArgv',
  'isAncestorArgv',
  'fetchArgv',
  'archiveArgv',
  'logArgv',
  'lsFilesArgv',
  'commitArgv',
  'pushArgv',
  'status',
  'diffNameOnly',
  'diff',
  'diffNoIndex',
  'show',
  'revParse',
  'isAncestor',
  'fetch',
  'archive',
  'log',
  'lsFiles',
  'commit',
  'push',
].sort();

test('git.mjs exports exactly the known set — nothing added or removed unexamined', () => {
  assert.deepEqual(Object.keys(git).sort(), EXPECTED_EXPORTS);
});

test('git.mjs exports exactly the *Argv builders this test knows about', () => {
  const exported = Object.keys(git).filter((name) => name.endsWith('Argv')).sort();
  const known = Object.keys(ARGV_CALLS).sort();
  assert.deepEqual(exported, known, 'a new *Argv export was added without updating this test');
});

test('against an INDEPENDENT write/read verb classification, only commit/push builders produce a write', () => {
  const writers = [];
  const readers = [];

  for (const [name, invoke] of Object.entries(ARGV_CALLS)) {
    const argv = invoke();
    assert.equal(argv[0], 'git');
    const subcommand = argv[1];
    const isCommitOrPush = name === 'commitArgv' || name === 'pushArgv';

    if (isCommitOrPush) {
      assert.ok(KNOWN_WRITE_VERBS.includes(subcommand), `${name} (${subcommand}) is expected to be a write`);
      writers.push({ name, subcommand });
    } else {
      assert.ok(
        KNOWN_READ_VERBS.includes(subcommand),
        `${name} produced subcommand "${subcommand}", not in the independent read-verb allowlist`,
      );
      assert.equal(
        KNOWN_WRITE_VERBS.includes(subcommand),
        false,
        `${name} (${subcommand}) is a write verb by the independent classification`,
      );
      readers.push({ name, subcommand });
    }
  }

  assert.equal(writers.length, 2);
  assert.deepEqual(writers.map((w) => w.subcommand).sort(), ['commit', 'push']);
  assert.equal(readers.length, Object.keys(ARGV_CALLS).length - 2);
});

test(
  '`fetch` is deliberately classified READ (network + remote-tracking refs, never the local branch/working tree)',
  () => {
    assert.ok(git.READ_COMMANDS.includes('fetch'));
    assert.equal(git.WRITE_COMMANDS.includes('fetch'), false);
  },
);

// ── Executing helpers, with a mocked `exec` (node's `--experimental-test-module-mocks`) ────────
//
// The *Argv tests above prove the pure builders are correct. This proves the same about the
// executing wrappers: an exported function that called `exec(['git', 'reset', ...])` directly,
// bypassing an *Argv builder entirely, would not be caught by the tests above — it has to be
// exercised for real, with `exec` intercepted so no process actually spawns.

test('every executing helper issues exactly its own argv — and only commit()/push() write', async () => {
  await withMockedExec({ result: 'ok', code: 0 }, async (mockedGit, calls) => {
    const cwd = '/tmp/fake-repo';
    /** [helper name, invocation, the exact argv it must hand to exec] */
    /** @type {[string, () => Promise<unknown>, string[]][]} */
    const expectations = [
      ['status', () => mockedGit.status(cwd), ['git', 'status', '--porcelain', '--untracked-files=all']],
      ['diffNameOnly', () => mockedGit.diffNameOnly('HEAD', cwd), ['git', 'diff', '--name-only', 'HEAD']],
      ['diff', () => mockedGit.diff('HEAD', 'foo.txt', cwd), ['git', 'diff', 'HEAD', '--', 'foo.txt']],
      ['diffNoIndex', () => mockedGit.diffNoIndex('foo.txt', cwd), ['git', 'diff', '--no-index', '/dev/null', 'foo.txt']],
      ['show', () => mockedGit.show('HEAD', 'foo.txt', cwd), ['git', 'show', 'HEAD:foo.txt']],
      ['revParse', () => mockedGit.revParse('HEAD', cwd), ['git', 'rev-parse', 'HEAD']],
      ['isAncestor', () => mockedGit.isAncestor('HEAD~1', 'HEAD', cwd), ['git', 'merge-base', '--is-ancestor', 'HEAD~1', 'HEAD']],
      ['fetch', () => mockedGit.fetch(cwd), ['git', 'fetch']],
      ['archive', () => mockedGit.archive('HEAD', cwd), ['git', 'archive', 'HEAD']],
      ['log', () => mockedGit.log(cwd), ['git', 'log']],
      ['lsFiles', () => mockedGit.lsFiles(cwd), ['git', 'ls-files']],
      ['commit', () => mockedGit.commit('a message', cwd), ['git', 'commit', '-m', 'a message']],
      ['push', () => mockedGit.push(cwd, { branch: 'feature/x' }), ['git', 'push', 'origin', 'feature/x']],
    ];

    for (const [name, invoke, expectedArgv] of expectations) {
      const before = calls.length;
      await invoke();
      assert.equal(calls.length, before + 1, `${name} must call exec exactly once`);
      assert.deepEqual(calls[before].argv, expectedArgv, `${name} issued the wrong argv`);
      assert.equal(calls[before].opts.cwd, cwd, `${name} must pass cwd through`);
    }

    assert.equal(calls.length, 13);
    assert.equal(calls[11].argv[1], 'commit');
    assert.equal(calls[12].argv[1], 'push');
    const writerIndexes = calls.flatMap((c, i) => (KNOWN_WRITE_VERBS.includes(c.argv[1]) ? [i] : []));
    assert.deepEqual(writerIndexes, [11, 12], 'only the commit() and push() calls may carry a write verb');
    for (const call of calls.slice(0, 11)) {
      assert.ok(KNOWN_READ_VERBS.includes(call.argv[1]), `unexpected read subcommand: ${call.argv[1]}`);
    }
  });
});

test('fetch() and push() pass GIT_TERMINAL_PROMPT=0 and a default timeout to exec()', async () => {
  await withMockedExec({ result: 'ok', code: 0 }, async (mockedGit, calls) => {
    await mockedGit.fetch('/tmp/fake-repo');
    await mockedGit.push('/tmp/fake-repo', { branch: 'feature/x' });

    assert.equal(calls.length, 2);
    for (const { opts } of calls) {
      assert.equal(opts.env.GIT_TERMINAL_PROMPT, '0');
      assert.equal(opts.timeoutMs, 30000);
    }
  });
});

// ── Option-injection and type guards ────────────────────────────────────────

test('read-only builders reject a leading-dash ref/path (option injection), naming the guard', () => {
  const injection = { name: 'TypeError', message: /must not start with "-" .* option injection/ };
  assert.throws(() => git.diffNameOnlyArgv('--upload-pack=evil'), injection);
  assert.throws(() => git.diffArgv('--upload-pack=evil', 'foo.txt'), injection);
  assert.throws(() => git.showArgv('--output=/tmp/evil', 'foo.txt'), injection);
  assert.throws(() => git.revParseArgv('--output=/tmp/evil'), injection);
  assert.throws(() => git.archiveArgv('--output=/tmp/evil'), injection);
  assert.throws(() => git.isAncestorArgv('--evil', 'HEAD'), injection);
  assert.throws(() => git.isAncestorArgv('HEAD', '--evil'), injection);
  assert.throws(() => git.diffNoIndexArgv('--evil'), injection);
  assert.throws(() => git.logArgv(['--output=/tmp/evil']), injection);
  assert.throws(() => git.lsFilesArgv(['-o']), injection);
});

test('diffArgv/showArgv path and commitArgv message must be non-empty strings', () => {
  const badPath = { name: 'TypeError', message: /path must be a non-empty string/ };
  const badMessage = { name: 'TypeError', message: /message must be a non-empty string/ };
  // `undefined` is what a caller that forgot the argument passes; tsconfig is not strict, so the
  // type checker accepts it silently — which is exactly why the runtime check has to exist.
  assert.throws(() => git.diffArgv('HEAD', undefined), badPath);
  assert.throws(() => git.diffArgv('HEAD', ''), badPath);
  assert.throws(() => git.showArgv('HEAD', undefined), badPath);
  assert.throws(() => git.commitArgv(undefined), badMessage);
  assert.throws(() => git.commitArgv(''), badMessage);
});

test('diffArgv does not need to guard `path` — it already sits after a literal `--`', () => {
  // `path` starting with '-' is safe here specifically because of the `--` separator; unlike
  // `base`, it is not rejected.
  const argv = git.diffArgv('HEAD', '-oddly-named-file.txt');
  assert.deepEqual(argv, ['git', 'diff', 'HEAD', '--', '-oddly-named-file.txt']);
});

// ── diffNoIndexArgv / commitArgv / pushArgv content ──────────────────────────

test('diffNoIndexArgv builds a /dev/null comparison so every line of a new file is a +', () => {
  const argv = git.diffNoIndexArgv('src/new-file.mjs');
  assert.deepEqual(argv, ['git', 'diff', '--no-index', '/dev/null', 'src/new-file.mjs']);
});

test('pushArgv with no opts is a bare push', () => {
  assert.deepEqual(git.pushArgv(), ['git', 'push']);
});

test('pushArgv only adds -u origin <branch> when setUpstream is requested', () => {
  assert.deepEqual(git.pushArgv({ branch: 'feature/x' }), ['git', 'push', 'origin', 'feature/x']);
  assert.deepEqual(
    git.pushArgv({ branch: 'feature/x', setUpstream: true }),
    ['git', 'push', '-u', 'origin', 'feature/x'],
  );
});

test('pushArgv throws when setUpstream is true without a branch', () => {
  assert.throws(() => git.pushArgv({ setUpstream: true }), {
    name: 'TypeError',
    message: /setUpstream: true \}\) requires a branch/,
  });
});

/** [branch, the refusal it must produce] — every one would otherwise reach `git push origin <branch>`. */
/** @type {[string, RegExp][]} */
const INVALID_BRANCHES = [
  ['--force', /must not start with "-"/],
  ['--mirror', /must not start with "-"/],
  ['--delete', /must not start with "-"/],
  ['+main', /must not start with "\+" — a \+refspec is a force-push/],
  [':main', /must not contain ":"/],
  ['a:b', /must not contain ":"/],
  ['has space', /whitespace, control characters/],
  ['tab\there', /whitespace, control characters/],
  ['a~1', /~ \^ \? \* \[/],
  ['a^', /~ \^ \? \* \[/],
  ['a?b', /~ \^ \? \* \[/],
  ['a*', /~ \^ \? \* \[/],
  ['a[b', /~ \^ \? \* \[/],
  ['a\\b', /~ \^ \? \* \[/],
  ['a..b', /must not contain "\.\.", "@\{"/],
  ['a@{1}', /must not contain "\.\.", "@\{"/],
  ['@', /or be "@"/],
  ['feature/', /must not end with "\/"/],
  ['feature.', /must not end with "\/" or "\."/],
  ['a//b', /contain "\/\/"/],
  ['.hidden', /start with "\." or end with "\.lock"/],
  ['a/.b', /start with "\." or end with "\.lock"/],
  ['main.lock', /start with "\." or end with "\.lock"/],
];

for (const [branch, message] of INVALID_BRANCHES) {
  test(`pushArgv refuses branch ${JSON.stringify(branch)}`, () => {
    assert.throws(() => git.pushArgv({ branch }), { name: 'TypeError', message });
  });
}

test('pushArgv accepts ordinary branch names unchanged', () => {
  for (const branch of ['main', 'feature/x', 'release-1.2', 'user/name_x', 'fix/B0-round3', 'a.b']) {
    assert.deepEqual(git.pushArgv({ branch }), ['git', 'push', 'origin', branch]);
  }
});

test('pushArgv ignores unknown options and never emits a force flag', () => {
  // @ts-expect-error - deliberately passing an option pushArgv doesn't recognize
  const argv = git.pushArgv({ branch: 'feature/x', force: true, setUpstream: true });
  assert.deepEqual(argv, ['git', 'push', '-u', 'origin', 'feature/x']);
});

// ── isAncestor: exit 0 → true, exit 1 → false, anything else → throws ──────

test('isAncestor resolves exactly true on exit 0', async () => {
  await withMockedExec({ result: 'ok', code: 0 }, async (mockedGit) => {
    assert.equal(await mockedGit.isAncestor('HEAD~1', 'HEAD', '/tmp/r'), true);
  });
});

test('isAncestor resolves exactly false on exit 1 ("not an ancestor" is a normal answer)', async () => {
  await withMockedExec({ result: 'ok', code: 1 }, async (mockedGit, calls) => {
    assert.equal(await mockedGit.isAncestor('HEAD', 'HEAD~1', '/tmp/r'), false);
    assert.deepEqual(calls[0].opts.okExitCodes, [0, 1]);
  });
});

test('isAncestor throws (never silently returns false) when git fails with exit 128', async () => {
  await withMockedExec({ result: 'failed', code: 128, stderr: 'fatal: Not a valid object name nope' }, async (mockedGit) => {
    await assert.rejects(() => mockedGit.isAncestor('nope', 'HEAD', '/tmp/r'), {
      name: 'Error',
      message: /isAncestor\(nope, HEAD\) failed \(code 128\): fatal: Not a valid object name nope/,
    });
  });
});
