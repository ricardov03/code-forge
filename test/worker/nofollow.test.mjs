/**
 * B11.1: the lstat→read race. A file that `lstat` saw as regular but that is a symlink when it
 * is opened is refused with `bad-path` (O_NOFOLLOW + fstat on the open fd), in the hasher and in
 * the packet builder; its target's FAKE secret reaches 0 hashes / packets.
 *
 * The race is made deterministic by mocking `node:fs` BEFORE any `src` module loads: `lstatSync`
 * of an armed path answers with the stat of a regular decoy file, so every lstat check passes
 * while the path on disk is a symlink — exactly the state after a swap between check and read.
 */

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs';
import path from 'node:path';
import { after, describe, mock, test } from 'node:test';

const realLstat = fs.lstatSync;
const realRealpath = fs.realpathSync;
/** @type {Map<string, string>} armed path (as given AND realpath'd parent + basename) ⇒ the regular decoy whose stat lstat reports. */
const armed = new Map();
/** @type {Map<string, number>} armed decoy ⇒ how many lstat calls the mock redirected to it. */
const hits = new Map();
/** @param {string} p @returns {string} the path with its parent realpath'd (the link itself is not followed). */
function resolvedKey(p) {
  try {
    return path.join(realRealpath(path.dirname(p)), path.basename(p));
  } catch {
    return path.resolve(p);
  }
}
const lstatSync = (/** @type {any} */ p, /** @type {any} */ opts) => {
  const decoy = armed.get(String(p)) ?? armed.get(resolvedKey(String(p)));
  if (decoy) hits.set(decoy, (hits.get(decoy) ?? 0) + 1);
  return realLstat(decoy ?? p, opts);
};
const { default: _default, ...named } = /** @type {any} */ (fs);
mock.module('node:fs', { namedExports: { ...named, lstatSync }, defaultExport: { ...named, lstatSync } });
after(() => mock.reset());

const mockedFs = await import('node:fs');
const { commitAll, freshDir, lines, makeRepo, writeFile } = await import('../review/helpers.mjs');
const { contentHash, readRegularFileNoFollow, DELETED_HASH } = await import('../../src/worker/ticket.mjs');
const { attachFiles, buildPacket, PacketError } = await import('../../src/review/packet.mjs');

const MARKER = 'FAKE-secret-b11-1-nofollow-7a2c';

/**
 * A repo whose `rel` is a symlink to an outside FAKE secret, armed so lstat reports a regular file.
 * @param {string} repo @param {string} rel
 * @returns {() => number} reads and resets the lstat hit count on the armed path.
 */
function swapToSymlink(repo, rel) {
  const outside = path.join(freshDir('outside'), 'secret.txt');
  fs.writeFileSync(outside, `${MARKER}\n`);
  const full = path.join(repo, rel);
  const decoy = path.join(freshDir('decoy'), 'decoy.mjs');
  fs.writeFileSync(decoy, 'export const decoy = 1;\n');
  fs.rmSync(full, { force: true });
  fs.symlinkSync(outside, full);
  armed.set(full, decoy);
  armed.set(resolvedKey(full), decoy);
  assert.deepEqual([realLstat(full).isSymbolicLink(), mockedFs.lstatSync(full).isSymbolicLink()], [true, false]); // the link is hidden from the lstat checks
  hits.set(decoy, 0);
  /** @returns {number} the mock's lstat hits on this path since the last call (then resets). */
  return () => {
    const n = hits.get(decoy) ?? 0;
    hits.set(decoy, 0);
    return n;
  };
}

