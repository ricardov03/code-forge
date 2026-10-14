/**
 * `code-forge context pack --run <id> --block <id> [--json]` (block B63a; issue #13 item 5, issue
 * #10 item 2). Builds the coder context pack of one block from the `context` key of the project's
 * LIVE `.code-forge.yml` in the main tree (not the run-start snapshot: `context` is not authority)
 * — see `src/context/pack.mjs` for the sections, the order and the line cap — and writes it to
 * `<run root>/context/<block>.md`. The run root comes from the run's own config snapshot
 * (`tmp.root` is fixed for the run), as for the worker. The orchestrator sends the pack's text with
 * every coder task.
 *
 * Prints two lines: the path, then `tokens ~<n>` (the review packets' bytes / 4 estimate); with
 * `--json` one line `{"path":…,"tokens":…,"lines":…,"cut":…,"sha256":…}`. Each pack written
 * appends a signed `context.pack {block, path, sha256, tokens, lines, cut}` ledger row — unless
 * the block's last verified `context.pack` row has the same sha256 and path (same pack: no row,
 * same output). The ledger is read before anything is written. The new text goes to a temp file
 * first; the signed row is appended while it is still there, and only then is it renamed over
 * `<block>.md`. A row that cannot be appended removes only the temp file, so an earlier pack stays
 * as it was and nothing was written. A rename that fails after the row was appended says so.
 *
 * With no `context` key, the pack holds only the block header and built-in proof rule (300 lines).
 * Exit codes: 0 written, 1 refused (a rules or lessons file missing or outside
 * the repository, a bad `context` value, a control character, an unknown run or block, a file or
 * ledger write that fails), 2 usage. Never runs any command it lists.
 */

