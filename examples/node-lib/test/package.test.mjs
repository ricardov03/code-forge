// The release files a measurement export must restore: `.gitattributes` marks both export-ignore.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const root = new URL('../', import.meta.url);
const read = (/** @type {string} */ rel) => readFileSync(new URL(rel, root), 'utf8');

test('CHANGELOG.md has a section for the package version', () => {
  const { version } = JSON.parse(read('package.json'));
  assert.equal(read('CHANGELOG.md').includes(`\n## ${version}\n`), true);
});

test('the CI workflow runs npm test on Node 22', () => {
  const workflow = read('.github/workflows/test.yml');
  assert.equal(workflow.includes('node-version: 22'), true);
  assert.equal(workflow.includes('- run: npm test'), true);
});
