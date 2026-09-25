/**
 * Builds the `--scan-git` fixture repo (C12) — a throwaway git repo with two "reviewed then
 * reverted" files: one reverted a few days after ITS OWN review (inside a 7-day post-review
 * window) and one reverted well after ITS OWN review (outside it). Built fresh, in a temp dir, by
 * the test that calls this — never committed as a nested `.git` directory.
 *
 * Fix round 1 (2 MAJOR + 2 MINOR):
 * - The window the scanner checks is now relative to EACH FILE'S OWN review time, not scan time
 *   (see `src/ledger/outcome.mjs`), so this fixture's day offsets are expressed the same way:
 *   "days after that file's own review commit". Every commit still carries a MONOTONICALLY
 *   increasing date (base → outside-feature → inside-feature → inside-revert → outside-revert =
 *   HEAD) — a real repo's history looks like this; a version that back-dated reverts before the
 *   commits they reverted could pass for the wrong reason (a `git log --since` traversal
 *   shortcut, not the window logic).
 * - `baseEnv` isolates every git call from the developer's real global/system config
 *   (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`) and disables commit signing
 *   (`-c commit.gpgsign=false`); the caller is still responsible for isolating `HOME` (see
 *   `test/ledger/helpers.mjs`'s `withTempHome`), since `git` also reads `~/.gitconfig` directly.
 * - The reverts are made with real `git revert --no-edit`, so each carries the standard
 *   `This reverts commit <sha>.` body line a scanner can key off of, not just a matching subject.
 * - `git init -q` + `git symbolic-ref` instead of `git init -b main` (needs git ≥ 2.28).
 *
 * Fix round 2 (MAJOR): `git()` used to spread the WHOLE of `process.env` into the child. If this
 * suite ever runs from inside a git hook (a pre-commit hook running `npm test`, say), git sets
 * `GIT_DIR`/`GIT_WORK_TREE`/`GIT_INDEX_FILE`/`GIT_PREFIX`/`GIT_OBJECT_DIRECTORY`/`GIT_COMMON_DIR`
 * etc. in its own child's environment — with those present, git ignores `cwd` for repository
 * discovery entirely, so every `init`/`add`/`commit`/`revert` call below would hit the OUTER repo
 * (the one running the hook), not this temp fixture. Every `GIT_*` variable is now stripped from
 * the inherited environment before `baseEnv`'s deliberate ones are layered on top.
 */

import { execFileSync } from 'node:child_process';
import os from 'node:os';
import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** `process.env`, minus every `GIT_*` key — the deliberate ones the caller passes in `env` are layered back on top. */
function cleanEnv() {
  const out = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) out[key] = value;
  }
  return out;
}

/** @param {string[]} args @param {string} cwd @param {NodeJS.ProcessEnv} env */
function git(args, cwd, env) {
  return execFileSync('git', args, { cwd, env: { ...cleanEnv(), ...env }, stdio: 'pipe', encoding: 'utf8' });
}

/** @param {number} days @returns {string} */
function isoDaysAgo(days) {
  return new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();
}

/**
 * @param {string} dir - an existing, empty directory (the test creates and removes it).
 * @param {{outsideFeatureDaysAgo?: number, insideFeatureDaysAgo?: number, insideRevertDaysAgo?: number, outsideRevertDaysAgo?: number}} [opts]
 *   Defaults: outside file reviewed 25 days ago, reverted 17 days after review (OUTSIDE a 7-day
 *   window); inside file reviewed 20 days ago, reverted 5 days after review (INSIDE a 7-day window).
 * @returns {Promise<{insideFile: string, outsideFile: string, insideFeatureReviewedAt: string, outsideFeatureReviewedAt: string, insideRevertSha: string, outsideRevertSha: string}>}
 */
