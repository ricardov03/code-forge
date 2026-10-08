/**
 * B57: what each EXACT owned entry is on disk when a block is opened or a path claimed — the
 * answer the registry's ownership rule (`state/registry.mjs`) reads through `owned_kinds`. Only a
 * regular file (`lstat`, so never a symlink, whatever it points at) is `file`: it owns only itself.
 * Everything else stays directory-like (fail closed): `dir`, `absent` (not there yet, or a parent
 * is not a directory), `other` (a symlink, a socket, or a path `lstat` could not read).
 */

import { lstatSync } from 'node:fs';
import path from 'node:path';
import { isExactEntry } from './registry.mjs';

/** @typedef {'file' | 'dir' | 'absent' | 'other'} OwnedKind */

/**
 * @param {string} root - the directory the entry is relative to (the run's workspace).
 * @param {string} entry - a repo-relative POSIX path (already through `assertOwned`).
 * @returns {OwnedKind}
 */
export function pathKind(root, entry) {
  let st;
  try {
    st = lstatSync(path.join(root, ...entry.split('/')));
  } catch (err) {
    const code = /** @type {any} */ (err)?.code;
    return code === 'ENOENT' || code === 'ENOTDIR' ? 'absent' : 'other';
  }
  if (st.isFile()) return 'file';
  if (st.isDirectory()) return 'dir';
  return 'other';
}

/**
 * The kind of every exact entry of `owned` (`isExactEntry` — a glob has no kind), keyed by the
 * entry as written.
 * @param {string} root @param {ReadonlyArray<string>} owned
 * @returns {Record<string, OwnedKind>}
 */
export function ownedKinds(root, owned) {
  /** @type {Record<string, OwnedKind>} */
  const kinds = Object.create(null); // an entry named `__proto__` is a key like any other
  for (const entry of owned) if (isExactEntry(entry)) kinds[entry] = pathKind(root, entry);
  return kinds;
}
