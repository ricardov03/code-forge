import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as config from '../../src/config/validate.mjs';

test('review.exclude defaults to lock globs at any depth; project lists replace defaults and [] disables them', () => {
  const match = config.reviewExcludeRule ?? (() => null);
  const locks = ['package-lock.json', 'npm-shrinkwrap.json', 'yarn.lock', 'pnpm-lock.yaml', 'bun.lock', 'bun.lockb', 'composer.lock', 'Cargo.lock', 'Gemfile.lock', 'poetry.lock', 'uv.lock', 'go.sum', 'Package.resolved', 'Podfile.lock', 'pubspec.lock', 'mix.lock', 'flake.lock'];
  for (const name of locks) {
    for (const file of [name, `site/${name}`, `site/deep/${name}`]) {
      assert.equal(match({}, file), `**/${name}`, `${file}: default rule`);
      assert.equal(match({ review: { exclude: [] } }, file), null, `${file}: disabled`);
    }
  }
  const replaced = { review: { exclude: ['site/{generated,vendor}/**', '*.snapshot'] } };
  assert.equal(match(replaced, 'site/generated/deep/code.mjs'), 'site/{generated,vendor}/**');
  assert.equal(match(replaced, 'a.snapshot'), '*.snapshot');
  assert.equal(match(replaced, 'nested/a.snapshot'), null, 'project globs are repo-relative');
  assert.equal(match(replaced, 'package-lock.json'), null, 'project list replaces defaults');
  for (const file of ['package.json', 'site/package-lock.json.bak', 'src/index.mjs']) assert.equal(match({}, file), null, file);
});
