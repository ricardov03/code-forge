/**
 * `test/imports-order.test.mjs` (plan §10.1 C17, §9.1, §10.4 B15) — the seam rule: for every
 * `src/<dir>/` file, a static relative import resolving to another top-level `src/` directory is
 * allowed only when that target directory is owned by a block in the FILE's owning block's
 * `depends_on` (transitively), per `docs/reference/blocks.json`. `src/util/**` and anything
 * outside `src/` (schema/, bare packages, `node:*`) are always allowed. `src/cli/` is a SHARED
 * directory — every block adds its own verb file there (help.mjs/version.mjs are B0's, the rest
 * are named file by file across a dozen later blocks' `owned` lists) — so it is checked by each
 * FILE's owning blocks, never as a whole: a cli verb file gets exactly the directories its own
 * block reaches through `depends_on`, and an import of ANOTHER cli file is allowed only when that
 * file is baseline-owned (listed by no block) or owned by a block the importer reaches. The only
 * exemptions are the exact files in `EXEMPT_FILES`, each with its reason.
 *
 * Wave 0/1 (`baseline_ids` in blocks.json: B0–B8) predate blocks.json's per-block tracking
 * (its own `baseline_note` says so), so their directories form one FOUNDATION set, always
 * allowed to import each other and `util` — this is what the block's own row means by "resolve
 * only to `src/util/**`, `node:*`, or a directory owned by a block in `depends_on`" for the
 * blocks that DO have a `depends_on` entry (session, worker, review, proof, doctor, and the
 * schema/decide/engines/keys amendments): those are checked against the real graph, precisely.
 *
 * The checker itself (`checkImportsOrder`) is shared between the real tree (must pass) and a
 * synthetic fixture (`test/fixtures/imports-order/batch-mate/`) with two blocks dispatched in the
 * same wave, neither depending on the other — proving the check actually catches a batch-mate
 * import (C17), not just repeating what the real tree already does.
 */
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, '..');
const BLOCKS_JSON = path.join(ROOT, 'docs', 'reference', 'blocks.json');
const FIXTURE_ROOT = path.join(__dirname, 'fixtures', 'imports-order', 'batch-mate');

/** Directories several blocks add files to: checked per FILE owner, never per directory. */
const SHARED_DIRS = new Set(['cli']);

/**
 * The exact files exempt from the real-tree check, and for each ONLY the target directories it
 * is exempt for, with the reason. Every one is a verb file (the composition root of its verb)
 * that a LATER block amended to wire its own module in, without that later block appearing in
 * the file's `owned` entry in blocks.json: the import is the amending block's, and that block
 * does reach the target through its own `depends_on`. Any other import in these files, and every
 * other file under `src/cli/`, is checked for real.
 * @type {Map<string, {dirs: string[], reason: string}>}
 */
const EXEMPT_FILES = new Map([
  ['src/cli/block.mjs', { dirs: ['review'], reason: 'B12b (commit 4947f30) wired its block-gate review check and `block waive` into the `block` verb; B12b owns src/review/gate-check.mjs' }],
  ['src/cli/gates.mjs', { dirs: ['proof'], reason: 'B12b (commit 4947f30) wrapped the gate run in the proof lock (runGatesGuarded); B12b reaches src/proof/ through B12a -> B10b -> B10a' }],
  ['src/cli/proof.mjs', { dirs: ['review'], reason: 'B12b (commit 4947f30) made the `proof` verb read the block file set from src/review/gate-check.mjs, which B12b owns' }],
  ['src/cli/run.mjs', { dirs: ['worker'], reason: 'B8 baseline verb; B11 (commit 6b3a68c) wired worker launch/re-pin into `run start`; B11 owns src/worker/**' }],
]);

/**
 * Every `.mjs` file under `<root>/src`, as a POSIX path relative to `root` (e.g. `src/worker/loop.mjs`).
 * @param {string} root
 * @returns {Promise<string[]>}
 */
async function listSrcFiles(root) {
  const srcDir = path.join(root, 'src');
  /** @param {string} dir @returns {Promise<string[]>} */
  async function walk(dir) {
    const entries = await readdir(dir, { withFileTypes: true });
    const files = [];
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) files.push(...(await walk(full)));
      else if (entry.isFile() && entry.name.endsWith('.mjs')) files.push(full);
    }
    return files;
  }
  const files = await walk(srcDir);
  return files.map((f) => path.relative(root, f).split(path.sep).join('/')).sort();
}

/**
 * Every static or dynamic import specifier a file's source text names — `import ... from '...'`
 * (`\s` spans newlines, so a multi-line named-import list is still matched: the whole text is
 * scanned with `matchAll`, never split into lines first, which would miss a `from` clause that
 * lands on its own line), a side-effect `import '...'` (no `from` at all), and `import('...')`.
 * @param {string} text
 * @returns {string[]}
 */
