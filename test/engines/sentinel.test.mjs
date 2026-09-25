import assert from 'node:assert/strict';
import { test } from 'node:test';
import { parseAck, parseSentinel, verifyAck } from '../../src/engines/sentinel.mjs';

// ── parseSentinel: last-occurrence rule ──────────────────────────────────────

test('parseSentinel returns null when no sentinel line is present', () => {
  assert.equal(parseSentinel('just some ordinary coder chatter\nno sentinel here\n'), null);
});

test('parseSentinel returns null when only ANOTHER block\'s sentinel is present (no fallback to an unfiltered match)', () => {
  assert.equal(parseSentinel('===BLOCK B7 COMPLETE===\n', 'B4'), null);
});

test('parseSentinel parses a single COMPLETE line', () => {
  const result = parseSentinel('doing work...\n===BLOCK B4 COMPLETE===\n', 'B4');
  assert.deepEqual(result, { status: 'complete', blockId: 'B4' });
});

test('parseSentinel parses a single FAILED line with its reason', () => {
  const result = parseSentinel('===BLOCK B4 FAILED: gate red on pint===\n');
  assert.deepEqual(result, { status: 'failed', blockId: 'B4', reason: 'gate red on pint' });
});

test('the forecast-exceeded FAILED shape (plan §10.7) parses correctly', () => {
  const result = parseSentinel('===BLOCK B9 FAILED: forecast exceeded, split plan B9a/B9b===\n');
  assert.deepEqual(result, { status: 'failed', blockId: 'B9', reason: 'forecast exceeded, split plan B9a/B9b' });
});

test('parseSentinel takes the LAST sentinel line, not the first (a coder retried and revised its own report)', () => {
  const transcript = ['first attempt...', '===BLOCK B4 FAILED: pint red===', 'fixed it, retrying...', '===BLOCK B4 COMPLETE==='].join('\n');
  assert.deepEqual(parseSentinel(transcript, 'B4'), { status: 'complete', blockId: 'B4' });
});

test('parseSentinel takes the LAST line even when it reverses a later success back to failure', () => {
  const transcript = ['===BLOCK B4 COMPLETE===', 'actually the gate caught something after all', '===BLOCK B4 FAILED: gate red==='].join('\n');
  assert.deepEqual(parseSentinel(transcript, 'B4'), { status: 'failed', blockId: 'B4', reason: 'gate red' });
});

test('parseSentinel with a blockId filters out another block\'s interleaved sentinel lines', () => {
  const transcript = ['===BLOCK B7 COMPLETE===', '===BLOCK B4 FAILED: unrelated===', '===BLOCK B7 FAILED: this one is B7\'s==='].join('\n');
  assert.deepEqual(parseSentinel(transcript, 'B4'), { status: 'failed', blockId: 'B4', reason: 'unrelated' });
});

// A sentinel must sit ALONE on its (trimmed) line — both regexes are anchored `^...$`, so a coder
// quoting the sentinel text inside prose can never match, even combined with the last-wins rule.
test('a sentinel quoted inside ordinary prose ("I will not print ===BLOCK B4 COMPLETE=== yet") never matches — the FULL-LINE anchor rejects it even though it comes after a real FAILED line', () => {
  const transcript = ['===BLOCK B4 FAILED: x===', 'note: do not emit ===BLOCK B4 COMPLETE=== until green'].join('\n');
  assert.deepEqual(parseSentinel(transcript, 'B4'), { status: 'failed', blockId: 'B4', reason: 'x' });
});

test('parseSentinel throws for a non-string output', () => {
  assert.throws(() => parseSentinel(/** @type {any} */ (null)), TypeError);
});

// ── parseAck / verifyAck ──────────────────────────────────────────────────────

test('parseAck extracts sha8 and lines from a well-formed ACK line', () => {
  assert.deepEqual(parseAck('ACK 1a2b3c4d lines=42\n'), { sha8: '1a2b3c4d', lines: 42 });
});

