/**
 * `code-forge block open|attempt|rebase|claim|close|stop` (plan §4.9). The skill calls
 * `block open` before every spawn in every engine; it prints the brief pointer (≤ 200 bytes).
 *
 *   block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file>
 *              [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <forecast>]
 *   block attempt <id> --run <r>
 *   block rebase <id> --run <r> [--head <ref>]
 *   block claim <id> <path> --run <r>
 *   block close <id> --run <r> [--worker-pid <pid>]
 *   block stop <id> --run <r> --reason <text>
 *   block waive <id> <finding-id> --run <r> --file <path> --reason <text>   (human-only, §4.9)
 *
 * `block close [--transcript <file>] [--no-require-reviews]` also runs B12b's review rows
 * (`review/gate-check.mjs`): a signed `review.approved` row for the current content hash of every
 * changed owned file (deletions included) — `--no-require-reviews` is the human's way out, is
 * forbidden to coders (`mergeForbidden`), and leaves a signed `gate.reviews_waived {by: human}`
 * row before the close (never a silent skip); late findings ruled; `review_cap` findings waived; no
 * `rule_break`, plus the transcript grep. The transcript is `--transcript`, else the block's
 * `transcript` in the run record, else `.code-forge/runs/<run>/<block>.log`; none ⇒ a signed
 * `gate.transcript_missing` row and a WARN line (never a silent pass, never a refusal). An id the
 * run does not know is `unknown_block` before any check runs.
 *
 * `--acceptance` is a YAML/JSON list of `{clause, tests: [test ids…]}`. `block close` runs B8's
 * rows of the gate (worker pin, MACs, orphans); the gate rows owned by later blocks join it
 * through `closeBlock`'s `extraChecks`.
 */

import { readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { parse as parseYAML } from 'yaml';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { attemptBlock, claimPath, closeBlock, openBlock, rebaseBlock, stopBlock } from '../state/block.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { findOverlap } from '../state/registry.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { loadKey } from '../state/signer.mjs';
import { blockFileSet, reviewGateCheck, waiveFinding } from '../review/gate-check.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE =
  'usage: code-forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>]\n' +
  '       code-forge block attempt|rebase|close <id> --run <r> · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>\n' +
  '       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text>\n';

const SUBCOMMANDS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive'];
const FLAG_VALUES = ['run', 'level', 'acceptance', 'brief', 'attempt', 'base', 'lines', 'head', 'worker-pid', 'reason', 'file', 'transcript'];

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, probe?: import('../state/run.mjs').StartTimeProbe}} [deps]
 * @returns {Promise<number>}
 */