export async function buildRevertWindowRepo(dir, opts = {}) {
  const { outsideFeatureDaysAgo = 25, insideFeatureDaysAgo = 20, insideRevertDaysAgo = 15, outsideRevertDaysAgo = 8 } = opts;

  // The doc comment above promises a monotonic commit history (base < outside-feature <
  // inside-feature < inside-revert < outside-revert = HEAD) — enforce the day-offset ordering that
  // produces it, rather than trusting every caller to pass values that happen to preserve it
  // (fix round 2: an out-of-order caller silently brought back the exact bug round 1 removed).
  for (const [name, value] of Object.entries({ outsideFeatureDaysAgo, insideFeatureDaysAgo, insideRevertDaysAgo, outsideRevertDaysAgo })) {
    if (!Number.isFinite(value) || value < 0) {
      throw new RangeError(`buildRevertWindowRepo: ${name} must be a finite number >= 0, got ${value}`);
    }
  }
  if (!(outsideFeatureDaysAgo > insideFeatureDaysAgo && insideFeatureDaysAgo > insideRevertDaysAgo && insideRevertDaysAgo > outsideRevertDaysAgo)) {
    throw new RangeError(
      'buildRevertWindowRepo: day offsets must satisfy outsideFeatureDaysAgo > insideFeatureDaysAgo > insideRevertDaysAgo > outsideRevertDaysAgo ' +
        `(got ${JSON.stringify({ outsideFeatureDaysAgo, insideFeatureDaysAgo, insideRevertDaysAgo, outsideRevertDaysAgo })})`,
    );
  }

  const baseEnv = {
    GIT_AUTHOR_NAME: 'B6 Fixture',
    GIT_AUTHOR_EMAIL: 'b6@example.test',
    GIT_COMMITTER_NAME: 'B6 Fixture',
    GIT_COMMITTER_EMAIL: 'b6@example.test',
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
  };
  const insideFile = 'inside-window.txt';
  const outsideFile = 'outside-window.txt';
  const at = (iso) => ({ ...baseEnv, GIT_AUTHOR_DATE: iso, GIT_COMMITTER_DATE: iso });
  const commit = (msg, cwd, env) => git(['-c', 'commit.gpgsign=false', 'commit', '-q', '-m', msg], cwd, env);
  const revert = (sha, cwd, env) => git(['-c', 'commit.gpgsign=false', 'revert', '--no-edit', sha], cwd, env);

  // Monotonic date plan, oldest to newest (HEAD = the outside-window revert):
  //   base < outside-feature < inside-feature < inside-revert < outside-revert
  const baseDate = isoDaysAgo(outsideFeatureDaysAgo + 5);
  const outsideFeatureDate = isoDaysAgo(outsideFeatureDaysAgo);
  const insideFeatureDate = isoDaysAgo(insideFeatureDaysAgo);
  const insideRevertDate = isoDaysAgo(insideRevertDaysAgo);
  const outsideRevertDate = isoDaysAgo(outsideRevertDaysAgo);

  git(['init', '-q'], dir, baseEnv);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir, baseEnv);

  await writeFile(path.join(dir, insideFile), 'original\n');
  await writeFile(path.join(dir, outsideFile), 'original\n');
  git(['add', '-A'], dir, baseEnv);
  commit('base commit', dir, at(baseDate));

  await writeFile(path.join(dir, outsideFile), 'feature\n');
  git(['add', outsideFile], dir, baseEnv);
  commit('Add feature to outside file', dir, at(outsideFeatureDate));
  const outsideFeatureSha = git(['rev-parse', 'HEAD'], dir, baseEnv).trim();

  await writeFile(path.join(dir, insideFile), 'feature\n');
  git(['add', insideFile], dir, baseEnv);
  commit('Add feature to inside file', dir, at(insideFeatureDate));
  const insideFeatureSha = git(['rev-parse', 'HEAD'], dir, baseEnv).trim();

  revert(insideFeatureSha, dir, at(insideRevertDate));
  const insideRevertSha = git(['rev-parse', 'HEAD'], dir, baseEnv).trim();

  revert(outsideFeatureSha, dir, at(outsideRevertDate));
  const outsideRevertSha = git(['rev-parse', 'HEAD'], dir, baseEnv).trim();

  return {
    insideFile,
    outsideFile,
    insideFeatureReviewedAt: insideFeatureDate,
    outsideFeatureReviewedAt: outsideFeatureDate,
    insideRevertSha,
    outsideRevertSha,
  };
}

