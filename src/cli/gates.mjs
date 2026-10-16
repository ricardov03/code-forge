/**
 * `code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep` (plan
 * §2.2, §4.6, §8.6). Every subcommand prints one JSON object (redacted — `redactJSON`) to stdout
 * and returns a process exit code: `0` gate green/check passed, `1` gate red/check failed,
 * `2` usage error.
 *
 *   gates detect --cwd <dir>
 *   gates run --cwd <dir> [--slug <slug>] [--timeout-ms <n>] [--run <r> --block <id>]
 *     (with --run/--block the gates run behind the proof lock, `runGatesGuarded`: another block
 *     holding the lock ⇒ `{"result":"proof.busy","holder":…}` and exit 1. B60: a gate argv item
 *     that is exactly `{test_files}` runs the block's related tests, or is dropped for the full
 *     suite — over `gates.full_suite_threshold_files` owned files (globs and directories count
 *     the tracked and new (not ignored) files they cover), no test file known, or no --run/--block (`reason: no_tests`);
 *     that gate's result and `gate.red` row, and the `gate.done` row, carry `scope: related|full`
 *     with `test_count` or `reason: threshold|no_tests`, and with --run/--block the rows name `run`
 *     and `block`. A gate given as `{argv, env}` runs with those variables, shown by name only
 *     (`env_keys`). code-forge never prints or records an env value. Gate env is not a secret
 *     store. Child output is masked only for values of 8 or more characters.
 *     B62b: for a block with a worktree (`proof.isolation: worktree`) the gates run in that tree —
 *     at the workspace's place in it (`blockTree`) — and `{test_files}` and the threshold count
 *     the files there. The gates are detected in the tree and configured by the main workspace's
 *     `.code-forge.yml`. `--cwd` may be left out, or must name the workspace's place in the tree:
 *     the tree's top level when the workspace is the repository's top level, else the same
 *     subdirectory of the tree (then the tree's top level is refused too). Any other `--cwd`, and
 *     any `--cwd=<dir>` argument, is refused (exit 2, nothing run). A tree that is gone, malformed or off its branch is refused
 *     with exit 1 (the message on stderr, no gate run, no row). The JSON output and the `gate.red` and
 *     `gate.done` rows then carry `worktree: <tree path>`.)
 *   gates secret-scan --cwd <dir> --file <path> [--file <path> ...]
 *   gates safe-edit --cwd <dir> --base <sha>
 *   gates scope --cwd <dir> --base <sha>
 *   gates acceptance --clauses <file.json> --tap <file>
 *   gates transcript-grep --file <path>
 */

import { realpathSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { writeSafe, redactJSON } from '../util/redact.mjs';
import { appendRow } from '../ledger/write.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { detectGates } from '../gates/detect.mjs';
import { printableGates, scopeFields, testScope, thresholdOf } from '../gates/expand.mjs';
import { runGates } from '../gates/run.mjs';
import { runGatesGuarded } from '../proof/lock.mjs';
import { blockTree } from '../proof/worktree.mjs';
import { readRun } from '../state/run.mjs';
import { loadKey, signRow } from '../state/signer.mjs';
import { StateError } from '../state/paths.mjs';
import { scanFiles } from '../gates/secret-scan.mjs';
import { checkSafeEdit } from '../gates/safe-edit.mjs';
import { computeFileSet } from '../gates/scope.mjs';
import { checkAcceptance, parseTapPassed } from '../gates/acceptance.mjs';
import { checkTranscript } from '../gates/transcript-grep.mjs';

/** B60: the gate env rule, in the same words as the schema and proof.md. */
const GATE_ENV_RULE = 'code-forge never prints or records an env value. Gate env is not a secret store. Child output is masked only for values of 8 or more characters.';

/**
 * Auto-detected gates (`detectGates`), overridden per-key by an on-disk `.code-forge.yml`'s
 * `gates.{test,lint,types,format}` when that key is explicitly present — a project that already
 * confirmed/edited its gates at `init` step 7 (plan §2 step 7) is authoritative over the table.
 * A missing/unparseable config file is not an error here: detection alone is a valid answer.
 * @param {string} cwd
 * @param {Record<string, any> | null} configured - the config's `gates` section ({@link configuredGates}).
 * @returns {import('../gates/detect.mjs').DetectedGates}
 */
function resolveGates(cwd, configured) {
  const detected = detectGates(cwd);
  if (!configured) return detected;
  return {
    stack: detected.stack,
    test: 'test' in configured ? configured.test : detected.test,
    lint: 'lint' in configured ? configured.lint : detected.lint,
    types: 'types' in configured ? configured.types : detected.types,
    format: 'format' in configured ? configured.format : detected.format,
  };
}

/**
 * The on-disk `.code-forge.yml`'s `gates` section, or null when there is none (or it does not load).
 * @param {string} cwd
 * @returns {Promise<Record<string, any> | null>}
 */
async function configuredGates(cwd) {
  const loaded = await loadProjectConfig(cwd);
  return loaded.ok && loaded.config?.gates && typeof loaded.config.gates === 'object' ? loaded.config.gates : null;
}

/**
 * `production.markers`/`production.names` from an on-disk config, merged into the forbidden list
 * `transcript-grep` scans with — absent/unparseable config yields no extra tokens.
 * @param {string} cwd
 * @returns {Promise<string[]>}
 */
async function resolveProductionMarkers(cwd) {
  const loaded = await loadProjectConfig(cwd);
  if (!loaded.ok || !loaded.config?.production) return [];
  const { markers, names } = loaded.config.production;
  return [...(Array.isArray(markers) ? markers : []), ...(Array.isArray(names) ? names : [])];
}

/**
 * @param {string[]} args @param {string} flag
 * @returns {{ok: true, value: string|undefined} | {ok: false}} see `ledger.mjs`'s twin helper —
 *   `ok: false` means the flag WAS given with no usable value (missing, or the next token is
 *   itself another flag), which a caller must treat as a usage error rather than a silent default.
 */
function readOptionalFlag(args, flag) {
  const i = args.indexOf(flag);
  if (i < 0) return { ok: true, value: undefined };
  const next = args[i + 1];
  if (next === undefined || next.startsWith('--')) return { ok: false };
  return { ok: true, value: next };
}

/** @param {string[]} args @param {string} flag @returns {string|undefined} */
function flagValue(args, flag) {
  const result = readOptionalFlag(args, flag);
  return result.ok ? result.value : undefined;
}

/**
 * Resolve `--cwd`, defaulting to `process.cwd()` ONLY when the flag is absent entirely. A flag
 * given with no usable value (`--cwd` at the end of argv, or immediately followed by another
 * flag) is a usage error, not a silent fallback to the wrong directory — every subcommand that
 * reads files or runs commands against a project root depends on this (finding: `--cwd` used to
 * go through `flagValue(...) ?? process.cwd()`, which cannot tell "absent" from "malformed" apart
 * and silently ran gates in the wrong directory for the latter).
 * @param {string[]} args @param {(s: string) => void} err @param {string} label
 * `given` says whether `--cwd` was on the command line (`value` is then its value, not the default).
 * @returns {{ok: true, value: string, given: boolean} | {ok: false}}
 */
function resolveCwd(args, err, label) {
  const flag = readOptionalFlag(args, '--cwd');
  if (!flag.ok) {
    err(`${label}: --cwd requires a value\n`);
    return { ok: false };
  }
  return { ok: true, value: flag.value ?? process.cwd(), given: flag.value !== undefined };
}

/** @param {string[]} args @param {string} flag @returns {string[]} every value of a repeatable flag. */
function repeatedFlagValues(args, flag) {
  const values = [];
  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === flag && i + 1 < args.length) values.push(args[i + 1]);
  }
  return values;
}

/**
 * B62b: the worktree the block `blockId` of run `runId` works in, with the run's main workspace;
 * null for a block without one. A run or block that cannot be read is null too: the proof lock
 * (`runGatesGuarded`) refuses it with its own message.
 * @param {string} runId @param {string} blockId
 * @returns {Promise<{tree: import('../proof/worktree.mjs').BlockTree, workspace: string} | null>}
 * @throws {import('../state/paths.mjs').StateError} a `worktree-*` code when the recorded tree is
 *   malformed, gone or unusable; the message names the block and run and says no gate was run.
 */
