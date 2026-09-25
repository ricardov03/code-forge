/**
 * `code-forge proof tier|export|lock|unlock|baseline|restore` (plan §7, §4.7). Prints one JSON
 * object (redacted) per call. Exit codes: 0 done, 1 refused (`proof.busy`, not the holder, bad
 * ref…), 2 usage or a subcommand that has not landed.
 *
 *   proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]
 *   proof export <block> --run <r> [--remove]
 *   proof lock <block> --run <r>
 *   proof unlock <block> --run <r>
 *   proof baseline            (held on Q16 — block B10c)
 *   proof restore <block> --run <r>   (the red→green journal, B10b: restores the block's export
 *                             and the workspace; refuses with exit 1 when neither holds a journal)
 *   proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]
 *                             (B17: one red→green proof through `runRedGreen`, measured in a
 *                             freshly built export — or in the workspace under `proof lock`,
 *                             which this block must hold; exit 0 proven, 1 not proven or refused)
 *
 * `export` reads `proof.export.{link_dirs,copy_untracked}` from the workspace's `.code-forge.yml`
 * (defaults §1.3) and writes a signed `proof` row `isolation: export`; `lock`/`unlock` write
 * `isolation: lock` rows (B6 writer, B8 signer).
 */

import { lstat } from 'node:fs/promises';
import path from 'node:path';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { appendRow } from '../ledger/write.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { redactJSON, writeSafe } from '../util/redact.mjs';
import { DEFAULT_COPY_UNTRACKED, DEFAULT_LINK_DIRS, buildExport, exportDirFor, recordProof, removeExport } from '../proof/export.mjs';
import { acquireProofLock, lockHolder, releaseProofLock } from '../proof/lock.mjs';
import { MECHANISMS, readJournal, restoreJournal, runRedGreen } from '../proof/red-green.mjs';
import { tierFor } from '../proof/tiers.mjs';
import { blockFileSet } from '../review/gate-check.mjs';
import { findOverlap } from '../state/registry.mjs';

const USAGE =
  'usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]\n' +
  '       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>\n' +
  '       code-forge proof restore <block> --run <r>\n' +
  '       code-forge proof red-green <block> --run <r> --test <file[::case]> [--mechanism revert|assertion-deletion]\n';

/** A test file by path convention: never reverted as a "source" by the `revert` mechanism. */
const TEST_PATH = /(?:^|\/)(?:test|tests|__tests__|spec)\/|\.(?:test|spec)\.[cm]?[jt]sx?$|Test\.php$/;

/** The JavaScript-family test files the filtered command below knows how to run. */
const NODE_TEST_FILE = /\.(?:mjs|cjs|js)$/;

/**
 * `--test` as a repo-relative POSIX path: `./` segments dropped, never absolute, never escaping
 * the repository with `..`, nothing under `.code-forge/`.
 * @param {string} raw @returns {string}
 * @throws {StateError} `usage`
 */
function normalizeTestPath(raw) {
  if (raw.length === 0 || path.isAbsolute(raw) || raw.includes('\\') || raw.includes('\0')) throw new StateError('usage', '--test must be a repo-relative path');
  const normalized = path.posix.normalize(raw);
  if (normalized === '.' || normalized === '..' || normalized.startsWith('../')) throw new StateError('usage', '--test must stay inside the repository (no .. escape)');
  if (normalized === '.code-forge' || normalized.startsWith('.code-forge/')) throw new StateError('usage', '--test cannot be under .code-forge/');
  return normalized;
}

/**
 * @param {string} dir - the work dir the test runs in @param {string} file - normalized, repo-relative
 * @throws {StateError} `usage` when `file` is not a regular file under `dir`
 */
async function assertTestFile(dir, file) {
  const st = await lstat(path.join(dir, file)).catch(() => null);
  if (!st?.isFile()) throw new StateError('usage', `--test ${file} is not a file in ${dir}`);
}

/**
 * The test command filtered to one test: `node --test [--test-name-pattern ^<case>$] <file>`, run
 * with the Node that runs this CLI. Other stacks (Pest, Vitest…) have no filtered command here yet.
 * @param {string} file @param {string | undefined} testCase
 * @returns {string[]}
 * @throws {StateError} `unsupported-test`
 */
function filteredTestArgv(file, testCase) {
  if (!NODE_TEST_FILE.test(file)) throw new StateError('unsupported-test', 'only node:test files (.mjs, .cjs, .js) have a filtered test command in this version');
  const pattern = testCase === undefined ? [] : ['--test-name-pattern', `^${testCase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`];
  return [process.execPath, '--test', ...pattern, file];
}

const NOT_LANDED = {
  baseline: 'proof baseline: held on Q16 (block B10c) — the gate enforces red→green only',
};

