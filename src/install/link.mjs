/**
 * Symlink/copy install mechanics and the per-user install ledger, `~/.code-forge/installs.json`
 * (plan §1.2 "Install locations (per user)"; §2.1: "`code-forge list` prints what is linked where
 * and whether the target resolves"). This is the library the `list`/`remove`/`upgrade` verbs (and
 * later, B13's `init` wizard) are built on: nothing here prompts or prints — every function takes
 * explicit paths and returns data.
 *
 * `home` is always an explicit parameter, defaulted to `os.homedir()` at the call site in
 * `src/cli/*.mjs`, never hardcoded here — the only way a test can point this at a fixture HOME
 * without ever touching the real `~/.code-forge` (coder-rules.md rule 10).
 *
 * ## Install record shape
 *
 * `{harness, scope, method, source, target, linked_at}` — `scope` is `"project"` or `"global"`,
 * `method` is `"symlink"` or `"copy"`. `(harness, scope)` is the natural key: installing the same
 * harness at the same scope again replaces its record rather than duplicating it.
 */

import { lstatSync, readdirSync, realpathSync } from 'node:fs';
import { cp, lstat, mkdir, readFile, rename, rm, stat, symlink, unlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { randomUUID } from 'node:crypto';

/** @typedef {"symlink"|"copy"} LinkMethod */
/** @typedef {"project"|"global"} InstallScope */

/**
 * @typedef {object} InstallRecord
 * @property {string} harness
 * @property {InstallScope} scope
 * @property {LinkMethod} method
 * @property {string} source - absolute path to the skill directory that was linked/copied.
 * @property {string} target - absolute path to the installed skill directory.
 * @property {string} linked_at - ISO timestamp of the most recent link/upgrade.
 */

/**
 * @param {string} [home]
 * @returns {string}
 */
export function installsPath(home = os.homedir()) {
  return path.join(home, '.code-forge', 'installs.json');
}

/**
 * A record is only ever trusted if every field is present and correctly typed — a partially
 * corrupt file (hand-edited, or truncated by a crash mid-write) drops just the bad rows rather
 * than throwing away, or blindly trusting, the whole ledger.
 * @param {unknown} value
 * @returns {value is InstallRecord}
 */
function isValidRecord(value) {
  if (!value || typeof value !== 'object') {
    return false;
  }
  const r = /** @type {Record<string, unknown>} */ (value);
  return (
    typeof r.harness === 'string' &&
    r.harness.length > 0 &&
    (r.scope === 'project' || r.scope === 'global') &&
    (r.method === 'symlink' || r.method === 'copy') &&
    typeof r.source === 'string' &&
    r.source.length > 0 &&
    path.isAbsolute(r.source) &&
    typeof r.target === 'string' &&
    r.target.length > 0 &&
    path.isAbsolute(r.target) &&
    typeof r.linked_at === 'string'
  );
}

/**
 * Tolerant read: a missing, corrupt, or wrongly-shaped file yields `[]` rather than throwing —
 * matching `src/config/refresh.mjs`'s `readExistingSeen` convention for this package's other
 * per-user caches. A write later in the same run overwrites the bad file with a valid one.
 * @param {string} file
 * @returns {Promise<InstallRecord[]>}
 */
export async function readInstalls(file) {
  let raw;
  try {
    raw = await readFile(file, 'utf8');
  } catch {
    return [];
  }
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [];
  }
  const list = Array.isArray(parsed) ? parsed : Array.isArray(/** @type {any} */ (parsed)?.installs) ? parsed.installs : null;
  if (!list) {
    return [];
  }
  return list.filter(isValidRecord);
}

/**
 * Writes `records` to `file` atomically: write to a sibling temp file, then `rename()` over the
 * target, so a reader never observes a partial write and a crash mid-write leaves the ORIGINAL
 * file intact (same pattern as `src/config/refresh.mjs`'s `writeModelsCacheAtomically`).
 * @param {string} file
 * @param {InstallRecord[]} records
 * @returns {Promise<void>}
 */
export async function writeInstallsAtomically(file, records) {
  await mkdir(path.dirname(file), { recursive: true });
  const tempFile = path.join(path.dirname(file), `.installs.json.${randomUUID()}.tmp`);
  await writeFile(tempFile, `${JSON.stringify({ version: 1, installs: records }, null, 2)}\n`, 'utf8');
  try {
    await rename(tempFile, file);
  } catch (err) {
    await unlink(tempFile).catch(() => {});
    throw err;
  }
}

/**
 * @param {string} maybeAncestor
 * @param {string} maybeDescendant
 * @returns {boolean} whether `maybeDescendant` is STRICTLY inside `maybeAncestor` (equal paths
 *   are not "inside").
 */
