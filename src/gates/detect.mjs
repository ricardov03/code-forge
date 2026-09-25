/**
 * Gate auto-detection (plan §2.2, `src/gates/detect.mjs`): given a project root, decide which
 * stack it is and produce the four gate commands (`test`, `lint`, `types`, `format`) as argv
 * arrays, or `null` when the stack has no tool for that gate. Detection is pure filesystem
 * evidence (`fs.existsSync`/`readFileSync`) — no `git`, no child process, no network.
 *
 * Evidence is checked in the table's own order (PHP/Pest, then `package.json` scripts, Cargo,
 * pyproject, go.mod); the first stack whose marker file exists wins. A project with no marker
 * file at all is `unknown`: every gate is `null`, and `code` (a later block) refuses to run a
 * block whose `gates.test` is null — a block without a test gate cannot produce evidence.
 */

import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

/**
 * A gate command is normally a plain argv array (its own exit code decides red/green). Some tools
 * (`gofmt -l`) exit 0 regardless of whether anything is unformatted and instead print the
 * offending file names to stdout — for those, the descriptor carries `failOnStdout: true` so
 * `run.mjs` treats non-empty stdout as red too, since a bare exit-code check can never fail.
 * @typedef {string[] | {argv: string[], failOnStdout: true}} GateCommand
 * @typedef {GateCommand | null} Gate
 */
/** @typedef {{stack: string, test: Gate, lint: Gate, types: Gate, format: Gate}} DetectedGates */

/** @param {string} root @param {string} rel @returns {boolean} */
function has(root, rel) {
  return existsSync(path.join(root, rel));
}

/**
 * @param {string} root @param {string} rel
 * @returns {Record<string, any> | null} parsed JSON, or null when the file is absent or invalid.
 */
function readJSON(root, rel) {
  try {
    return JSON.parse(readFileSync(path.join(root, rel), 'utf8'));
  } catch {
    return null;
  }
}

/** @param {string} root @param {string} rel @returns {string | null} */
function readText(root, rel) {
  try {
    return readFileSync(path.join(root, rel), 'utf8');
  } catch {
    return null;
  }
}

// ── PHP / Pest ───────────────────────────────────────────────────────────────

/** @param {string} root @returns {DetectedGates | null} */
function detectPhp(root) {
  if (!has(root, 'composer.json') || !has(root, 'vendor/bin/pest')) return null;
  const test = has(root, 'artisan') ? ['php', 'artisan', 'test', '--compact'] : ['vendor/bin/pest'];
  const types = has(root, 'phpstan.neon') || has(root, 'phpstan.neon.dist') ? ['vendor/bin/phpstan'] : null;
  const format = has(root, 'pint.json') || has(root, 'vendor/bin/pint') ? ['vendor/bin/pint', '--test'] : null;
  return { stack: 'php-pest', test, lint: null, types, format };
}

// ── Node (package.json scripts) ────────────────────────────────────────────

/**
 * `pm` resolution order (§2.2): the `packageManager` field, then the lockfile present
 * (`pnpm-lock.yaml` → pnpm, `yarn.lock` → yarn), else `npm`.
 * @param {string} root @param {Record<string, any>} pkg @returns {string}
 */
function resolvePackageManager(root, pkg) {
  if (typeof pkg.packageManager === 'string' && pkg.packageManager.length > 0) {
    const name = pkg.packageManager.split('@')[0];
    if (name === 'pnpm' || name === 'yarn' || name === 'npm' || name === 'bun') return name;
  }
  if (has(root, 'pnpm-lock.yaml')) return 'pnpm';
  if (has(root, 'yarn.lock')) return 'yarn';
  return 'npm';
}

/**
 * A `package.json` `devDependencies`/`dependencies` entry named `dep`.
 * @param {Record<string, any>} pkg @param {string} dep @returns {boolean}
 */
function hasDependency(pkg, dep) {
  return Boolean(pkg.dependencies?.[dep] || pkg.devDependencies?.[dep]);
}

