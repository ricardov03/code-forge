// helpers FIRST: its import-time guard moves $HOME and cwd to a temp dir before any src module loads.
import { withFixture } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { assertOwned, computeScopes, expandBraces, findOverlap, globMatch, isExactEntry, ownsFile, scopeGate } from '../../src/state/registry.mjs';
import { dirtyFiles } from '../fixtures/repos/two-blocks/build.mjs';

test('registry X{a,b} Y{c} with the fixture tree dirty on {a,c,d}: X scope {a}, Y scope {c}, orphans {d}, both gates refuse', async () => {
  await withFixture(async ({ ws }) => {
    const files = dirtyFiles(ws);
    assert.deepEqual(files, ['a.txt', 'c.txt', 'd.txt']);
    const blocks = { X: { owned_files: ['a.txt', 'b.txt'] }, Y: { owned_files: ['c.txt'] } };
    assert.deepEqual(computeScopes(blocks, files), { scopes: { X: ['a.txt'], Y: ['c.txt'] }, orphans: ['d.txt'] });
    assert.deepEqual(scopeGate(blocks, 'X', files), { ok: false, scope: ['a.txt'], orphans: ['d.txt'] });
    assert.deepEqual(scopeGate(blocks, 'Y', files), { ok: false, scope: ['c.txt'], orphans: ['d.txt'] });
    const claimed = { ...blocks, X: { owned_files: ['a.txt', 'b.txt', 'd.txt'] } };
    assert.deepEqual(scopeGate(claimed, 'X', files), { ok: true, scope: ['a.txt', 'd.txt'], orphans: [] });
    assert.deepEqual(scopeGate(claimed, 'Y', files), { ok: true, scope: ['c.txt'], orphans: [] });
    assert.throws(() => scopeGate(blocks, 'Z', files), { code: 'no-block' });
  });
});

/** [ownedA, ownedB, expected pair or null] */
const OVERLAP_CASES = [
  [['src/**'], ['src/cli/run.mjs'], ['src/**', 'src/cli/run.mjs']],
  [['src/cli/run.mjs'], ['src/cli/run.mjs'], ['src/cli/run.mjs', 'src/cli/run.mjs']],
  [['src/cli/run.mjs'], ['src/cli/block.mjs'], null],
  [['src/*.mjs'], ['src/cli/run.mjs'], null],
  [['test/fixtures/repos/{node,go}/**'], ['test/fixtures/repos/two-blocks/**'], null],
  [['test/fixtures/repos/{node,two-blocks}/**'], ['test/fixtures/repos/two-blocks/build.mjs'], ['test/fixtures/repos/{node,two-blocks}/**', 'test/fixtures/repos/two-blocks/build.mjs']],
  [['src/state/**'], ['src/state/*.json'], ['src/state/**', 'src/state/*.json']],
  // Two globs whose static prefixes are segment-prefixes of each other (`src` ⊂ `src/cli`) are
  // treated as overlapping even though no path can match both — the prefix rule cannot rule it
  // out, so it fails closed (the next case shows sibling prefixes stay disjoint).
  [['src/*.mjs'], ['src/cli/*.mjs'], ['src/*.mjs', 'src/cli/*.mjs']],
  [['src/a/*.mjs'], ['src/b/*.mjs'], null],
  [['src/state/**'], ['src/engines/**'], null],
  // B57: an exact entry owns its path and every path below it — exact/exact, dir/file, file/dir,
  // dir/dir parent-child either way, siblings that only share a name prefix, glob/dir
  [['src/feature'], ['src/feature'], ['src/feature', 'src/feature']],
  [['src/feature'], ['src/feature/x.swift'], ['src/feature', 'src/feature/x.swift']],
  [['src/feature/x.swift'], ['src/feature'], ['src/feature/x.swift', 'src/feature']],
  [['src/feature'], ['src/feature/sub'], ['src/feature', 'src/feature/sub']],
  [['src/feature/sub'], ['src/feature'], ['src/feature/sub', 'src/feature']],
  [['a.txt', 'src/feature/deep/sub'], ['b.txt', 'src'], ['src/feature/deep/sub', 'src']],
  [['src/feature'], ['src/featureX'], null],
  [['src/feature'], ['src/featureX/a.swift'], null],
  [['src/featureX/a.swift'], ['src/feature'], null],
  [['src/feature'], ['src/**'], ['src/feature', 'src/**']],
  [['src/feature/*.swift'], ['src/feature'], ['src/feature/*.swift', 'src/feature']],
  [['src/feature'], ['src/*'], ['src/feature', 'src/*']],
  [['src/feature'], ['src/*/*.swift'], ['src/feature', 'src/*/*.swift']],
  [['src/feature'], ['src/feat*/x/**'], ['src/feature', 'src/feat*/x/**']],
  [['src/feature'], ['src/{feature,other}/*.swift'], ['src/feature', 'src/{feature,other}/*.swift']],
  [['src/*.mjs'], ['src/feature'], null],
  [['src/featureX/*'], ['src/feature'], null],
  [['src/feature'], ['lib/**'], null],
  // fails closed: a FILE named exactly under a `**` glob overlaps (the entry could be a directory)
  [['src/README'], ['src/**/*.mjs'], ['src/README', 'src/**/*.mjs']],
  // a brace entry is a glob: its alternatives own exactly themselves, nothing below
  [['src/{feature,other}'], ['src/feature/x.swift'], null],
];