import { createHash } from 'node:crypto';
import { mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { assemblePack, ContextError, contextSettings, packTokens, readSources } from '../context/pack.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { runRootFor } from '../session/spawn.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { configInForce } from '../state/config-snapshot.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { loadKey, verifyRow } from '../state/signer.mjs';
import { assertBlockId, mainRootOf, WorkerError } from '../worker/ticket.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE = 'usage: code-forge context pack --run <id> --block <id> [--json]\n';

/** @param {unknown} err @returns {string} the fs error's code (or message) for a one-line refusal. */
const fsCode = (err) => /** @type {NodeJS.ErrnoException} */ (err)?.code ?? (err instanceof Error ? err.message : 'error');

/**
 * @typedef {object} ContextDeps
 * @property {{write: (s: string) => unknown}} [stdout]
 * @property {{write: (s: string) => unknown}} [stderr]
 * @property {(row: Record<string, any>) => Promise<unknown>} [appendRow] - the ledger append
 *   (default: the project's ledger file).
 */

/**
 * The last verified `context.pack` row of this run and block, or null.
 * @param {string} runId @param {string} slug @param {string} block
 * @returns {Promise<Record<string, any> | null>}
 */
async function lastPackRow(runId, slug, block) {
  const rows = (await readAllRows(slug)).filter((r) => r?.event === 'context.pack' && r.run === runId && r.block === block);
  if (rows.length === 0) return null;
  const key = await loadKey(runId);
  const valid = rows.filter((r) => verifyRow(r, key).ok);
  return valid.length > 0 ? valid[valid.length - 1] : null;
}

/**
 * @param {string[]} args @param {ContextDeps} [deps]
 * @returns {Promise<number>}
 */
export async function runContext(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const [sub, ...rest] = args;
  if (sub !== 'pack') {
    writeSafe(stderr, USAGE);
    return 2;
  }
  try {
    const { flags, positionals } = parseFlags(rest, { values: ['run', 'block'], booleans: ['json'] });
    if (positionals.length > 0 || typeof flags.run !== 'string' || typeof flags.block !== 'string') throw new StateError('usage', 'context pack needs --run and --block');
    const runId = flags.run;
    let block;
    try {
      block = assertBlockId(flags.block);
    } catch {
      throw new StateError('usage', `--block: ${JSON.stringify(flags.block)} is not a block id`);
    }
    const record = await readRun(runId);
    const entry = record.blocks?.[block];
    if (!entry) throw new ContextError('no-block', `run ${runId} has no block ${block}`);
    // rules and lessons come from the MAIN tree, never from a block worktree; so does the config
    const repoRoot = await mainRootOf(record.workspace);
    // only `context` is read LIVE (it is not authority)
    const loaded = await loadProjectConfig(repoRoot);
    if (!loaded.ok) throw new ContextError('bad-config', loaded.message);
    const live = /** @type {Record<string, any>} */ (loaded.config);
    const settings = contextSettings(live);
    if (settings === null && Object.hasOwn(live, 'context')) throw new ContextError('bad-context', 'context must be a mapping');
    const sources = readSources(repoRoot, settings);
    const { text, lines, cut } = assemblePack({
      settings,
      sources,
      block: {
        run: runId,
        block,
        level: entry.level ?? null,
        owned: Array.isArray(entry.owned_files) ? entry.owned_files : [],
        tests: Array.isArray(entry.test_files) ? entry.test_files : null,
        worktree: entry.worktree && typeof entry.worktree.path === 'string' ? { path: entry.worktree.path, branch: entry.worktree.branch } : null,
      },
    });
    const sha256 = createHash('sha256').update(text).digest('hex');
    const tokens = packTokens(text);
    // the run root from the run's own snapshot (`tmp.root` is fixed for the run, as the worker
    // made it); a record from before snapshots has none, and the live value is all there is
    const snapshot = configInForce(record) ?? live;
    const tmpRoot = typeof snapshot?.tmp?.root === 'string' ? snapshot.tmp.root : undefined;
    let runRoot;
    try {
      runRoot = runRootFor(runId, tmpRoot);
    } catch (err) {
      throw new ContextError('no-run-root', `the run root cannot be used (${err instanceof Error ? err.message : 'error'})`);
    }
    const dir = path.join(runRoot, 'context');
    const file = path.join(dir, `${block}.md`);
    // every ledger read and check happens BEFORE the file is touched: a refusal here writes nothing
    const last = await lastPackRow(runId, record.project, block);
    const needsRow = last === null || last.sha256 !== sha256 || last.path !== file;
    const tmp = `${file}.${process.pid}.tmp`;
    try {
      mkdirSync(dir, { recursive: true, mode: 0o700 });
      writeFileSync(tmp, text, { mode: 0o600 });
    } catch (err) {
      rmSync(tmp, { force: true });
      throw new ContextError('write-failed', `cannot write ${file} (${fsCode(err)})`);
    }
    // the row first, while the new text is only in the temp file: a failed append leaves the
    // earlier pack (if any) exactly as it was
    if (needsRow) {
      const append = deps.appendRow ?? ((/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project }));
      try {
        await writeSigned(runId, append, { event: 'context.pack', block, path: file, sha256, tokens, lines, cut });
      } catch (err) {
        rmSync(tmp, { force: true });
        writeSafe(stderr, `context: the context.pack ledger row could not be written (${err instanceof Error ? err.message : 'error'}); nothing was written\n`);
        return 1;
      }
    }
    try {
      renameSync(tmp, file);
    } catch (err) {
      rmSync(tmp, { force: true });
      const after = needsRow ? 'its context.pack ledger row was already written' : 'nothing was written';
      writeSafe(stderr, `context: cannot write ${file} (${fsCode(err)}); ${after}\n`);
      return 1;
    }
    if (flags.json === true) writeSafe(stdout, `${JSON.stringify({ path: file, tokens, lines, cut, sha256 })}\n`);
    else writeSafe(stdout, `${file}\ntokens ~${tokens}\n`);
    return 0;
  } catch (thrown) {
    if (thrown instanceof StateError && thrown.code === 'usage') {
      writeSafe(stderr, `context: ${thrown.message}\n${USAGE}`);
      return 2;
    }
    if (thrown instanceof ContextError || thrown instanceof StateError || thrown instanceof WorkerError) {
      writeSafe(stderr, `context: ${thrown.message}; nothing was written\n`);
      return 1;
    }
    throw thrown;
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function context(args) {
  return runContext(args);
}
