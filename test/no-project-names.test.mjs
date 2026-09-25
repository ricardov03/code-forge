/**
 * `test/no-project-names.test.mjs` (plan §9.1, §10.4 B15; R13, V19). code-forge never names the
 * real project it must never touch, anywhere in package content: 0 case-insensitive hits across
 * `src/**`, `skill/**`, `evals/**`, `docs/**`, `examples/**` and `test/**` — except
 * `docs/design/**`, a private maintainer note excluded by root ruling (2026-09-25), the same way
 * the plan already carves out its other private note in §7.3 (see `EXCLUDED_PATHS` below), and
 * proven never to ship by the `npm pack --dry-run` test at the bottom of this file. The denylist
 * is assembled from string
 * parts (`['condo', 'mera'].join('')`, the same technique `test/skill/structure.test.mjs` uses)
 * so this file itself never carries either name as a literal token, and a control string proves
 * the sweep's own `countHits` matcher actually catches something.
 */
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, '..');

/** The excluded names, assembled from parts (never a literal token in this file). */
const NAMES = [['condo', 'mera'].join(''), ['finance', '360'].join('')];
/** The sweep's one matcher: case-insensitive, global so every occurrence counts. */
const PATTERN = new RegExp(NAMES.join('|'), 'gi');

/**
 * The number of excluded-name hits in `text` — the ONE matcher both the sweep and the control
 * use (a fresh RegExp from `PATTERN`'s own source and flags, so no `lastIndex` state is shared).
 * @param {string} text
 * @returns {number}
 */
const countHits = (text) => (text.match(new RegExp(PATTERN.source, PATTERN.flags)) ?? []).length;

/** Directories swept for a name hit; every one is real package content (never `plans/`, `sources/`, `research/`, `reports/`). */
const SWEPT_DIRS = ['src', 'skill', 'evals', 'docs', 'examples', 'test'];

/**
 * Root ruling (2026-09-25, on B15's original report): `docs/design/**` is the maintainer's own
 * internal design page, not package content — `npm pack` ships only `docs/reference` (this
 * file's own package.json-`files` clause, and the test below that pins it). It is the same kind
 * of private note the plan's §7.3 already carves out for a root-level file ("stays a private note
 * outside the product"), just under `docs/` instead of the repo root, so it is excluded from this
 * sweep the same way — never fixed up to read as product content, and never shipped.
 */
const EXCLUDED_PATHS = ['docs/design'];

/** @param {string} relPath - POSIX-joined, relative to ROOT @returns {boolean} */
const isExcluded = (relPath) => EXCLUDED_PATHS.some((p) => relPath === p || relPath.startsWith(`${p}/`));

/**
 * @param {string} dir
 * @returns {Promise<string[]>} every regular file under `dir` that exists, sorted, absolute.
 */
async function walk(dir) {
  /** @type {import('node:fs').Dirent[]} */
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return [];
  }
  const files = [];
  for (const entry of entries) {
    if (entry.name === 'node_modules') continue;
    const full = path.join(dir, entry.name);
    if (entry.isSymbolicLink()) continue; // never follow a symlink out of the swept tree
    if (entry.isDirectory()) files.push(...(await walk(full)));
    else if (entry.isFile()) files.push(full);
  }
  return files.sort();
}

test('the sweep\'s own matcher (countHits) catches a control line exactly once', () => {
  assert.equal(PATTERN.flags, 'gi');
  // The plan's control: one line naming the first name once, in UPPER CASE ⇒ exactly 1 hit.
  assert.equal(countHits(`ref: ${NAMES[0].toUpperCase()} run`), 1);
  // Both names on one line, mixed case ⇒ 2; a clean line ⇒ 0.
  assert.equal(countHits(`the ${NAMES[0].toUpperCase()} run, ${NAMES[1]} numbers`), 2);
  assert.equal(countHits('a neutral example library'), 0);
});

test('0 case-insensitive hits for the excluded project names across src/**, skill/**, evals/**, docs/**, examples/**, test/**', async () => {
  /** @type {string[]} */
  const hits = [];
  let checked = 0;
  for (const dir of SWEPT_DIRS) {
    const files = await walk(path.join(ROOT, dir));
    for (const file of files) {
      const relPath = path.relative(ROOT, file).split(path.sep).join('/');
      if (isExcluded(relPath)) continue;
      // Binary/asset files never carry the name as readable text and a non-utf8 read would only
      // add noise; every file this sweep cares about is source/text.
      if (/\.(png|jpg|jpeg|gif|ico|woff2?|ttf|eot|zip|gz|tgz)$/i.test(file)) continue;
      checked += 1;
      const text = await readFile(file, 'utf8');
      if (countHits(text) > 0) hits.push(relPath);
    }
  }
  assert.ok(checked > 100, `expected to check well over 100 files, checked ${checked}`);
  assert.deepEqual(hits, []);
});

test('the docs/design/** exclusion can never leak into the package: npm pack --dry-run lists nothing under docs/design/', () => {
  const res = spawnSync('npm', ['pack', '--dry-run', '--json'], { cwd: ROOT, encoding: 'utf8' });
  assert.equal(res.status, 0, res.stderr);
  const [pkg] = JSON.parse(res.stdout);
  const underDesign = pkg.files.map((/** @type {{path: string}} */ f) => f.path).filter((/** @type {string} */ p) => p.startsWith('docs/design/'));
  assert.deepEqual(underDesign, []);
});
