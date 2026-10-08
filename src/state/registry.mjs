/**
 * Shared-tree scope maths for parallel blocks in one working tree (plan §4.7, O2). Pure — the
 * run record (`run.mjs`) stores the registry, the caller supplies the file set (B5 computes it:
 * tracked changes ∪ untracked).
 *
 * Owned entries are exact paths or globs in a deliberately SMALL syntax, repo-relative, POSIX:
 * `*` (any run of characters inside one segment), `?` (one character inside a segment), `**` (a
 * whole segment matching zero or more segments) and `{a,b}` (expanded before anything else).
 * Inside a GLOB, character classes and extglobs (`[` `]` `(` `)` `!` `+` `@`) are REFUSED by
 * `assertOwned`, so no two readers can disagree on what an entry covers; in an EXACT path
 * (no `*`, `?`, `{`) they are ordinary characters (`pages/[id].vue`) matched literally. The matcher is this module's own
 * (`globMatch`), not `path.matchesGlob`, which Node still marks experimental. Dot-files are
 * matched like any other name (fail closed for overlap).
 *
 * Overlap between two GLOBS cannot be decided in general, so it FAILS CLOSED: two globs are
 * treated as overlapping when one's static directory prefix (the segments before the first glob
 * character) is a segment-prefix of the other's — `test/fixtures/repos/two-blocks/**` and
 * `test/fixtures/repos/node/**` are disjoint, `src/**` and `src/cli/*.mjs` overlap, and so do
 * `src/*.mjs` and `src/cli/*.mjs` (they cannot share a path, but overlap is not ruled out by
 * the prefix rule — the owner resolves it by naming exact paths).
 *
 * B57 — ONE ownership rule, here, for every caller (block open/claim/rebase, the scope gate, the
 * worker's file set and budget, proof, plan check, the close gate's `ownsPath`): an entry with no
 * glob character (`*`, `?`, `{`) owns that exact path AND every path below it, as a directory
 * would (`src/feature` owns `src/feature/a.swift`, never `src/featureX/a.swift`); a glob entry
 * matches as above and nothing more — a brace entry (`src/{a,b}`) is a glob, so each alternative
 * owns exactly itself, nothing below it. This module is pure and cannot tell a file from a
 * directory, so the caller passes what it saw: `kinds` (a block's `owned_kinds`, recorded at
 * `block open` and `block claim` by `state/owned-kinds.mjs`) maps an exact entry to `file` when it
 * was a regular file — then it owns only itself. Any other kind, or no kind (not there yet, an
 * older block, a plan), stays directory-like: fail closed. So `findOverlap` reports a parent/child
 * pair of directory-like entries (either way round), and a directory-like entry against a glob
 * that matches the entry itself or ANY path below it (decided exactly, `globBelow`: `src/feature`
 * vs `src/**` and vs `src/feature/*.swift` overlap, vs `src/*.mjs` and `src/featureX/*` do not);
 * a `file` entry against a glob overlaps only when the glob matches it (`src/README` vs
 * `src/**\/*.mjs` does not).
 */

import { StateError } from './paths.mjs';

