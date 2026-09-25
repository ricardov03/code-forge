/**
 * A minimal Laravel+Vue fixture for `detect.mjs` (plan §2.2 row 1: `composer.json` +
 * `vendor/bin/pest`). Carries an `artisan` file (present in every real Laravel app) so detection
 * picks the `php artisan test --compact` form over the bare `vendor/bin/pest` fallback, plus
 * `phpstan.neon` and `pint.json` so `types`/`format` are both non-null.
 */

import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  await writeFile(path.join(dir, 'composer.json'), JSON.stringify({ name: 'fixture/laravel-vue', require: { php: '^8.2' } }, null, 2));
  await writeFile(path.join(dir, 'artisan'), '#!/usr/bin/env php\n<?php\n// fixture artisan\n');
  await mkdir(path.join(dir, 'vendor', 'bin'), { recursive: true });
  await writeFile(path.join(dir, 'vendor', 'bin', 'pest'), '#!/usr/bin/env php\n<?php\n// fixture pest binary\n');
  await writeFile(path.join(dir, 'phpstan.neon'), "parameters:\n    level: 5\n");
  await writeFile(path.join(dir, 'pint.json'), JSON.stringify({ preset: 'psr12' }, null, 2));
}
