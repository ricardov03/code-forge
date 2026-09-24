import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';
import * as log from '../../src/util/log.mjs';
import { clearSecrets, redact, redactJSON, registerSecret, writeSafe } from '../../src/util/redact.mjs';

const FAKE_KEY = 'sk-fake-3f8a1c9d7e2b4a6f8091cdef1234567890abcdef';
/** A 12+ char substring of FAKE_KEY's secret portion — must not survive a partial mask either. */
const FAKE_KEY_SUBSTRING = '3f8a1c9d7e2b';

/** Occurrences of `needle` in `haystack`, via split — used for count-based (not boolean) proof. */
function occurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}

afterEach(() => {
  clearSecrets();
});

// ── Control: prove the key WOULD appear without redaction (the tests can fail) ─

test('control: with no secret registered, the same output paths DO carry the key verbatim', () => {
  // No registerSecret here (afterEach cleared the registry). The stdout and JSON paths exercised
  // below must then leak the key exactly once each — proving the 0-occurrence assertions in the
  // guarded tests come from redaction, not from a path that drops or mangles the text anyway.
  let captured = '';
  writeSafe({ write: (text) => { captured += text; } }, `Authorization: Bearer ${FAKE_KEY}`);
  const json = redactJSON({ apiKey: FAKE_KEY });

  assert.equal(occurrences(captured, FAKE_KEY), 1);
  assert.equal(occurrences(captured, '[REDACTED]'), 0);
  assert.equal(occurrences(json, FAKE_KEY), 1);
  assert.equal(occurrences(json, '[REDACTED]'), 0);
});

// ── The 3 guarded output paths (clause 4: 0 occurrences across all 3) ────────

test('redact scrubs the log path (src/util/log.mjs) — capturing BOTH stdout and stderr', () => {
  registerSecret(FAKE_KEY);

  const originalStdoutWrite = process.stdout.write.bind(process.stdout);
  const originalStderrWrite = process.stderr.write.bind(process.stderr);
  let capturedStdout = '';
  let capturedStderr = '';
  process.stdout.write = (chunk) => {
    capturedStdout += chunk;
    return true;
  };
  process.stderr.write = (chunk) => {
    capturedStderr += chunk;
    return true;
  };
  try {
    log.info(`resolved key: ${FAKE_KEY}`);
    log.error(`also resolved key: ${FAKE_KEY}`);
  } finally {
    process.stdout.write = originalStdoutWrite;
    process.stderr.write = originalStderrWrite;
  }

  const combined = capturedStdout + capturedStderr;
  // Proves something was actually written (a silently-swallowed message would pass a
  // boolean-only "key absent" check for the wrong reason).
  assert.ok(capturedStdout.includes('resolved key:'), `expected content on stdout, got: ${capturedStdout}`);
  assert.ok(capturedStderr.includes('also resolved key:'), `expected content on stderr, got: ${capturedStderr}`);
  assert.equal(occurrences(capturedStdout, '[REDACTED]'), 1);
  assert.equal(occurrences(capturedStderr, '[REDACTED]'), 1);
  assert.equal(occurrences(combined, FAKE_KEY), 0);
  assert.equal(occurrences(combined, FAKE_KEY_SUBSTRING), 0, 'a partial mask left a chunk of the key visible');
});

test('redact scrubs the raw stdout path (redact.writeSafe) — and lets the rest of the text through', () => {
  registerSecret(FAKE_KEY);

  let captured = '';
  const fakeStream = { write: (text) => { captured += text; } };
  writeSafe(fakeStream, `Authorization: Bearer ${FAKE_KEY}`);

  assert.ok(captured.includes('Authorization: Bearer '), `expected the non-secret text to survive, got: ${captured}`);
  assert.equal(occurrences(captured, '[REDACTED]'), 1);
  assert.equal(occurrences(captured, FAKE_KEY), 0);
  assert.equal(occurrences(captured, FAKE_KEY_SUBSTRING), 0);
});

test('redact scrubs the JSON path (redact.redactJSON) — and the rest of the object survives intact', () => {
  registerSecret(FAKE_KEY);

  const json = redactJSON({ provider: 'typesafe', apiKey: FAKE_KEY });
  const parsed = JSON.parse(json);

  assert.equal(parsed.provider, 'typesafe');
  assert.equal(parsed.apiKey, '[REDACTED]');
  assert.equal(occurrences(json, FAKE_KEY), 0);
  assert.equal(occurrences(json, FAKE_KEY_SUBSTRING), 0);
});

