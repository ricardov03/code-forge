import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
  FORBIDDEN,
  isForbidden,
  mergeForbidden,
  renderForClaude,
  renderForCodex,
  renderForGrok,
} from '../../src/util/forbidden.mjs';

// The exact set of ids §8.4 requires — a dropped/renamed entry must fail this, not just a count.
const REQUIRED_IDS = [
  'gh-pr-ready',
  'gh-pr-merge',
  'gh-pr-close',
  'gh-pr-base-retarget',
  'git-push-force',
  'git-reset-hard',
  'git-checkout-discard',
  'git-restore',
  'git-clean',
  'git-stash',
  'git-branch-force-delete',
  'git-branch-force-delete-long',
  'git-rm',
  'rm-rf',
  'production-marker',
].sort();

/**
 * The exact Claude/Grok rules each entry must render to — written out by hand (an independent
 * oracle, not derived from forbidden.mjs). One prefix rule per flag spelling right after the
 * command. `production-marker` renders to none: a match-anywhere rule is not expressible.
 */
const EXPECTED_RULES = {
  'gh-pr-ready': ['Bash(gh pr ready:*)'],
  'gh-pr-merge': ['Bash(gh pr merge:*)'],
  'gh-pr-close': ['Bash(gh pr close:*)'],
  'gh-pr-base-retarget': ['Bash(gh pr edit --base:*)', 'Bash(gh pr edit -B:*)'],
  'git-push-force': [
    'Bash(git push --force:*)',
    'Bash(git push -f:*)',
    'Bash(git push --force-with-lease:*)',
    'Bash(git push --force-if-includes:*)',
    'Bash(git push --mirror:*)',
  ],
  'git-reset-hard': ['Bash(git reset --hard:*)'],
  'git-checkout-discard': ['Bash(git checkout --:*)', 'Bash(git checkout --force:*)', 'Bash(git checkout -f:*)'],
  'git-restore': ['Bash(git restore:*)'],
  'git-clean': ['Bash(git clean:*)'],
  'git-stash': ['Bash(git stash:*)'],
  'git-branch-force-delete': ['Bash(git branch -D:*)'],
  'git-branch-force-delete-long': [
    'Bash(git branch --delete --force:*)',
    'Bash(git branch --delete -f:*)',
    'Bash(git branch -d --force:*)',
    'Bash(git branch -d -f:*)',
    'Bash(git branch --force --delete:*)',
    'Bash(git branch --force -d:*)',
    'Bash(git branch -f --delete:*)',
    'Bash(git branch -f -d:*)',
  ],
  'git-rm': ['Bash(git rm:*)'],
  'rm-rf': [
    'Bash(rm --recursive:*)',
    'Bash(rm --force:*)',
    'Bash(rm -r:*)',
    'Bash(rm -R:*)',
    'Bash(rm -f:*)',
    'Bash(rm -rf:*)',
    'Bash(rm -fr:*)',
    'Bash(rm -Rf:*)',
    'Bash(rm -fR:*)',
    'Bash(rm -rR:*)',
    'Bash(rm -Rr:*)',
  ],
  'production-marker': [],
};

/** The ONLY ids no pinned CLI rule syntax can express (asserted exactly). */
const UNEXPRESSIBLE_IDS = ['production-marker'];

/** @param {string} rule e.g. `Bash(git push -f:*)` → `['git', 'push', '-f']` */
function ruleToTokens(rule) {
  const match = /^Bash\((.*):\*\)$/.exec(rule);
  assert.ok(match, `not a Bash(...:*) rule: ${rule}`);
  return match[1].split(' ');
}

/** @param {string[]} values */
const sorted = (values) => [...values].sort();

test('the forbidden list has at least 12 entries', () => {
  assert.ok(FORBIDDEN.length >= 12, `expected >= 12 entries, got ${FORBIDDEN.length}`);
});

test('the forbidden list is exactly the §8.4 id set', () => {
  assert.deepEqual(FORBIDDEN.map((e) => e.id).sort(), REQUIRED_IDS);
});

test('every entry has a well-shaped id and kind, and well-shaped fields for its kind', () => {
  const ids = new Set();
  for (const entry of FORBIDDEN) {
    assert.equal(typeof entry.id, 'string');
    assert.ok(entry.id.length > 0, 'id must be non-empty');
    assert.equal(ids.has(entry.id), false, `duplicate id ${entry.id}`);
    ids.add(entry.id);

    assert.ok(['prefix', 'contains', 'commandFlag'].includes(entry.kind), `entry ${entry.id} has an invalid kind`);

    if (entry.kind === 'prefix' || entry.kind === 'contains') {
      assert.ok(Array.isArray(entry.tokens), `entry ${entry.id}.tokens must be an array`);
      assert.ok(entry.tokens.length > 0, `entry ${entry.id} has no tokens`);
      for (const token of entry.tokens) {
        assert.equal(typeof token, 'string', `entry ${entry.id} has a non-string token`);
        assert.ok(token.length > 0, `entry ${entry.id} has an empty token`);
      }
    } else {
      assert.ok(Array.isArray(entry.command) && entry.command.length > 0, `entry ${entry.id}.command must be a non-empty array`);
      assert.ok(Array.isArray(entry.groups) && entry.groups.length > 0, `entry ${entry.id}.groups must be a non-empty array`);
      for (const group of entry.groups) {
        const size = (group.anyOf?.length ?? 0) + (group.anyPrefixOf?.length ?? 0) + (group.shortLetters?.length ?? 0);
        assert.ok(size > 0, `entry ${entry.id} has an empty flag group`);
      }
    }
  }
});