for (const [a, b, expected] of OVERLAP_CASES) {
  test(`findOverlap(${JSON.stringify(a)}, ${JSON.stringify(b)}) is ${JSON.stringify(expected)}`, () => {
    assert.deepEqual(findOverlap(a, b), expected);
  });
}

test('expandBraces expands every group, nested ones included, and leaves a brace-free pattern alone', () => {
  assert.deepEqual(expandBraces('src/{a,b/{c,d}}.mjs'), ['src/a.mjs', 'src/b/c.mjs', 'src/b/d.mjs']);
  assert.deepEqual(expandBraces('x/{p,q}/{1,2}'), ['x/p/1', 'x/p/2', 'x/q/1', 'x/q/2']);
  assert.deepEqual(expandBraces('plain/path.mjs'), ['plain/path.mjs']);
});

/** [file, pattern, expected] — the in-repo matcher (no experimental path.matchesGlob). */
/** @type {[string, string, boolean][]} */
const GLOB_CASES = [
  ['src/state/run.mjs', 'src/**', true],
  ['src', 'src/**', true], // ** matches zero segments
  ['src/a/b/c.mjs', 'src/**/c.mjs', true],
  ['src/c.mjs', 'src/**/c.mjs', true],
  ['src/a/b/c.mjs', 'src/*/c.mjs', false],
  ['src/x.mjs', 'src/*.mjs', true],
  ['src/cli/x.mjs', 'src/*.mjs', false], // * never crosses /
  ['src/ab.mjs', 'src/a?.mjs', true],
  ['src/a.mjs', 'src/a?.mjs', false],
  ['src/a.mjs', 'src/a.m*', true],
  ['src/a.mjs', 'src/a.mj', false],
  ['src/.hidden', 'src/*', true], // dot-files are ordinary names (fail closed)
  ['srcx/a.mjs', 'src/**', false],
  ['src/a+b.mjs', 'src/a+b.mjs', true], // regex metacharacters are literal
];

for (const [file, pattern, expected] of GLOB_CASES) {
  test(`globMatch(${JSON.stringify(file)}, ${JSON.stringify(pattern)}) is ${expected}`, () => {
    assert.equal(globMatch(file, pattern), expected);
  });
}

test('exact route paths with [ ] ( ) @ are accepted and own exactly that file — nothing a class/group reading would add', () => {
  const owned = ['pages/[id].vue', 'app/(group)/page.tsx', '@types/node.d.ts'];
  assert.deepEqual(assertOwned(owned), owned);
  // near misses a character-class, group or wildcard reading of the brackets would pull in
  const files = [
    'pages/[id].vue', 'pages/i.vue', 'pages/d.vue', 'pages/id.vue', 'pages/xid].vue',
    'app/(group)/page.tsx', 'app/group/page.tsx', 'app/page.tsx', 'app/xgroup)/page.tsx', '@types/node.d.ts',
  ];
  const { scopes, orphans } = computeScopes({ R: { owned_files: owned } }, files);
  assert.deepEqual(scopes.R, ['@types/node.d.ts', 'app/(group)/page.tsx', 'pages/[id].vue']);
  assert.deepEqual(orphans, ['app/group/page.tsx', 'app/page.tsx', 'app/xgroup)/page.tsx', 'pages/d.vue', 'pages/i.vue', 'pages/id.vue', 'pages/xid].vue']);
  assert.deepEqual(findOverlap(['pages/[id].vue'], ['pages/*.vue']), ['pages/[id].vue', 'pages/*.vue']);
  assert.equal(findOverlap(['pages/[id].vue'], ['pages/i.vue']), null);
  assert.throws(() => assertOwned(['src/[ab]*.ts']), { code: 'bad-owned', message: /a glob may not contain \[ \] \( \) ! \+ @/ });
});