async function blockWorktree(runId, blockId) {
  let record;
  try {
    record = await readRun(runId);
  } catch {
    return null;
  }
  let tree;
  try {
    tree = await blockTree(record.workspace, record.blocks?.[blockId]);
  } catch (thrown) {
    if (thrown instanceof StateError) throw new StateError(thrown.code, `block ${blockId} of run ${runId}: ${thrown.message}; no gate was run`);
    throw thrown;
  }
  return tree ? { tree, workspace: record.workspace } : null;
}

/** @param {string} p @returns {string} the realpath of `p`, or `p` resolved when it does not exist. */
function realOrResolved(p) {
  try {
    return realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

/**
 * B60: the scope fields of a `{test_files}` gate's result (`scope` with `test_count` or `reason`),
 * or none for any other gate.
 * @param {Record<string, any>} result @returns {Record<string, any>}
 */
function scopeOf({ scope, test_count: testCount, reason }) {
  if (scope === undefined) return {};
  if (scope === 'changed') return { scope, test_count: testCount, ...(reason ? { reason } : {}) };
  return scope === 'related' ? { scope, test_count: testCount } : { scope, reason };
}

/**
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runGatesVerb(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr } = deps;
  const out = (/** @type {object} */ value) => writeSafe(stdout, `${redactJSON(value)}\n`);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const usage = () =>
    err(
      'usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]\n' +
        `  gates run: an argv item exactly {test_files} runs the block's related tests (needs --run and --block); over gates.full_suite_threshold_files owned files, or with no test file known, the full suite runs.\n` +
        `  gates run: a gate may be {argv, env}. ${GATE_ENV_RULE}\n`,
    );

  const [sub] = args;

  try {
    if (sub === 'detect') {
      const cwdArg = resolveCwd(args, err, 'gates detect');
      if (!cwdArg.ok) return 2;
      const gates = resolveGates(cwdArg.value, await configuredGates(cwdArg.value));
      // B60: a gate given with env is printed with its variable NAMES (`env_keys`), never the values
      out(printableGates(gates));
      return 0;
    }

    if (sub === 'run') {
      const cwdArg = resolveCwd(args, err, 'gates run');
      if (!cwdArg.ok) return 2;
      let cwd = cwdArg.value;

      const timeoutFlag = readOptionalFlag(args, '--timeout-ms');
      if (!timeoutFlag.ok) {
        err('gates run: --timeout-ms requires a value\n');
        return 2;
      }
      const timeoutMs = timeoutFlag.value !== undefined ? Number(timeoutFlag.value) : undefined;
      if (timeoutMs !== undefined && !(Number.isFinite(timeoutMs) && timeoutMs > 0)) {
        err(`gates run: --timeout-ms must be a finite number > 0, got "${timeoutFlag.value}"\n`);
        return 2;
      }

      // `--slug` has no sensible default (unlike `--cwd`): given-but-malformed must refuse, not
      // silently run the gates while dropping every ledger row on the floor.
      const slugFlag = readOptionalFlag(args, '--slug');
      if (!slugFlag.ok) {
        err('gates run: --slug requires a value\n');
        return 2;
      }
      const slug = slugFlag.value;

      const runFlag = readOptionalFlag(args, '--run');
      const blockFlag = readOptionalFlag(args, '--block');
      if (!runFlag.ok || !blockFlag.ok || (runFlag.value === undefined) !== (blockFlag.value === undefined)) {
        err('gates run: --run <id> and --block <id> go together, each with a value\n');
        return 2;
      }
      const changed = args.includes('--changed');
      if (changed && runFlag.value === undefined) {
        err('gates run: --changed needs --run <id> and --block <id>\n');
        return 2;
      }

      // B62b: a block with a worktree runs its gates in that tree, configured by the main workspace
      const placed = runFlag.value !== undefined && blockFlag.value !== undefined ? await blockWorktree(runFlag.value, blockFlag.value) : null;
      if (placed) {
        // `--cwd=<dir>` is no form of --cwd: for a worktree block it is refused, never ignored
        const joined = args.find((a) => a.startsWith('--cwd='));
        if (joined !== undefined) {
          err(`gates run: block ${blockFlag.value} of run ${runFlag.value} works in its worktree ${placed.tree.workspace}, not in ${joined} — drop it (--cwd=<dir> is not read as --cwd); no gate was run\n`);
          return 2;
        }
        if (cwdArg.given && realOrResolved(cwd) !== realOrResolved(placed.tree.workspace)) {
          err(`gates run: block ${blockFlag.value} of run ${runFlag.value} works in its worktree ${placed.tree.workspace}, not in --cwd ${cwd} — drop --cwd (or give the worktree); no gate was run\n`);
          return 2;
        }
        cwd = placed.tree.workspace;
      }
      const configured = await configuredGates(placed ? placed.workspace : cwd); // loaded once: the gates and the threshold
      const gates = resolveGates(cwd, configured);
      const inTree = placed ? { worktree: placed.tree.path } : {};
      let ran;
      if (runFlag.value !== undefined && blockFlag.value !== undefined) {
        const threshold = thresholdOf(configured?.full_suite_threshold_files);
        /** @type {typeof gates & {repeatNewTests?: import('../gates/run.mjs').RepeatNewTests, lightTestFiles?: string[]}} */
        let planned = gates;
        if (configured?.repeat_new_tests !== undefined) {
          const record = await readRun(runFlag.value);
          const block = record.blocks?.[blockFlag.value];
          if (block) planned = { ...gates,
            repeatNewTests: { n: configured.repeat_new_tests, base: block.worktree?.base_sha ?? block.base_sha, owned: block.owned_files, kinds: block.owned_kinds, onSkip: (line) => err(`${line}\n`) },
          };
        }
        // Missing records are diagnosed by the guarded runner, after its lock decision.
        const block = await readRun(runFlag.value).then((record) => record.blocks?.[blockFlag.value]).catch((error) => {
          if (error.code === 'no-run') return null;
          throw error;
        });
        const light = block?.kind === 'light';
        if (light) {
          const scope = testScope({ testFiles: block.test_files, owned: block.owned_files, threshold: Infinity });
          const command = planned.test;
          const argv = Array.isArray(command) ? command : command?.argv;
          if (scope.scope !== 'related' || !argv?.includes('{test_files}')) throw new StateError('light-tests', 'light-tests: block tests and a gates.test {test_files} item are required');
          planned = { ...planned, lightTestFiles: scope.files };
        }
        const guarded = await runGatesGuarded({ runId: runFlag.value, blockId: blockFlag.value, gates: planned, cwd, threshold, changed: light ? false : changed, slug, ...(timeoutMs ? { timeoutMs } : {}) });
        if (guarded.result === 'proof.busy') {
          out({ result: 'proof.busy', holder: /** @type {{holder: string}} */ (guarded).holder });
          return 1;
        }
        ran = /** @type {{value: {results: any[], allOk: boolean, ledger_slug?: string, files?: Record<string, string>, changed_scope?: import('../gates/expand.mjs').ChangedScope}}} */ (guarded).value;
      } else {
        ran = await runGates(gates, { cwd, ...(timeoutMs ? { timeoutMs } : {}) });
      }
      const { results, allOk } = ran;
      const repeats = results.find((r) => r.repeats)?.repeats;

      const ledgerSlug = slug ?? ran.ledger_slug;
      if (ledgerSlug) {
        // Preserve the existing rows when off; repeat evidence is covered by the run signature.
        const writeGateRow = async (/** @type {Record<string, any>} */ row) => repeats && runFlag.value !== undefined
          ? appendRow(signRow({ run: runFlag.value, ...row }, await loadKey(runFlag.value)), { slug: ledgerSlug })
          : appendRow(row, { slug: ledgerSlug });
        // B60: with --run/--block the rows name the run and the block, so the run's binnacle reads them
        const owner = runFlag.value !== undefined ? { run: runFlag.value, block: blockFlag.value, ...inTree } : {};
        for (const result of results) {
          if (!result.skipped && !result.ok) {
            await writeGateRow({ ...owner, event: 'gate.red', gate: result.gate, code: result.code, ...scopeOf(result),
              ...(result.reason === 'flaky-test' ? { reason: 'flaky-test' } : {}),
              ...(result.repeats ? { repeats: result.repeats } : {}) });
          }
        }
        // B60: one scope per run (every `{test_files}` gate shares it), named on gate.done too;
        // without --run/--block that is `scope: full, reason: no_tests`
        const scoped = results.find((r) => r.scope !== undefined);
        await writeGateRow({ ...owner, event: 'gate.done', stack: gates.stack, all_ok: allOk,
          ...(ran.changed_scope ? scopeFields(ran.changed_scope) : scoped ? scopeOf(scoped) : {}),
          ...(ran.files ? { files: ran.files } : {}), ...(repeats ? { repeats } : {}) });
      }
      out({ stack: gates.stack, ...inTree, results, allOk, ...(repeats ? { repeats } : {}) });
      return allOk ? 0 : 1;
    }

    if (sub === 'secret-scan') {
      const cwdArg = resolveCwd(args, err, 'gates secret-scan');
      if (!cwdArg.ok) return 2;
      const files = repeatedFlagValues(args, '--file');
      if (files.length === 0) {
        err('gates secret-scan: at least one --file <path> is required\n');
        return 2;
      }
      const hits = await scanFiles(cwdArg.value, files);
      out({ hits, count: hits.length });
      return hits.length === 0 ? 0 : 1;
    }

    if (sub === 'safe-edit') {
      const cwdArg = resolveCwd(args, err, 'gates safe-edit');
      if (!cwdArg.ok) return 2;
      const base = flagValue(args, '--base');
      if (!base) {
        err('gates safe-edit: --base <sha> is required\n');
        return 2;
      }
      const result = await checkSafeEdit({ cwd: cwdArg.value, base });
      out(result);
      return result.ok ? 0 : 1;
    }

    if (sub === 'scope') {
      const cwdArg = resolveCwd(args, err, 'gates scope');
      if (!cwdArg.ok) return 2;
      const base = flagValue(args, '--base');
      if (!base) {
        err('gates scope: --base <sha> is required\n');
        return 2;
      }
      const fileSet = await computeFileSet({ cwd: cwdArg.value, base });
      out(fileSet);
      return 0;
    }

    if (sub === 'acceptance') {
      const clausesPath = flagValue(args, '--clauses');
      const tapPath = flagValue(args, '--tap');
      if (!clausesPath || !tapPath) {
        err('gates acceptance: --clauses <file.json> and --tap <file> are required\n');
        return 2;
      }
      const clauses = JSON.parse(await readFile(clausesPath, 'utf8'));
      const tapOutput = await readFile(tapPath, 'utf8');
      const { passed, failed } = parseTapPassed(tapOutput);
      const result = checkAcceptance(clauses, passed, failed);
      out(result);
      return result.ok ? 0 : 1;
    }

    if (sub === 'transcript-grep') {
      const filePath = flagValue(args, '--file');
      if (!filePath) {
        err('gates transcript-grep: --file <path> is required\n');
        return 2;
      }
      // --cwd is OPTIONAL here (only used to pull in production.markers/names) — absent entirely
      // means "no config lookup", but given-with-no-value is still a usage error, not silently
      // treated the same as absent.
      const cwdFlag = readOptionalFlag(args, '--cwd');
      if (!cwdFlag.ok) {
        err('gates transcript-grep: --cwd requires a value\n');
        return 2;
      }
      const text = await readFile(filePath, 'utf8');
      const extraTokens = cwdFlag.value ? await resolveProductionMarkers(cwdFlag.value) : [];
      const result = checkTranscript(text, { extraTokens });
      out(result);
      return result.ok ? 0 : 1;
    }
  } catch (thrown) {
    err(`gates ${sub}: ${thrown?.message ?? String(thrown)}\n`);
    return 1;
  }

  usage();
  return 2;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function gates(args) {
  return runGatesVerb(args);
}
