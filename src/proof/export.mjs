/**
 * The measurement export (plan §4.7, `proof.isolation: export`, hardened per C5). A block's gates,
 * red→green and test-count delta run in `.code-forge/export/<block>/`, never in the shared tree
 * where other blocks are writing:
 *
 *   (a) `git archive <base>` extracted with `tar -x`;
 *   (b) export-ignore restoration — every base path whose `export-ignore` attribute is set (the
 *       path itself or a parent directory; attributes read AT BASE with `git check-attr
 *       --source=<base>`, plus `.git/info/attributes`) is written back from the base tree;
 *   (c) `proof.export.copy_untracked` paths that exist in the main tree ride along through
 *       `git archive --add-file` (never anything under `.code-forge/`);
 *   (d) the block's owned files at their CURRENT content (new files included, deleted ones removed);
 *   (e) `proof.export.link_dirs` symlinked from the main tree.
 *
 * A gate that fails inside the export on a file the export lacks but the main tree has is
 * `export.incomplete: <path>`, never a red test (`classifyExportFailure`).
 *
 * Git calls: `rev-parse` goes through B0's `git.mjs`; the verbs `git.mjs` does not model
 * (`archive -o` with `--add-file`, `ls-tree`, `check-attr`, `read-tree`/`checkout-index` on a
 * TEMPORARY index, `ls-files` with flags) run through `exec` with the same environment rule:
 * every inherited `GIT_*` variable is stripped (a hook's `GIT_DIR` must not redirect us), the
 * only one set is the temporary `GIT_INDEX_FILE`. The real index and working tree are never
 * written. Blob contents are never decoded to strings (binary-safe: `archive -o` + `tar`,
 * `checkout-index`).
 */

import { copyFile, lstat, mkdir, mkdtemp, readlink, realpath, rm, stat, symlink } from 'node:fs/promises';
import path from 'node:path';
import { runGates } from '../gates/run.mjs';
import { StateError } from '../state/paths.mjs';
import { ownsFile } from '../state/registry.mjs';
import { writeSigned } from '../state/run.mjs';
import { exec } from '../util/exec.mjs';
import { revParse } from '../util/git.mjs';
import { currentRunRoot } from '../util/tmp.mjs';

/** §1.3 defaults (C5). */
export const DEFAULT_LINK_DIRS = Object.freeze(['vendor', 'node_modules']);
export const DEFAULT_COPY_UNTRACKED = Object.freeze(['.env', '.env.testing']);

/** Where a proof row may say it measured (§6.1 `isolation`). */
export const ISOLATIONS = Object.freeze(/** @type {const} */ (['export', 'lock']));

const BLOCK_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/;
const GIT_TIMEOUT_MS = 120000;
const STATE_DIR = '.code-forge';

/**
 * `process.env` minus every `GIT_*`, plus `extra`.
 * @param {Record<string, string>} [extra]
 * @returns {NodeJS.ProcessEnv}
 */
