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
 *                             which this block must hold; exit 0 proven, 1 not proven or refused).
 *                             The signed row carries `covers` (B20): the reverted sources, or
 *                             the test file for `assertion-deletion`.
 *                             B87: a test file a `proof.runners` entry of the run snapshot matches
 *                             runs with that entry's argv and is read from its JUnit report; the
 *                             row and the output then carry `runner`, `env_keys` and `failed_tests`.
 *
 * `export` reads `proof.export.{link_dirs,copy_untracked}` from the workspace's `.code-forge.yml`
 * (defaults §1.3) and writes a signed `proof` row `isolation: export`; `lock`/`unlock` write
 * `isolation: lock` rows (B6 writer, B8 signer).
 *
 * B62b: for a block with a worktree, `export` and `red-green` read the block's files, the test
 * and the base in its tree (`blockTree`, against the tree's `base_sha`) and measure in an export
 * built FROM the tree into the main tree's `.code-forge/export/<block>` — never in the tree, which
 * is left exactly as it was. The config still comes from the main tree. A recorded tree that is
 * gone or malformed refuses (exit 1, `…; no proof was run`). `lock`, `unlock`, `restore` and
 * `export --remove` touch only the run's lock and that export directory, as before.
 */

import { lstat } from 'node:fs/promises';
import path from 'node:path';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { appendRow } from '../ledger/write.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { proofRunners } from '../state/config-snapshot.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { redactJSON, writeSafe } from '../util/redact.mjs';
import { DEFAULT_COPY_UNTRACKED, DEFAULT_LINK_DIRS, buildExport, exportDirFor, recordProof, removeExport } from '../proof/export.mjs';
import { acquireProofLock, lockHolder, releaseProofLock } from '../proof/lock.mjs';
import { MECHANISMS, readJournal, restoreJournal, runRedGreen } from '../proof/red-green.mjs';
import { adapterPrecheck, runnerCovers, runnerHook, runnerTimeoutMs, selectRunner } from '../proof/runners.mjs';
import { tierFor } from '../proof/tiers.mjs';
import { blockTree } from '../proof/worktree.mjs';
import { blockFileSet, repoRelativePath } from '../review/gate-check.mjs';
import { ownsFile } from '../state/registry.mjs';

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
 * with the Node that runs this CLI. Other stacks need a `proof.runners` entry (B87).
 * @param {string} file @param {string | undefined} testCase
 * @returns {string[]}
 * @throws {StateError} `unsupported-test`
 */
function filteredTestArgv(file, testCase) {
  if (!NODE_TEST_FILE.test(file)) {
    throw new StateError('unsupported-test', 'only node:test files (.mjs, .cjs, .js) have a filtered test command in this version — add a proof.runners entry for it (see proof.md §2)');
  }
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
 * @typedef {object} ProofPlace
 * @property {string} dir - where the block's files, its test and its base are read: the workspace,
 *   or the workspace's place in the block's worktree.
 * @property {string} base - the block's `base_sha`, or its tree's.
 * @property {string | null} tree - the block's worktree, null without one.
 */

/**
 * B62b: where a block's proof is measured. A block without a worktree: the workspace and the
 * block's base, as before. A block with one: its tree (`blockTree`) and the tree's `base_sha`. A
 * recorded tree that is gone or malformed refuses (exit 1), as `gates run` does.
 * @param {string} runId @param {Record<string, any>} record @param {string} blockId @param {Record<string, any>} block
 * @returns {Promise<ProofPlace>}
 * @throws {StateError} `worktree-*`
 */
async function proofPlace(runId, record, blockId, block) {
  let wt;
  try {
    wt = await blockTree(record.workspace, block);
  } catch (thrown) {
    if (thrown instanceof StateError) throw new StateError(thrown.code, `block ${blockId} of run ${runId}: ${thrown.message}; no proof was run`);
    throw thrown;
  }
  return wt === null ? { dir: record.workspace, base: block.base_sha, tree: null } : { dir: wt.workspace, base: wt.base_sha, tree: wt.path };
}

/**
 * Build the block's measurement export and write its signed `proof` row (`step: export`). A block
 * with a worktree is exported FROM its tree, into the main tree's `.code-forge/export/<block>`: the
 * tree itself is never written to, and its row names it (`worktree`).
 * @param {{runId: string, record: Record<string, any>, blockId: string, block: Record<string, any>, place: ProofPlace, config: Record<string, any>, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 */
async function exportAndRecord({ runId, record, blockId, block, place, config, writeRow }) {
  const exp = await buildExport({
    cwd: place.dir,
    blockId,
    baseSha: place.base,
    owned: block.owned_files,
    ownedKinds: block.owned_kinds,
    linkDirs: config?.proof?.export?.link_dirs ?? DEFAULT_LINK_DIRS,
    copyUntracked: config?.proof?.export?.copy_untracked ?? DEFAULT_COPY_UNTRACKED,
    exportRoot: place.tree === null ? undefined : path.dirname(exportDirFor(record.workspace, blockId)),
  });
  await recordProof({
    runId,
    writeRow,
    row: { block: blockId, isolation: 'export', step: 'export', base_sha: exp.base, restored: exp.restored, untracked: exp.untracked, ...(place.tree === null ? {} : { worktree: place.tree }) },
  });
  return exp;
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown},
 *   precheck?: (entry: import('../proof/runners.mjs').RunnerEntry) => Promise<void>,
 *   hook?: (entry: import('../proof/runners.mjs').RunnerEntry, test: {file: string, testCase?: string}) => import('../proof/runners.mjs').RunnerHook | null}} [deps] -
 *   `precheck` and `hook` are test-only seams for the runner adapter's precheck and hook (B87)
 * @returns {Promise<number>}
 */