test('exact paths with ! + } are accepted and own exactly themselves', () => {
  const owned = ['docs/a!b.md', 'c++/x.h', 'lit}.txt'];
  assert.deepEqual(assertOwned(owned), owned);
  // near misses an extglob / brace / regex reading of ! + } would pull in
  const files = ['docs/a!b.md', 'docs/ab.md', 'docs/a.md', 'c++/x.h', 'c+/x.h', 'c/x.h', 'cc/x.h', 'lit}.txt', 'lit.txt', 'lit}}.txt'];
  const { scopes, orphans } = computeScopes({ R: { owned_files: owned } }, files);
  assert.deepEqual(scopes.R, ['c++/x.h', 'docs/a!b.md', 'lit}.txt']);
  assert.deepEqual(orphans, ['c+/x.h', 'c/x.h', 'cc/x.h', 'docs/a.md', 'docs/ab.md', 'lit.txt', 'lit}}.txt']);
});

const GLOB_REFUSAL =
  'a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}';

// the last case has no parentheses: a leading `!` is negation in many globbers
for (const entry of ['src/+(a|b)/*.ts', 'src/!(a)/*.ts', 'src/@(a)/**', 'src/*(a)/x.ts', 'src/?(a)/x.ts', 'src/{x,@(a|b)}/*.ts', 'src/!*.ts']) {
  test(`an extglob inside a glob is refused with the exact message: ${entry}`, () => {
    assert.throws(() => assertOwned([entry]), { code: 'bad-owned', message: `owned files: invalid path ${JSON.stringify(entry)}: ${GLOB_REFUSAL}` });
  });
}

test('ownsFile matches exact paths and globs, not near misses', () => {
  assert.equal(ownsFile(['src/state/**'], 'src/state/run.mjs'), true);
  assert.equal(ownsFile(['src/state/**'], 'src/stateful.mjs'), false);
  assert.equal(ownsFile(['src/cli/{run,block}.mjs'], 'src/cli/block.mjs'), true);
  assert.equal(ownsFile(['src/cli/{run,block}.mjs'], 'src/cli/keys.mjs'), false);
});

test('B57: an exact entry owns its path and every path below it, never a sibling sharing its prefix; a glob only what it matches', () => {
  const owned = ['src/feature', 'test/feature/', 'lib/*.mjs', 'docs/{a,b}'];
  const files = ['src/feature', 'src/feature/Fan.swift', 'src/feature/deep/A.swift', 'src/featureX/B.swift', 'src/featur', 'src', 'test/feature/T.swift', 'lib/a.mjs', 'lib/sub/a.mjs', 'docs/a', 'docs/a/x.md'];
  assert.deepEqual(files.map((f) => ownsFile(owned, f)), [true, true, true, false, false, false, true, true, false, true, false]);
  assert.equal(ownsFile([null, '', '/'], 'x'), false);
  const { scopes, orphans } = computeScopes({ A: { owned_files: ['src/feature'] }, B: { owned_files: ['src/featureX/B.swift'] } }, ['src/feature/Fan.swift', 'src/feature/deep/A.swift', 'src/featureX/B.swift', 'src/other.swift']);
  assert.deepEqual(scopes, { A: ['src/feature/Fan.swift', 'src/feature/deep/A.swift'], B: ['src/featureX/B.swift'] });
  assert.deepEqual(orphans, ['src/other.swift']);
});

