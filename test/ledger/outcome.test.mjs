import assert from 'node:assert/strict';
import { mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { after, before, test } from 'node:test';
import { isRevertCommit, scanGitAndRecord, scanGitOutcomes } from '../../src/ledger/outcome.mjs';
import { readAllRows } from '../../src/ledger/write.mjs';
import { buildRevertChainRepo, buildRevertWindowRepo } from '../fixtures/repos/revert-window/build.mjs';

let repoDir;
let homeDir;
let originalHome;
let originalGitConfigNoSystem;
let originalGitConfigGlobal;
let fixture;

// The whole file's git calls — building the fixture AND scanning it — run under an isolated HOME
// set up front, in before(), never the developer's real one (fix round 1, MAJOR): build.mjs's own
// commits are already isolated via GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL, but outcome.mjs's
// read-only `git log`/`diff-tree`/`rev-parse` calls (run via `exec()`, which passes no env
// override of its own) don't get those automatically — fix round 2 sets them here too, at the
// process level, so this file's SCANNING calls are isolated from the machine's system git config
// exactly like the fixture's own commits are, not just from HOME/~/.gitconfig.
before(async () => {
  homeDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-home-'));
  originalHome = process.env.HOME;
  process.env.HOME = homeDir;
  originalGitConfigNoSystem = process.env.GIT_CONFIG_NOSYSTEM;
  originalGitConfigGlobal = process.env.GIT_CONFIG_GLOBAL;
  process.env.GIT_CONFIG_NOSYSTEM = '1';
  process.env.GIT_CONFIG_GLOBAL = os.devNull;
  repoDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-revert-window-'));
  fixture = await buildRevertWindowRepo(repoDir);
});

after(async () => {
  if (originalHome === undefined) delete process.env.HOME;
  else process.env.HOME = originalHome;
  if (originalGitConfigNoSystem === undefined) delete process.env.GIT_CONFIG_NOSYSTEM;
  else process.env.GIT_CONFIG_NOSYSTEM = originalGitConfigNoSystem;
  if (originalGitConfigGlobal === undefined) delete process.env.GIT_CONFIG_GLOBAL;
  else process.env.GIT_CONFIG_GLOBAL = originalGitConfigGlobal;
  await rm(repoDir, { recursive: true, force: true });
  await rm(homeDir, { recursive: true, force: true });
});

function reviewedEntries() {
  return [
    { file: fixture.insideFile, reviewedAt: fixture.insideFeatureReviewedAt, block: 'bIn' },
    { file: fixture.outsideFile, reviewedAt: fixture.outsideFeatureReviewedAt, block: 'bOut' },
  ];
}

test('scanGitOutcomes marks exactly 1 row reverted inside the 7-day post-review window, 0 for the revert outside it', async () => {
  const rows = await scanGitOutcomes({ cwd: repoDir, reviewed: reviewedEntries(), days: 7 });
  assert.equal(rows.length, 1);
  assert.equal(rows[0].file, fixture.insideFile);
  assert.equal(rows[0].outcome, 'reverted');
  assert.equal(rows[0].outcome_source, 'git');
  assert.equal(rows.some((r) => r.file === fixture.outsideFile), false);
});

test('control: a window wide enough to cover both reverts finds BOTH files, proving the 7-day case above excludes the outside file because of the window, not because it is undetectable', async () => {
  const rows = await scanGitOutcomes({ cwd: repoDir, reviewed: reviewedEntries(), days: 3650 });
  assert.deepEqual(
    rows.map((r) => r.file).sort(),
    [fixture.insideFile, fixture.outsideFile].sort(),
  );
});

test('a file that was never reviewed at all is not reported, even with a wide window', async () => {
  const rows = await scanGitOutcomes({
    cwd: repoDir,
    reviewed: [{ file: 'never-reviewed.txt', reviewedAt: fixture.insideFeatureReviewedAt }],
    days: 3650,
  });
  assert.equal(rows.length, 0);
});

test('scanGitAndRecord persists exactly 1 row for the inside file, under the isolated HOME', async () => {
  const slug = 'scan-git-e2e';
  const written = await scanGitAndRecord({ cwd: repoDir, reviewed: reviewedEntries(), days: 7, slug });
  assert.equal(written.length, 1);
  assert.equal(written[0].file, fixture.insideFile);

  const persisted = await readAllRows(slug);
  const outcomeRows = persisted.filter((r) => r.event === 'outcome');
  assert.equal(outcomeRows.length, 1);
  assert.equal(outcomeRows[0].file, fixture.insideFile);
  assert.equal(outcomeRows[0].outcome, 'reverted');
  assert.equal(outcomeRows[0].outcome_source, 'git');
  assert.equal(outcomeRows.filter((r) => r.file === fixture.outsideFile).length, 0);
  // The block gate's acceptance clause requires tokens_source/cost_source on 100% of rows,
  // including outcome rows — write.mjs stamps them unconditionally, asserted directly here too.
  assert.equal(outcomeRows[0].tokens_source, null);
  assert.equal(outcomeRows[0].cost_source, null);
});

test('isRevertCommit does NOT false-positive on a subject that merely mentions "revert" near a PR number', () => {
  assert.equal(isRevertCommit({ subject: 'Prevent revert of cache (#12)', body: '' }), false);
  assert.equal(isRevertCommit({ subject: "don't revert #3", body: '' }), false);
});

test('isRevertCommit recognizes gits real revert subject form and the "This reverts commit" body marker', () => {
  assert.equal(isRevertCommit({ subject: 'Revert "Add feature X"', body: '' }), true);
  assert.equal(isRevertCommit({ subject: 'Something else entirely', body: 'This reverts commit abc1234.' }), true);
});

test('isRevertCommit does NOT count a revert-of-a-revert as a new "reverted" outcome (it restores the original change)', () => {
  // Real `git revert` of a revert nests quotes WITHOUT escaping — `Revert "Revert "X""`, not
  // `Revert "Revert \"X\""`. The earlier (round-1) version of this test used the escaped form,
  // which an implementation that only recognizes the literal backslash form would still pass.
  assert.equal(isRevertCommit({ subject: 'Revert "Revert "Add feature X""', body: '' }), false);
  // A real revert-of-a-revert ALSO carries the standard body marker (its own commit reverts the
  // first revert commit) — the double-revert exclusion must win regardless, since
  // REVERTS_COMMIT_BODY_RE alone would otherwise still classify it as a fresh "reverted" outcome.
  assert.equal(isRevertCommit({ subject: 'Revert "Revert "Add feature X""', body: 'This reverts commit abc1234.' }), false);
});

test('scanGitAndRecord is idempotent: running it a second time appends 0 new rows and the ledger still holds exactly 1', async () => {
  const slug = 'scan-git-idempotent';
  const first = await scanGitAndRecord({ cwd: repoDir, reviewed: reviewedEntries(), days: 7, slug });
  assert.equal(first.length, 1);
  const second = await scanGitAndRecord({ cwd: repoDir, reviewed: reviewedEntries(), days: 7, slug });
  assert.equal(second.length, 0);

  const persisted = await readAllRows(slug);
  assert.equal(persisted.filter((r) => r.event === 'outcome' && r.outcome === 'reverted').length, 1);
});

// ── Fix round 2 ──────────────────────────────────────────────────────────────

test('PIN: the ledger write actually lands under the temp HOME, not wherever write.mjs/outcome.mjs were imported from — proves HOME being switched AFTER the top-of-file import still routes correctly (ledgerDir() reads os.homedir() fresh on every call, never caches it at import time)', async () => {
  const slug = 'pin-temp-home';
  await scanGitAndRecord({ cwd: repoDir, reviewed: reviewedEntries(), days: 7, slug });
  const { stat } = await import('node:fs/promises');
  const expected = path.join(homeDir, '.code-forge', 'ledger', `${slug}.jsonl`);
  await assert.doesNotReject(() => stat(expected), `expected the ledger to exist at ${expected} (under the isolated HOME)`);
});

test('a revert between an EARLIER and a LATER review of the same file is still attributed to the earlier review, not missed (keeps every review, not just the latest)', async () => {
  // The inside file was reviewed at insideFeatureReviewedAt and reverted 5 days later (inside a
  // 7-day window). Add a SECOND, much later "re-review" of the same file — under the round-1
  // design (latest review only), the revert (which predates this later review) would no longer be
  // judged against the review that actually preceded it, and would be silently dropped.
  const muchLaterReview = new Date(new Date(fixture.insideFeatureReviewedAt).getTime() + 1000 * 24 * 60 * 60 * 1000).toISOString();
  const rows = await scanGitOutcomes({
    cwd: repoDir,
    reviewed: [
      { file: fixture.insideFile, reviewedAt: fixture.insideFeatureReviewedAt, block: 'bIn-early' },
      { file: fixture.insideFile, reviewedAt: muchLaterReview, block: 'bIn-late' },
      { file: fixture.outsideFile, reviewedAt: fixture.outsideFeatureReviewedAt, block: 'bOut' },
    ],
    days: 7,
  });
  assert.equal(rows.length, 1);
  assert.equal(rows[0].file, fixture.insideFile);
  assert.equal(rows[0].block, 'bIn-early', 'the revert predates the later review and must be linked to the EARLIER one that actually preceded it');
});

test('a reviewed entry with an unparseable reviewedAt is skipped, not a crash for the whole scan', async () => {
  const rows = await scanGitOutcomes({
    cwd: repoDir,
    reviewed: [
      { file: fixture.insideFile, reviewedAt: 'not-a-real-date', block: 'broken' },
      { file: fixture.outsideFile, reviewedAt: fixture.outsideFeatureReviewedAt, block: 'bOut' },
    ],
    days: 3650,
  });
  // insideFile's only reviewed entry is unparseable and must be dropped (0 rows for it);
  // outsideFile's valid entry must still be scanned normally (1 row, wide window).
  assert.deepEqual(rows.map((r) => r.file), [fixture.outsideFile]);
});

test('scanGitOutcomes rejects a non-finite or non-positive days instead of crashing on Date math or silently returning 0 rows', async () => {
  await assert.rejects(() => scanGitOutcomes({ cwd: repoDir, reviewed: reviewedEntries(), days: NaN }), RangeError);
  await assert.rejects(() => scanGitOutcomes({ cwd: repoDir, reviewed: reviewedEntries(), days: 0 }), RangeError);
  await assert.rejects(() => scanGitOutcomes({ cwd: repoDir, reviewed: reviewedEntries(), days: -5 }), RangeError);
});

test('a reviewed[].file relative to a SUBDIRECTORY cwd is normalized to repo-root-relative before matching git\'s output', async () => {
  const { mkdir } = await import('node:fs/promises');
  const subdir = path.join(repoDir, 'a-subdir');
  await mkdir(subdir, { recursive: true });
  // insideFile lives at the repo root; from `subdir`, its path is "../inside-window.txt".
  const relativeFromSubdir = path.join('..', fixture.insideFile);
  const rows = await scanGitOutcomes({
    cwd: subdir,
    reviewed: [{ file: relativeFromSubdir, reviewedAt: fixture.insideFeatureReviewedAt, block: 'bIn' }],
    days: 7,
  });
  assert.equal(rows.length, 1);
  assert.equal(rows[0].file, fixture.insideFile, 'must be normalized to the repo-root-relative form git itself reports');
});

test('buildRevertWindowRepo rejects day offsets that would break its promised monotonic commit order', async () => {
  const badDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-bad-offsets-'));
  try {
    // insideRevertDaysAgo (15) < outsideRevertDaysAgo (8) is fine on its own scale, but the
    // required ordering is outsideFeature > insideFeature > insideRevert > outsideRevert — flip
    // insideFeature and outsideFeature to violate it.
    await assert.rejects(
      () => buildRevertWindowRepo(badDir, { outsideFeatureDaysAgo: 10, insideFeatureDaysAgo: 20, insideRevertDaysAgo: 15, outsideRevertDaysAgo: 8 }),
      RangeError,
    );
  } finally {
    await rm(badDir, { recursive: true, force: true });
  }
});

test('the fixture repo still builds and is scanned correctly even when GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE point at a bogus outer repo — every inherited GIT_* var is stripped, not just overridden by the fixture\'s own', async () => {
  const bogusDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-bogus-outer-'));
  const leaked = {
    GIT_DIR: path.join(bogusDir, 'does-not-exist', '.git'),
    GIT_WORK_TREE: path.join(bogusDir, 'does-not-exist'),
    GIT_INDEX_FILE: path.join(bogusDir, 'does-not-exist', 'index'),
    GIT_PREFIX: 'bogus/',
    GIT_OBJECT_DIRECTORY: path.join(bogusDir, 'does-not-exist', 'objects'),
    GIT_COMMON_DIR: path.join(bogusDir, 'does-not-exist', 'common'),
  };
  for (const [key, value] of Object.entries(leaked)) process.env[key] = value;
  try {
    const leakDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-revert-window-leak-'));
    try {
      const leakFixture = await buildRevertWindowRepo(leakDir); // must not throw despite the bogus GIT_DIR etc.
      const rows = await scanGitOutcomes({
        cwd: leakDir,
        reviewed: [
          { file: leakFixture.insideFile, reviewedAt: leakFixture.insideFeatureReviewedAt },
          { file: leakFixture.outsideFile, reviewedAt: leakFixture.outsideFeatureReviewedAt },
        ],
        days: 7,
      });
      assert.equal(rows.length, 1);
      assert.equal(rows[0].file, leakFixture.insideFile);
    } finally {
      await rm(leakDir, { recursive: true, force: true });
    }
  } finally {
    for (const key of Object.keys(leaked)) delete process.env[key];
    await rm(bogusDir, { recursive: true, force: true });
  }
});

// ── Fix round 3 ──────────────────────────────────────────────────────────────

test('isRevertCommit: plain revert = true; old-style `Revert "Revert …"` and git ≥ 2.43 `Reapply "…"` = false; a revert of a reapply = true', () => {
  const body = 'This reverts commit abc1234def.';
  assert.equal(isRevertCommit({ subject: 'Revert "Add X"', body }), true);
  assert.equal(isRevertCommit({ subject: 'Revert "Revert "Add X""', body }), false);
  assert.equal(isRevertCommit({ subject: 'Reapply "Add X"', body }), false);
  assert.equal(isRevertCommit({ subject: 'Reapply “Add X”', body }), false);
  assert.equal(isRevertCommit({ subject: 'Revert "Reapply "Add X""', body }), true);
  assert.equal(isRevertCommit({ subject: 'Reapply "Reapply "Add X""', body }), false);
  assert.equal(isRevertCommit({ subject: 'Revert "Revert "Revert "Add X"""', body }), true);
  assert.equal(isRevertCommit({ subject: 'Reapply the cache layer', body: '' }), false, 'a bare word "Reapply" without the quote is not git\'s form');
});

test('scanGitOutcomes on a REAL revert chain (local git) marks exactly the 3 true reverts — not the Reapply, not the old-style double revert, not the hand-edited restore', async () => {
  const chainDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-revert-chain-'));
  try {
    const chain = await buildRevertChainRepo(chainDir);
    const rows = await scanGitOutcomes({
      cwd: chainDir,
      reviewed: [
        { file: chain.chainFile, reviewedAt: chain.reviewedAt, block: 'bChain' },
        { file: chain.oldStyleFile, reviewedAt: chain.reviewedAt, block: 'bOld' },
      ],
      days: 7,
    });
    assert.deepEqual(rows.map((r) => r.commit).sort(), [...chain.expectedRevertShas].sort());
    assert.equal(rows.length, 3);
    for (const sha of [chain.subjects.chainReapplySha, chain.subjects.oldDoubleSha, chain.subjects.chainRestoreSha]) {
      assert.equal(rows.filter((r) => r.commit === sha).length, 0, `restore commit ${sha} must not be counted`);
    }
    assert.equal(rows.filter((r) => r.file === chain.chainFile).length, 2);
    assert.equal(rows.filter((r) => r.file === chain.oldStyleFile).length, 1);
  } finally {
    await rm(chainDir, { recursive: true, force: true });
  }
});

test('the chain fixture really exercises the git ≥ 2.43 subject form on this machine (pins what the scan test above is proving)', async () => {
  const chainDir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-revert-chain-subjects-'));
  try {
    const { subjects } = await buildRevertChainRepo(chainDir);
    assert.equal(subjects.chainRevert1, 'Revert "Add chain feature"');
    assert.equal(subjects.chainReapply, 'Reapply "Add chain feature"');
    assert.equal(subjects.chainRevert2, 'Revert "Reapply "Add chain feature""');
    assert.equal(subjects.chainRestore, 'Restore chain feature after fix');
    assert.equal(subjects.oldDouble, 'Revert "Revert "Add oldstyle feature""');
  } finally {
    await rm(chainDir, { recursive: true, force: true });
  }
});

test('an ABSOLUTE reviewed path to a file the revert DELETED still matches (realpath of the nearest existing ancestor, not the raw /var path)', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'code-forge-deleted-'));
  try {
    const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GIT_')));
    Object.assign(env, { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull, GIT_AUTHOR_NAME: 'a', GIT_AUTHOR_EMAIL: 'a@x.test', GIT_COMMITTER_NAME: 'a', GIT_COMMITTER_EMAIL: 'a@x.test' });
    const { execFileSync } = await import('node:child_process');
    const { mkdir, writeFile } = await import('node:fs/promises');
    const git = (args, date) => execFileSync('git', ['-c', 'commit.gpgsign=false', ...args], { cwd: dir, env: date ? { ...env, GIT_AUTHOR_DATE: date, GIT_COMMITTER_DATE: date } : env, stdio: 'pipe', encoding: 'utf8' });
    const reviewedAt = new Date(Date.now() - 5 * 86400000).toISOString();
    git(['init', '-q']);
    await writeFile(path.join(dir, 'keep.txt'), 'k\n');
    git(['add', '-A']);
    git(['commit', '-q', '-m', 'base'], new Date(Date.now() - 6 * 86400000).toISOString());
    await mkdir(path.join(dir, 'gone'), { recursive: true });
    await writeFile(path.join(dir, 'gone', 'new.txt'), 'n\n');
    git(['add', '-A']);
    git(['commit', '-q', '-m', 'Add new file'], reviewedAt);
    git(['revert', '--no-edit', 'HEAD'], new Date(Date.now() - 4 * 86400000).toISOString()); // deletes gone/new.txt (and gone/)
    const absolute = path.join(dir, 'gone', 'new.txt'); // unresolved os.tmpdir() form (/var/... on macOS)
    const rows = await scanGitOutcomes({ cwd: dir, reviewed: [{ file: absolute, reviewedAt }], days: 7 });
    assert.deepEqual(rows.map((r) => r.file), ['gone/new.txt']);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

test('recordCiOutcome rejects a missing, NaN, fractional or non-positive pr and writes 0 rows', async () => {
  const { recordCiOutcome } = await import('../../src/ledger/outcome.mjs');
  const slug = 'ci-bad-pr';
  for (const pr of [undefined, NaN, 1.5, 0, -3]) {
    await assert.rejects(async () => recordCiOutcome({ pr, ci: 'green', slug }), RangeError);
  }
  assert.equal((await readAllRows(slug)).length, 0);
  await recordCiOutcome({ pr: 7, ci: 'green', slug });
  assert.deepEqual((await readAllRows(slug)).map((r) => [r.pr, r.outcome]), [[7, 'correct']]);
});