describe('readRegularFileNoFollow (B11.1)', () => {
  test('a regular file reads byte-identical; a missing file is null', async () => {
    const repo = await makeRepo();
    const bytes = Buffer.from([0x00, 0xff, 0x0a, 0xc3, 0xa9, 0x0d, 0x0a, 0x41]);
    writeFile(repo, 'src/bin.dat', '');
    fs.writeFileSync(path.join(repo, 'src', 'bin.dat'), bytes);
    const got = readRegularFileNoFollow(repo, 'src/bin.dat');
    assert.deepEqual([Buffer.isBuffer(got), got && Buffer.compare(got, bytes)], [true, 0]);
    assert.equal(contentHash(repo, 'src/bin.dat'), createHash('sha256').update(bytes).digest('hex'));
    assert.equal(readRegularFileNoFollow(repo, 'src/gone.dat'), null);
    assert.equal(contentHash(repo, 'src/gone.dat'), DELETED_HASH);
  });

  test('a path swapped to a symlink after the lstat check: helper and contentHash refuse with bad-path; the secret is in 0 hashes', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/a.mjs', lines(3));
    writeFile(repo, 'src/b.mjs', lines(3));
    const took = swapToSymlink(repo, 'src/a.mjs');
    // 1 lstat hit each: the pre-open `assertInsideRoot` saw the decoy, so only O_NOFOLLOW refused
    assert.throws(() => readRegularFileNoFollow(repo, 'src/a.mjs'), { code: 'bad-path', message: 'the path is a symlink' });
    assert.equal(took(), 1);
    const secretHash = createHash('sha256').update(`${MARKER}\n`).digest('hex');
    /** @type {string[]} */
    const hashes = [];
    /** @type {string[]} */
    const refusals = [];
    for (const f of ['src/a.mjs', 'src/b.mjs']) {
      try {
        hashes.push(contentHash(repo, f));
      } catch (err) {
        refusals.push(`${f}: ${/** @type {any} */ (err).code} ${/** @type {Error} */ (err).message}`);
      }
    }
    assert.equal(took(), 1);
    assert.deepEqual(refusals, ['src/a.mjs: bad-path the path is a symlink']);
    assert.deepEqual(hashes, [createHash('sha256').update(lines(3)).digest('hex')]);
    assert.equal(hashes.filter((h) => h === secretHash).length, 0);
  });

  test('a FIFO is refused as not a regular file without blocking the open', async () => {
    const repo = await makeRepo();
    fs.mkdirSync(path.join(repo, 'src'), { recursive: true });
    const { exec } = await import('../../src/util/exec.mjs');
    const res = await exec(['mkfifo', path.join(repo, 'src', 'pipe')], { cwd: repo, timeoutMs: 5000 });
    assert.equal(res.result, 'ok');
    assert.throws(() => readRegularFileNoFollow(repo, 'src/pipe'), { code: 'bad-path', message: 'not a regular file' });
  });
});

describe('packet builder reads through O_NOFOLLOW (B11.1)', () => {
  test('buildPacket: a new file swapped to a symlink after the lstat checks is refused with bad-path; the secret is in 0 packets', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/new.mjs', lines(4));
    const took = swapToSymlink(repo, 'src/new.mjs');
    /** @type {string[]} */
    const texts = [];
    await assert.rejects(buildPacket({ repoRoot: repo, file: 'src/new.mjs', base: null, lens: 'full', cfg: {} }), (err) => {
      assert.ok(err instanceof PacketError);
      assert.deepEqual([/** @type {any} */ (err).code, err.message], ['bad-path', 'the path is a symlink']);
      texts.push(String(err.message));
      return true;
    });
    // 3 lstat hits: assertPacketPath, its assertInsideRoot, the read's pre-open assertInsideRoot
    assert.equal(took(), 3);
    assert.equal(texts.filter((t) => t.includes(MARKER)).length, 0);
  });

  test('attachFiles: a tracked file swapped to a symlink is named as refused; the regular one is attached byte-identical; the secret appears 0 times', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/ok.mjs', 'export const ok = "é";\n');
    writeFile(repo, 'src/swap.mjs', lines(2));
    await commitAll(repo);
    const took = swapToSymlink(repo, 'src/swap.mjs');
    const section = await attachFiles({ repoRoot: repo, paths: ['src/ok.mjs', 'src/swap.mjs'], budgetTokens: 10000 });
    // 4 lstat hits: assertPacketPath (2), attachRefusal (1), the read's pre-open assertInsideRoot (1)
    assert.equal(took(), 4);
    assert.equal(section.split(MARKER).length - 1, 0);
    assert.deepEqual(
      section.split('\n').filter((l) => l.startsWith('###')),
      ['### src/ok.mjs', '### src/swap.mjs (refused: the path is a symlink)'],
    );
    assert.equal(section.split('\n').filter((l) => l === '1| export const ok = "é";').length, 1);
  });
});