function importSpecifiers(text) {
  const specs = [];
  const patterns = [
    /\bfrom\s+['"]([^'"]+)['"]/g, // import {a} from '...'; export {a} from '...'; import x from '...'
    /\bimport\s+['"]([^'"]+)['"]/g, // side-effect: import '...'; (no binding, no `from`)
    /\bimport\(\s*['"]([^'"]+)['"]\s*\)/g, // dynamic import('...')
  ];
  for (const re of patterns) {
    for (const m of text.matchAll(re)) specs.push(m[1]);
  }
  return specs;
}

/** @param {string} p @returns {boolean} */
const isRelative = (p) => p.startsWith('./') || p.startsWith('../');

/**
 * The top-level `src/` directories an "owned" glob array touches (e.g. `["src/worker/**"]` ->
 * `["worker"]`; `["src/cli/block.mjs", "src/util/forbidden.mjs"]` -> `["cli", "util"]`).
 * @param {string[]} owned
 * @returns {Set<string>}
 */
function ownedTopDirs(owned) {
  const dirs = new Set();
  for (const pattern of owned) {
    const m = /^src\/([^/]+)\//.exec(pattern) ?? /^src\/([^/]+)\.mjs$/.exec(pattern);
    if (m) dirs.add(m[1]);
  }
  return dirs;
}

/**
 * Every block id in `blocksJson.blocks` (skipping `deleted: true`) whose `owned` array matches
 * `relFile` exactly or via a `dir/**` glob.
 * @param {string} relFile - e.g. `src/worker/loop.mjs`.
 * @param {Record<string, any>} blocksJson
 * @returns {string[]}
 */
function owningBlocks(relFile, blocksJson) {
  const owners = [];
  for (const [id, entry] of Object.entries(blocksJson.blocks ?? {})) {
    if (entry?.deleted === true) continue;
    for (const pattern of entry.owned ?? []) {
      if (pattern === relFile || (pattern.endsWith('/**') && relFile.startsWith(pattern.slice(0, -2)))) {
        owners.push(id);
        break;
      }
    }
  }
  return owners;
}

/**
 * The set of `src/` top-level directories `blockId` may import from: its own owned directories,
 * `baselineDirs` (always), and the same, transitively, for every id in its `depends_on` — a
 * baseline id (in `baselineIds`) resolves to exactly `baselineDirs` (blocks.json's own
 * `baseline_note`: Wave 0/1 blocks have no tracked `owned`/`depends_on` of their own).
 * @param {string} blockId @param {Record<string, any>} blocksJson @param {Set<string>} baselineIds
 * @param {Set<string>} baselineDirs @param {Map<string, Set<string>>} memo @param {Set<string>} visiting
 * @returns {Set<string>}
 */
function resolveDirs(blockId, blocksJson, baselineIds, baselineDirs, memo, visiting) {
  const cached = memo.get(blockId);
  if (cached) return cached;
  if (visiting.has(blockId)) return new Set(); // a dependency cycle contributes nothing further
  visiting.add(blockId);
  /** @type {Set<string>} */
  let result;
  if (baselineIds.has(blockId)) {
    result = new Set(baselineDirs);
  } else {
    const entry = blocksJson.blocks?.[blockId];
    result = new Set(baselineDirs);
    if (entry && entry.deleted !== true) {
      for (const d of ownedTopDirs(entry.owned ?? [])) result.add(d);
      for (const dep of entry.depends_on ?? []) {
        for (const d of resolveDirs(dep, blocksJson, baselineIds, baselineDirs, memo, visiting)) result.add(d);
      }
    }
  }
  visiting.delete(blockId);
  memo.set(blockId, result);
  return result;
}

/**
 * Every block id `blockId` reaches: itself and, transitively, every id in its `depends_on`
 * (baseline ids included as leaves — they have no entry of their own).
 * @param {string} blockId @param {Record<string, any>} blocksJson @param {Set<string>} [acc]
 * @returns {Set<string>}
 */
function reachableBlocks(blockId, blocksJson, acc = new Set()) {
  if (acc.has(blockId)) return acc;
  acc.add(blockId);
  const entry = blocksJson.blocks?.[blockId];
  if (entry && entry.deleted !== true) for (const dep of entry.depends_on ?? []) reachableBlocks(dep, blocksJson, acc);
  return acc;
}

/**
 * @param {{root: string, blocksJson: Record<string, any>, baselineIds: Set<string>, baselineDirs: Set<string>, sharedDirs?: Set<string>, exemptFiles?: Map<string, {dirs: string[], reason: string}>}} opts
 * @returns {Promise<Array<{file: string, target: string, targetDir: string}>>} every disallowed import, in file order.
 */