/** @param {Record<string, any>} config @returns {string[]} */
const highPathsOf = (config) => (Array.isArray(config?.proof?.tiers?.high?.paths) ? config.proof.tiers.high.paths : []);

/**
 * The project config. `{}` (the §1.3 defaults apply) ONLY when no `.code-forge.yml` exists; a file
 * that exists but does not load or validate is refused — a typo must never silently drop
 * `proof.tiers.high.paths` (a tier downgrade), the `proof.isolation: lock` guard or the export
 * lists. The message names the problem kind and key paths only, never values.
 * @param {string} cwd
 * @returns {Promise<Record<string, any>>}
 * @throws {StateError} `bad-config`
 */
async function configAt(cwd) {
  const loaded = await loadProjectConfig(cwd);
  if (!loaded.ok && loaded.error === 'not-found') return {};
  if (!loaded.ok || !loaded.config) {
    throw new StateError('bad-config', `${DEFAULT_CONFIG_FILENAME} does not load (${loaded.error ?? 'no config'}) — run code-forge validate`);
  }
  const result = validateConfig(loaded.config);
  if (!result.valid) {
    const where = [...new Set(result.errors.map((e) => (typeof e.path === 'string' ? e.path : e.rule)))].join(', ');
    throw new StateError('bad-config', `${DEFAULT_CONFIG_FILENAME} is invalid (${result.errors.length} error(s) at ${where}) — run code-forge validate`);
  }
  return loaded.config;
}

/**
 * Build the block's measurement export and write its signed `proof` row (`step: export`).
 * @param {{runId: string, record: Record<string, any>, blockId: string, block: Record<string, any>, config: Record<string, any>, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 */