export async function runProof(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr, precheck = adapterPrecheck, hook = runnerHook } = deps;
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
        // B62b: a block with its own tree never measures under the lock (see red-green)
        const recorded = record.blocks?.[blockId]?.worktree;
        if (recorded !== undefined && recorded !== null) throw new StateError('usage', `block ${blockId} is proven in an export built from its tree; proof lock does not apply`);
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
      const place = await proofPlace(runId, record, blockId, block);
      // a block with its own tree is always proven in an export made from it (see red-green)
      if (place.tree === null && config?.proof?.isolation === 'lock') throw new StateError('isolation-lock', 'proof.isolation is lock — use proof lock instead of an export');
      out(await exportAndRecord({ runId, record, blockId, block, place, config, writeRow }));
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
      const runId = flags.run;
      const record = await readRun(runId);
      // B87: the runner comes from the run snapshot only, never the live file; no entry matches ⇒
      // the built-in node:test command, or the refusal
      const runners = proofRunners(record);
      const entry = selectRunner(runners, file);
      const runner = entry === null ? null : (hook(entry, { file, testCase }) ?? null);
      // a matched entry is run by its hook or not at all: never a node:test fallback (no row, no precheck)
      if (entry !== null && runner === null) throw new StateError('usage', `the ${entry.adapter} runner for ${file} has no hook; no proof was run`);
      // exactly one of the two: the built-in argv without a runner, the runner (its own argv) with one
      const how = runner === null || entry === null ? { argv: filteredTestArgv(file, testCase) } : { runner, timeoutMs: runnerTimeoutMs(entry) };
      const block = record.blocks?.[blockId];
      if (block?.status !== 'open') throw new StateError('no-block', `block ${blockId} is not open in run ${runId}`);
      const place = await proofPlace(runId, record, blockId, block);
      await assertTestFile(place.dir, file);
      const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });
      const config = await configAt(record.workspace);
      // A block with its own tree is always proven in an export made from that tree (never in the
      // tree itself, never under the lock), whatever the config says; otherwise lock or export.
      const isolation = place.tree !== null ? 'export' : config?.proof?.isolation === 'lock' ? 'lock' : 'export';
      if (isolation === 'lock' && (await lockHolder(runId)) !== blockId) {
        throw new StateError('proof.busy', `proof.isolation is lock — run code-forge proof lock ${blockId} --run ${runId} first`);
      }
      // `revert` puts every changed non-test owned file back to base; `assertion-deletion` touches only the test.
      const changed = await blockFileSet({
        repoRoot: place.dir,
        base: place.base,
        owned: block.owned_files,
        matchOwned: (f) => ownsFile(block.owned_files, f, block.owned_kinds),
      });
      // B87: a file a configured runner matches is a test too, never a source
      const sources = mechanism === 'assertion-deletion' ? [] : changed.map((c) => c.file).filter((f) => f !== file && !TEST_PATH.test(f) && !runnerCovers(runners, f));
      if (mechanism === 'revert' && sources.length === 0) {
        throw new StateError('usage', `block ${blockId} changed no non-test source file to revert — use --mechanism assertion-deletion`);
      }
      // B87: the adapter's precheck runs before the export is built and before any file is reverted
      if (entry !== null) await precheck(entry);
      const exp = isolation === 'export' ? await exportAndRecord({ runId, record, blockId, block, place, config, writeRow }) : null;
      if (exp) await assertTestFile(exp.dir, file); // the test must also be where it runs (owned, or tracked at base)
      const result = await runRedGreen({
        workDir: exp ? exp.dir : place.dir,
        repoDir: place.dir,
        base: place.base,
        ...how,
        test: { file, ...(testCase === undefined ? {} : { case: testCase }), characterization: mechanism === 'assertion-deletion' },
        sources,
        isolation,
        print: (line) => err(`${line}\n`),
      });
      // B20: the signed row names what the proof covers — the block gate reads `covers` per
      // high-tier file. `revert` covers the sources it put back to base; `assertion-deletion`
      // covers only the test file itself (its `::case` label is never part of a path). Every entry
      // is normalized exactly as the gate normalizes its changed files (`repoRelativePath`).
      const covers = [...new Set((result.mechanism === 'revert' ? sources : [file]).map((f) => repoRelativePath(f)))].sort();
      // A result with no red phase (refused before the tree was touched) is never proven: the row
      // still lands, `proven: false` and `red_kind: null`, and the verb exits 1.
      const red = result.red ?? null;
      const proven = result.proven === true && red !== null;
      // B87: only when a runner really ran, names only — its adapter, the bounded failed test names
      // and the env variable names (row and JSON alike); `failed_ids` stays in memory, and env
      // values are never shown
      const runnerFields =
        runner === null ? {} : { runner: runner.adapter, failed_tests: red !== null && 'failed_tests' in red ? red.failed_tests : [], env_keys: [...runner.env_keys] };
      await recordProof({
        runId,
        writeRow,
        row: {
          block: blockId,
          isolation,
          step: 'red-green',
          test: result.label,
          mechanism: result.mechanism,
          red_kind: red?.red_kind ?? null,
          red: red?.verdict ?? null,
          green: result.green ?? null,
          proven,
          failed: red?.failed ?? null,
          covers,
          ...runnerFields,
        },
      });
      out({
        block: blockId,
        test: result.label,
        mechanism: result.mechanism,
        isolation,
        red: red?.verdict ?? null,
        red_kind: red?.red_kind ?? null,
        green: result.green ?? null,
        proven,
        ...runnerFields,
        ...(exp ? { export: { dir: exp.dir, restored: exp.restored, untracked: exp.untracked } } : {}),
      });
      return proven ? 0 : 1;
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