test('parseAck returns null when no ACK line is present', () => {
  assert.equal(parseAck('hello\nworld\n'), null);
});

// Fix round 1 (MAJOR): the protocol is "no/wrong ACK ⇒ resend once" — a caller passing the
// transcript through a resend must see the LAST (post-resend) ACK, not the pre-resend one.
test('parseAck takes the LAST ACK line — a wrong ACK, then a resend, then the correct ACK: the correct one wins', () => {
  const transcript = ['ACK 11111111 lines=10', 'wrong sha, resending...', 'ACK 22222222 lines=10'].join('\n');
  assert.deepEqual(parseAck(transcript), { sha8: '22222222', lines: 10 });
});

test('parseAck matches hex case-insensitively and normalizes to lowercase', () => {
  assert.deepEqual(parseAck('ACK DEADBEEF lines=7\n'), { sha8: 'deadbeef', lines: 7 });
  assert.deepEqual(parseAck('ACK DeAdBeEf lines=7\n'), { sha8: 'deadbeef', lines: 7 });
});

// Negative parse cases: a too-lenient ACK_RE must not accept these.
test('parseAck returns null for a malformed sha (7 or 9 hex chars, or non-hex)', () => {
  assert.equal(parseAck('ACK 1a2b3c4 lines=4\n'), null); // 7 chars
  assert.equal(parseAck('ACK 1a2b3c4dd lines=4\n'), null); // 9 chars
  assert.equal(parseAck('ACK zzzzzzzz lines=4\n'), null); // non-hex
});

test('parseAck returns null for a non-numeric "lines="', () => {
  assert.equal(parseAck('ACK 1a2b3c4d lines=x\n'), null);
});

test('parseAck returns null when "ACK ..." is embedded mid-line in prose, not alone on its own line', () => {
  assert.equal(parseAck('I will ACK 1a2b3c4d lines=4 later\n'), null);
});

// `after`: scopes the search window to strictly after the LAST occurrence of a marker — lets a
// caller exclude an earlier echo of the wire-receipt instructions from being mistaken for a real
// ACK, and lets a resend re-scan cleanly.
test('parseAck with "after": an earlier ACK-shaped line that is really just an ECHOED instruction is excluded when a marker separates it from the true launch window', () => {
  const transcript = [
    'BRIEF /tmp/brief.md lines=10 sha=deadbeef <<<EOM>>>',
    'the brief says: reply ACK deadbeef lines=10 before any tool call', // an echo, NOT a real ACK
    '>>> real window starts here >>>',
    // the coder never actually acks in this scenario
    'reading the brief...',
  ].join('\n');
  const result = parseAck(transcript, { after: '>>> real window starts here >>>' });
  assert.equal(result, null, 'the echoed instruction before the marker must not be mistaken for a real ACK');
});

test('parseAck with "after": the real ACK after the marker IS found, even though an echo appears before it', () => {
  const transcript = [
    'the brief says: reply ACK deadbeef lines=10 before any tool call',
    '>>> real window starts here >>>',
    'ACK deadbeef lines=10',
  ].join('\n');
  const result = parseAck(transcript, { after: '>>> real window starts here >>>' });
  assert.deepEqual(result, { sha8: 'deadbeef', lines: 10 });
});

test('verifyAck: matching sha and lines -> ok', () => {
  const result = verifyAck('ACK deadbeef lines=7\n', 'deadbeef', 7);
  assert.deepEqual(result, { status: 'ok', ack: { sha8: 'deadbeef', lines: 7 } });
});

test('verifyAck: a wrong sha -> parked (not ok, not a silent pass)', () => {
  const result = verifyAck('ACK badc0ffe lines=7\n', 'deadbeef', 7);
  assert.equal(result.status, 'parked');
  assert.match(result.reason, /sha mismatch/);
});