async function exportAndRecord({ runId, record, blockId, block, config, writeRow }) {
  const exp = await buildExport({
    cwd: record.workspace,
    blockId,
    baseSha: block.base_sha,
    owned: block.owned_files,
    linkDirs: config?.proof?.export?.link_dirs ?? DEFAULT_LINK_DIRS,
    copyUntracked: config?.proof?.export?.copy_untracked ?? DEFAULT_COPY_UNTRACKED,
  });
  await recordProof({
    runId,
    writeRow,
    row: { block: blockId, isolation: 'export', step: 'export', base_sha: exp.base, restored: exp.restored, untracked: exp.untracked },
  });
  return exp;
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runProof(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr } = deps;
  const out = (/** @type {object} */ value) => writeSafe(stdout, `${redactJSON(value)}\n`);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const [sub, ...rest] = args;

  if (typeof sub === 'string' && Object.hasOwn(NOT_LANDED, sub)) {
    err(`${NOT_LANDED[/** @type {'baseline'} */ (sub)]}\n`);
    return 2;
  }
  try {
    if (sub === 'tier') {
      const { flags, positionals } = parseFlags(rest, { values: ['file', 'risk', 'cwd'], booleans: ['security'] });
      if (positionals.length > 0 || typeof flags.file !== 'string' || typeof flags.risk !== 'string') {
        throw new StateError('usage', 'proof tier needs --file and --risk');
      }
      if (!/^[0-3]$/.test(flags.risk)) throw new StateError('usage', '--risk must be 0, 1, 2 or 3');
      const config = await configAt(typeof flags.cwd === 'string' ? flags.cwd : process.cwd());
      const result = tierFor({ file: flags.file, risk: Number(flags.risk), securitySensitive: flags.security === true, highPaths: highPathsOf(config) });
      out({ file: flags.file, ...result });
      return 0;
    }

    if (sub === 'restore') {
      const { flags, positionals } = parseFlags(rest, { values: ['run'] });
      const [blockId, extra] = positionals;
      if (!blockId || extra !== undefined || typeof flags.run !== 'string') throw new StateError('usage', 'proof restore needs one block id and --run');
      const record = await readRun(flags.run);
      // The runner journals in the directory it measured in: the block's export, or the
      // workspace itself under the proof lock.
      const dirs = [exportDirFor(record.workspace, blockId), record.workspace];
      const restored = [];
      for (const workDir of dirs) {
        if ((await readJournal(workDir)) === null) continue;
        restored.push({ dir: workDir, files: (await restoreJournal({ workDir })).restored });
      }
      if (restored.length === 0) throw new StateError('no-journal', `no red→green journal for block ${blockId} — nothing to restore`);
      out({ restored });
      return 0;
    }

    if (sub === 'export' || sub === 'lock' || sub === 'unlock') {
      const { flags, positionals } = parseFlags(rest, { values: ['run'], booleans: sub === 'export' ? ['remove'] : [] });
      const [blockId, extra] = positionals;
      if (!blockId || extra !== undefined || typeof flags.run !== 'string') throw new StateError('usage', `proof ${sub} needs one block id and --run`);
      const runId = flags.run;
      const record = await readRun(runId);
      const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });

      if (sub === 'lock') {
        out({ locked: await acquireProofLock({ runId, blockId, writeRow }) });
        return 0;
      }
      if (sub === 'unlock') {
        await releaseProofLock({ runId, blockId, writeRow });
        out({ unlocked: blockId });
        return 0;
      }

      const block = record.blocks?.[blockId];
      if (block?.status !== 'open') throw new StateError('no-block', `block ${blockId} is not open in run ${runId}`);
      if (flags.remove === true) {
        await removeExport({ cwd: record.workspace, blockId });
        out({ removed: blockId });
        return 0;
      }
      const config = await configAt(record.workspace);
      if (config?.proof?.isolation === 'lock') throw new StateError('isolation-lock', 'proof.isolation is lock — use proof lock instead of an export');
      out(await exportAndRecord({ runId, record, blockId, block, config, writeRow }));
      return 0;
    }

    if (sub === 'red-green') {
      const { flags, positionals } = parseFlags(rest, { values: ['run', 'test', 'mechanism'] });
      const [blockId, extra] = positionals;
      if (!blockId || extra !== undefined || typeof flags.run !== 'string' || typeof flags.test !== 'string') {
        throw new StateError('usage', 'proof red-green needs one block id, --run and --test <file[::case]>');
      }
      const mechanism = flags.mechanism;
      if (mechanism !== undefined && !MECHANISMS.includes(/** @type {any} */ (mechanism))) throw new StateError('usage', `--mechanism must be one of ${MECHANISMS.join(', ')}`);
      const sep = flags.test.indexOf('::');
      const testCase = sep < 0 ? undefined : flags.test.slice(sep + 2);
      if (testCase === '') throw new StateError('usage', '--test <file>::<case> needs a case name after ::');
      const file = normalizeTestPath(sep < 0 ? flags.test : flags.test.slice(0, sep));
      const argv = filteredTestArgv(file, testCase);
      const runId = flags.run;
      const record = await readRun(runId);
      const block = record.blocks?.[blockId];
      if (block?.status !== 'open') throw new StateError('no-block', `block ${blockId} is not open in run ${runId}`);
      await assertTestFile(record.workspace, file);
      const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });
      const config = await configAt(record.workspace);
      const isolation = config?.proof?.isolation === 'lock' ? 'lock' : 'export';
      if (isolation === 'lock' && (await lockHolder(runId)) !== blockId) {
        throw new StateError('proof.busy', `proof.isolation is lock — run code-forge proof lock ${blockId} --run ${runId} first`);
      }
      // `revert` puts every changed non-test owned file back to base; `assertion-deletion` touches only the test.
      const changed = await blockFileSet({
        repoRoot: record.workspace,
        base: block.base_sha,
        owned: block.owned_files,
        matchOwned: (f) => findOverlap(block.owned_files, [f]) !== null,
      });
      const sources = mechanism === 'assertion-deletion' ? [] : changed.map((c) => c.file).filter((f) => f !== file && !TEST_PATH.test(f));
      if (mechanism === 'revert' && sources.length === 0) {
        throw new StateError('usage', `block ${blockId} changed no non-test source file to revert — use --mechanism assertion-deletion`);
      }
      const exp = isolation === 'export' ? await exportAndRecord({ runId, record, blockId, block, config, writeRow }) : null;
      if (exp) await assertTestFile(exp.dir, file); // the test must also be where it runs (owned, or tracked at base)
      const result = await runRedGreen({
        workDir: exp ? exp.dir : record.workspace,
        repoDir: record.workspace,
        base: block.base_sha,
        argv,
        test: { file, ...(testCase === undefined ? {} : { case: testCase }), characterization: mechanism === 'assertion-deletion' },
        sources,
        isolation,
        print: (line) => err(`${line}\n`),
        record: { runId, blockId, writeRow },
      });
      out({
        block: blockId,
        test: result.label,
        mechanism: result.mechanism,
        isolation,
        red: result.red.verdict,
        red_kind: result.red.red_kind,
        green: result.green,
        proven: result.proven,
        ...(exp ? { export: { dir: exp.dir, restored: exp.restored, untracked: exp.untracked } } : {}),
      });
      return result.proven ? 0 : 1;
    }
  } catch (thrown) {
    const code = thrown instanceof StateError ? thrown.code : 'error';
    err(`proof ${sub}: ${thrown?.message ?? String(thrown)}\n`);
    return code === 'usage' ? 2 : 1;
  }

  err(USAGE);
  return 2;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function proof(args) {
  return runProof(args);
}
