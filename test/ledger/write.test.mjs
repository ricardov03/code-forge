import assert from 'node:assert/strict';
import { access, appendFile, link, mkdir, readFile, readdir, unlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { test } from 'node:test';
import { ledgerDir, ledgerPath } from '../../src/ledger/paths.mjs';
import { appendRow, claimRotatedName, readAllRows, readRows } from '../../src/ledger/write.mjs';
import { withTempHome } from './helpers.mjs';

test('appendRow writes one JSON line under a temp HOME at ~/.code-forge/ledger/<slug>.jsonl, with the exact fields written', async () => {
  await withTempHome(async (home) => {
    await appendRow({ event: 'run.start', run: 'r1' }, { slug: 'proj' });
    const file = ledgerPath('proj');
    assert.equal(file, path.join(home, '.code-forge', 'ledger', 'proj.jsonl'));
    const rows = await readRows('proj');
    assert.equal(rows.length, 1);
    assert.equal(rows[0].event, 'run.start');
    assert.equal(rows[0].run, 'r1');
  });
});

test('appendRow rejects a row with no event, and leaves no side effect (no row written)', async () => {
  await withTempHome(async () => {
    await assert.rejects(() => appendRow({ run: 'r1' }, { slug: 'proj' }), TypeError);
    assert.deepEqual(await readRows('proj'), []);
  });
});

test('appendRow rejects a slug shaped like a path escape ("..")', async () => {
  await withTempHome(async () => {
    await assert.rejects(() => appendRow({ event: 'run.start' }, { slug: '..' }), TypeError);
  });
});

test('every row written carries tokens_source AND cost_source — 100% of rows, across mixed shapes', async () => {
  await withTempHome(async () => {
    const slug = 'coverage';
    await appendRow({ event: 'run.start' }, { slug });
    await appendRow({ event: 'session', role: 'reviewer', tokens_in: 1000, tokens_out: 200, cost_usd: 0.05 }, { slug });
    await appendRow({ event: 'session', role: 'coder', tokens_in: 500, tokens_out: 100, tokens_source: 'reported' }, { slug });
    const rows = await readRows(slug);
    assert.equal(rows.length, 3);
    assert.equal(rows.filter((r) => 'tokens_source' in r && 'cost_source' in r).length, 3);
    assert.equal(rows[0].tokens_source, null);
    assert.equal(rows[0].cost_source, null);
    assert.equal(rows[1].tokens_source, 'estimated');
    assert.equal(rows[1].cost_source, 'estimated');
    assert.equal(rows[2].tokens_source, 'reported');
    assert.equal(rows[2].cost_source, null);
  });
});

test('the ledger rotates to <slug>.1.jsonl once it crosses a (tiny, test-only) byte threshold, and rotates AGAIN to .2.jsonl on a second oversized append — no row lost across either rotation', async () => {
  await withTempHome(async (home) => {
    const slug = 'rotate';
    await appendRow({ event: 'a', pad: 'x'.repeat(50) }, { slug, rotateAtBytes: 40 });
    await appendRow({ event: 'b', pad: 'x'.repeat(50) }, { slug, rotateAtBytes: 40 });
    await appendRow({ event: 'c' }, { slug, rotateAtBytes: 40 });

    const rotated1 = JSON.parse((await readFile(path.join(home, '.code-forge', 'ledger', 'rotate.1.jsonl'), 'utf8')).trim());
    assert.equal(rotated1.event, 'a');
    const rotated2 = JSON.parse((await readFile(path.join(home, '.code-forge', 'ledger', 'rotate.2.jsonl'), 'utf8')).trim());
    assert.equal(rotated2.event, 'b');
    const live = JSON.parse((await readFile(ledgerPath(slug), 'utf8')).trim());
    assert.equal(live.event, 'c');

    const all = await readAllRows(slug);
    assert.deepEqual(all.map((r) => r.event), ['a', 'b', 'c'], 'readAllRows must return every row across rotations, in order');
  });
});

test('100 concurrent appendRow calls on the same slug, with a tiny rotateAtBytes forcing MANY contested rotations, lose nothing: readAllRows returns exactly 100 rows', async () => {
  // N=100 / rotateAtBytes=80 / a 60-byte pad is deliberately tight enough to force many
  // back-to-back rotation events under real concurrency — a smaller N (e.g. 20) or a looser
  // threshold did NOT reliably reproduce the race this test exists to catch (confirmed against a
  // deliberately reintroduced plain-rename, no-queue implementation: it either lost rows or threw
  // ENOENT on `rename` on every single run at these parameters, and was flaky-to-silent at N=20).
  await withTempHome(async () => {
    const slug = 'race';
    await Promise.all(
      Array.from({ length: 100 }, (_, i) => appendRow({ event: 'row', i, pad: 'x'.repeat(60) }, { slug, rotateAtBytes: 80 })),
    );
    const all = await readAllRows(slug);
    assert.equal(all.length, 100);
    assert.deepEqual(
      all.map((r) => r.i).sort((a, b) => a - b),
      Array.from({ length: 100 }, (_, i) => i),
    );
  });
});

test('readRows tolerates a torn final line (skips it) but throws on corruption in the middle of the file', async () => {
  await withTempHome(async (home) => {
    const slug = 'torn';
    await appendRow({ event: 'good-1' }, { slug });
    await appendRow({ event: 'good-2' }, { slug });
    const file = ledgerPath(slug);
    await appendFile(file, '{"event":"torn"'); // no closing brace/newline — simulates a crash mid-write
    const rows = await readRows(slug);
    assert.equal(rows.length, 2);
    assert.deepEqual(rows.map((r) => r.event), ['good-1', 'good-2']);

    // Corrupt the MIDDLE line for a second slug — this must throw, not be silently skipped.
    const slug2 = 'corrupt-middle';
    await appendRow({ event: 'first' }, { slug: slug2 });
    await appendRow({ event: 'third' }, { slug: slug2 });
    const file2 = ledgerPath(slug2);
    const lines = (await readFile(file2, 'utf8')).trim().split('\n');
    lines.splice(1, 0, '{not valid json');
    await writeFile(file2, `${lines.join('\n')}\n`, 'utf8');
    await assert.rejects(() => readRows(slug2));
  });
});

test('readRows returns [] for a slug with no ledger file yet; readAllRows does too', async () => {
  await withTempHome(async () => {
    assert.deepEqual(await readRows('never-written'), []);
    assert.deepEqual(await readAllRows('never-written'), []);
  });
});

// ── Fix round 2 ──────────────────────────────────────────────────────────────

test('the torn-line recovery survives a SECOND write: appending after a torn final line truncates the incomplete fragment away instead of fusing onto it (or leaving it as unreadable middle-of-file corruption)', async () => {
  await withTempHome(async () => {
    const slug = 'torn-then-append';
    await appendRow({ event: 'good-1' }, { slug });
    await appendFile(ledgerPath(slug), '{"event":"torn"'); // crash mid-write, no trailing newline
    await appendRow({ event: 'good-2' }, { slug });
    const rows = await readRows(slug);
    assert.deepEqual(rows.map((r) => r.event), ['good-1', 'good-2'], 'both real rows must survive, in order; the incomplete fragment is discarded, not left behind');

    const raw = await readFile(ledgerPath(slug), 'utf8');
    assert.equal(raw.includes('torn'), false, 'the incomplete fragment must be gone from disk, not merely isolated on its own unparseable line');
  });
});

// ── Fix round 3 ──────────────────────────────────────────────────────────────

test('torn-tail recovery counts BYTES: multi-byte rows before a torn tail survive byte-exact and parse (3 rows, exact bytes)', async () => {
  await withTempHome(async () => {
    const slug = 'torn-multibyte';
    const file = ledgerPath(slug);
    await appendRow({ event: 'info', note: 'información' }, { slug });
    await appendRow({ event: 'emoji', note: 'deploy 🚀 listo — ñandú' }, { slug });
    const before = await readFile(file); // Buffer: the exact bytes of the 2 good rows
    // A torn tail that itself ends mid-way through a multi-byte character (first 2 of 4 emoji bytes).
    const emojiBytes = Buffer.from('🚀', 'utf8');
    await appendFile(file, Buffer.concat([Buffer.from('{"event":"torn","note":"x'), emojiBytes.subarray(0, 2)]));
    await appendRow({ event: 'after', note: 'café' }, { slug });

    const after = await readFile(file);
    const afterRow = Buffer.from(`${JSON.stringify((await readRows(slug))[2])}\n`, 'utf8');
    assert.equal(Buffer.compare(after.subarray(0, before.length), before), 0, 'the 2 pre-existing rows must survive byte-for-byte');
    assert.equal(Buffer.compare(after.subarray(before.length), afterRow), 0, 'only the new row follows them — the torn fragment is gone');

    const rows = await readRows(slug);
    assert.deepEqual(
      rows.map((r) => [r.event, r.note]),
      [
        ['info', 'información'],
        ['emoji', 'deploy 🚀 listo — ñandú'],
        ['after', 'café'],
      ],
    );
  });
});

test('appendRow rejects a slug containing a dot (pins the fact SLUG_PATTERN has no ".", which is why a rotated-file collision between "proj" and "proj.x" cannot occur)', async () => {
  await withTempHome(async () => {
    await assert.rejects(() => appendRow({ event: 'run.start' }, { slug: 'proj.x' }), TypeError);
  });
});

test('readAllRows("proj") does not pick up a rotated file belonging to a DIFFERENT, longer slug that merely starts with "proj." (exact match, not startsWith)', async () => {
  await withTempHome(async () => {
    const dir = ledgerDir();
    await mkdir(dir, { recursive: true });
    // Hand-craft the file directly — 'proj.x' cannot be reached via appendRow (rejected above),
    // but the exact-match fix must hold structurally even if that ever changes.
    await writeFile(path.join(dir, 'proj.1.jsonl'), `${JSON.stringify({ event: 'mine' })}\n`);
    await writeFile(path.join(dir, 'proj.x.1.jsonl'), `${JSON.stringify({ event: 'not-mine' })}\n`);
    const rows = await readAllRows('proj');
    assert.deepEqual(rows.map((r) => r.event), ['mine']);
  });
});

test('appendRow rejects a non-finite or non-positive rotateAtBytes (NaN, 0, negative) instead of rotating on every append or never', async () => {
  await withTempHome(async () => {
    await assert.rejects(() => appendRow({ event: 'a' }, { slug: 'bad-rotate', rotateAtBytes: NaN }), RangeError);
    await assert.rejects(() => appendRow({ event: 'a' }, { slug: 'bad-rotate', rotateAtBytes: 0 }), RangeError);
    await assert.rejects(() => appendRow({ event: 'a' }, { slug: 'bad-rotate', rotateAtBytes: -1 }), RangeError);
  });
});

test('an invalid slug is rejected before ~/.code-forge is ever created — no directory side effect from a bad call', async () => {
  await withTempHome(async (home) => {
    await assert.rejects(() => appendRow({ event: 'a' }, { slug: '..' }), TypeError);
    await assert.rejects(() => access(path.join(home, '.code-forge')));
  });
});

test('claimRotatedName: when two racers both successfully link() to different candidates before either unlinks the source, the one that loses the unlink race removes its OWN candidate — no duplicate hardlinks left behind', async () => {
  await withTempHome(async () => {
    const dir = ledgerDir();
    await mkdir(dir, { recursive: true });
    const file = path.join(dir, 'dup.jsonl');
    await writeFile(file, `${JSON.stringify({ event: 'shared' })}\n`);

    // Simulate racer A: already claimed n=1 by linking, but has NOT unlinked the source yet —
    // exactly the window in which a second racer (B, driven by claimRotatedName below) can still
    // see the source and link its own candidate before A's unlink runs.
    await link(file, path.join(dir, 'dup.1.jsonl'));

    // Racer B (the real code under test): tries n=1 (EEXIST, A owns it), claims n=2, then races
    // its own unlink against "A" finishing first (scheduled to fire after B's link resolves).
    const bDone = claimRotatedName(file, dir, 'dup');
    await new Promise((resolve) => setImmediate(resolve)); // let B's link() land before A unlinks
    await unlink(file).catch(() => {}); // "A" finishes its unlink now, racing with B's own unlink
    await bDone;

    const rotatedFiles = (await readdir(dir)).filter((f) => f.startsWith('dup.') && f.endsWith('.jsonl'));
    assert.equal(rotatedFiles.length, 1, `expected exactly 1 surviving rotated file, got: ${rotatedFiles.join(', ')}`);
    const rows = await readAllRows('dup');
    assert.equal(rows.length, 1, 'the shared row must be counted once, not twice via two hardlinks');
  });
});