test('the list, every entry, and every nested array are frozen', () => {
  assert.ok(Object.isFrozen(FORBIDDEN), 'FORBIDDEN itself is not frozen');
  for (const entry of FORBIDDEN) {
    assert.ok(Object.isFrozen(entry), `entry ${entry.id} is not frozen`);
    for (const key of ['tokens', 'command', 'groups']) {
      if (Array.isArray(entry[key])) {
        assert.ok(Object.isFrozen(entry[key]), `entry ${entry.id}.${key} is not frozen`);
      }
    }
    for (const group of entry.groups ?? []) {
      assert.ok(Object.isFrozen(group), `entry ${entry.id} has an unfrozen group`);
      for (const values of Object.values(group)) {
        assert.ok(Object.isFrozen(values), `entry ${entry.id} has an unfrozen group array`);
      }
    }
  }
  const prefixEntry = FORBIDDEN.find((e) => e.kind === 'prefix');
  const commandFlagEntry = FORBIDDEN.find((e) => e.kind === 'commandFlag');
  assert.ok(prefixEntry, 'no prefix entry to probe');
  assert.ok(commandFlagEntry, 'no commandFlag entry to probe');
  const frozenError = { name: 'TypeError', message: /not extensible|read.only|frozen/ };
  assert.throws(() => {
    // @ts-expect-error - tokens is typed ReadonlyArray; this proves it's ALSO frozen at runtime
    prefixEntry.tokens.push('x');
  }, frozenError);
  assert.throws(() => {
    // @ts-expect-error - command is typed ReadonlyArray; this proves it's ALSO frozen at runtime
    commandFlagEntry.command.push('x');
  }, frozenError);
  assert.throws(() => {
    // @ts-expect-error - groups is typed ReadonlyArray; this proves it's ALSO frozen at runtime
    commandFlagEntry.groups.push({});
  }, frozenError);
});

// ── isForbidden — the structural matcher ────────────────────────────────────

