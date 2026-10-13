import { BLOCK, makeRepo, queued } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

// B75 (issue #10 item 4): `review-file <path> --block <id> --fresh` drops the cached done marker
// and queues the ticket again only when the cached result never counted as a review. Every test
// drives the real verb in process, in a temp HOME and a temp repo; this live process stands in for
// the worker (announceWorker), and the cached results are written with the real run key.

const { announceWorker, dropDoneIf, enqueue, queueDir, retractWorker, writeResult } = await import('../../src/worker/queue.mjs');
const { runReviewFile } = await import('../../src/cli/review-file.mjs');
const { loadKey } = await import('../../src/state/signer.mjs');
const { isForbidden, mergeForbidden } = await import('../../src/util/forbidden.mjs');

const FILE = 'src/a.mjs';

/** @returns {{write: (s: string) => boolean, text: () => string}} */
function capture() {
  let text = '';
  return {
    write: (s) => {
      text += s;
      return true;
    },
    text: () => text,
  };
}

/**
 * `review-file` in process, from the repo root.
 * @param {string} repo @param {string[]} args @param {Record<string, any>} [extra] - more deps.
 */
async function reviewFile(repo, args, extra = {}) {
  const stdout = capture();
  const stderr = capture();
  const code = await runReviewFile(args, { cwd: repo, stdout, stderr, ...extra });
  const line = stdout.text().trim();
  return { code, json: line === '' ? null : JSON.parse(line), err: stderr.text() };
}

/**
 * A repo with a live worker and FILE's ticket queued; `cache(result)` writes a signed cached result.
 */
async function fixture() {
  const { repo, runId } = await makeRepo();
  announceWorker(repo, { pid: process.pid, run: runId });
  const key = await loadKey(runId);
  const first = enqueue({ repoRoot: repo, run: runId, block: BLOCK, file: FILE });
  const marker = path.join(queueDir(repo), `${first.ticket}.done`);
  const cache = (/** @type {Record<string, any>} */ result) =>
    writeResult({ repoRoot: repo, runId, ticket: first.ticket, key, result: { event: 'review.result', block: BLOCK, file: FILE, content_hash: first.content_hash, approved: false, ...result } });
  return { repo, runId, first, marker, cache, done: () => retractWorker(repo, process.pid) };
}

describe('B75 review-file --fresh: a result that never counted is run again', () => {
  test('(1) stale, unavailable and refused other-run / context-unreadable / worktree-missing: the marker is dropped and the ticket queued again', async () => {
    const f = await fixture();
    try {
      const rerunnable = [
        { status: 'stale' },
        { status: 'stale', reason: 'missing-file' },
        { status: 'unavailable', reason: 'engine-error' },
        { status: 'refused', reason: 'other-run' },
        { status: 'refused', reason: 'context-unreadable' },
        { status: 'refused', reason: 'worktree-missing' },
      ];
      const answers = [];
      for (const result of rerunnable) {
        f.cache(result);
        assert.deepEqual(queued(f.repo, 'done'), [f.first.ticket]);
        const res = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
        answers.push([res.code, res.err, res.json, queued(f.repo, 'done').length, queued(f.repo, 'json')]);
      }
      const want = [0, '', { ticket: f.first.ticket, status: 'queued', file: FILE, content_hash: f.first.content_hash, fresh: true }, 0, [f.first.ticket]];
      assert.deepEqual(answers, rerunnable.map(() => want));
      // the drop leaves no claim file behind: the queue holds the worker file and one ticket only
      assert.deepEqual(readdirSync(queueDir(f.repo)).sort(), [`${f.first.ticket}.json`, 'worker.json']);
    } finally {
      f.done();
    }
  });

  test('(4) a second --fresh after the first one queued: still exactly 1 queued ticket, no marker to drop', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'refused', reason: 'other-run' });
      const one = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
      // the second call finds no marker (the plain-enqueue path: no `fresh` key); that only one
      // caller can take a marker at all is pinned by the dropDoneIf tests below
      const two = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
      assert.deepEqual([one.code, one.json.fresh, two.code, Object.hasOwn(two.json, 'fresh')], [0, true, 0, false]);
      assert.deepEqual(two.json, { ticket: f.first.ticket, status: 'queued', file: FILE, content_hash: f.first.content_hash });
      assert.deepEqual([queued(f.repo, 'json'), queued(f.repo, 'done')], [[f.first.ticket], []]);
    } finally {
      f.done();
    }
  });

  test('(3) no cached marker at all: --fresh answers exactly what a normal review-file does (queued)', async () => {
    const { repo, runId } = await makeRepo();
    announceWorker(repo, { pid: process.pid, run: runId });
    try {
      const fresh = await reviewFile(repo, ['src/b.mjs', '--block', BLOCK, '--fresh']);
      const plain = await reviewFile(repo, ['src/b.mjs', '--block', BLOCK]);
      assert.deepEqual([fresh.code, plain.code], [0, 0]);
      assert.deepEqual(fresh.json, plain.json);
      assert.deepEqual(Object.keys(fresh.json).sort(), ['content_hash', 'file', 'status', 'ticket']);
      assert.equal(fresh.json.status, 'queued');
      assert.deepEqual(queued(repo, 'json'), [fresh.json.ticket]);
    } finally {
      retractWorker(repo, process.pid);
    }
  });
});

