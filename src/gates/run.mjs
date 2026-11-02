/**
 * The gate runner (plan §4.6 row 3: "all gates green in one run — each exit status read"). Runs
 * every configured gate command (`test`, `lint`, `types`, `format`) INDEPENDENTLY — never chained
 * with `&&`, so a red gate never hides the ones after it: "a red gate in the middle of 3 is
 * reported red" means each of the three still gets its own read exit status, not that the run
 * stops early.
 */

import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { lstat, open, readdir, realpath } from 'node:fs/promises';
import path from 'node:path';
import { customGateMessages } from '../config/validate.mjs';
import { exec } from '../util/exec.mjs';
import { registerSecret } from '../util/redact.mjs';
import { CHANGED_FILES_TOKEN } from '../util/gate-token.mjs';
import { NO_BLOCK_SCOPE, TEST_FILES_TOKEN, changedTestFiles, envOf, expandArgv, maskableEnvValues, scopeFields, testsForRunner } from './expand.mjs';

/** @typedef {import('./detect.mjs').Gate} Gate */
/** @typedef {import('./detect.mjs').GateCommand} GateCommand */
/** @typedef {import('./detect.mjs').DetectedGates} DetectedGates */
/** @typedef {import('./expand.mjs').TestScope} TestScope */
/** @typedef {{n: number, files: string[], failed: boolean}} Repeats */
/** @typedef {{name: string, argv: string[], env?: Record<string, string>, evidence?: string[]}} CustomGate */
/** @typedef {{path: string, bytes: number, sha256: string}} Evidence */
/** @typedef {{kind: string, names: string[]}} KindGates */
/**
 * Internal block context carried with the gate plan through the proof lock, so repeats run
 * inside the same slot as the initial commands.
 * @typedef {{n: number, base: string, owned: string[], kinds?: Record<string, string>, onSkip?: (line: string) => void}} RepeatNewTests
 */

/**
 * @typedef {object} GateResult
 * @property {string} gate
 * @property {number} duration_ms - wall time including repeats; zero for skipped gates.
 * @property {true} [custom]
 * @property {Evidence[]} [evidence]
 * @property {string[]} [missing]
 * @property {string[] | null} argv
 * @property {boolean} skipped - true when unconfigured or excluded by the block's kind set.
 * @property {boolean} ok - true when skipped, or when the command exited with code 0 (and, for a
 *   `failOnStdout` gate, its stdout was empty).
 * @property {number | null} code
 * @property {true} [timed_out] - the command exceeded its gate deadline, not a test failure.
 * @property {string} stdout
 * @property {string} stderr
 * @property {string} [message] - termination diagnostic with the last lines of both streams.
 * @property {'related'|'full'|'changed'} [scope] - only on a gate with a scope token.
 * @property {number} [test_count] - B60: with `scope: related`, how many test files it ran.
 * @property {'threshold'|'no_tests'|'runner-filter'|'no_baseline'|'unpaired'|'no_changes'|'flaky-test'|'timeout'|'prompt'|'spawn-error'|'terminated'|NodeJS.Signals|`not in gates.kinds.${string}`} [reason] - fallback, exclusion or gate failure.
 * @property {Repeats} [repeats]
 * @property {string[]} [env_keys] - B60: only on a gate given with a non-empty `env`: the variable
 *   NAMES, sorted (the values are never in a result).
 */

/** Fixed run order — always all four, regardless of an earlier one's result. */
const GATE_ORDER = /** @type {const} */ (['test', 'lint', 'types', 'format']);

/** Leave room below typical ARG_MAX limits for the inherited environment and spawn overhead. */
const FORMAT_ARGV_BUDGET_BYTES = 100 * 1024;

/** Repeat the executable and flags in every batch; count UTF-8 bytes and NUL terminators.
 * @param {string[]} prefix @param {string[]} files @param {number} budget
 * @returns {string[][]}
 */
