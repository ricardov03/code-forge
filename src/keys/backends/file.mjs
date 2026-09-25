/**
 * Last-resort backend: one file per key under `~/.code-forge/store/`, file mode 0600, directory
 * mode 0700 (plan §1.2, §8.1). Selecting it makes `doctor` WARN (see `../store.mjs`).
 *
 * A secret is never written into an existing file: `writePrivateFile` creates a NEW temp file
 * with `O_CREAT|O_EXCL` at mode 0600 (so a pre-planted file or symlink is never opened), writes,
 * then renames it over the target. A key file with a loose mode, or a symlink planted at the
 * key's path, is replaced, never written through. The rename is also what makes the write atomic.
 *
 * Values are stored verbatim; on read one trailing newline (a hand-edited file) is dropped and
 * an empty file reads as absent.
 *
 * @typedef {import('../store.mjs').Backend} Backend
 */

import { randomBytes } from 'node:crypto';
import { chmod, mkdir, open, readFile, rename, rm } from 'node:fs/promises';
import path from 'node:path';

export const FILE_MODE = 0o600;
export const DIR_MODE = 0o700;

/**
 * Write `content` to `file` so it only ever exists at mode 0600: temp file created exclusively
 * at 0600 in the same directory, then renamed over the target.
 * @param {string} file
 * @param {string} content
 */
export async function writePrivateFile(file, content) {
  const dir = path.dirname(file);
  await mkdir(dir, { recursive: true, mode: DIR_MODE });
  await chmod(dir, DIR_MODE);
  const tmp = path.join(dir, `.${path.basename(file)}.${process.pid}.${randomBytes(6).toString('hex')}.tmp`);
  const handle = await open(tmp, 'wx', FILE_MODE);
  try {
    // The umask can only remove bits from 0600, never add them; this makes the mode exact.
    await handle.chmod(FILE_MODE);
    await handle.writeFile(content, 'utf8');
    await handle.sync();
  } catch (err) {
    await handle.close();
    await rm(tmp, { force: true });
    throw err;
  }
  await handle.close();
  await rename(tmp, file);
}

/**
 * @param {object} opts
 * @param {string} opts.dir - e.g. `~/.code-forge/store`.
 * @returns {Backend}
 */
export function createFileBackend({ dir }) {
  const root = path.resolve(dir);
  /** @param {string} name */
  const fileFor = (name) => {
    const file = path.resolve(root, `${name}.key`);
    if (typeof name !== 'string' || name.length === 0 || /[/\\]/.test(name) || name.startsWith('.') || path.dirname(file) !== root) {
      throw new TypeError('file backend: key name must be a plain file name inside the store');
    }
    return file;
  };

  return {
    name: 'file',
    writable: true,
    async available() {
      return true;
    },
    async get(name) {
      const file = fileFor(name);
      try {
        const raw = (await readFile(file, 'utf8')).replace(/\n$/, '');
        return raw.length > 0 ? raw : null;
      } catch (err) {
        if (err?.code === 'ENOENT') {
          return null;
        }
        throw err;
      }
    },
    async set(name, value) {
      await writePrivateFile(fileFor(name), value);
    },
    async delete(name) {
      const file = fileFor(name);
      try {
        await rm(file);
        return true;
      } catch (err) {
        if (err?.code === 'ENOENT') {
          return false;
        }
        throw err;
      }
    },
  };
}
