import { open, readdir } from 'node:fs/promises';
import path from 'node:path';
import { ledgerPath } from './paths.mjs';

/**
 * A worker's project-wide ledger view. Concurrent callers share one serialized
 * cursor; incomplete UTF-8/JSON bytes stay buffered until a newline arrives.
 * @param {string} slug
 * @returns {{read: () => Promise<Record<string, any>[]>}}
 */
export function createTailReader(slug) {
  const file = ledgerPath(slug);
  const dir = path.dirname(file);
  const rotatedName = new RegExp(`^${slug}\\.(\\d+)\\.jsonl$`);
  /** @type {Record<string, any>[]} */
  let rows = [];
  let files = '';
  let inode = '';
  let offset = 0;
  /** @type {Buffer} */
  let pending = Buffer.alloc(0);
  let chain = Promise.resolve();

  /** @param {Buffer} bytes */
  function parse(bytes) {
    const end = bytes.lastIndexOf(0x0a) + 1;
    return {
      rows: bytes.subarray(0, end).toString('utf8').split('\n').filter((line) => line.length > 0).map((line) => JSON.parse(line)),
      pending: bytes.subarray(end),
    };
  }

  /** @param {import('node:fs/promises').FileHandle} handle @param {number} start @param {number} size */
  async function readBytes(handle, start, size) {
    const bytes = Buffer.alloc(Math.max(0, size - start));
    let n = 0;
    while (n < bytes.length) {
      const { bytesRead } = await handle.read(bytes, n, bytes.length - n, start + n);
      if (bytesRead === 0) break;
      n += bytesRead;
    }
    return bytes.subarray(0, n);
  }

  async function update() {
    let names;
    try { names = await readdir(dir); } catch (err) {
      if (err.code !== 'ENOENT') throw err;
      names = [];
    }
    const rotated = names.filter((name) => rotatedName.test(name))
      .sort((a, b) => Number(rotatedName.exec(a)[1]) - Number(rotatedName.exec(b)[1]));
    let handle;
    try { handle = await open(file, 'r'); } catch (err) {
      if (err.code !== 'ENOENT') throw err;
    }
    try {
      const info = await handle?.stat();
      const identity = info ? `${info.dev}:${info.ino}` : '';
      const listing = JSON.stringify(rotated);
      const reset = listing !== files || identity !== inode || (info?.size ?? 0) < offset;
      let next = reset ? [] : rows;
      if (reset) {
        for (const name of rotated) {
          const history = await open(path.join(dir, name), 'r');
          try {
            next = next.concat(parse(await readBytes(history, 0, (await history.stat()).size)).rows);
          } finally { await history.close(); }
        }
      }
      const start = reset ? 0 : offset;
      const bytes = handle ? await readBytes(handle, start, info.size) : Buffer.alloc(0);
      const parsed = parse(Buffer.concat([reset ? Buffer.alloc(0) : pending, bytes]));
      // Commit only after every complete line parsed, so corruption cannot poison the cursor.
      rows = next.concat(parsed.rows);
      pending = parsed.pending;
      offset = start + bytes.length;
      files = listing;
      inode = identity;
      return rows.slice();
    } finally { await handle?.close(); }
  }

  return { read() {
    const result = chain.then(update);
    chain = result.then(() => {}, () => {});
    return result;
  } };
}