const GLOB_CHARS = /[*?]/;
/** What makes an owned ENTRY a glob (B57: the one definition): `*`, `?`, or a `{a,b}` group. */
const GLOB_ENTRY_CHARS = /[*?{]/;
/** Glob syntax this matcher does NOT implement — refused inside a glob entry only. */
const UNSUPPORTED_GLOB_CHARS = /[[\]()!+@]/;

/** @param {string} segment @returns {RegExp} */
function segmentRegExp(segment) {
  const body = segment
    .split('')
    .map((c) => (c === '*' ? '[^/]*' : c === '?' ? '[^/]' : c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    .join('');
  return new RegExp(`^${body}$`);
}

/**
 * Does repo-relative `file` match brace-free `pattern` (`*`, `?`, `**` — see the module doc)?
 * @param {string} file @param {string} pattern
 * @returns {boolean}
 */
export function globMatch(file, pattern) {
  const f = file.split('/');
  const p = pattern.split('/');
  /** @type {(i: number, j: number) => boolean} */
  const match = (i, j) => {
    if (j === p.length) return i === f.length;
    if (p[j] === '**') {
      for (let k = i; k <= f.length; k += 1) if (match(k, j + 1)) return true;
      return false;
    }
    return i < f.length && segmentRegExp(p[j]).test(f[i]) && match(i + 1, j + 1);
  };
  return match(0, 0);
}

/**
 * @param {string} pattern
 * @returns {string[]} every alternative of every `{a,b}` group (nested groups included).
 */
export function expandBraces(pattern) {
  const open = pattern.indexOf('{');
  if (open < 0) return [pattern];
  let depth = 0;
  let close = -1;
  const commas = [];
  for (let i = open; i < pattern.length; i += 1) {
    if (pattern[i] === '{') depth += 1;
    else if (pattern[i] === '}' && --depth === 0) {
      close = i;
      break;
    } else if (pattern[i] === ',' && depth === 1) commas.push(i);
  }
  if (close < 0) return [pattern];
  const bounds = [open, ...commas, close];
  const head = pattern.slice(0, open);
  const tail = pattern.slice(close + 1);
  return bounds.slice(0, -1).flatMap((start, i) => expandBraces(head + pattern.slice(start + 1, bounds[i + 1]) + tail));
}

/**
 * @param {unknown} owned
 * @returns {string[]}
 * An EXACT entry (no `*`, `?` or `{`) may contain `[ ] ( ) ! + @ }` — real routes such as
 * `pages/[id].vue`, `app/(group)/page.tsx`, `@types/x.d.ts` — and is matched literally by
 * string equality. A GLOB entry may not: there those characters would be character-class or
 * extglob syntax this matcher does not implement, so it is refused with that reason rather than
 * silently read one way by us and another way by git or a shell.
 * @throws {StateError} `bad-owned` — empty, absolute, `.`/`..`/empty segments (checked on every
 *   brace alternative), backslashes, unbalanced braces, or unsupported glob syntax in a glob.
 */
export function assertOwned(owned) {
  if (!Array.isArray(owned) || owned.length === 0) throw new StateError('bad-owned', 'owned files: at least one path is required');
  for (const entry of owned) {
    /** @param {string} why */
    const refuse = (why) => {
      throw new StateError('bad-owned', `owned files: invalid path ${JSON.stringify(entry)}: ${why}`);
    };
    if (typeof entry !== 'string' || entry.length === 0) refuse('must be a non-empty string');
    if (entry.includes('\\')) refuse('backslashes are not allowed (POSIX paths only)');
    const isGlob = !isExactEntry(entry);
    if (isGlob && UNSUPPORTED_GLOB_CHARS.test(entry)) {
      refuse('a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}');
    }
    for (const alt of expandBraces(entry)) {
      if (alt.startsWith('/')) refuse('must be repo-relative, not absolute');
      if (isGlob && /[{}]/.test(alt)) refuse('unbalanced braces');
      if (alt.split('/').some((segment) => segment === '..' || segment === '.' || segment === '')) refuse('no ".", ".." or empty segments');
    }
  }
  return owned;
}

/** @param {string} p @returns {string[]} segments before the first glob character */
function staticSegments(p) {
  const segments = p.split('/');
  const firstGlob = segments.findIndex((s) => GLOB_CHARS.test(s));
  return firstGlob < 0 ? segments : segments.slice(0, firstGlob);
}

/** @param {string[]} a @param {string[]} b */
const segmentPrefix = (a, b) => a.length <= b.length && a.every((s, i) => s === b[i]);

/**
 * What a caller saw on disk for each exact owned entry (B57, the module doc); only `file` changes
 * the rule. Keys are the entries' original spellings.
 * @typedef {Readonly<Record<string, string>> | null | undefined} OwnedKinds
 */

/**
 * B57: is owned `entry` an EXACT path — no `*`, `?` or `{` (`[ ] ( ) ! + @ }` are literal there)?
 * The one definition `assertOwned`, `block claim`, the ownership rule, proof export and
 * `state/owned-kinds.mjs` share.
 * @param {string} entry @returns {boolean}
 */
export function isExactEntry(entry) {
  return !GLOB_ENTRY_CHARS.test(entry);
}

/**
 * B57: an exact entry that is not a recorded regular file — it owns its path and everything below
 * it (see the module doc). `kinds` is keyed by the entry as written.
 * @param {string} entry @param {OwnedKinds} kinds @returns {boolean}
 */
const isTreeEntry = (entry, kinds) => isExactEntry(entry) && !(kinds != null && Object.hasOwn(kinds, entry) && kinds[entry] === 'file');

/**
 * B57: an exact entry's path for matching — trailing slashes dropped (`assertOwned` refuses them in
 * the run record; the close gate's callers may still pass one), the same in both functions.
 * @param {string} entry @returns {string}
 */
const exactPath = (entry) => entry.replace(/\/+$/, '');

/** @param {string} root @param {string} file @returns {boolean} `file` is `root` or a path below it. */
const coversPath = (root, file) => file === root || file.startsWith(`${root}/`);

/**
 * B57: can brace-free glob `pattern` match some path strictly below directory `dir` (one or more
 * segments after it)? Decided exactly: `dir`'s segments must match a prefix of the pattern, and
 * any pattern left over (or a `**` reached on the way) can always be met by some name below.
 * @param {string} dir @param {string} pattern
 * @returns {boolean}
 */
function globBelow(dir, pattern) {
  const f = dir.split('/');
  const p = pattern.split('/');
  for (let i = 0; ; i += 1) {
    if (i < p.length && p[i] === '**') return true;
    if (i === f.length) return i < p.length;
    if (i === p.length || !segmentRegExp(p[i]).test(f[i])) return false;
  }
}

/**
 * One brace alternative of an owned entry: `tree` when the entry is directory-like (it owns the
 * paths below it too). An exact entry is one alternative, its {@link exactPath}; an empty one
 * (`/`) has none.
 * @typedef {{p: string, tree: boolean}} Alternative
 * @param {string} entry @param {OwnedKinds} kinds @returns {Alternative[]}
 */
function alternatives(entry, kinds) {
  if (!isExactEntry(entry)) return expandBraces(entry).map((p) => ({ p, tree: false }));
  const p = exactPath(entry);
  return p.length === 0 ? [] : [{ p, tree: isTreeEntry(entry, kinds) }];
}

/**
 * @param {Alternative} a @param {Alternative} b
 * @returns {boolean}
 */
function patternsOverlap(a, b) {
  if (b.tree && !a.tree) return patternsOverlap(b, a);
  if (a.tree) {
    if (b.tree) return coversPath(a.p, b.p) || coversPath(b.p, a.p);
    if (!GLOB_CHARS.test(b.p)) return coversPath(a.p, b.p);
    return globMatch(a.p, b.p) || globBelow(a.p, b.p);
  }
  const aGlob = GLOB_CHARS.test(a.p);
  const bGlob = GLOB_CHARS.test(b.p);
  if (!aGlob && !bGlob) return a.p === b.p;
  if (!aGlob) return globMatch(a.p, b.p);
  if (!bGlob) return globMatch(b.p, a.p);
  const sa = staticSegments(a.p);
  const sb = staticSegments(b.p);
  return segmentPrefix(sa, sb) || segmentPrefix(sb, sa);
}

/**
 * Could two owned lists ever own one path? An exact entry is directory-like unless its kind says
 * `file` (B57, the module doc), so `src/feature` and `src/feature/x.swift` overlap either way round.
 * @param {ReadonlyArray<string>} ownedA @param {ReadonlyArray<string>} ownedB
 * @param {{kindsA?: OwnedKinds, kindsB?: OwnedKinds}} [kinds] - each list's `owned_kinds`.
 * @returns {[string, string] | null} the first overlapping pair (original spellings), or null.
 */
export function findOverlap(ownedA, ownedB, { kindsA, kindsB } = {}) {
  for (const a of ownedA) {
    for (const b of ownedB) {
      if (alternatives(a, kindsA).some((x) => alternatives(b, kindsB).some((y) => patternsOverlap(x, y)))) return [a, b];
    }
  }
  return null;
}

/**
 * Does `owned` own repo-relative `file`? A directory-like exact entry owns its path and every path
 * below it, a `file` one (per `kinds`) only itself (B57); a glob entry owns what it matches. A
 * non-string or empty entry owns nothing; an exact entry is matched as its {@link exactPath}, as
 * in `findOverlap`.
 * @param {ReadonlyArray<unknown>} owned @param {string} file @param {OwnedKinds} [kinds] - the block's `owned_kinds`.
 * @returns {boolean}
 */
export function ownsFile(owned, file, kinds) {
  return owned.some((entry) => {
    if (typeof entry !== 'string' || entry.length === 0) return false;
    if (!isExactEntry(entry)) return expandBraces(entry).some((p) => p === file || (GLOB_CHARS.test(p) && globMatch(file, p)));
    const root = exactPath(entry);
    return root.length > 0 && (isTreeEntry(entry, kinds) ? coversPath(root, file) : root === file);
  });
}

/**
 * @typedef {{owned_files: string[], owned_kinds?: Record<string, string>, status?: string}} RegistryBlock
 * @param {Record<string, RegistryBlock>} blocks - the ACTIVE blocks only (status `open`)
 * @param {ReadonlyArray<string>} files - the shared tree's changed + untracked files
 * @returns {{scopes: Record<string, string[]>, orphans: string[]}}
 */
export function computeScopes(blocks, files) {
  /** @type {Record<string, string[]>} */
  const scopes = Object.fromEntries(Object.keys(blocks).map((id) => [id, []]));
  const orphans = [];
  for (const file of [...new Set(files)].sort()) {
    const owners = Object.keys(blocks).filter((id) => ownsFile(blocks[id].owned_files, file, blocks[id].owned_kinds));
    if (owners.length === 0) orphans.push(file);
    for (const id of owners) scopes[id].push(file);
  }
  return { scopes, orphans };
}

/**
 * The scope row of block `blockId`'s gate: its files are what it owns; ANY orphan in the tree
 * refuses EVERY active block's gate until it is claimed (`block claim`) or deleted.
 * @param {Record<string, RegistryBlock>} blocks @param {string} blockId @param {ReadonlyArray<string>} files
 * @returns {{ok: boolean, scope: string[], orphans: string[]}}
 */
export function scopeGate(blocks, blockId, files) {
  if (!blocks[blockId]) throw new StateError('no-block', `block ${blockId} is not active`);
  const { scopes, orphans } = computeScopes(blocks, files);
  return { ok: orphans.length === 0, scope: scopes[blockId], orphans };
}