describe('B75 review-file --fresh: a counted result is never re-rolled', () => {
  test('(2) findings, approved, stopped, review-state-lost, answers, unknown status/reason and a mismatched or unreadable marker: refused fresh-needs-reopen, the marker kept byte-for-byte', async () => {
    const f = await fixture();
    try {
      const counted = [
        { status: 'reviewed', reason: 'findings-open', findings: [{ id: 'F1', severity: 'major', claim: 'c', fix: 'x' }] },
        { status: 'reviewed', approved: true, findings: [] },
        { status: 'stopped', stopped: 'review_cap', findings: [] },
        { status: 'refused', reason: 'review-state-lost' },
        { status: 'answers', findings: [] },
        { status: 'refused', reason: 'bad-path' },
        { status: 'refused' },
        { status: 'split_required' },
        { status: 'something-new' },
      ];
      const answers = [];
      for (const result of counted) {
        f.cache(result);
        const before = readFileSync(f.marker);
        const st = statSync(f.marker);
        const res = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
        const now = statSync(f.marker);
        // never renamed, moved or rewritten: the same file (inode), the same mtime, the same bytes
        answers.push([res.code, res.json.status, res.json.reason, res.json.ticket, res.json.exits, Buffer.compare(before, readFileSync(f.marker)), now.ino === st.ino, now.mtimeMs === st.mtimeMs, queued(f.repo, 'done')]);
      }
      const want = [1, 'refused', 'fresh-needs-reopen', f.first.ticket, ['block reopen', 'block waive'], 0, true, true, [f.first.ticket]];
      assert.deepEqual(answers, counted.map(() => want));

      // a marker whose status is not its result's, and a marker that is not JSON: refused, kept
      f.cache({ status: 'reviewed', approved: true, findings: [] });
      const marker = JSON.parse(readFileSync(f.marker, 'utf8'));
      const odd = [`${JSON.stringify({ ...marker, status: 'stale' })}\n`, 'not json\n', ''];
      const oddAnswers = [];
      for (const text of odd) {
        writeFileSync(f.marker, text);
        const res = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
        oddAnswers.push([res.code, res.json.reason, readFileSync(f.marker, 'utf8')]);
      }
      assert.deepEqual(oddAnswers, odd.map((text) => [1, 'fresh-needs-reopen', text]));
      assert.deepEqual(queued(f.repo, 'json'), [f.first.ticket]);
    } finally {
      f.done();
    }
  });

  test('(2) the message names block reopen and block waive; a tampered state is fresh-tampered and names only block close --no-require-reviews', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stopped', stopped: 'review_cap', findings: [] });
      const stopped = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
      assert.equal(stopped.json.message, `fresh-needs-reopen: the cached result of ${FILE} counts as a review (or cannot be read); --fresh re-runs only stale, unavailable or refused other-run/worktree-*/context-unreadable results; the human's ways out are block reopen and block waive; the cached result was kept`);

      f.cache({ status: 'refused', reason: 'review-state-tampered' });
      const before = readFileSync(f.marker);
      const st = statSync(f.marker);
      const tampered = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh']);
      assert.deepEqual([statSync(f.marker).ino, statSync(f.marker).mtimeMs], [st.ino, st.mtimeMs]);
      assert.deepEqual([tampered.code, tampered.json.status, tampered.json.reason, tampered.json.exits], [1, 'refused', 'fresh-tampered', ['block close --no-require-reviews']]);
      assert.equal(tampered.json.message.includes('reopen'), false);
      assert.equal(tampered.json.message.includes('block close --no-require-reviews'), true);
      assert.equal(Buffer.compare(before, readFileSync(f.marker)), 0);
    } finally {
      f.done();
    }
  });

  test('--fresh with --wait or --answers is a usage error (exit 2); nothing is dropped', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      const wait = await reviewFile(f.repo, ['--wait', f.first.ticket, '--fresh']);
      const answers = await reviewFile(f.repo, ['src/b.mjs', '--block', BLOCK, '--answers', 'F1', '--of', FILE, '--fresh']);
      assert.deepEqual([wait.code, wait.json, answers.code, answers.json], [2, null, 2, null]);
      assert.equal(wait.err.startsWith('review-file: --wait takes only a ticket and --max\n'), true);
      assert.equal(answers.err.startsWith('review-file: --fresh takes no --answers or --of\n'), true);
      assert.deepEqual(queued(f.repo, 'done'), [f.first.ticket]);
    } finally {
      f.done();
    }
  });

  test('a marker that turns tampered under the claim is restored and classified again: fresh-tampered, never reopen', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      /** @type {string[]} */
      const calls = [];
      const dropDone = (/** @type {string} */ repo, /** @type {string} */ ticket, /** @type {Buffer} */ seen) => {
        // a newer (tampered) result lands between the in-place read and the claim
        if (calls.length === 0) f.cache({ status: 'refused', reason: 'review-state-tampered' });
        const outcome = dropDoneIf(repo, ticket, seen);
        calls.push(outcome);
        return outcome;
      };
      const res = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh'], { dropDone });
      assert.deepEqual([res.code, res.json.reason, res.json.exits, calls], [1, 'fresh-tampered', ['block close --no-require-reviews'], ['changed']]);
      assert.equal(res.json.message.includes('reopen'), false);
      assert.equal(JSON.parse(readFileSync(f.marker, 'utf8')).status, 'refused');
      assert.deepEqual(readdirSync(queueDir(f.repo)).sort(), [`${f.first.ticket}.done`, `${f.first.ticket}.json`, 'worker.json']);
    } finally {
      f.done();
    }
  });

  test('an unknown claim outcome or a throw from the claim is refused with a named code; nothing is queued again', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      const before = readFileSync(f.marker, 'utf8');
      const odd = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh'], { dropDone: () => 'maybe' });
      const boom = await reviewFile(f.repo, [FILE, '--block', BLOCK, '--fresh'], {
        dropDone: () => {
          throw new Error(`/secret/path ${f.repo}`);
        },
      });
      assert.deepEqual(
        [odd.code, odd.json, boom.code, boom.json],
        [
          1,
          { status: 'refused', reason: 'fresh-claim-unknown', message: `the claim of the cached result of ${FILE} gave an unknown outcome; nothing was queued` },
          1,
          { status: 'refused', reason: 'fresh-claim-failed', message: `the claim of the cached result of ${FILE} failed; nothing was queued` },
        ],
      );
      // the hooks never touched the marker: it is still there, and no fresh queue answer was given
      assert.equal(readFileSync(f.marker, 'utf8'), before);
      assert.deepEqual([queued(f.repo, 'json'), queued(f.repo, 'done')], [[f.first.ticket], [f.first.ticket]]);
    } finally {
      f.done();
    }
  });

  test('(3) --wait with no --fresh answers the cached result as before', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      const before = readFileSync(f.marker, 'utf8');
      const res = await reviewFile(f.repo, ['--wait', f.first.ticket, '--max', '1s']);
      assert.deepEqual([res.code, res.err, res.json.ticket, res.json.status, res.json.result.status, Object.keys(res.json).sort()], [0, '', f.first.ticket, 'done', 'stale', ['result', 'status', 'ticket']]);
      assert.equal(readFileSync(f.marker, 'utf8'), before);
    } finally {
      f.done();
    }
  });

  test('(5) a coder may run --fresh: no forbidden entry matches it', () => {
    const list = mergeForbidden();
    assert.equal(isForbidden(['code-forge', 'review-file', FILE, '--block', BLOCK, '--fresh'], list), null);
    assert.equal(isForbidden(['forge', 'review-file', FILE, '--block', BLOCK, '--fresh'], list), null);
  });
});

