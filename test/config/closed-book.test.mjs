import assert from 'node:assert/strict';
import { test } from 'node:test';
import { closedBookRefused, needsClosedBook, openBookCodexAllowed } from '../../src/config/closed-book.mjs';

test('B32: needsClosedBook compares provider and role lowercased and trimmed; facts and coder never need it', () => {
  const cases = [
    ['openai', 'reviewer', true],
    ['OpenAI', 'Reviewer', true],
    [' OPENAI ', 'JUDGE', true],
    ['openai', 'S2', true],
    ['openai', 'author', true],
    ['openai', 'facts', false],
    ['openai', 'coder', false],
    ['anthropic', 'reviewer', false],
    ['xai', 'judge', false],
    [undefined, 'reviewer', false],
    ['openai', undefined, false],
  ];
  assert.deepEqual(cases.map(([p, r]) => needsClosedBook(p, r)), cases.map(([, , want]) => want));
});

test('B32: closedBookRefused is needsClosedBook unless the opt-in is exactly true', () => {
  assert.deepEqual(
    [closedBookRefused('OpenAI', 'reviewer', false), closedBookRefused('OpenAI', 'reviewer', true), closedBookRefused('anthropic', 'reviewer', false)],
    [true, false, false],
  );
  assert.deepEqual(
    [openBookCodexAllowed({ review: { allow_open_book_codex: true } }), openBookCodexAllowed({ review: { allow_open_book_codex: 'true' } }), openBookCodexAllowed({}), openBookCodexAllowed(undefined)],
    [true, false, false, false],
  );
});