// ── Ordering bug: a shorter registered secret sitting inside a longer one ───

test('redactString replaces the LONGEST matching secret first, so a shorter registered secret does not leave the remainder of a longer one visible', () => {
  registerSecret('abc');
  registerSecret('abcdef');

  const result = redact('xabcdefx');

  assert.equal(result, 'x[REDACTED]x');
  assert.equal(occurrences(result, 'def'), 0);
});

test('two secrets that OVERLAP without nesting are masked as one range — no tail leaks', () => {
  registerSecret('abcd');
  registerSecret('cdef');

  const result = redact('xabcdefx');

  assert.equal(result, 'x[REDACTED]x');
  assert.equal(occurrences(result, 'ef'), 0);
});

test('a short secret never matches inside an earlier mask', () => {
  registerSecret(FAKE_KEY);
  registerSecret('RED');

  const result = redact(`k=${FAKE_KEY} and RED`);

  assert.equal(result, 'k=[REDACTED] and [REDACTED]');
  assert.equal(occurrences(result, '[REDACTED]'), 2);
});

test('repeated occurrences of one secret are each masked', () => {
  registerSecret('s3cr3t');
  assert.equal(redact('s3cr3t-s3cr3t s3cr3t'), '[REDACTED]-[REDACTED] [REDACTED]');
});

test('two different secret KEYS keep both entries (suffixed), never overwrite each other', () => {
  registerSecret('key-one-aaaa');
  registerSecret('key-two-bbbb');

  const parsed = JSON.parse(redactJSON({ 'key-one-aaaa': 1, 'key-two-bbbb': 2 }));

  assert.equal(Object.keys(parsed).length, 2);
  assert.deepEqual(parsed, { '[REDACTED]': 1, '[REDACTED]#2': 2 });
});

test('Map entries whose keys stringify identically keep both entries', () => {
  const map = new Map([
    [{ a: 1 }, 'first'],
    [{ b: 2 }, 'second'],
  ]);

  const parsed = JSON.parse(redactJSON(map));

  assert.equal(Object.keys(parsed).length, 2);
  assert.deepEqual(parsed, { '[object Object]': 'first', '[object Object]#2': 'second' });
});

// ── Object-key redaction ─────────────────────────────────────────────────────

test('redact scrubs a secret used as an object KEY, not just as a value', () => {
  registerSecret(FAKE_KEY);

  const json = redactJSON({ [FAKE_KEY]: 'some value' });

  assert.equal(occurrences(json, FAKE_KEY), 0);
  assert.deepEqual(JSON.parse(json), { '[REDACTED]': 'some value' });
});

// ── Non-plain objects: Date / Map / Set / circular ──────────────────────────

test('redact does not corrupt a Date into {} — it uses toJSON like JSON.stringify would', () => {
  const date = new Date('2026-01-01T00:00:00.000Z');
  const json = redactJSON({ createdAt: date });
  assert.equal(JSON.parse(json).createdAt, '2026-01-01T00:00:00.000Z');
});

test('redact renders a Map as a plain object and a Set as an array, redacting their contents', () => {
  registerSecret(FAKE_KEY);
  const map = new Map([['token', FAKE_KEY], ['ok', 'fine']]);
  const set = new Set(['fine', FAKE_KEY]);

  const mapJson = redactJSON(map);
  const setJson = redactJSON(set);

  assert.deepEqual(JSON.parse(mapJson), { token: '[REDACTED]', ok: 'fine' });
  assert.deepEqual(JSON.parse(setJson), ['fine', '[REDACTED]']);
});

test('redact substitutes [Circular] instead of overflowing the stack on a circular structure', () => {
  const obj = { name: 'root' };
  obj.self = obj;

  const result = redact(obj);

  assert.deepEqual(result, { name: 'root', self: '[Circular]' });
});

test('redact does NOT flag the same object appearing twice in unrelated branches as circular', () => {
  const shared = { shared: true };
  const result = redact({ a: shared, b: shared });

  assert.deepEqual(result, { a: { shared: true }, b: { shared: true } });
});

test('redact() on a non-string returns a JSON-safe copy, as documented (Map → object, Set → array)', () => {
  assert.deepEqual(redact(new Map([['k', 'v']])), { k: 'v' });
  assert.deepEqual(redact(new Set(['v'])), ['v']);
  assert.equal(redact('plain'), 'plain');
});