function formatBatches(prefix, files, budget) {
  if (!Number.isSafeInteger(budget) || budget <= 0) throw new TypeError('format: argv byte budget must be a positive safe integer');
  const prefixBytes = prefix.reduce((sum, arg) => sum + Buffer.byteLength(arg) + 1, 0);
  const batches = [];
  let batch = prefix.slice();
  let bytes = prefixBytes;
  for (const file of files) {
    const size = Buffer.byteLength(file) + 1;
    if (prefixBytes + size > budget) throw new RangeError(`format: command and file exceed argv byte budget: ${file}`);
    if (bytes + size > budget) {
      batches.push(batch);
      batch = prefix.slice();
      bytes = prefixBytes;
    }
    batch.push(file);
    bytes += size;
  }
  batches.push(batch);
  return batches;
}

/** @param {string} stream @returns {string} */
function outputTail(stream) {
  const tail = stream.trimEnd().split(/\r?\n/).slice(-10).join('\n');
  return tail ? `${tail}\n` : '';
}

/** Existing supported files only, never a directory or glob for the formatter to traverse.
 * @param {string} cwd @param {'pint'|'prettier'} formatter
 * @param {TestScope | import('./expand.mjs').ChangedScope} scope
 * @returns {Promise<string[]>}
 */
async function formatFiles(cwd, formatter, scope) {
  const changed = scope.scope === 'changed';
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GIT_')));
  const opts = { cwd, env, timeoutMs: 30000 };
  const listed = await exec(['git', 'ls-files', '-z', '--cached', ...(changed ? ['--others'] : []), '--exclude-standard'], opts);
  if (listed.result !== 'ok') throw new Error(`format: cannot list Git files: ${listed.stderr || listed.error || listed.signal || 'git failed'}`);
  const wanted = changed ? new Set(scope.changedFiles) : null;
  const files = [...new Set(listed.stdout.split('\0').filter((file) => file && (!wanted || wanted.has(file))))].sort();
  if (files.length === 0) return [];
  // --no-index also excludes force-added ignored paths. A cached path alone is not proof
  // that the current ignore rules permit it.
  const ignored = await exec(['git', 'check-ignore', '--no-index', '-z', '--stdin'], {
    ...opts, input: `${files.join('\0')}\0`, okExitCodes: [0, 1],
  });
  if (ignored.result !== 'ok') throw new Error(`format: cannot check Git ignores: ${ignored.stderr || ignored.error || ignored.signal || 'git failed'}`);
  const excluded = new Set(ignored.stdout.split('\0'));
  const root = await realpath(cwd);
  const selected = [];
  for (const file of files) {
    if (excluded.has(file)) continue;
    const supported = formatter === 'pint' ? /\.php$/i.test(file)
      : /\.(?:[cm]?[jt]sx?|json5?|jsonc|css|scss|less|html?|vue|md|mdx|markdown|ya?ml|graphql|gql|hbs|handlebars|mustache|mjml)$/i.test(file)
        || ['.prettierrc', '.babelrc', '.eslintrc', '.stylelintrc'].includes(path.basename(file));
    if (!supported) continue;
    try {
      const absolute = path.resolve(root, file);
      if (!(await lstat(absolute)).isFile() || await realpath(absolute) !== absolute) continue;
    } catch (error) {
      if (['ENOENT', 'ENOTDIR'].includes(error.code)) continue;
      throw error;
    }
    // Leading dashes cannot become options, and existing paths are not treated as globs.
    selected.push(`./${file}`);
  }
  return selected;
}

/** Unanswered prompt suffixes; inspect only each stream's last non-empty line. */
export const UNANSWERED_PROMPT_PATTERN = /(?:\?|\(y(?:\/n)?\)|\[y\/n\]|yes\/no)\s*$/i;

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

/** Missing, inaccessible or replaced entries are not evidence.
 * @param {NodeJS.ErrnoException} error @returns {boolean}
 */
function evidenceUnavailable(error) {
  return ['ENOENT', 'ENOTDIR', 'EACCES', 'ELOOP'].includes(error.code);
}