function gitEnv(extra = {}) {
  /** @type {NodeJS.ProcessEnv} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith('GIT_')) env[key] = value;
  }
  return { ...env, ...extra };
}

/**
 * @param {string[]} args @param {string} cwd
 * @param {{input?: string, env?: Record<string, string>}} [opts]
 * @returns {Promise<string>} stdout
 */
async function git(args, cwd, opts = {}) {
  const res = await exec(['git', ...args], { cwd, env: gitEnv(opts.env), input: opts.input, timeoutMs: GIT_TIMEOUT_MS });
  if (res.result !== 'ok') throw new StateError('git-failed', `git ${args[0]} failed (exit ${res.code})`);
  return res.stdout;
}

/**
 * A repo-relative POSIX path with no `.`/`..`/empty segment.
 * @param {unknown} p @param {string} key - the config key or flag the value came from
 * @returns {string}
 */
function assertRelPath(p, key) {
  if (typeof p !== 'string' || p.length === 0 || path.isAbsolute(p) || p.includes('\\')) {
    throw new StateError('bad-path', `${key}: every entry must be a repo-relative POSIX path`);
  }
  if (p.split('/').some((s) => s === '' || s === '.' || s === '..')) {
    throw new StateError('bad-path', `${key}: no ".", ".." or empty segments`);
  }
  return p;
}

/** @param {string} p @returns {Promise<import('node:fs').Stats | null>} */
async function lstatOrNull(p) {
  try {
    return await lstat(p);
  } catch {
    return null;
  }
}

/**
 * @param {string} cwd @param {string} blockId @param {string} [exportRoot]
 * @returns {string} `<exportRoot | <cwd>/.code-forge/export>/<block>`
 */
export function exportDirFor(cwd, blockId, exportRoot) {
  if (typeof blockId !== 'string' || !BLOCK_ID.test(blockId)) throw new StateError('bad-block-id', `block id must match ${BLOCK_ID}`);
  return path.join(exportRoot ?? path.join(cwd, STATE_DIR, 'export'), blockId);
}

/**
 * `git ls-tree -r -t -z <base>` parsed.
 * @param {string} base @param {string} cwd
 * @returns {Promise<{mode: string, type: string, path: string}[]>}
 */
async function lsTree(base, cwd) {
  const out = await git(['ls-tree', '-r', '-t', '-z', '--full-tree', base], cwd);
  return out
    .split('\0')
    .filter((line) => line.length > 0)
    .map((line) => {
      const tab = line.indexOf('\t');
      const [mode, type] = line.slice(0, tab).split(' ');
      return { mode, type, path: line.slice(tab + 1) };
    });
}

/**
 * Base blobs the plain archive drops: `export-ignore` set on the path or on a parent directory,
 * with the attributes as they are AT BASE.
 * @param {{type: string, path: string}[]} entries @param {string} base @param {string} cwd
 * @returns {Promise<string[]>}
 */
async function exportIgnoredBlobs(entries, base, cwd) {
  const relevant = entries.filter((e) => e.type === 'tree' || e.type === 'blob');
  if (relevant.length === 0) return [];
  // A directory pattern (`docs/`) only matches a path spelled as a directory, so trees are asked
  // with a trailing slash.
  const input = `${relevant.map((e) => (e.type === 'tree' ? `${e.path}/` : e.path)).join('\0')}\0`;
  const out = await git(['check-attr', `--source=${base}`, '-z', '--stdin', 'export-ignore'], cwd, { input });
  const fields = out.split('\0');
  const ignored = new Set();
  for (let i = 0; i + 2 < fields.length; i += 3) {
    if (fields[i + 2] === 'set') ignored.add(fields[i].replace(/\/$/, ''));
  }
  const underIgnored = (/** @type {string} */ p) => {
    const segments = p.split('/');
    for (let n = 1; n <= segments.length; n += 1) if (ignored.has(segments.slice(0, n).join('/'))) return true;
    return false;
  };
  return relevant.filter((e) => e.type === 'blob' && underIgnored(e.path)).map((e) => e.path);
}

/**
 * Build (or rebuild) the export of one block.
 * @param {{
 *   cwd: string, blockId: string, baseSha: string, owned: ReadonlyArray<string>,
 *   linkDirs?: ReadonlyArray<string>, copyUntracked?: ReadonlyArray<string>, exportRoot?: string,
 * }} opts
 * @returns {Promise<{dir: string, base: string, restored: string[], untracked: string[], owned: string[], removed: string[], linked: string[]}>}
 *   repo-relative lists, sorted.
 */
export async function buildExport(opts) {
  const { blockId, baseSha, owned, linkDirs = DEFAULT_LINK_DIRS, copyUntracked = DEFAULT_COPY_UNTRACKED } = opts;
  if (typeof opts.cwd !== 'string' || !path.isAbsolute(opts.cwd)) throw new StateError('bad-path', 'buildExport: cwd must be an absolute path');
  const cwd = await realpath(opts.cwd);
  const dir = exportDirFor(cwd, blockId, opts.exportRoot);
  if (!Array.isArray(owned)) throw new StateError('bad-owned', 'buildExport: owned must be an array');
  for (const p of linkDirs) assertRelPath(p, 'proof.export.link_dirs');
  for (const p of copyUntracked) {
    assertRelPath(p, 'proof.export.copy_untracked');
    if (p === STATE_DIR || p.startsWith(`${STATE_DIR}/`)) {
      throw new StateError('bad-path', `proof.export.copy_untracked: nothing under ${STATE_DIR}/ is ever exported`);
    }
  }
  const resolved = await revParse(`${baseSha}^{commit}`, cwd);
  if (resolved.result !== 'ok') throw new StateError('bad-ref', 'buildExport: the base is not a commit in this repository');
  const base = resolved.stdout.trim();

  await rm(dir, { recursive: true, force: true });
  await mkdir(dir, { recursive: true });
  const scratch = await mkdtemp(path.join(currentRunRoot(), 'export-'));
  try {
    const entries = await lsTree(base, cwd);
    const baseBlobs = new Set(entries.filter((e) => e.type === 'blob').map((e) => e.path));

    // (a) + (c): one archive, untracked requirements added at their own directory.
    const untracked = [];
    const addArgs = [];
    for (const rel of copyUntracked) {
      if (baseBlobs.has(rel)) continue; // tracked at base — already in the archive (or restored below)
      const st = await lstatOrNull(path.join(cwd, rel));
      if (!st?.isFile()) continue;
      const parent = path.posix.dirname(rel);
      addArgs.push(`--prefix=${parent === '.' ? '' : `${parent}/`}`, `--add-file=${path.join(cwd, rel)}`);
      untracked.push(rel);
    }
    const tar = path.join(scratch, 'base.tar');
    // The rightmost --prefix applies to the tracked tree: reset it to the root.
    await git(['archive', '--format=tar', '-o', tar, ...addArgs, '--prefix=', base], cwd);
    const extracted = await exec(['tar', '-xf', tar, '-C', dir], { timeoutMs: GIT_TIMEOUT_MS });
    if (extracted.result !== 'ok') throw new StateError('tar-failed', `tar -x failed (exit ${extracted.code})`);

    // (b) export-ignore restoration from a temporary index (the real index is never touched).
    const restored = await exportIgnoredBlobs(entries, base, cwd);
    if (restored.length > 0) {
      const env = { GIT_INDEX_FILE: path.join(scratch, 'index') };
      await git(['read-tree', base], cwd, { env });
      await git(['checkout-index', '-f', '-z', '--stdin', `--prefix=${dir}${path.sep}`], cwd, { env, input: `${restored.join('\0')}\0` });
    }

    // (d) owned files at current content; owned base files deleted in the tree leave the export.
    const listed = (await git(['ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd)).split('\0').filter(Boolean);
    const exact = owned.filter((o) => !/[*?{]/.test(o));
    const candidates = [...new Set([...listed, ...exact, ...baseBlobs])].filter((f) => ownsFile(owned, f)).sort();
    const ownedCopied = [];
    const removed = [];
    for (const rel of candidates) {
      assertRelPath(rel, 'owned files');
      const src = path.join(cwd, rel);
      const dest = path.join(dir, rel);
      const st = await lstatOrNull(src);
      await rm(dest, { recursive: true, force: true });
      if (st === null) {
        if (baseBlobs.has(rel)) removed.push(rel);
        continue;
      }
      if (!st.isFile() && !st.isSymbolicLink()) continue;
      await mkdir(path.dirname(dest), { recursive: true });
      if (st.isSymbolicLink()) await symlink(await readlink(src), dest);
      else await copyFile(src, dest);
      ownedCopied.push(rel);
    }

    // (e) dependency dirs, linked from the main tree.
    const linked = [];
    for (const rel of linkDirs) {
      const src = path.join(cwd, rel);
      const st = await stat(src).catch(() => null);
      if (!st?.isDirectory()) continue;
      const dest = path.join(dir, rel);
      await rm(dest, { recursive: true, force: true });
      await mkdir(path.dirname(dest), { recursive: true });
      await symlink(src, dest, 'dir');
      linked.push(rel);
    }

    return { dir, base, restored: [...restored].sort(), untracked: untracked.sort(), owned: ownedCopied, removed, linked: linked.sort() };
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

/**
 * Remove a block's export (block close).
 * @param {{cwd: string, blockId: string, exportRoot?: string}} opts
 */
export async function removeExport({ cwd, blockId, exportRoot }) {
  await rm(exportDirFor(cwd, blockId, exportRoot), { recursive: true, force: true });
}

/** Missing-file errors a gate prints (§4.7 refusal). Each captures one candidate path or class. */
const MISSING_FILE_PATTERNS = Object.freeze([
  /ENOENT: no such file or directory, \w+ '([^']+)'/g,
  /\(([^()\s]+)\): [Ff]ailed to open stream: No such file or directory/g,
  /could not open (?:input file: )?['"]?([^'"\s]+)/gi,
  /['"]?([^\s'"]+?)['"]?: No such file or directory/g,
]);
const CLASS_NOT_FOUND = /Class "?([A-Za-z_][\w\\]*)"? not found/g;

/**
 * The repo-relative spelling of a path a gate printed, or null when it is outside both trees.
 * @param {string} candidate @param {string[]} exportDirs @param {string[]} mainTrees
 * @returns {string | null}
 */
function toRepoRelative(candidate, exportDirs, mainTrees) {
  const inside = (/** @type {string} */ root, /** @type {string} */ p) => {
    const rel = path.relative(root, p);
    return rel.length > 0 && !rel.startsWith('..') && !path.isAbsolute(rel) ? rel.split(path.sep).join('/') : null;
  };
  if (!path.isAbsolute(candidate)) return inside(exportDirs[0], path.join(exportDirs[0], candidate));
  for (const root of [...exportDirs, ...mainTrees]) {
    const rel = inside(root, candidate);
    if (rel !== null) return rel;
  }
  return null;
}

/**
 * Is a failed gate's output a missing-file error for a path the MAIN tree has and the export
 * lacks? Then it is `export.incomplete`, not a red test.
 * @param {{stdout?: string, stderr?: string, exportDir: string, mainTree: string}} opts
 * @returns {Promise<string | null>} the repo-relative missing path, or null (a real red)
 */
export async function classifyExportFailure({ stdout = '', stderr = '', exportDir, mainTree }) {
  const text = `${stderr}\n${stdout}`;
  const exportDirs = [exportDir, await realpath(exportDir).catch(() => exportDir)];
  const mainTrees = [mainTree, await realpath(mainTree).catch(() => mainTree)];
  /** @type {string[]} */
  const candidates = [];
  for (const pattern of MISSING_FILE_PATTERNS) {
    for (const m of text.matchAll(pattern)) candidates.push(m[1]);
  }
  /** @type {string[]} */
  const relatives = candidates.map((c) => toRepoRelative(c, exportDirs, mainTrees)).filter((r) => r !== null);
  for (const m of text.matchAll(CLASS_NOT_FOUND)) {
    // PSR-4 guesses for an autoloaded class: `App\Models\X` ⇒ `App/Models/X.php`, `app/Models/X.php`.
    const file = `${m[1].replace(/^\\/, '').split('\\').join('/')}.php`;
    relatives.push(file, file.replace(/^[^/]+/, (s) => s.toLowerCase()));
  }
  for (const rel of relatives) {
    if (rel.split('/').includes('..')) continue;
    if ((await lstatOrNull(path.join(mainTree, rel))) !== null && (await lstatOrNull(path.join(exportDir, rel))) === null) return rel;
  }
  return null;
}

/** @param {string} missing @returns {string} */
export function incompleteMessage(missing) {
  return `export.incomplete: ${missing} — add it to proof.export.copy_untracked or set proof.isolation: lock`;
}

/**
 * Run the block's gates (B5 `runGates`) inside the export; a red gate whose output is a
 * missing-file error for a main-tree path is re-labelled `export.incomplete`.
 * @param {{gates: Parameters<typeof runGates>[0], exportDir: string, mainTree: string, timeoutMs?: number}} opts
 */
export async function runGatesInExport({ gates, exportDir, mainTree, timeoutMs }) {
  const { results } = await runGates(gates, { cwd: exportDir, ...(timeoutMs ? { timeoutMs } : {}) });
  /** @type {{gate: string, path: string, message: string}[]} */
  const incomplete = [];
  const labelled = [];
  for (const r of results) {
    const missing = r.ok ? null : await classifyExportFailure({ stdout: r.stdout, stderr: r.stderr, exportDir, mainTree });
    if (missing === null) {
      labelled.push({ ...r, result: r.ok ? 'ok' : 'red' });
    } else {
      incomplete.push({ gate: r.gate, path: missing, message: incompleteMessage(missing) });
      labelled.push({ ...r, result: 'export.incomplete', missing });
    }
  }
  return { results: labelled, allOk: labelled.every((r) => r.ok), incomplete };
}

/**
 * A ledger `proof` row (§6.1). `isolation` must be `export` or `lock`.
 * @param {{block: string, isolation: string} & Record<string, any>} fields
 * @returns {Record<string, any>}
 */
export function proofRow(fields) {
  const { block, isolation, ...rest } = fields ?? /** @type {any} */ ({});
  if (typeof block !== 'string' || !BLOCK_ID.test(block)) throw new StateError('bad-block-id', `proof row: block must match ${BLOCK_ID}`);
  if (!ISOLATIONS.includes(/** @type {any} */ (isolation))) throw new StateError('bad-isolation', `proof row: isolation must be one of ${ISOLATIONS.join(', ')}`);
  return { ...rest, event: 'proof', block, isolation };
}

/**
 * Sign a `proof` row with the run key (B8) and hand it to the B6 writer (`writeRow`).
 * @param {{runId: string, writeRow: import('../state/run.mjs').WriteRow, row: {block: string, isolation: string} & Record<string, any>}} opts
 */
export async function recordProof({ runId, writeRow, row }) {
  return writeSigned(runId, writeRow, proofRow(row));
}
