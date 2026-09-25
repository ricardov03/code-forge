/**
 * `code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep` (plan
 * §2.2, §4.6, §8.6). Every subcommand prints one JSON object (redacted — `redactJSON`) to stdout
 * and returns a process exit code: `0` gate green/check passed, `1` gate red/check failed,
 * `2` usage error.
 *
 *   gates detect --cwd <dir>
 *   gates run --cwd <dir> [--slug <slug>] [--timeout-ms <n>] [--run <r> --block <id>]
 *     (with --run/--block the gates run behind the proof lock, `runGatesGuarded`: another block
 *     holding the lock ⇒ `{"result":"proof.busy","holder":…}` and exit 1)
 *   gates secret-scan --cwd <dir> --file <path> [--file <path> ...]
 *   gates safe-edit --cwd <dir> --base <sha>
 *   gates scope --cwd <dir> --base <sha>
 *   gates acceptance --clauses <file.json> --tap <file>
 *   gates transcript-grep --file <path>
 */

import { readFile } from 'node:fs/promises';
import { writeSafe, redactJSON } from '../util/redact.mjs';
import { appendRow } from '../ledger/write.mjs';
import { loadProjectConfig } from '../config/load.mjs';
import { detectGates } from '../gates/detect.mjs';
import { runGates } from '../gates/run.mjs';
import { runGatesGuarded } from '../proof/lock.mjs';
import { scanFiles } from '../gates/secret-scan.mjs';
import { checkSafeEdit } from '../gates/safe-edit.mjs';
import { computeFileSet } from '../gates/scope.mjs';
import { checkAcceptance, parseTapPassed } from '../gates/acceptance.mjs';
import { checkTranscript } from '../gates/transcript-grep.mjs';

/**
 * Auto-detected gates (`detectGates`), overridden per-key by an on-disk `.code-forge.yml`'s
 * `gates.{test,lint,types,format}` when that key is explicitly present — a project that already
 * confirmed/edited its gates at `init` step 7 (plan §2 step 7) is authoritative over the table.
 * A missing/unparseable config file is not an error here: detection alone is a valid answer.
 * @param {string} cwd
 * @returns {Promise<import('../gates/detect.mjs').DetectedGates>}
 */
async function resolveGates(cwd) {
  const detected = detectGates(cwd);
  const loaded = await loadProjectConfig(cwd);
  const configured = loaded.ok && loaded.config?.gates && typeof loaded.config.gates === 'object' ? loaded.config.gates : null;
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
 * @returns {{ok: true, value: string} | {ok: false}}
 */
function resolveCwd(args, err, label) {
  const flag = readOptionalFlag(args, '--cwd');
  if (!flag.ok) {
    err(`${label}: --cwd requires a value\n`);
    return { ok: false };
  }
  return { ok: true, value: flag.value ?? process.cwd() };
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
 * @param {string[]} args
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @returns {Promise<number>}
 */
export async function runGatesVerb(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr } = deps;
  const out = (/** @type {object} */ value) => writeSafe(stdout, `${redactJSON(value)}\n`);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const usage = () =>
    err('usage: code-forge gates detect|run|secret-scan|safe-edit|scope|acceptance|transcript-grep --cwd <dir> [...]\n');

  const [sub] = args;

  try {
    if (sub === 'detect') {
      const cwdArg = resolveCwd(args, err, 'gates detect');
      if (!cwdArg.ok) return 2;
      out(await resolveGates(cwdArg.value));
      return 0;
    }

    if (sub === 'run') {
      const cwdArg = resolveCwd(args, err, 'gates run');
      if (!cwdArg.ok) return 2;
      const cwd = cwdArg.value;

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

      const gates = await resolveGates(cwd);
      let ran;
      if (runFlag.value !== undefined && blockFlag.value !== undefined) {
        const guarded = await runGatesGuarded({ runId: runFlag.value, blockId: blockFlag.value, gates, cwd, ...(timeoutMs ? { timeoutMs } : {}) });
        if (guarded.result === 'proof.busy') {
          out({ result: 'proof.busy', holder: /** @type {{holder: string}} */ (guarded).holder });
          return 1;
        }
        ran = /** @type {{value: {results: any[], allOk: boolean}}} */ (guarded).value;
      } else {
        ran = await runGates(gates, { cwd, ...(timeoutMs ? { timeoutMs } : {}) });
      }
      const { results, allOk } = ran;

      if (slug) {
        for (const result of results) {
          if (!result.skipped && !result.ok) {
            await appendRow({ event: 'gate.red', gate: result.gate, code: result.code }, { slug });
          }
        }
        await appendRow({ event: 'gate.done', stack: gates.stack, all_ok: allOk }, { slug });
      }
      out({ stack: gates.stack, results, allOk });
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