async function checkImportsOrder({ root, blocksJson, baselineIds, baselineDirs, sharedDirs = new Set(), exemptFiles = new Map() }) {
  const memo = new Map();
  /** @type {Array<{file: string, target: string, targetDir: string}>} */
  const violations = [];
  for (const relFile of await listSrcFiles(root)) {
    const exemptDirs = new Set(exemptFiles.get(relFile)?.dirs ?? []);
    const topDir = relFile.split('/')[1];
    const owners = owningBlocks(relFile, blocksJson);
    const allowed = new Set(baselineDirs);
    // A file's own directory is allowed only when no other block shares it; a shared directory's
    // files are checked one by one below.
    if (!sharedDirs.has(topDir)) allowed.add(topDir);
    const reached = new Set(baselineIds);
    for (const id of owners) {
      for (const d of resolveDirs(id, blocksJson, baselineIds, baselineDirs, memo, new Set())) if (!sharedDirs.has(d)) allowed.add(d);
      reachableBlocks(id, blocksJson, reached);
    }

    const text = await readFile(path.join(root, relFile), 'utf8');
    for (const spec of importSpecifiers(text)) {
      if (!isRelative(spec)) continue; // node:*, bare packages (yaml, ajv, @clack/prompts, …)
      const targetAbs = path.resolve(path.dirname(path.join(root, relFile)), spec);
      const targetRel = path.relative(root, targetAbs).split(path.sep).join('/');
      if (!targetRel.startsWith('src/')) continue; // schema/**, package.json, … — outside the seam graph
      const targetDir = targetRel.split('/')[1];
      let ok;
      if (sharedDirs.has(targetDir)) {
        // A file in a shared directory: allowed when baseline-owned (no block lists it) or owned
        // by a block the importer reaches through depends_on.
        const targetOwners = owningBlocks(targetRel, blocksJson);
        ok = targetOwners.length === 0 || targetOwners.some((id) => reached.has(id));
      } else {
        ok = allowed.has(targetDir);
      }
      if (!ok && !exemptDirs.has(targetDir)) violations.push({ file: relFile, target: spec, targetDir });
    }
  }
  return violations;
}

test('every src/<dir>/ import (src/cli/ checked file by file, four exact file/dir exemptions) resolves to util, outside src/, or a directory reachable through depends_on', async () => {
  const blocksJson = JSON.parse(await readFile(BLOCKS_JSON, 'utf8'));
  const baselineIds = new Set(blocksJson.baseline_ids);
  assert.ok(baselineIds.size >= 9, `expected >= 9 baseline ids, found ${baselineIds.size}`);
  // Wave 0/1's own top-level src/ directories (B0 util; B1 config; B2 keys; B3 decide; B4 engines;
  // B5 gates; B6 ledger; B7 install; B8 state) — none of them import each other's directories
  // outside this set (verified below), so this is the whole foundation, not a guess.
  const baselineDirs = new Set(['util', 'config', 'keys', 'decide', 'engines', 'gates', 'ledger', 'install', 'state']);
  // Exactly these four files carry an exemption, each for one directory, each with a reason.
  assert.deepEqual(
    [...EXEMPT_FILES].map(([file, { dirs, reason }]) => [file, dirs, reason.length > 0]),
    [
      ['src/cli/block.mjs', ['review'], true],
      ['src/cli/gates.mjs', ['proof'], true],
      ['src/cli/proof.mjs', ['review'], true],
      ['src/cli/run.mjs', ['worker'], true],
    ],
  );
  const violations = await checkImportsOrder({ root: ROOT, blocksJson, baselineIds, baselineDirs, sharedDirs: SHARED_DIRS, exemptFiles: EXEMPT_FILES });
  assert.deepEqual(violations, []);
});

test('a fixture batch-mate import (two same-wave blocks, neither in the other\'s depends_on) fails the check — a normal import, a side-effect import, and a cli verb file importing its batch-mate all caught', async () => {
  const blocksJson = JSON.parse(await readFile(path.join(FIXTURE_ROOT, 'blocks.json'), 'utf8'));
  const baselineIds = new Set(blocksJson.baseline_ids);
  const baselineDirs = new Set(['util']);
  const violations = await checkImportsOrder({ root: FIXTURE_ROOT, blocksJson, baselineIds, baselineDirs, sharedDirs: SHARED_DIRS });
  // 4 violations exactly. src/cli/verb-x.mjs's import of its OWN block's ../block-x/mod.mjs is
  // allowed and absent; verb-y.mjs and block-y/thing.mjs import nothing.
  assert.deepEqual(violations, [
    { file: 'src/block-x/mod.mjs', target: '../block-y/thing.mjs', targetDir: 'block-y' },
    { file: 'src/block-x/side-effect.mjs', target: '../block-y/thing.mjs', targetDir: 'block-y' },
    { file: 'src/cli/verb-x.mjs', target: '../block-y/thing.mjs', targetDir: 'block-y' },
    { file: 'src/cli/verb-x.mjs', target: './verb-y.mjs', targetDir: 'cli' },
  ]);
});
