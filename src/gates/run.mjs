/**
 * The gate runner (plan §4.6 row 3: "all gates green in one run — each exit status read"). Runs
 * every configured gate command (`test`, `lint`, `types`, `format`) INDEPENDENTLY — never chained
 * with `&&`, so a red gate never hides the ones after it: "a red gate in the middle of 3 is
 * reported red" means each of the three still gets its own read exit status, not that the run
 * stops early.
 */

import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { open, readdir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { customGateMessages } from '../config/validate.mjs';
import { exec } from '../util/exec.mjs';
import { registerSecret } from '../util/redact.mjs';
import { CHANGED_FILES_TOKEN } from '../util/gate-token.mjs';
import { NO_BLOCK_SCOPE, TEST_FILES_TOKEN, changedTestFiles, envOf, expandArgv, maskableEnvValues, scopeFields } from './expand.mjs';

/** @typedef {import('./detect.mjs').Gate} Gate */
/** @typedef {import('./detect.mjs').GateCommand} GateCommand */
/** @typedef {import('./detect.mjs').DetectedGates} DetectedGates */
/** @typedef {import('./expand.mjs').TestScope} TestScope */
/** @typedef {{n: number, files: string[], failed: boolean}} Repeats */
/** @typedef {{name: string, argv: string[], env?: Record<string, string>, evidence?: string[]}} CustomGate */
/** @typedef {{path: string, bytes: number, sha256: string}} Evidence */
/**
 * Internal block context carried with the gate plan through the proof lock, so repeats run
 * inside the same slot as the initial commands.
 * @typedef {{n: number, base: string, owned: string[], kinds?: Record<string, string>, onSkip?: (line: string) => void}} RepeatNewTests
 */

/**
 * @typedef {object} GateResult
 * @property {string} gate
 * @property {true} [custom]
 * @property {Evidence[]} [evidence]
 * @property {string[]} [missing]
 * @property {string[] | null} argv
 * @property {boolean} skipped - true when the gate has no configured command (argv was null).
 * @property {boolean} ok - true when skipped, or when the command exited with code 0 (and, for a
 *   `failOnStdout` gate, its stdout was empty).
 * @property {number | null} code
 * @property {string} stdout
 * @property {string} stderr
 * @property {'related'|'full'|'changed'} [scope] - only on a gate with a scope token.
 * @property {number} [test_count] - B60: with `scope: related`, how many test files it ran.
 * @property {'threshold'|'no_tests'|'no_baseline'|'unpaired'|'no_changes'|'flaky-test'} [reason] - fallback or repeat failure.
 * @property {Repeats} [repeats]
 * @property {string[]} [env_keys] - B60: only on a gate given with a non-empty `env`: the variable
 *   NAMES, sorted (the values are never in a result).
 */

/** Fixed run order — always all four, regardless of an earlier one's result. */
const GATE_ORDER = /** @type {const} */ (['test', 'lint', 'types', 'format']);

/** Simple globs only: * stays in one segment; ** can cross segments.
 * @param {string} pattern @returns {RegExp}
 */
function evidenceMatcher(pattern) {
  const normalized = pattern.split('/').filter((part) => part !== '.' && part !== '').join('/');
  let source = '';
  for (let i = 0; i < normalized.length; i++) {
    const char = normalized[i];
    if (char === '*' && normalized[i + 1] === '*') {
      i++;
      if (normalized[i + 1] === '/') { source += '(?:.*/)?'; i++; }
      else source += '.*';
    } else if (char === '*') source += '[^/]*';
    else source += char.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }
  return new RegExp(`^${source}$`);
}

/** Never traverse symlinks. Hash at most 200 unique files, without retaining their content.
 * @param {string} cwd @param {string[]} patterns
 * @returns {Promise<{evidence: Evidence[], missing?: string[]}>}
 */
async function collectEvidence(cwd, patterns) {
  if (patterns.length === 0) return { evidence: [] };
  const root = await realpath(cwd);
  const matchers = patterns.map(evidenceMatcher);
  const matched = new Set();
  /** @type {string[]} */
  const files = [];
  /** @param {string} dir @returns {Promise<void>} */
  async function walk(dir) {
    for (const entry of await readdir(path.join(root, dir), { withFileTypes: true })) {
      const file = dir ? `${dir}/${entry.name}` : entry.name;
      if (entry.isSymbolicLink()) continue;
      // Also check the resolved path before descending/opening, in case a parent changed.
      let resolved;
      try { resolved = await realpath(path.join(root, file)); }
      catch (error) { if (error.code === 'ENOENT') continue; throw error; }
      const relative = path.relative(root, resolved);
      if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) continue;
      if (entry.isDirectory()) await walk(file);
      else if (entry.isFile()) {
        const hits = matchers.flatMap((matcher, i) => matcher.test(file) ? [i] : []);
        if (hits.length > 0) {
          files.push(file);
          for (const i of hits) matched.add(i);
        }
      }
    }
  }
  await walk('');
  /** @type {Evidence[]} */
  const evidence = [];
  for (const file of files.sort().slice(0, 200)) {
    const handle = await open(path.join(root, file), constants.O_RDONLY | constants.O_NOFOLLOW);
    try {
      if (!(await handle.stat()).isFile()) continue;
      const hash = createHash('sha256');
      let bytes = 0;
      for await (const chunk of handle.createReadStream({ autoClose: false })) {
        bytes += chunk.length;
        hash.update(chunk);
      }
      evidence.push({ path: file, bytes, sha256: hash.digest('hex') });
    } finally { await handle.close(); }
  }
  const missing = patterns.filter((_, i) => !matched.has(i));
  return { evidence, ...(missing.length ? { missing } : {}) };
}