/**
 * Fix round 3: a revert CHAIN built with the local `git revert` (so the subjects are whatever this
 * git writes — `Reapply "…"` on git ≥ 2.43), all inside one 7-day window after the review:
 *
 * `chain.txt`, reviewed at day 0 (10 days ago):
 *   d+1 `git revert` of the feature         → depth 1, REVERTED
 *   d+2 `git revert` of that revert          → depth 2 (`Reapply "…"` on modern git), restore
 *   d+3 `git revert` of the reapply          → depth 3, REVERTED
 *   d+4 `git revert` of that, message hand-edited to "Restore chain feature after fix"
 *       (body still `This reverts commit <d+3>.`) → depth 2, restore — only the sha chain can tell
 * `oldstyle.txt`, reviewed at day 0:
 *   d+1 `git revert` of the feature          → REVERTED
 *   d+2 `git revert` of that revert, message rewritten to the pre-2.43 form
 *       `Revert "Revert "…""` + `This reverts commit <d+1>.` → restore
 *
 * @param {string} dir - an existing, empty directory.
 * @returns {Promise<{chainFile: string, oldStyleFile: string, reviewedAt: string, expectedRevertShas: string[], subjects: Record<string, string>}>}
 */
export async function buildRevertChainRepo(dir) {
  const baseEnv = {
    GIT_AUTHOR_NAME: 'B6 Fixture',
    GIT_AUTHOR_EMAIL: 'b6@example.test',
    GIT_COMMITTER_NAME: 'B6 Fixture',
    GIT_COMMITTER_EMAIL: 'b6@example.test',
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: os.devNull,
  };
  const at = (iso) => ({ ...baseEnv, GIT_AUTHOR_DATE: iso, GIT_COMMITTER_DATE: iso });
  const noSign = ['-c', 'commit.gpgsign=false'];
  const head = () => git(['rev-parse', 'HEAD'], dir, baseEnv).trim();
  const subjectOf = (sha) => git(['log', '-1', '--format=%s', sha], dir, baseEnv).trim();
  const revert = (sha, iso) => {
    git([...noSign, 'revert', '--no-edit', sha], dir, at(iso));
    return head();
  };
  const reword = (subject, body, iso) => {
    git([...noSign, 'commit', '--amend', '-q', '-m', subject, '-m', body], dir, at(iso));
    return head();
  };
  const reviewDaysAgo = 10;
  const day = (offset) => isoDaysAgo(reviewDaysAgo - offset);
  const chainFile = 'chain.txt';
  const oldStyleFile = 'oldstyle.txt';

  git(['init', '-q'], dir, baseEnv);
  git(['symbolic-ref', 'HEAD', 'refs/heads/main'], dir, baseEnv);
  await writeFile(path.join(dir, chainFile), 'original\n');
  await writeFile(path.join(dir, oldStyleFile), 'original\n');
  git(['add', '-A'], dir, baseEnv);
  git([...noSign, 'commit', '-q', '-m', 'base commit'], dir, at(isoDaysAgo(reviewDaysAgo + 5)));

  await writeFile(path.join(dir, chainFile), 'feature\n');
  await writeFile(path.join(dir, oldStyleFile), 'feature\n');
  git(['add', '-A'], dir, baseEnv);
  // Two separate feature commits so each file's chain reverts only that file.
  git(['reset', '-q', oldStyleFile], dir, baseEnv);
  git([...noSign, 'commit', '-q', '-m', 'Add chain feature'], dir, at(day(0)));
  const chainFeature = head();
  git(['add', oldStyleFile], dir, baseEnv);
  git([...noSign, 'commit', '-q', '-m', 'Add oldstyle feature'], dir, at(day(0)));
  const oldFeature = head();

  const chainRevert1 = revert(chainFeature, day(1));
  const oldRevert1 = revert(oldFeature, day(1));
  const chainReapply = revert(chainRevert1, day(2));
  revert(oldRevert1, day(2));
  const oldDouble = reword(`Revert "${subjectOf(oldRevert1)}"`, `This reverts commit ${oldRevert1}.`, day(2));
  const chainRevert2 = revert(chainReapply, day(3));
  revert(chainRevert2, day(4));
  const chainRestore = reword('Restore chain feature after fix', `This reverts commit ${chainRevert2}.`, day(4));

  return {
    chainFile,
    oldStyleFile,
    reviewedAt: day(0),
    expectedRevertShas: [chainRevert1, oldRevert1, chainRevert2],
    subjects: {
      chainRevert1: subjectOf(chainRevert1),
      chainReapply: subjectOf(chainReapply),
      chainRevert2: subjectOf(chainRevert2),
      chainRestore: subjectOf(chainRestore),
      oldDouble: subjectOf(oldDouble),
      chainReapplySha: chainReapply,
      chainRestoreSha: chainRestore,
      oldDoubleSha: oldDouble,
    },
  };
}