/** Never traverse symlinks. Hash at most 200 unique files, without retaining their content.
 * @param {string} cwd @param {string[]} patterns
 * @returns {Promise<{evidence: Evidence[], missing?: string[]}>}
 */
async function collectEvidence(cwd, patterns) {
  if (patterns.length === 0) return { evidence: [] };
  let root;
  try { root = await realpath(cwd); }
  catch (error) {
    if (evidenceUnavailable(error)) return { evidence: [], missing: patterns };
    throw error;
  }
  const matchers = patterns.map(evidenceMatcher);
  /** @type {Set<string>} */
  const matched = new Set();
  /** @param {string} dir @returns {Promise<void>} */
  async function walk(dir) {
    let entries;
    try { entries = await readdir(path.join(root, dir), { withFileTypes: true }); }
    catch (error) {
      if (evidenceUnavailable(error)) return;
      throw error;
    }
    for (const entry of entries) {
      const file = dir ? `${dir}/${entry.name}` : entry.name;
      if (entry.isSymbolicLink()) continue;
      // Also check the resolved path before descending/opening, in case a parent changed.
      let resolved;
      try { resolved = await realpath(path.join(root, file)); }
      catch (error) { if (evidenceUnavailable(error)) continue; throw error; }
      const relative = path.relative(root, resolved);
      if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) continue;
      if (entry.isDirectory()) await walk(file);
      else if (entry.isFile()) {
        if (matchers.some((matcher) => matcher.test(file))) matched.add(file);
      }
    }
  }
  await walk('');
  /** @type {Evidence[]} */
  const evidence = [];
  for (const file of [...matched].sort().slice(0, 200)) {
    try {
      // Recheck parents at the time of hashing, rather than relying on the earlier walk.
      const resolved = await realpath(path.join(root, file));
      const relative = path.relative(root, resolved);
      if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) { matched.delete(file); continue; }
      const handle = await open(resolved, constants.O_RDONLY | constants.O_NOFOLLOW);
      try {
        if (!(await handle.stat()).isFile()) { matched.delete(file); continue; }
        const hash = createHash('sha256');
        let bytes = 0;
        for await (const chunk of handle.createReadStream({ autoClose: false })) {
          bytes += chunk.length;
          hash.update(chunk);
        }
        evidence.push({ path: file, bytes, sha256: hash.digest('hex') });
      } finally { await handle.close(); }
    } catch (error) {
      if (!evidenceUnavailable(error)) throw error;
      matched.delete(file);
    }
  }
  const missing = patterns.filter((_, i) => ![...matched].some((file) => matchers[i].test(file)));
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

/** Node reports cancelled tests as failures after a timeout. Keep their diagnostics, but
 * remove the failure markers consumed by TAP lists and shown by the spec reporter.
 * @param {string} output @returns {string}
 */