/**
 * A gate command is either a plain argv array or `{argv, failOnStdout?, env?}` (detect.mjs's
 * `GateCommand`: `failOnStdout` for `gofmt -l`, which always exits 0; B60 `env` from the config).
 * Normalized here so the run loop below has one shape to deal with.
 * @param {GateCommand} command
 * @returns {{argv: string[], failOnStdout: boolean, env: Record<string, string> | null}}
 */
function normalizeCommand(command) {
  if (Array.isArray(command)) return { argv: command, failOnStdout: false, env: null };
  return { argv: command.argv, failOnStdout: command.failOnStdout === true, env: envOf(command) }; // `env: {}` is no env
}

/**
 * B60: `opts.scope` is the block's {@link TestScope} (`testScope`, gates/expand.mjs); a gate argv
 * item that is exactly `{test_files}` expands to its files, or is dropped for a full run. Left out,
 * no test file is known (`scope: full, reason: no_tests`). A gate with `env` runs with those
 * variables added to the inherited environment; a gate with neither runs exactly as before B60.
 * @param {{test?: Gate, lint?: Gate, types?: Gate, format?: Gate, custom?: CustomGate[], repeatNewTests?: RepeatNewTests, lightTestFiles?: string[]}} gates
 * @param {{cwd: string, timeoutMs?: number, scope?: TestScope | import('./expand.mjs').ChangedScope}} opts
 * @returns {Promise<{results: GateResult[], allOk: boolean, repeats?: Repeats}>}
 */
