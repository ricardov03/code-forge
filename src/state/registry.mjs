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
 */

import { StateError } from './paths.mjs';

const GLOB_CHARS = /[*?]/;
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
    const isGlob = /[*?{]/.test(entry);
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
 * @param {string} a @param {string} b - brace-free patterns
 * @returns {boolean}
 */
function patternsOverlap(a, b) {
  const aGlob = GLOB_CHARS.test(a);
  const bGlob = GLOB_CHARS.test(b);
  if (!aGlob && !bGlob) return a === b;
  if (!aGlob) return globMatch(a, b);
  if (!bGlob) return globMatch(b, a);
  const sa = staticSegments(a);
  const sb = staticSegments(b);
  return segmentPrefix(sa, sb) || segmentPrefix(sb, sa);
}

/**
 * @param {ReadonlyArray<string>} ownedA @param {ReadonlyArray<string>} ownedB
 * @returns {[string, string] | null} the first overlapping pair (original spellings), or null.
 */
export function findOverlap(ownedA, ownedB) {
  for (const a of ownedA) {
    for (const b of ownedB) {
      if (expandBraces(a).some((x) => expandBraces(b).some((y) => patternsOverlap(x, y)))) return [a, b];
    }
  }
  return null;
}

/**
 * @param {ReadonlyArray<string>} owned @param {string} file
 * @returns {boolean}
 */
export function ownsFile(owned, file) {
  return owned.some((entry) => expandBraces(entry).some((p) => p === file || (GLOB_CHARS.test(p) && globMatch(file, p))));
}

/**
 * @typedef {{owned_files: string[], status?: string}} RegistryBlock
 * @param {Record<string, RegistryBlock>} blocks - the ACTIVE blocks only (status `open`)
 * @param {ReadonlyArray<string>} files - the shared tree's changed + untracked files
 * @returns {{scopes: Record<string, string[]>, orphans: string[]}}
 */
export function computeScopes(blocks, files) {
  /** @type {Record<string, string[]>} */
  const scopes = Object.fromEntries(Object.keys(blocks).map((id) => [id, []]));
  const orphans = [];
  for (const file of [...new Set(files)].sort()) {
    const owners = Object.keys(blocks).filter((id) => ownsFile(blocks[id].owned_files, file));
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