function isAncestorOf(maybeAncestor, maybeDescendant) {
  if (maybeAncestor === maybeDescendant) {
    return false;
  }
  const withSep = maybeAncestor.endsWith(path.sep) ? maybeAncestor : `${maybeAncestor}${path.sep}`;
  return maybeDescendant.startsWith(withSep);
}

/**
 * Resolves `p` to its true on-disk path (following symlinks AND normalizing case on a
 * case-insensitive filesystem, via `fs.realpathSync.native` — the libuv-native realpath, which
 * consults the OS itself rather than doing a literal string walk) via the nearest EXISTING
 * ancestor: walks up from `p` until an ancestor resolves, then re-joins whatever segments below
 * that ancestor don't exist yet, LITERALLY (there is nothing on disk to resolve them against).
 * Falls back to `p` itself, unresolved, only if nothing up to the filesystem root resolves —
 * this is a safety check and must never throw.
 * @param {string} p - an already-absolute path.
 * @returns {string}
 */
function canonicalizeExistingPrefix(p) {
  let current = p;
  /** @type {string[]} */
  const trailing = [];
  for (;;) {
    try {
      const real = realpathSync.native(current);
      return trailing.length > 0 ? path.join(real, ...trailing.reverse()) : real;
    } catch {
      const parent = path.dirname(current);
      if (parent === current) {
        return p; // reached the root without resolving anything — best effort, never throws.
      }
      trailing.push(path.basename(current));
      current = parent;
    }
  }
}

/**
 * `dir`'s entries' TRUE on-disk casing for whichever one matches `name` case-INsensitively, or
 * `name` itself when no entry matches (nothing on disk to correct against — a case-sensitive
 * filesystem needs no correction, and a genuinely missing entry has no "true case" to find).
 * @param {string} dir
 * @param {string} name
 * @returns {string}
 */
function trueCaseOf(dir, name) {
  let entries;
  try {
    entries = readdirSync(dir);
  } catch {
    return name;
  }
  const lower = name.toLowerCase();
  return entries.find((entry) => entry.toLowerCase() === lower) ?? name;
}

/**
 * Canonicalizes `target` for a SAFETY comparison — the true on-disk path, with every ancestor
 * AND `target`'s own case corrected, but WITHOUT ever following `target` if it is itself a
 * symlink (deleting a symlink removes the link entry, not its destination — the wrong thing to
 * resolve here is "what `target` refers to", not "where `target` itself sits").
 *
 * When `target` exists and is NOT itself a symlink, a single `realpathSync.native` call resolves
 * ancestor symlinks AND corrects the whole path's case (including its own final component) in
 * one step — nothing is "followed" beyond `target`'s own location, since it isn't a link. When
 * `target` IS a symlink (or does not exist yet), its PARENT chain is resolved instead, and its
 * own basename is case-corrected via a case-insensitive lookup within that resolved parent —
 * never realpath'd directly.
 * @param {string} target - an already-absolute path.
 * @returns {string}
 */
function canonicalizeTargetLocation(target) {
  let info;
  try {
    info = lstatSync(target);
  } catch {
    info = null;
  }
  if (info && !info.isSymbolicLink()) {
    try {
      return realpathSync.native(target);
    } catch {
      // Raced away (or unreadable) between lstat and realpath: fall through to the parent-chain
      // resolution below — a safety check must never throw.
    }
  }

  const dir = path.dirname(target);
  const name = path.basename(target);
  const resolvedDir = canonicalizeExistingPrefix(dir);
  return path.join(resolvedDir, trueCaseOf(resolvedDir, name));
}

/**
 * Every canonical form under which a PROTECTED path (`source`, or `$HOME`) must be guarded,
 * deduplicated: where it FULLY resolves to (following the path itself when it is a symlink — an
 * npm global install's link, or a `$HOME` that lives behind a symlink) AND where its own entry
 * SITS (its parents resolved, the path itself not followed, § {@link canonicalizeTargetLocation}).
 * A target is compared against BOTH: the target is located without being followed, so a target
 * spelled exactly like a symlinked protected path only ever matches the un-followed form —
 * comparing against the followed form alone let it through.
 * @param {string} protectedPath - an already-absolute path.
 * @returns {string[]} one entry when `protectedPath` is not a symlink, two when it is.
 */
function protectedForms(protectedPath) {
  const followed = canonicalizeExistingPrefix(protectedPath);
  const location = canonicalizeTargetLocation(protectedPath);
  return followed === location ? [followed] : [followed, location];
}