export async function runGates(gates, opts) {
  if (!gates || typeof gates !== 'object') {
    throw new TypeError('runGates: gates must be an object with test/lint/types/format');
  }
  const { cwd, timeoutMs = 300000 } = opts ?? {};
  const scope = gates.lightTestFiles ? { scope: /** @type {const} */ ('related'), files: gates.lightTestFiles } : opts?.scope ?? NO_BLOCK_SCOPE;
  if (typeof cwd !== 'string' || cwd.length === 0) {
    throw new TypeError('runGates: opts.cwd must be a non-empty string');
  }
  const customErrors = customGateMessages(gates.custom);
  if (customErrors.length > 0) throw new TypeError(customErrors.join('; '));

  // B60: gate env is not a secret store. Its values are never put in a result (only `env_keys`);
  // a value of 8+ characters is also masked in the child's own output (a shorter one would mask
  // unrelated text — `MODE=test` vs `npm test`)
  for (const value of maskableEnvValues(gates)) registerSecret(value);
  for (const gate of gates.custom ?? []) {
    for (const value of maskableEnvValues({ gate })) registerSecret(value);
  }
  // Every gate is normalized and expanded BEFORE any runs, so a bad gate throws with nothing run.
  // B60: config validation refuses a gate whose only item is {test_files}; the empty-argv check
  // is the guard for one that reaches here anyway: it names the gate, not an opaque spawn error.
  const planned = GATE_ORDER.map((gate) => {
    const command = gates[gate] ?? null;
    if (command === null) return { gate, command: null };
    const normalized = normalizeCommand(command);
    if (normalized.argv[0] === CHANGED_FILES_TOKEN) throw new TypeError(`gates.${gate}: '{changed_files}' cannot be the command (argv[0])`);
    const { argv, scoped } = expandArgv(normalized.argv, scope);
    if (argv.length === 0 && normalized.argv.length > 0) throw new TypeError(`gates.${gate}: the argv is empty once ${normalized.argv.map((item) => `'${item}'`).join(' and ')} is dropped for the full suite — put the test command before it`);
    return { gate, command: { argv, originalArgv: normalized.argv, scoped, failOnStdout: normalized.failOnStdout, env: normalized.env } };
  });
  /** @type {GateResult[]} */
  const results = [];
  for (const { gate, command } of planned) {
    if (command === null) {
      results.push({ gate, argv: null, skipped: true, ok: true, code: null, stdout: '', stderr: '' });
      continue;
    }
    const { argv, scoped, failOnStdout, env } = command;
    // Each command is run and its exit status read on its own — never `&&`-chained with the
    // next, so a red gate is reported red without swallowing the gates that follow it.
    const res = await exec(argv, env ? { cwd, timeoutMs, env: { ...process.env, ...env } } : { cwd, timeoutMs });
    const ok = res.result === 'ok' && (!failOnStdout || res.stdout.trim().length === 0);
    const result = /** @type {GateResult} */ ({
      gate,
      argv,
      skipped: false,
      ok,
      code: res.code,
      stdout: res.stdout,
      stderr: res.stderr,
      ...(scoped ? scopeFields(scope) : {}),
      ...(env ? { env_keys: Object.keys(env).sort() } : {}),
    });
    const repeat = gates.repeatNewTests;
    if (gate === 'test' && ok && repeat) {
      if (!command.originalArgv.includes(TEST_FILES_TOKEN)) {
        repeat.onSkip?.('repeat_new_tests: skipped (gates.test has no {test_files})');
      } else {
        const files = (await changedTestFiles({ cwd, ...repeat })).filter((file) => !gates.lightTestFiles || gates.lightTestFiles.includes(file));
        if (files.length > 0) {
          result.repeats = { n: repeat.n, files, failed: false };
          const repeatArgv = expandArgv(command.originalArgv, { scope: 'related', files }).argv;
          for (let i = 0; i < repeat.n; i++) {
            const repeated = await exec(repeatArgv, env ? { cwd, timeoutMs, env: { ...process.env, ...env } } : { cwd, timeoutMs });
            const passed = repeated.result === 'ok' && (!failOnStdout || repeated.stdout.trim().length === 0);
            // Keep the first failed run's output, even when a later repeat passes.
            if (!passed && !result.repeats.failed) {
              result.repeats.failed = true;
              Object.assign(result, { ok: false, reason: 'flaky-test', argv: repeatArgv, code: repeated.code, stdout: repeated.stdout, stderr: repeated.stderr });
            }
          }
        }
      }
    }
    results.push(result);
  }
  for (const gate of gates.custom ?? []) {
    const { argv, env } = normalizeCommand(gate);
    const res = await exec(argv, env ? { cwd, timeoutMs, env: { ...process.env, ...env } } : { cwd, timeoutMs });
    results.push({ gate: gate.name, custom: true, argv, skipped: false,
      ok: res.result === 'ok', code: res.code, stdout: res.stdout, stderr: res.stderr,
      ...(env ? { env_keys: Object.keys(env).sort() } : {}),
      ...await collectEvidence(cwd, gate.evidence ?? []),
    });
  }
  const repeats = results.find((r) => r.repeats)?.repeats;
  return { results, allOk: results.every((r) => r.ok), ...(repeats ? { repeats } : {}) };
}
