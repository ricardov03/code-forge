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
import { readRun } from '../state/run.mjs';
import { writeSafe } from '../util/redact.mjs';

const USAGE =
  'usage: code-forge block open <id> --run <r> --level L<n> --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>]\n' +
  '       code-forge block attempt|rebase|close <id> --run <r> · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>\n';

const SUBCOMMANDS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop'];
const FLAG_VALUES = ['run', 'level', 'acceptance', 'brief', 'attempt', 'base', 'lines', 'head', 'worker-pid', 'reason'];

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
    const { flags, positionals } = parseFlags(rest, { values: FLAG_VALUES, multi: ['owned'] });
    const [id, extra] = positionals;
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    if (!id || !runId) throw new StateError('usage', 'a block id and --run are required');
    if (sub === 'open' && (typeof flags.acceptance !== 'string' || !Array.isArray(flags.owned) || typeof flags.level !== 'string')) {
      throw new StateError('usage', 'block open needs --level, --owned and --acceptance');
    }
    if (sub === 'claim' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block claim needs exactly one path');
    if (sub === 'stop' && typeof flags.reason !== 'string') throw new StateError('usage', 'block stop needs --reason');
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
    if (sub === 'close') {
      const rows = (await readAllRows(record.project)).filter((row) => row.run === runId);
      const result = await closeBlock({ runId, id, rows, writeRow, livePid: intFlag(flags['worker-pid'], 'worker-pid'), probe });
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

/** @param {string[]} args @returns {Promise<number>} */
export default async function block(args) {
  return runBlock(args);
}