/**
 * Refuses a deletion that could be catastrophic: the filesystem root, `$HOME` itself or an
 * ANCESTOR of it (deleting `target` would take `$HOME` down with it), or `source` itself or an
 * ancestor of it (deleting `target` would also destroy the thing being installed FROM). A target legitimately
 * INSIDE `$HOME` — the normal case, e.g. `~/.claude/skills/code-forge` — is always fine; only an
 * ancestor is refused.
 *
 * Every comparison runs on CANONICAL paths (§ {@link canonicalizeTargetLocation}), not raw
 * strings: a case-insensitive filesystem (macOS APFS by default) makes `/users/<name>` and
 * `/Users/<name>` the SAME on-disk entry, and a symlinked ancestor can make two textually
 * unrelated strings resolve to the same real location — either would defeat a plain
 * `path.resolve` + `startsWith` string comparison.
 * @param {string} target
 * @param {string} [source] - when given, also refused if `target` is it or an ancestor of it.
 * @param {string} [home] - defaults to `os.homedir()`.
 * @returns {{safe: boolean, reason: string}} `reason` is `''` when `safe` is `true`.
 */
function checkSafeToDelete(target, source, home = os.homedir()) {
  const canonicalTarget = canonicalizeTargetLocation(path.resolve(target));
  if (canonicalTarget === path.parse(canonicalTarget).root) {
    return { safe: false, reason: `target is the filesystem root (${canonicalTarget})` };
  }
  for (const canonicalHome of protectedForms(path.resolve(home))) {
    if (canonicalTarget === canonicalHome || isAncestorOf(canonicalTarget, canonicalHome)) {
      return { safe: false, reason: `target is $HOME or an ancestor of it (${canonicalTarget})` };
    }
  }
  for (const canonicalSource of source ? protectedForms(path.resolve(source)) : []) {
    if (canonicalTarget === canonicalSource) {
      return { safe: false, reason: `target is its own source (${canonicalTarget})` };
    }
    if (isAncestorOf(canonicalTarget, canonicalSource)) {
      return { safe: false, reason: `target is an ancestor of its own source (${canonicalTarget})` };
    }
  }
  return { safe: true, reason: '' };
}

/**
 * Removes whatever already exists at `target` (file, directory, or symlink — including a broken
 * one) so a fresh symlink/copy can take its place. A target that never existed is a silent no-op.
 * Refuses (throws) rather than delete anything {@link checkSafeToDelete} flags as catastrophic.
 * @param {string} target
 * @param {object} [opts]
 * @param {string} [opts.source] - passed through to {@link checkSafeToDelete}.
 * @returns {Promise<void>}
 */
async function clearTarget(target, { source } = {}) {
  const check = checkSafeToDelete(target, source);
  if (!check.safe) {
    throw new Error(`link: refusing to delete "${target}" — ${check.reason}`);
  }
  await rm(target, { recursive: true, force: true });
}

/**
 * Points `target` at `source` using `method` — `"symlink"` creates a directory symlink,
 * `"copy"` recursively copies `source`'s contents. Either way, `target`'s parent is created and
 * anything already at `target` is replaced (never merged: a stale copy from a previous version
 * must not leave orphaned files behind a fresh symlink, or vice versa).
 *
 * `source` is validated (exists, is a directory, is not `target` and does not contain/is not
 * contained by `target` — compared as canonical on-disk paths, with `source` checked both as
 * where it resolves to AND where its own entry sits when it is a symlink) BEFORE `target` is
 * touched — a bad `source` must never destroy a working install by clearing `target` and then
 * failing to create the replacement.
 * @param {object} opts
 * @param {string} opts.source - absolute path to the skill directory.
 * @param {string} opts.target - absolute path to install into.
 * @param {LinkMethod} opts.method
 * @returns {Promise<void>}
 */
export async function linkSkill({ source, target, method }) {
  if (method !== 'symlink' && method !== 'copy') {
    throw new TypeError(`link: method must be "symlink" or "copy", got ${JSON.stringify(method)}`);
  }

  let sourceStat;
  try {
    sourceStat = await stat(source);
  } catch (err) {
    const code = /** @type {NodeJS.ErrnoException} */ (err)?.code ?? 'unknown error';
    throw new Error(`link: source "${source}" does not exist or is not readable (${code})`);
  }
  if (!sourceStat.isDirectory()) {
    throw new Error(`link: source "${source}" is not a directory`);
  }

  // Canonical, not textual: a differently-cased path (case-insensitive APFS) or one reached through
  // a symlinked parent can name the source itself while never matching its string. `target` is
  // located without following it when it is itself a symlink (an existing install's link), so
  // `source` is checked in BOTH its followed and un-followed forms (§ protectedForms).
  const resolvedTarget = canonicalizeTargetLocation(path.resolve(target));
  for (const resolvedSource of protectedForms(path.resolve(source))) {
    if (resolvedSource === resolvedTarget) {
      throw new Error(`link: source and target must not be the same path (${resolvedTarget})`);
    }
    if (isAncestorOf(resolvedSource, resolvedTarget)) {
      throw new Error(`link: target must not be inside source (${resolvedTarget} is inside ${resolvedSource})`);
    }
    if (isAncestorOf(resolvedTarget, resolvedSource)) {
      throw new Error(`link: source must not be inside target (${resolvedSource} is inside ${resolvedTarget})`);
    }
  }

  await mkdir(path.dirname(target), { recursive: true });
  await clearTarget(target, { source });
  if (method === 'symlink') {
    await symlink(source, target, 'dir');
  } else {
    await cp(source, target, { recursive: true });
  }
}