/** [argv, the exact id isForbidden must return] */
/** @type {[string[], string][]} */
const FORBIDDEN_CASES = [
  // git push --force, every reported spelling
  [['git', 'push', '--force'], 'git-push-force'],
  [['git', 'push', '-f'], 'git-push-force'],
  [['git', 'push', 'origin', 'main', '--force'], 'git-push-force'],
  [['git', 'push', '--force-with-lease'], 'git-push-force'],
  [['git', 'push', '--force-with-lease=main'], 'git-push-force'],
  [['git', 'push', '--force-with-lease=main:abc123', 'origin', 'main'], 'git-push-force'],
  [['git', 'push', '--force-if-includes=x'], 'git-push-force'],
  [['git', 'push', '-uf', 'origin', 'x'], 'git-push-force'],
  [['git', 'push', '-fu', 'origin', 'x'], 'git-push-force'],
  [['git', 'push', '--mirror'], 'git-push-force'],
  [['git', 'push', 'origin', '+main'], 'git-push-force'],
  // git global options before the subcommand, and argv[0] as a path
  [['git', '-C', '.', 'push', '--force'], 'git-push-force'],
  [['git', '-c', 'core.x=y', 'reset', '--hard'], 'git-reset-hard'],
  [['git', '--git-dir=.git', 'clean', '-fd'], 'git-clean'],
  [['git', '--git-dir', '.git', 'clean', '-fd'], 'git-clean'],
  [['git', '--work-tree=.', 'rm', 'x'], 'git-rm'],
  [['git', '--no-pager', '-P', 'stash'], 'git-stash'],
  [['git', '--frobnicate', 'push', '--force'], 'git-push-force'],
  [['git', '--frobnicate', 'value', 'reset', '--hard'], 'git-reset-hard'],
  [['/usr/bin/git', 'reset', '--hard'], 'git-reset-hard'],
  [['git', 'reset', '-q', '--hard', 'HEAD~1'], 'git-reset-hard'],
  // checkout / restore / branch
  [['git', 'checkout', '--', 'foo.txt'], 'git-checkout-discard'],
  [['git', 'checkout', 'main', '--', 'foo.txt'], 'git-checkout-discard'],
  [['git', 'checkout', '-f', 'main'], 'git-checkout-discard'],
  [['git', 'restore', 'foo.txt'], 'git-restore'],
  [['git', 'branch', '-D', 'old'], 'git-branch-force-delete'],
  [['git', 'branch', '--delete', '--force', 'old'], 'git-branch-force-delete-long'],
  [['git', 'branch', '--force', '--delete', 'old'], 'git-branch-force-delete-long'],
  [['git', 'branch', '-d', '-f', 'old'], 'git-branch-force-delete-long'],
  [['git', 'branch', '-df', 'old'], 'git-branch-force-delete-long'],
  // rm, every reported spelling
  [['rm', '-rf', 'src/'], 'rm-rf'],
  [['rm', '-fr', 'src/'], 'rm-rf'],
  [['rm', '-r', '-f', 'src/'], 'rm-rf'],
  [['rm', '-R', 'src/'], 'rm-rf'],
  [['rm', '-Rf', 'src/'], 'rm-rf'],
  [['rm', '-fR', 'src/'], 'rm-rf'],
  [['rm', '-rfv', 'src/'], 'rm-rf'],
  [['rm', '-vrf', 'src/'], 'rm-rf'],
  [['rm', '-rfi', 'src/'], 'rm-rf'],
  [['rm', '-r', '--force', 'x'], 'rm-rf'],
  [['rm', '--recursive', '--force', 'src/'], 'rm-rf'],
  [['/bin/rm', '-rf', 'src/'], 'rm-rf'],
  // gh
  [['gh', 'pr', 'merge', '12'], 'gh-pr-merge'],
  [['gh', 'pr', 'edit', '123', '--base', 'main'], 'gh-pr-base-retarget'],
  [['gh', 'pr', 'edit', '--title', 'x', '--base', 'main'], 'gh-pr-base-retarget'],
  [['gh', 'pr', 'edit', '-B', 'main'], 'gh-pr-base-retarget'],
  [['gh', 'pr', 'edit', '12', '-Bmain'], 'gh-pr-base-retarget'],
  [['gh', 'pr', 'edit', '12', '--base=main'], 'gh-pr-base-retarget'],
  // contains: substring of any single token
  [['deploy', '--env=production'], 'production-marker'],
  [['deploy', '--target', 'x', '--env=production-eu'], 'production-marker'],
];

for (const [argv, expectedId] of FORBIDDEN_CASES) {
  test(`isForbidden(${JSON.stringify(argv)}) returns exactly ${expectedId}`, () => {
    assert.equal(isForbidden(argv)?.id ?? null, expectedId);
  });
}

/** Legitimate commands — each must return null. */
const ALLOWED_CASES = [
  ['git', 'push'],
  ['git', 'push', 'origin', 'feature/x'],
  ['git', 'push', '-u', 'origin', 'feature/x'],
  ['git', 'commit', '-m', 'x'],
  ['git', 'status'],
  ['git', 'diff', 'HEAD'],
  ['git', '-C', '.', 'status'],
  ['git', '-c', 'core.pager=cat', 'log'],
  ['/usr/bin/git', 'log'],
  ['git', 'branch', '-d', 'merged-branch'],
  ['git', 'branch', '-f', 'x', 'HEAD'],
  ['git', 'checkout', '-b', 'feature'],
  ['git', 'reset', '--soft', 'HEAD~1'],
  ['rm', 'file.txt'],
  ['rm', '-i', 'file.txt'],
  ['rm', '-v', 'file.txt'],
  ['gh', 'pr', 'edit', '123', '--title', 'x'],
  ['gh', 'pr', 'view', '12'],
  ['deploy', '--env', 'production'], // documented limit: contains never spans two tokens
];

for (const argv of ALLOWED_CASES) {
  test(`isForbidden(${JSON.stringify(argv)}) returns null`, () => {
    assert.equal(isForbidden(argv), null);
  });
}

test('isForbidden returns null for an empty argv and refuses a non-array', () => {
  assert.equal(isForbidden([]), null);
  assert.throws(
    // @ts-expect-error - deliberately passing a shell string
    () => isForbidden('git push --force'),
    { name: 'TypeError', message: /argv must be an array of strings/ },
  );
});

// ── mergeForbidden — validation ─────────────────────────────────────────────

test('mergeForbidden refuses an empty, blank or non-string token', () => {
  const invalid = { name: 'TypeError', message: /invalid token .* must be a non-empty string/ };
  assert.throws(() => mergeForbidden(['']), invalid);
  assert.throws(() => mergeForbidden(['   ']), invalid);
  // @ts-expect-error - deliberately passing a non-string token
  assert.throws(() => mergeForbidden([42]), invalid);
});