test('B57: `kinds` — an exact entry recorded as a regular file owns only itself; any other kind, or none, stays directory-like', () => {
  const file = { 'src/README': 'file' };
  assert.equal(findOverlap(['src/README'], ['src/**/*.mjs'], { kindsA: file }), null);
  assert.deepEqual(findOverlap(['src/**/*.mjs'], ['src/README'], { kindsB: file }), null);
  assert.deepEqual(findOverlap(['src/README'], ['src/**'], { kindsA: file }), ['src/README', 'src/**']);
  assert.deepEqual(findOverlap(['src/README'], ['src/README'], { kindsA: file, kindsB: file }), ['src/README', 'src/README']);
  assert.equal(findOverlap(['src/README'], ['src/README/x'], { kindsA: file }), null);
  for (const kind of ['dir', 'absent', 'other']) {
    assert.deepEqual(findOverlap(['src/README'], ['src/**/*.mjs'], { kindsA: { 'src/README': kind } }), ['src/README', 'src/**/*.mjs']);
  }
  assert.deepEqual(findOverlap(['src/README'], ['src/**/*.mjs'], { kindsB: file }), ['src/README', 'src/**/*.mjs']); // the kind belongs to its own list
  assert.equal(ownsFile(['src/a'], 'src/a/x', { 'src/a': 'file' }), false);
  assert.equal(ownsFile(['src/a'], 'src/a', { 'src/a': 'file' }), true);
  assert.equal(ownsFile(['src/a'], 'src/a/x', { 'src/a': 'dir' }), true);
  assert.equal(ownsFile(['constructor'], 'constructor/x', {}), true); // never a prototype key
  const { scopes, orphans } = computeScopes({ A: { owned_files: ['src/a'], owned_kinds: { 'src/a': 'file' } }, B: { owned_files: ['src/b'] } }, ['src/a', 'src/a/x', 'src/b/y']);
  assert.deepEqual([scopes, orphans], [{ A: ['src/a'], B: ['src/b/y'] }, ['src/a/x']]);
});

test('B57 fix 1: one normalisation — an exact entry with a trailing slash matches as its path in findOverlap as in ownsFile', () => {
  assert.deepEqual(findOverlap(['src/feature/'], ['src/feature/a']), ['src/feature/', 'src/feature/a']);
  assert.deepEqual(findOverlap(['src/feature/a'], ['src/feature//']), ['src/feature/a', 'src/feature//']);
  assert.deepEqual(findOverlap(['src/feature/'], ['src/feature']), ['src/feature/', 'src/feature']);
  assert.equal(findOverlap(['src/feature/'], ['src/featureX/a']), null);
  assert.equal(findOverlap(['/'], ['src/a']), null); // an empty path owns nothing, as in ownsFile
  assert.deepEqual([ownsFile(['src/feature/'], 'src/feature/a'), ownsFile(['/'], 'src/a')], [true, false]);
  assert.equal(findOverlap(['src/a/'], ['src/a/x'], { kindsA: { 'src/a/': 'file' } }), null); // kinds keyed by the entry as written
});

test('B57 fix 2: one glob definition — `*`, `?`, `{` make a glob; `[ ]` stay literal in an exact path', () => {
  assert.deepEqual(['src/a.mjs', 'pages/[id].vue', 'src/[ab].mjs', 'src/*.mjs', 'src/a?.mjs', 'src/{a,b}.mjs', 'lit}.txt'].map(isExactEntry), [true, true, true, false, false, false, true]);
  assert.deepEqual(assertOwned(['src/[ab].mjs']), ['src/[ab].mjs']);
  assert.equal(findOverlap(['src/[ab].mjs'], ['src/a.mjs']), null); // the literal name `[ab].mjs`, never a class
  assert.deepEqual(findOverlap(['src/[ab].mjs'], ['src/*.mjs']), ['src/[ab].mjs', 'src/*.mjs']);
  assert.deepEqual([ownsFile(['src/[ab].mjs'], 'src/a.mjs'), ownsFile(['src/[ab].mjs'], 'src/[ab].mjs')], [false, true]);
});

test('B57: a trailing slash stays refused at `block open` (owned entries are canonical paths)', () => {
  assert.throws(() => assertOwned(['src/feature/']), { code: 'bad-owned', message: 'owned files: invalid path "src/feature/": no ".", ".." or empty segments' });
});