/**
 * Links/copies `source` into `target` and records the install, replacing any existing record for
 * the same `(harness, scope)` (a re-run of `init` for a harness already installed updates its
 * record in place rather than accumulating duplicates).
 * @param {object} opts
 * @param {string} opts.installsFile
 * @param {string} opts.harness
 * @param {InstallScope} opts.scope
 * @param {LinkMethod} opts.method
 * @param {string} opts.source
 * @param {string} opts.target
 * @returns {Promise<InstallRecord[]>} the full record list after this install.
 */
export async function install({ installsFile, harness, scope, method, source, target }) {
  await linkSkill({ source, target, method });
  const existing = await readInstalls(installsFile);
  const kept = existing.filter((r) => !(r.harness === harness && r.scope === scope));
  const records = [...kept, { harness, scope, method, source, target, linked_at: new Date().toISOString() }];
  await writeInstallsAtomically(installsFile, records);
  return records;
}

/**
 * Re-points every recorded install at `source` (or, when `source` is omitted, re-applies each
 * record's own previously-recorded source — a refresh with no version change). For a symlink
 * record this re-creates the symlink so it points at the new source; for a copy record it
 * re-copies the new source's contents over the target. Every record's `linked_at` and `source`
 * are updated to reflect the upgrade.
 * @param {object} opts
 * @param {string} opts.installsFile
 * @param {string} [opts.source] - overrides every record's source when given.
 * @returns {Promise<InstallRecord[]>} the updated record list (same length as before).
 */
export async function upgradeAll({ installsFile, source }) {
  const records = await readInstalls(installsFile);
  const updated = [];
  for (const record of records) {
    const nextSource = source ?? record.source;
    await linkSkill({ source: nextSource, target: record.target, method: record.method });
    updated.push({ ...record, source: nextSource, linked_at: new Date().toISOString() });
  }
  await writeInstallsAtomically(installsFile, updated);
  return updated;
}

/**
 * Removes recorded installs matching the given filter (default: every record) — deletes ONLY the
 * `target` path of each matched record, never a glob or a parent directory scan, so an unrecorded
 * file that happens to sit alongside a real install survives untouched.
 *
 * A record whose `target` {@link checkSafeToDelete} flags as catastrophic (the filesystem root,
 * `$HOME` or an ancestor of it, or an ancestor of its own `source` — only reachable via a
 * hand-edited or corrupted `installs.json`, since every WRITE path here goes through
 * `linkSkill`'s own guards) is SKIPPED, not deleted, and stays in the ledger rather than being
 * silently dropped — one poisoned record must not block removal of the others in the same call.
 * @param {object} opts
 * @param {string} opts.installsFile
 * @param {string} [opts.harness] - when given, only records for this harness match.
 * @param {InstallScope} [opts.scope] - when given, only records at this scope match.
 * @returns {Promise<InstallRecord[]>} the records that were actually removed (excludes any
 *   matched-but-refused record).
 */
export async function removeRecords({ installsFile, harness, scope }) {
  const records = await readInstalls(installsFile);
  const matches = (/** @type {InstallRecord} */ r) =>
    (harness === undefined || r.harness === harness) && (scope === undefined || r.scope === scope);
  const candidates = records.filter(matches);
  const kept = records.filter((r) => !matches(r));

  const removed = [];
  for (const record of candidates) {
    const check = checkSafeToDelete(record.target, record.source);
    if (!check.safe) {
      kept.push(record);
      continue;
    }
    await rm(record.target, { recursive: true, force: true });
    removed.push(record);
  }

  await writeInstallsAtomically(installsFile, kept);
  return removed;
}

/**
 * Whether `target` resolves to something that exists — for a symlink this follows the link, so a
 * broken symlink (source removed/moved) reports `false` (plan §2.1: "list ... whether the target
 * resolves").
 * @param {string} target
 * @returns {Promise<boolean>}
 */
export async function targetResolves(target) {
  try {
    await stat(target);
    return true;
  } catch {
    return false;
  }
}

/**
 * Whether `target` is itself a symlink (as opposed to a real directory from a `copy` install).
 * @param {string} target
 * @returns {Promise<boolean>}
 */
export async function isSymlink(target) {
  try {
    const info = await lstat(target);
    return info.isSymbolicLink();
  } catch {
    return false;
  }
}
