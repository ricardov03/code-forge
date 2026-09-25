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
 *   proof restore             (the red→green journal — block B10b)
 *
 * `export` reads `proof.export.{link_dirs,copy_untracked}` from the workspace's `.code-forge.yml`
 * (defaults §1.3) and writes a signed `proof` row `isolation: export`; `lock`/`unlock` write
 * `isolation: lock` rows (B6 writer, B8 signer).
 */

import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { appendRow } from '../ledger/write.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { redactJSON, writeSafe } from '../util/redact.mjs';
import { DEFAULT_COPY_UNTRACKED, DEFAULT_LINK_DIRS, buildExport, recordProof, removeExport } from '../proof/export.mjs';
import { acquireProofLock, releaseProofLock } from '../proof/lock.mjs';
import { tierFor } from '../proof/tiers.mjs';

const USAGE =
  'usage: code-forge proof tier --file <path> --risk <0-3> [--security] [--cwd <dir>]\n' +
  '       code-forge proof export <block> --run <r> [--remove] · proof lock|unlock <block> --run <r>\n';

const NOT_LANDED = {
  baseline: 'proof baseline: held on Q16 (block B10c) — the gate enforces red→green only',
  restore: 'proof restore: ships with the red→green runner (block B10b)',
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
    err(`${NOT_LANDED[/** @type {'baseline' | 'restore'} */ (sub)]}\n`);
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
      out(exp);
      return 0;
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
