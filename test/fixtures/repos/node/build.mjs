/**
 * A Node fixture for `detect.mjs` (plan §2.2 row 2: `package.json` scripts). Exercises every
 * branch of the package.json row: a `packageManager` field (must win over the npm default), a
 * `lint` script, a `tsconfig.json` with NO `typecheck`/`vue-tsc` script (so `types` falls back to
 * the direct `tsc --noEmit` form), and a `prettier` devDependency with NO `format:check` script
 * (so `format` falls back to the direct `prettier --check .` form).
 */

import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  const pkg = {
    name: 'fixture-node',
    packageManager: 'pnpm@8.15.0',
    scripts: { test: 'node --test', lint: 'eslint .' },
    devDependencies: { prettier: '^3.0.0' },
  };
  await writeFile(path.join(dir, 'package.json'), JSON.stringify(pkg, null, 2));
  await writeFile(path.join(dir, 'tsconfig.json'), JSON.stringify({ compilerOptions: { strict: true } }, null, 2));
}