test('mergeForbidden refuses a bare string instead of iterating it one character at a time', () => {
  assert.throws(
    // @ts-expect-error - deliberately passing a string instead of an array
    () => mergeForbidden('prod'),
    { name: 'TypeError', message: /extraTokens must be an array/ },
  );
});

test('mergeForbidden accepts tokens with ( ) * : characters — contains entries are never rendered', () => {
  const merged = mergeForbidden(['db.prod:5432', 'postgres://prod-host', 'weird(*)']);
  assert.equal(merged.length, FORBIDDEN.length + 3);
  assert.equal(isForbidden(['psql', '-h', 'db.prod:5432'], merged)?.id, 'production-marker-extra-0');
  assert.equal(isForbidden(['psql', 'postgres://prod-host/db'], merged)?.id, 'production-marker-extra-1');
  assert.equal(isForbidden(['x', 'a-weird(*)-b'], merged)?.id, 'production-marker-extra-2');
  const claude = renderForClaude(merged);
  assert.deepEqual(
    claude.slice(-3).map((r) => [r.id, r.rules.length, r.enforced]),
    [
      ['production-marker-extra-0', 0, false],
      ['production-marker-extra-1', 0, false],
      ['production-marker-extra-2', 0, false],
    ],
  );
});

test('mergeForbidden matches a configured name as a substring of a larger token, and leaves FORBIDDEN untouched', () => {
  const merged = mergeForbidden(['prod_main']);
  assert.equal(isForbidden(['mysql', '--database=prod_main'], merged)?.id, 'production-marker-extra-0');
  assert.equal(isForbidden(['mysql', '--database=prod_main']), null);
  assert.equal(FORBIDDEN.length, REQUIRED_IDS.length);
});

// ── Renderers ────────────────────────────────────────────────────────────────

test('the Claude renderer emits one item per entry, in list order, with exactly the expected rules', () => {
  const rendered = renderForClaude();
  assert.equal(rendered.length, FORBIDDEN.length);
  assert.deepEqual(rendered.map((r) => r.id), FORBIDDEN.map((e) => e.id));
  for (const item of rendered) {
    assert.deepEqual(sorted(item.rules), sorted(EXPECTED_RULES[item.id]), `rules for ${item.id}`);
    assert.equal(item.enforced, EXPECTED_RULES[item.id].length > 0, `enforced for ${item.id}`);
  }
});

test('exactly the unexpressible ids render with enforced: false', () => {
  for (const render of [renderForClaude, renderForGrok, renderForCodex]) {
    const unenforced = render().filter((r) => !r.enforced).map((r) => r.id);
    assert.deepEqual(unenforced, UNEXPRESSIBLE_IDS, `${render.name}`);
  }
});

test('the Grok renderer emits exactly one item per entry, with the same rules as Claude', () => {
  const claude = renderForClaude();
  const grok = renderForGrok();
  assert.equal(grok.length, FORBIDDEN.length);
  for (const entry of FORBIDDEN) {
    const matches = grok.filter((r) => r.id === entry.id);
    assert.equal(matches.length, 1, `expected exactly 1 Grok item for ${entry.id}`);
    assert.deepEqual(matches[0], claude.find((r) => r.id === entry.id));
    assert.deepEqual(sorted(matches[0].rules), sorted(EXPECTED_RULES[entry.id]));
  }
});

test('the Codex renderer emits one item per entry with token-array patterns and decision: forbidden', () => {
  const rendered = renderForCodex();
  assert.equal(rendered.length, FORBIDDEN.length);
  assert.deepEqual(rendered.map((r) => r.id), FORBIDDEN.map((e) => e.id));
  for (const item of rendered) {
    const expectedPatterns = EXPECTED_RULES[item.id].map(ruleToTokens);
    assert.deepEqual(
      sorted(item.patterns.map((p) => JSON.stringify(p))),
      sorted(expectedPatterns.map((p) => JSON.stringify(p))),
      `patterns for ${item.id}`,
    );
    assert.equal(item.decision, 'forbidden', `decision for ${item.id}`);
    assert.equal(item.enforced, expectedPatterns.length > 0);
    assert.equal(item.description, FORBIDDEN.find((e) => e.id === item.id).description);
  }
});

test('every rendered rule is itself refused by isForbidden (the CLI layer never allows what the matcher forbids)', () => {
  for (const item of renderForClaude()) {
    for (const rule of item.rules) {
      assert.equal(isForbidden([...ruleToTokens(rule), 'x'])?.id, item.id, rule);
    }
  }
});

test('a shorter list yields a matching renderer count (renderers are not hardcoded to the default length)', () => {
  const shortList = FORBIDDEN.slice(0, 3);
  assert.equal(renderForClaude(shortList).length, 3);
  assert.equal(renderForGrok(shortList).length, 3);
  assert.equal(renderForCodex(shortList).length, 3);
});
