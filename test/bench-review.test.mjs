// B44: `scripts/bench-review.mjs` runs as a child process (own temp HOME, never ours) with 300 ms
// fake reviews, so CI stays fast: 6 tickets at parallel_tickets 3 must finish at least 2x faster
// than at 1 (ideal 3x; 2x leaves room for a loaded machine).
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, test } from 'node:test';
import { exec } from '../src/util/exec.mjs';
import { parseArgs } from '../scripts/bench-review.mjs';

const SCRIPT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', 'scripts', 'bench-review.mjs');

/** @param {string[]} args */
const bench = (args) => exec([process.execPath, SCRIPT, ...args], { env: { PATH: [path.dirname(process.execPath), '/usr/bin', '/bin'].join(path.delimiter), HOME: String(process.env.HOME), TMPDIR: String(process.env.TMPDIR ?? '/tmp') }, timeoutMs: 120000 });

describe('scripts/bench-review.mjs (B44)', () => {
  test('300 ms fake reviews over 6 tickets: parallel_tickets 3 is at least 2x faster than 1, and the text output names both times', async () => {
    const json = await bench(['--review-ms', '300', '--json']);
    assert.equal(json.code, 0);
    const res = JSON.parse(json.stdout.trim());
    assert.deepEqual([res.reviewMs, res.files, res.runs.map((/** @type {any} */ r) => r.tickets)], [300, 6, [1, 3]]);
    const [one, three] = res.runs;
    assert.equal(one.ms >= 6 * 300, true); // serial: at least 6 reviews back to back
    assert.equal(one.ms / three.ms >= 2, true, `N=1 ${one.ms} ms, N=3 ${three.ms} ms`);
    assert.equal(res.speedup, Math.round((one.ms / three.ms) * 100) / 100);

    const text = await bench(['--review-ms', '50', '--files', '2', '--tickets', '1,2']);
    assert.equal(text.code, 0);
    const lines = text.stdout.trim().split('\n');
    assert.equal(lines.length, 4);
    assert.equal(lines[0], '2 tickets, fake review 50 ms each');
    assert.match(lines[1] ?? '', /^parallel_tickets 1: \d+ ms$/);
    assert.match(lines[2] ?? '', /^parallel_tickets 2: \d+ ms$/);
    assert.match(lines[3] ?? '', /^speed-up: [\d.]+x \(parallel_tickets 2 vs 1\)$/);
  });

  test('bad arguments exit 2 with the usage line; parseArgs defaults are 2000 ms, 6 files, 1 and 3', async () => {
    assert.deepEqual(parseArgs([]), { reviewMs: 2000, files: 6, tickets: [1, 3], json: false });
    const bad = await bench(['--tickets', '3']);
    assert.equal(bad.code, 2);
    assert.match(bad.stderr, /^bench-review: --tickets needs two or more whole numbers from 1 to 16, like 1,3\nusage: /);
  });
});
