import { mkdirSync, readdirSync, symlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/** The node_modules folder this checkout resolves its dependencies from. */
export function realNodeModules() {
  return path.dirname(path.dirname(fileURLToPath(import.meta.resolve('ajv/package.json'))));
}

/**
 * B701: give a private package copy a node_modules with every installed dependency EXCEPT the
 * @codedology scope. CI installs the published @codedology/code-forge-<target> platform packages
 * (optionalDependencies), and src/util/native.mjs prefers them over the package's own
 * native/bin build, so a whole-folder link let the tests find the real release binary.
 * @param {string} pkgDir the private package root; node_modules is created inside it
 * @param {string} [modules] the source node_modules (default: the one this checkout resolves)
 * @returns {string} the created node_modules path
 */
export function linkDependenciesWithoutPlatformPackages(pkgDir, modules = realNodeModules()) {
  const dest = path.join(pkgDir, 'node_modules');
  mkdirSync(dest, { recursive: true });
  for (const name of readdirSync(modules)) {
    if (name === '@codedology') continue;
    symlinkSync(path.join(modules, name), path.join(dest, name));
  }
  return dest;
}