test('verifyAck: a wrong line count -> parked', () => {
  const result = verifyAck('ACK deadbeef lines=3\n', 'deadbeef', 7);
  assert.equal(result.status, 'parked');
  assert.match(result.reason, /lines mismatch/);
});

test('verifyAck: no ACK at all -> parked', () => {
  const result = verifyAck('the coder never replied\n', 'deadbeef', 7);
  assert.equal(result.status, 'parked');
  assert.match(result.reason, /no ACK line/);
});

test('verifyAck compares case-insensitively (an uppercase expected sha still matches a lowercase-normalized ACK)', () => {
  const result = verifyAck('ACK deadbeef lines=7\n', 'DEADBEEF', 7);
  assert.equal(result.status, 'ok');
});

// Fix round 1 (MINOR): verifyAck now validates expectedSha8's own shape up front, instead of
// silently parking with a confusing message when e.g. a full 40-char sha is passed by mistake.
test('verifyAck throws a TypeError for a malformed expectedSha8 (not exactly 8 hex chars)', () => {
  assert.throws(() => verifyAck('ACK deadbeef lines=7\n', 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef', 7), TypeError); // 40 chars
  assert.throws(() => verifyAck('ACK deadbeef lines=7\n', 'deadbee', 7), TypeError); // 7 chars
  assert.throws(() => verifyAck('ACK deadbeef lines=7\n', 'zzzzzzzz', 7), TypeError); // non-hex
});

test('verifyAck resolves a wrong-then-resent ACK scenario as ok (end to end, LAST-wins)', () => {
  const transcript = ['ACK aaaaaaaa lines=10', 'sha mismatch, resending...', 'ACK deadbeef lines=10'].join('\n');
  const result = verifyAck(transcript, 'deadbeef', 10);
  assert.equal(result.status, 'ok');
});

test('parseAck throws for a non-string output', () => {
  assert.throws(() => parseAck(/** @type {any} */ (42)), TypeError);
});

// ── Fix round 3 (MINOR ×2): argument validation ──────────────────────────────

test('parseAck throws a TypeError for opts.after === "" (would otherwise empty the window and always miss a valid ACK)', () => {
  const output = 'receipt\nACK deadbeef lines=3\n';
  assert.throws(() => parseAck(output, { after: '' }), { name: 'TypeError', message: 'parseAck: opts.after must be a non-empty string when given' });
  assert.throws(() => verifyAck(output, 'deadbeef', 3, { after: '' }), { name: 'TypeError', message: 'parseAck: opts.after must be a non-empty string when given' });
  assert.throws(() => parseAck(output, /** @type {any} */ ({ after: 42 })), { name: 'TypeError', message: 'parseAck: opts.after must be a non-empty string when given' });
});

test('parseAck and verifyAck accept opts = null (searches the whole output, finds the ACK)', () => {
  const output = 'receipt\nACK deadbeef lines=3\n';
  assert.deepEqual(parseAck(output, null), { sha8: 'deadbeef', lines: 3 });
  assert.deepEqual(verifyAck(output, 'deadbeef', 3, null), { status: 'ok', ack: { sha8: 'deadbeef', lines: 3 } });
});

test('verifyAck throws a TypeError for an expectedLines that is not a non-negative integer (null, NaN, "3", -1, 1.5)', () => {
  const output = 'ACK deadbeef lines=3\n';
  for (const bad of [null, NaN, '3', -1, 1.5]) {
    assert.throws(
      () => verifyAck(output, 'deadbeef', /** @type {any} */ (bad)),
      { name: 'TypeError', message: /^verifyAck: expectedLines must be a non-negative integer when given/ },
      `expectedLines ${String(bad)} should have been refused`,
    );
  }
  assert.deepEqual(verifyAck('ACK deadbeef lines=0\n', 'deadbeef', 0), { status: 'ok', ack: { sha8: 'deadbeef', lines: 0 } });
});
