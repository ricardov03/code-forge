import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { capitalize, greet, pad } from '../src/strings.mjs';

test('pad on either side', () => {
  assert.equal(pad('7', 3), '  7');
  assert.equal(pad('7', 3, { side: 'right', fill: '.' }), '7..');
});

test('capitalize keeps the empty string', () => {
  assert.equal(capitalize('ada'), 'Ada');
  assert.equal(capitalize(''), '');
});

test('greet reads GREETING from the env file, else says hi', () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), 'node-lib-'));
  try {
    const file = path.join(dir, '.env');
    writeFileSync(file, 'GREETING=hello\n');
    assert.equal(greet('Ada', file), 'Hello, Ada!');
    assert.equal(greet('Ada', path.join(dir, 'missing.env')), 'Hi, Ada!');
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