function unfinishedOutput(output) {
  const lines = output.split('\n');
  /** @type {Map<number, {indent: number, tap: boolean, diagnostic: string}>} */
  const failures = new Map();
  for (let i = 0; i < lines.length; i++) {
    const tap = /^([ \t]*)not ok\s+\d+\s+- /.exec(lines[i]);
    const spec = /^([ \t]*)✖ /.exec(lines[i]);
    if (!tap && !spec) continue;
    let diagnostic = lines[i];
    const indent = (tap ?? spec)[1].length;
    for (let j = i + 1; j < lines.length; j++) {
      if (lines[j].trim() && lines[j].search(/\S/) <= indent) break;
      if (/^\s*(?:not ok\s+\d+\s+- |✖ |# Subtest: |▶ )/.test(lines[j])) break;
      diagnostic += `\n${lines[j]}`;
      if (tap && lines[j].trim() === '...') break;
    }
    failures.set(i, { indent, tap: !!tap, diagnostic });
  }
  /** @type {Set<number>} */
  const unfinished = new Set();
  // Reporters normally close a suite after its children; also accept a parent-first list.
  // Visit deeper entries first so unfinished status propagates through every ancestor.
  for (const [i, { indent, diagnostic }] of [...failures].sort((a, b) => b[1].indent - a[1].indent)) {
    const children = [];
    for (const direction of [-1, 1]) {
      for (let j = i + direction; j >= 0 && j < lines.length; j += direction) {
        if (lines[j].trim() && lines[j].search(/\S/) <= indent) break;
        if (failures.has(j)) children.push(j);
      }
    }
    const pending = diagnostic.includes('Promise resolution is still pending but the event loop has already resolved');
    const parent = /subtestsFailed|\b\d+ subtests? failed\b/.test(diagnostic) || diagnostic.trimEnd() === lines[i].trimEnd();
    const allUnfinished = children.length > 0 && children.every((j) => unfinished.has(j));
    if (children.length ? !allUnfinished || !(pending || parent) : !pending) continue;
    unfinished.add(i);
  }
  for (const i of unfinished) {
    lines[i] = failures.get(i).tap ? lines[i].replace('not ok', '# unfinished') : lines[i].replace('✖', 'ℹ unfinished:');
  }
  return lines.join('\n');
}

/** Interpret timeouts and unanswered prompts for every gate, including repeats.
 * @param {import('../util/exec.mjs').ExecResult} res
 * @param {number} timeoutMs
 * @param {boolean} [failOnStdout]
 * @returns {Pick<GateResult, 'ok'|'code'|'stdout'|'stderr'|'reason'|'message'|'timed_out'>}
 */
function gateOutcome(res, timeoutMs, failOnStdout = false) {
  const prompt = res.result === 'ok' && [res.stdout, res.stderr].some((stream) =>
    UNANSWERED_PROMPT_PATTERN.test(stream.trimEnd().split(/\r?\n/).at(-1)));
  const reason = res.timedOut ? 'timeout'
    : res.code === null ? res.signal ?? (res.error ? 'spawn-error' : 'terminated')
    : prompt ? 'prompt' : undefined;
  const diagnostic = res.timedOut
    ? `gate timeout after ${timeoutMs / 1000} seconds; raise gates.timeout_s (or --timeout-ms)`
    : res.code === null ? `gate terminated: ${res.signal ?? res.error ?? 'no exit status (no signal reported)'}`
    : prompt ? 'gate ended on an interactive prompt; install the tool or make the command non-interactive' : '';
  const stdout = res.timedOut ? unfinishedOutput(res.stdout) : res.stdout;
  const stderr = res.timedOut ? unfinishedOutput(res.stderr) : res.stderr;
  const tails = [stdout, stderr].flatMap((output, index) => {
    const stream = index === 0 ? 'stdout' : 'stderr';
    const tail = output.trimEnd().split(/\r?\n/).slice(-10).join('\n');
    return tail ? [`${stream} tail:\n${tail}`] : [];
  });
  const message = [res.timedOut
    ? `TIMEOUT after ${timeoutMs / 1000} s (not a test failure); rerun with --timeout-ms ${timeoutMs * 2} or raise gates.timeout_s`
    : diagnostic, ...tails].join('\n');
  return {
    ok: res.result === 'ok' && !reason && (!failOnStdout || res.stdout.trim().length === 0),
    code: res.code,
    stdout,
    stderr: diagnostic
      ? `${stderr}${stderr && !stderr.endsWith('\n') ? '\n' : ''}${diagnostic}\n`
      : stderr,
    ...(res.timedOut ? { timed_out: /** @type {const} */ (true) } : {}),
    ...(reason ? { reason } : {}),
    ...(diagnostic ? { message } : {}),
  };
}

/**
 * B60: `opts.scope` is the block's {@link TestScope} (`testScope`, gates/expand.mjs); a gate argv
 * item that is exactly `{test_files}` expands to its files, or is dropped for a full run. Left out,
 * no test file is known (`scope: full, reason: no_tests`). A gate with `env` runs with those
 * variables added to the inherited environment, overriding the CI=1/npm_config_yes=false defaults.
 * @param {{test?: Gate, lint?: Gate, types?: Gate, format?: Gate, custom?: CustomGate[], repeatNewTests?: RepeatNewTests, lightTestFiles?: string[], kindGates?: KindGates}} gates
 * @param {{cwd: string, timeoutMs?: number, signal?: AbortSignal, scope?: TestScope | import('./expand.mjs').ChangedScope, formatArgvBudgetBytes?: number}} opts - formatArgvBudgetBytes overrides the detected formatter batch budget (default 100 KiB).
 * @returns {Promise<{results: GateResult[], allOk: boolean, duration_ms: number, repeats?: Repeats}>}
 */
export async function runGates(gates, opts) {
  if (!gates || typeof gates !== 'object') {
    throw new TypeError('runGates: gates must be an object with test/lint/types/format');
  }
  const { cwd, timeoutMs = 300000, signal } = opts ?? {};
  const scope = gates.lightTestFiles ? { scope: /** @type {const} */ ('related'), files: gates.lightTestFiles } : opts?.scope ?? NO_BLOCK_SCOPE;
  if (typeof cwd !== 'string' || cwd.length === 0) {
    throw new TypeError('runGates: opts.cwd must be a non-empty string');
  }
  const customErrors = customGateMessages(gates.custom);
  if (customErrors.length > 0) throw new TypeError(customErrors.join('; '));
  const kindSet = gates.lightTestFiles ? undefined : gates.kindGates;
  const excluded = (/** @type {string} */ name) => kindSet && !kindSet.names.includes(name);
  const skipReason = () => /** @type {const} */ (`not in gates.kinds.${kindSet.kind}`);

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
  const planned = [];
  for (const gate of GATE_ORDER) {
    const command = gates[gate] ?? null;
    if (command === null || excluded(gate)) { planned.push({ gate, command: null }); continue; }
    const normalized = normalizeCommand(command);
    if (normalized.argv[0] === CHANGED_FILES_TOKEN) throw new TypeError(`gates.${gate}: '{changed_files}' cannot be the command (argv[0])`);
    let { argv, scoped, full, reason } = expandArgv(normalized.argv, scope);
    const commandScope = full ? { scope: /** @type {const} */ ('full'), files: [], reason }
      : scope.scope !== 'full' && normalized.argv.includes(TEST_FILES_TOKEN)
        ? { ...scope, files: testsForRunner(normalized.argv, scope.files) } : scope;
    if (argv.length === 0 && normalized.argv.length > 0) throw new TypeError(`gates.${gate}: the argv is empty once ${normalized.argv.map((item) => `'${item}'`).join(' and ')} is dropped for the full suite — put the test command before it`);
    const formatter = gate === 'format' && !Array.isArray(command) ? command.formatter : undefined;
    let batches;
    if (formatter) {
      const files = await formatFiles(cwd, formatter, scope);
      if (files.length === 0) { planned.push({ gate, command: null, noFiles: true }); continue; }
      batches = formatBatches(argv, files, opts.formatArgvBudgetBytes ?? FORMAT_ARGV_BUDGET_BYTES);
      argv = argv.concat(files);
    }
    planned.push({ gate, command: { argv, batches, originalArgv: normalized.argv, scoped, full, scope: commandScope, failOnStdout: normalized.failOnStdout, env: normalized.env } });
  }
  /** @type {GateResult[]} */
  const results = [];
  for (const { gate, command, noFiles } of planned) {
    if (signal?.aborted) break;
    if (command === null) {
      results.push({ gate, duration_ms: 0, argv: null, skipped: true, ok: true, code: null, stdout: '', stderr: '',
        ...(gates[gate] != null && excluded(gate) ? { reason: skipReason() } : {}),
        ...(noFiles ? { reason: 'no_changes' } : {}),
      });
      continue;
    }
    const { argv, batches = [argv], scoped, failOnStdout, env } = command;
    // Each command is run and its exit status read on its own — never `&&`-chained with the
    // next, so a red gate is reported red without swallowing the gates that follow it.
    const execOpts = { cwd, timeoutMs, signal, env: { ...process.env, CI: '1', npm_config_yes: 'false', ...env } };
    const started = performance.now();
    const outcomes = [];
    for (const batch of batches) {
      const res = await exec(batch, execOpts);
      const batchOutcome = gateOutcome(res, timeoutMs, failOnStdout);
      outcomes.push({ ...batchOutcome, ...(batches.length > 1 ? {
        stdout: outputTail(batchOutcome.stdout), stderr: outputTail(batchOutcome.stderr),
      } : {}) });
      if (signal?.aborted) break;
    }
    // Retain the first failure's status and diagnostic even if later batches pass.
    const outcome = {
      ...(outcomes.find((entry) => entry.timed_out) ?? outcomes.find((entry) => !entry.ok) ?? outcomes[0]),
      stdout: outcomes.map((entry) => entry.stdout).join(''),
      stderr: outcomes.map((entry) => entry.stderr).join(''),
    };
    const result = /** @type {GateResult} */ ({
      gate,
      duration_ms: 0,
      argv,
      skipped: false,
      ...(scoped ? scopeFields(command.scope) : {}),
      ...outcome,
      ...(env ? { env_keys: Object.keys(env).sort() } : {}),
    });
    const repeat = gates.repeatNewTests;
    if (gate === 'test' && outcome.ok && repeat && !signal?.aborted) {
      if (!command.originalArgv.includes(TEST_FILES_TOKEN)) {
        repeat.onSkip?.('repeat_new_tests: skipped (gates.test has no {test_files})');
      } else if (command.full) {
        repeat.onSkip?.('repeat_new_tests: skipped (no changed tests for this runner)');
      } else {
        const changedFiles = (await changedTestFiles({ cwd, ...repeat })).filter((file) => !gates.lightTestFiles || gates.lightTestFiles.includes(file));
        const files = testsForRunner(command.originalArgv, changedFiles);
        if (changedFiles.length > 0 && files.length === 0) {
          repeat.onSkip?.('repeat_new_tests: skipped (no changed tests for this runner)');
        }
        if (files.length > 0) {
          result.repeats = { n: repeat.n, files, failed: false };
          const repeatArgv = expandArgv(command.originalArgv, { scope: 'related', files }).argv;
          for (let i = 0; i < repeat.n; i++) {
            if (signal?.aborted) break;
            const repeated = await exec(repeatArgv, execOpts);
            const repeatedOutcome = gateOutcome(repeated, timeoutMs, failOnStdout);
            // Keep the first failed run's output, even when a later repeat passes.
            if (!repeatedOutcome.ok && !result.repeats.failed) {
              result.repeats.failed = true;
              Object.assign(result, { reason: 'flaky-test', argv: repeatArgv, ...repeatedOutcome });
            }
          }
        }
      }
    }
    result.duration_ms = Math.round(performance.now() - started);
    results.push(result);
  }
  for (const gate of gates.custom ?? []) {
    if (signal?.aborted) break;
    if (excluded(gate.name)) {
      results.push({ gate: gate.name, duration_ms: 0, custom: true, argv: null, skipped: true, ok: true, code: null,
        stdout: '', stderr: '', reason: skipReason(), evidence: [],
      });
      continue;
    }
    const { argv, env } = normalizeCommand(gate);
    const started = performance.now();
    const res = await exec(argv, { cwd, timeoutMs, signal, env: { ...process.env, CI: '1', npm_config_yes: 'false', ...env } });
    const evidence = await collectEvidence(cwd, gate.evidence ?? []);
    results.push({ gate: gate.name, custom: true, argv, skipped: false,
      duration_ms: Math.round(performance.now() - started),
      ...gateOutcome(res, timeoutMs),
      ...(env ? { env_keys: Object.keys(env).sort() } : {}),
      ...evidence,
    });
  }
  const repeats = results.find((r) => r.repeats)?.repeats;
  return { results, allOk: !signal?.aborted && results.every((r) => r.ok), duration_ms: results.reduce((sum, r) => sum + r.duration_ms, 0), ...(repeats ? { repeats } : {}) };
}
