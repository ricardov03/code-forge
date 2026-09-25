import './support.mjs';
import assert from 'node:assert/strict';
import test from 'node:test';
import { checkTranscript } from '../../src/gates/transcript-grep.mjs';

test('transcript-grep flags `cat ~/.code-forge/runs/r.key` and `code-forge worker` (2 of 2), and 0 in a clean transcript', () => {
  const transcript = ['Reading the brief…', '$ cat ~/.code-forge/runs/r.key', '$ npx code-forge worker --run r1'].join('\n');
  const result = checkTranscript(transcript);
  assert.equal(result.ok, false);
  assert.equal(result.hits.length, 2);
  assert.deepEqual(
    result.hits.map((h) => h.id),
    ['code-forge-runs-access', 'code-forge-worker-from-coder'],
  );

  const clean = 'code-forge review-file src/a.mjs --block B5\n===BLOCK B5 COMPLETE===';
  const cleanResult = checkTranscript(clean);
  assert.equal(cleanResult.ok, true);
  assert.deepEqual(cleanResult.hits, []);
});

test('ok is true with 0 hits and false with ≥ 1 hit', () => {
  assert.equal(checkTranscript('nothing forbidden here').ok, true);
  assert.equal(checkTranscript('$ git push --force').ok, false);
});

test('extraTokens merges configured production.markers/names into the scan', () => {
  const clean = checkTranscript('$ mysql --host=prod-db.internal');
  assert.equal(clean.ok, true); // not forbidden without a configured marker

  const withMarker = checkTranscript('$ mysql --host=prod-db.internal', { extraTokens: ['prod-db.internal'] });
  assert.equal(withMarker.ok, false);
  assert.equal(withMarker.hits.length, 1);
});

test('hits carry {id, line} only — `line` is the real 1-based LINE NUMBER, not the matched line\'s text (which would leak the argv/secret it matched)', () => {
  const transcript = ['first line, nothing here', '$ git push --force origin main', 'third line'].join('\n');
  const result = checkTranscript(transcript);
  assert.equal(result.hits.length, 1);
  assert.deepEqual(Object.keys(result.hits[0]).sort(), ['id', 'line']);
  // The finding this closes: a test that only checks the KEYS would still pass if `line` held the
  // raw matched text instead of a line number — assert the type AND the exact value, and assert
  // the matched command text is nowhere in the JSON-serialized hit.
  assert.equal(typeof result.hits[0].line, 'number');
  assert.equal(result.hits[0].line, 2);
  const serialized = JSON.stringify(result.hits);
  assert.ok(!serialized.includes('git push'), 'the hit must not embed the matched command text');
  assert.ok(!serialized.includes('--force'), 'the hit must not embed the matched flag text');
});