/** @param {string} root @returns {DetectedGates | null} */
function detectNode(root) {
  const pkg = readJSON(root, 'package.json');
  if (!pkg || typeof pkg !== 'object') return null;
  const scripts = pkg.scripts && typeof pkg.scripts === 'object' ? pkg.scripts : {};
  if (typeof scripts.test !== 'string') return null;

  const pm = resolvePackageManager(root, pkg);
  // `bun test` invokes Bun's OWN built-in test runner and ignores the package.json `test` script
  // entirely — `bun run test` is the form that actually runs the configured script, matching
  // every other package manager's `[pm, "test"]` shorthand behaviour.
  const test = pm === 'bun' ? [pm, 'run', 'test'] : [pm, 'test'];

  let lint = null;
  if (typeof scripts.lint === 'string') lint = [pm, 'run', 'lint'];
  else if (typeof scripts['lint:check'] === 'string') lint = [pm, 'run', 'lint:check'];

  let types = null;
  if (typeof scripts.typecheck === 'string') types = [pm, 'run', 'typecheck'];
  else if (typeof scripts['vue-tsc'] === 'string') types = [pm, 'run', 'vue-tsc'];
  else if (has(root, 'tsconfig.json')) types = ['npx', 'tsc', '--noEmit'];

  let format = null;
  if (typeof scripts['format:check'] === 'string') format = [pm, 'run', 'format:check'];
  else if (has(root, '.prettierrc') || has(root, '.prettierrc.json') || has(root, '.prettierrc.js') || hasDependency(pkg, 'prettier')) {
    format = ['npx', 'prettier', '--check', '.'];
  }

  return { stack: 'node', test, lint, types, format };
}

// ── Cargo ───────────────────────────────────────────────────────────────────

/** @param {string} root @returns {DetectedGates | null} */
function detectRust(root) {
  if (!has(root, 'Cargo.toml')) return null;
  return {
    stack: 'rust',
    test: ['cargo', 'test'],
    lint: ['cargo', 'clippy', '--', '-D', 'warnings'],
    types: null,
    format: ['cargo', 'fmt', '--check'],
  };
}

// ── Python (pyproject.toml) ─────────────────────────────────────────────────

/** @param {string} text @returns {boolean} a `[tool.mypy]` section is present. */
function hasMypySection(text) {
  return /^\[tool\.mypy(\..+)?\]\s*$/m.test(text);
}

/** @param {string} root @returns {DetectedGates | null} */
function detectPython(root) {
  const text = readText(root, 'pyproject.toml');
  if (text === null) return null;
  return {
    stack: 'python',
    test: ['pytest'],
    lint: ['ruff', 'check'],
    types: hasMypySection(text) ? ['mypy'] : null,
    format: ['ruff', 'format', '--check'],
  };
}

// ── Go ───────────────────────────────────────────────────────────────────────

/** @param {string} root @returns {DetectedGates | null} */
function detectGo(root) {
  if (!has(root, 'go.mod')) return null;
  return {
    stack: 'go',
    test: ['go', 'test', './...'],
    lint: ['go', 'vet', './...'],
    types: null,
    // `gofmt -l` prints the names of unformatted files to stdout but ALWAYS exits 0 — a bare
    // exit-code check can never turn this gate red. `failOnStdout` tells run.mjs to also treat
    // non-empty stdout as a failure.
    format: { argv: ['gofmt', '-l', '.'], failOnStdout: true },
  };
}

/** Detectors in table order — the first stack whose marker evidence exists wins. */
const DETECTORS = Object.freeze([detectPhp, detectNode, detectRust, detectPython, detectGo]);

/**
 * @param {string} root - an absolute path to a project root.
 * @returns {DetectedGates} the detected stack and its four gate commands (argv or null each).
 *   `stack: 'unknown'` with every gate `null` when no marker evidence matches.
 */
export function detectGates(root) {
  if (typeof root !== 'string' || root.length === 0) {
    throw new TypeError('detectGates: root must be a non-empty string');
  }
  for (const detector of DETECTORS) {
    const result = detector(root);
    if (result) return result;
  }
  return { stack: 'unknown', test: null, lint: null, types: null, format: null };
}