export async function runBlock(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr, probe } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const [sub, ...rest] = args;
  if (!SUBCOMMANDS.includes(sub)) {
    err(USAGE);
    return 2;
  }
  try {
    const { flags, positionals } = parseFlags(rest, { values: FLAG_VALUES, multi: ['owned'], booleans: ['no-require-reviews'] });
    const [id, extra] = positionals;
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    if (!id || !runId) throw new StateError('usage', 'a block id and --run are required');
    if (sub === 'open' && (typeof flags.acceptance !== 'string' || !Array.isArray(flags.owned) || typeof flags.level !== 'string')) {
      throw new StateError('usage', 'block open needs --level, --owned and --acceptance');
    }
    if (sub === 'claim' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block claim needs exactly one path');
    if ((sub === 'stop' || sub === 'waive') && typeof flags.reason !== 'string') throw new StateError('usage', `block ${sub} needs --reason`);
    if (sub === 'waive' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block waive needs exactly one finding id');
    if (sub === 'waive' && typeof flags.file !== 'string') throw new StateError('usage', 'block waive needs --file <path> (a finding id is scoped to one file)');
    const record = await readRun(runId);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });

    if (sub === 'open') {
      const acceptance = parseYAML(await readFile(/** @type {string} */ (flags.acceptance), 'utf8'), { prettyErrors: false });
      let brief;
      if (typeof flags.brief === 'string') {
        const absolute = await realpath(flags.brief);
        const relative = path.relative(record.workspace, absolute);
        if (relative.startsWith('..') || path.isAbsolute(relative)) throw new StateError('usage', '--brief must be a file inside the workspace');
        brief = { path: relative, content: await readFile(absolute) };
      }
      const { block, pointer } = await openBlock({
        runId,
        id,
        level: /** @type {string} */ (flags.level),
        owned: /** @type {string[]} */ (flags.owned),
        acceptance,
        attempt: intFlag(flags.attempt, 'attempt'),
        base: typeof flags.base === 'string' ? flags.base : undefined,
        lines: intFlag(flags.lines, 'lines') ?? null,
        brief,
        writeRow,
      });
      out(`block ${id} open · ${block.level} · attempt ${block.attempt} · base ${block.base_sha.slice(0, 12)}\n`);
      out(`${pointer ?? 'brief: none given (--brief <file>)'}\n`);
      return 0;
    }
    if (sub === 'attempt') {
      out(`block ${id} attempt ${await attemptBlock({ runId, id, writeRow })}\n`);
      return 0;
    }
    if (sub === 'rebase') {
      const moved = await rebaseBlock({ runId, id, head: typeof flags.head === 'string' ? flags.head : undefined, writeRow });
      out(`block ${id} rebased ${moved.old_base.slice(0, 12)} → ${moved.new_base.slice(0, 12)}\n`);
      return 0;
    }
    if (sub === 'claim') {
      await claimPath({ runId, id, file: extra, writeRow });
      out(`block ${id} claimed ${extra}\n`);
      return 0;
    }
    if (sub === 'stop') {
      await stopBlock({ runId, id, reason: /** @type {string} */ (flags.reason), writeRow });
      out(`block ${id} stopped\n`);
      return 0;
    }
    if (sub === 'waive') {
      await waiveFinding({ runId, id, finding: extra, reason: /** @type {string} */ (flags.reason), file: /** @type {string} */ (flags.file), writeRow });
      out(`block ${id} waived ${extra} in ${flags.file} (by: human)\n`);
      return 0;
    }
    if (sub === 'close') {
      const entry = record.blocks?.[id];
      if (!entry) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
      const rows = (await readAllRows(record.project)).filter((row) => row.run === runId);
      const reviewsWaived = flags['no-require-reviews'] === true;
      const reviews = reviewGateCheck(async () => {
        const transcript = await findTranscript(record, id, typeof flags.transcript === 'string' ? flags.transcript : null);
        if (transcript === null) {
          await writeSigned(runId, writeRow, { event: 'gate.transcript_missing', block: id });
          err(`WARN block ${id}: no coder transcript found (--transcript, run record, .code-forge/runs/${runId}/${id}.log); the transcript grep did not run\n`);
        }
        // the human's way out of the per-file review check leaves a signed audit row before the close
        if (reviewsWaived) await writeSigned(runId, writeRow, { event: 'gate.reviews_waived', block: id, by: 'human' });
        return {
          block: id,
          runId,
          rows,
          key: await loadKey(runId),
          transcript,
          files: reviewsWaived
            ? []
            : await blockFileSet({ repoRoot: record.workspace, base: entry.base_sha, owned: entry.owned_files, matchOwned: (f) => findOverlap(entry.owned_files, [f]) !== null }),
        };
      });
      const result = await closeBlock({ runId, id, rows, writeRow, livePid: intFlag(flags['worker-pid'], 'worker-pid'), probe, extraChecks: [reviews] });
      (result.ok ? out : err)(`block ${id} ${result.status}${result.event ? ` (${result.event})` : ''}${result.reason ? `: ${result.reason}` : ''}\n`);
      return result.ok ? 0 : 1;
    }
  } catch (thrown) {
    err(`block ${sub ?? ''}: ${thrown?.message ?? String(thrown)}\n`);
    return thrown instanceof StateError && thrown.code === 'usage' ? 2 : 1;
  }
  err(USAGE);
  return 2;
}

/**
 * The block's coder transcript: `--transcript`, else the run record's `blocks.<id>.transcript`
 * (workspace-relative), else `.code-forge/runs/<run>/<id>.log`; null when none exists.
 * @param {Record<string, any>} record @param {string} id @param {string | null} given
 * @returns {Promise<string | null>}
 */
async function findTranscript(record, id, given) {
  if (given !== null) return readFile(given, 'utf8');
  const recorded = record.blocks?.[id]?.transcript;
  const candidates = [
    ...(typeof recorded === 'string' && recorded.length > 0 ? [path.resolve(record.workspace, recorded)] : []),
    path.join(record.workspace, '.code-forge', 'runs', record.run_id, `${id}.log`),
  ];
  for (const file of candidates) {
    try {
      return await readFile(file, 'utf8');
    } catch (thrown) {
      if (/** @type {any} */ (thrown)?.code !== 'ENOENT') throw thrown;
    }
  }
  return null;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function block(args) {
  return runBlock(args);
}