describe('B75 dropDoneIf: claim only what was classified', () => {
  test('no marker is gone; the classified bytes are dropped once (a second caller finds it gone); other bytes are linked back in place', async () => {
    const f = await fixture();
    try {
      assert.equal(dropDoneIf(f.repo, f.first.ticket, Buffer.from('x')), 'gone');
      f.cache({ status: 'stale' });
      const bytes = readFileSync(f.marker);
      const st = statSync(f.marker);
      // bytes that are not the marker's: restored by link, the same inode and bytes
      assert.equal(dropDoneIf(f.repo, f.first.ticket, Buffer.from('other')), 'changed');
      assert.deepEqual([statSync(f.marker).ino, Buffer.compare(readFileSync(f.marker), bytes)], [st.ino, 0]);
      assert.deepEqual([dropDoneIf(f.repo, f.first.ticket, bytes), dropDoneIf(f.repo, f.first.ticket, bytes)], ['dropped', 'gone']);
      assert.deepEqual(readdirSync(queueDir(f.repo)).sort(), [`${f.first.ticket}.json`, 'worker.json']);
    } finally {
      f.done();
    }
  });

  test('a newer marker written before the restore wins (EEXIST); any other link error keeps the claim and refuses fresh-restore-failed', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      const newer = `${JSON.stringify({ ticket: f.first.ticket, status: 'reviewed', result: 'x' })}\n`;
      const link = (/** @type {string} */ from, /** @type {string} */ to) => {
        writeFileSync(to, newer);
        throw Object.assign(new Error('exists'), { code: 'EEXIST' });
      };
      assert.equal(dropDoneIf(f.repo, f.first.ticket, Buffer.from('other'), { link }), 'changed');
      assert.equal(readFileSync(f.marker, 'utf8'), newer);
      assert.deepEqual(readdirSync(queueDir(f.repo)).sort(), [`${f.first.ticket}.done`, `${f.first.ticket}.json`, 'worker.json']);

      const denied = () => {
        throw Object.assign(new Error('denied'), { code: 'EPERM' });
      };
      assert.throws(() => dropDoneIf(f.repo, f.first.ticket, Buffer.from('other'), { link: denied }), {
        code: 'fresh-restore-failed',
        message: `the done marker of ticket ${f.first.ticket} changed and could not be restored (EPERM); its claim was kept; nothing was queued`,
      });
      const left = readdirSync(queueDir(f.repo)).sort();
      const claims = left.filter((n) => n.startsWith(`${f.first.ticket}.done.`) && n.endsWith('.fresh'));
      assert.deepEqual([left.length, claims.length, left.includes(`${f.first.ticket}.done`)], [3, 1, false]);
      assert.equal(readFileSync(path.join(queueDir(f.repo), claims[0]), 'utf8'), newer);
      rmSync(path.join(queueDir(f.repo), claims[0]));
    } finally {
      f.done();
    }
  });

  test('a claim that cannot be read is linked back and refused fresh-restore-failed with the code only', async () => {
    const f = await fixture();
    try {
      f.cache({ status: 'stale' });
      const bytes = readFileSync(f.marker);
      const st = statSync(f.marker);
      const read = () => {
        throw Object.assign(new Error(`EIO at ${f.marker}`), { code: 'EIO' });
      };
      assert.throws(() => dropDoneIf(f.repo, f.first.ticket, bytes, { read }), (/** @type {any} */ err) => {
        assert.deepEqual([err.code, err.message], ['fresh-restore-failed', `the claimed done marker of ticket ${f.first.ticket} could not be read (EIO); it was put back; nothing was queued`]);
        return true;
      });
      assert.deepEqual([statSync(f.marker).ino, Buffer.compare(readFileSync(f.marker), bytes)], [st.ino, 0]);
      assert.deepEqual(readdirSync(queueDir(f.repo)).sort(), [`${f.first.ticket}.done`, `${f.first.ticket}.json`, 'worker.json']);
    } finally {
      f.done();
    }
  });
});
