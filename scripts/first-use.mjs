#!/usr/bin/env node
/**
 * The first real use of code-forge (plan v1.3 §10.4 B17, R13): one L1 block, "add `clamp()` with
 * tests", end to end on a COPY of the example repository shipped in this package
 * (`examples/node-lib/`). Never on this package's tree, never on a project of the user's: the copy
 * lives in `<tmp-root>/first-use/<run>/`, where `<tmp-root>` is `--tmp-root` or the default temp
 * base (`<os tmpdir>/code-forge`, the directory run roots live in). A given `--tmp-root` is also
 * written to the copy's `tmp.root`, so the worker's and the sessions' run roots sit beside the copy.
 *
 *   node scripts/first-use.mjs [--engine harness|subprocess] [--coder scripted|harness|spawn]
 *                              [--tmp-root <abs dir>] [--run <id>] [--until <step>]
 *
 * Steps (each prints `== <n>. <step> ==`):
 *    1 copy    copy the example, `git init`, one base commit, `.env.example` → `.env` (untracked)
 *    2 init    `code-forge init --no-interaction …` writes `.code-forge.yml` in the copy; the
 *              engine banner of `skill/references/degraded.md` §2 is printed once
 *    3 facts   `code-forge facts --brief plans/clamp.md` (the facts rule: before any author)
 *    4 author  `code-forge author --job plan --brief … --facts …` → `plans/clamp.plan.md`
 *    5 start   `code-forge run start` (launches the review worker)
 *    6 open    `code-forge block open B1 --level L1 --owned … --acceptance … --brief …`
 *    7 code    the coder: `scripted` applies `examples/first-use/coder/` (the automated test);
 *              `harness` waits for the harness subagent (Agent tool) to finish — it appends its
 *              sentinel line to `.code-forge/runs/<run>/B1.log`; `spawn` runs
 *              `code-forge spawn --role coder` (engine subprocess)
 *    8 review  `code-forge review-file <file>` + `review-file --wait` per owned file
 *    9 proof   `proof export` (lists the export-ignore paths it restored), the test gate inside the
 *              export, `proof red-green` per test the acceptance names, `proof export --remove`
 *   10 close   `code-forge block close B1` — then the cost of the completed block is printed
 *   11 report  `code-forge report --slug <slug>`
 * then `code-forge run end` (always, once the run started: it stops the worker).
 *
 * Engines: `harness` is the default (R2: the no-Solo default). `subprocess` is OPT-IN: it is
 * never chosen unless `--engine subprocess` is given, and then it is written to `.code-forge.yml`
 * as an explicit config edit (init never writes it, R2). Solo is manual evidence only: this script
 * refuses `--engine solo` — run the skill under Solo instead.
 *
 * Output: the step lines, then one final line `FIRST-USE {json}` (run, copy, slug, approvals,
 * proofs, sections, cost). Exit 0 when every step up to `--until` passed, 1 on a failed step,
 * 2 on usage. Every child is an argv array (never a shell string); git runs with every inherited
 * `GIT_*` stripped and no system/global config.
 */

import { randomBytes } from 'node:crypto';
import { appendFileSync, cpSync, existsSync, mkdirSync, readFileSync, realpathSync, renameSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { parse as parseYAML, parseDocument } from 'yaml';
import { readAllRows } from '../src/ledger/write.mjs';
import { estimateCostUsd } from '../src/ledger/prices.mjs';
import { exec } from '../src/util/exec.mjs';
import { runRoot, setRunRoot, tmpBase } from '../src/util/tmp.mjs';

const PKG = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const BIN = path.join(PKG, 'bin', 'code-forge.mjs');
export const EXAMPLE_DIR = path.join(PKG, 'examples', 'node-lib');
export const INPUTS_DIR = path.join(PKG, 'examples', 'first-use');
const DEGRADED_DOC = path.join(PKG, 'skill', 'references', 'degraded.md');

export const BLOCK = 'B1';
export const OWNED = Object.freeze(['src/math.mjs', 'test/clamp.test.mjs']);
export const STEPS = Object.freeze(['copy', 'init', 'facts', 'author', 'start', 'open', 'code', 'review', 'proof', 'close', 'report']);
const ENGINES = ['harness', 'subprocess'];
const CODERS = ['scripted', 'harness', 'spawn'];
const CLI_TIMEOUT_MS = 30 * 60 * 1000;
const GIT_TIMEOUT_MS = 60 * 1000;
/** How long `--coder harness` waits for the sentinel line by default (one hour). */
export const DEFAULT_CODER_TIMEOUT_S = 3600;
/** The example ships `gitignore` (npm pack drops nested `.gitignore` files); the copy renames it. */
const SHIPPED_GITIGNORE = 'gitignore';

export const USAGE = [
  'usage: node scripts/first-use.mjs [--engine harness|subprocess] [--coder scripted|harness|spawn]',
  '                                  [--tmp-root <abs dir>] [--run <id>] [--until <step>] [--coder-timeout <s>]',
  '  --engine    harness (default, R2) · subprocess (OPT-IN: never chosen unless given here; the',
  '              script writes `engine: subprocess` to the copy\'s .code-forge.yml, which init never',
  '              does) · Solo is manual evidence only: run the skill under Solo, not this script',
  '  --coder     scripted: the deterministic edit in examples/first-use/coder (the automated test)',
  '              harness:  wait for the harness subagent; it appends its ===BLOCK B1 COMPLETE===',
  '                        (or FAILED) line to .code-forge/runs/<run>/B1.log (default for harness)',
  '              spawn:    `code-forge spawn --role coder` (default for subprocess)',
  '  --tmp-root  where <tmp-root>/first-use/<run>/ is made (default: the code-forge temp base);',
  '              when given it is also written to the copy\'s tmp.root, so the run roots live there too',
  `  --coder-timeout  seconds --coder harness waits for the sentinel line (default ${DEFAULT_CODER_TIMEOUT_S})`,
  `  --until     stop after this step: ${STEPS.join(' | ')}`,
].join('\n');

class UsageError extends Error {}
export class StepFailed extends Error {}

/**
 * @param {string[]} argv
 * @returns {{engine: string, coder: string, tmpRoot: string | undefined, run: string, until: string, coderTimeoutMs: number, help: boolean}}
 * @throws {UsageError}
 */
export function parseArgs(argv) {
  /** @type {Record<string, string>} */
  const flags = {};
  let help = false;
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--help' || arg === '-h') {
      help = true;
      continue;
    }
    const name = arg.startsWith('--') ? arg.slice(2) : '';
    if (!['engine', 'coder', 'tmp-root', 'run', 'until', 'coder-timeout'].includes(name)) throw new UsageError(`unknown argument ${JSON.stringify(arg)}`);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith('--')) throw new UsageError(`--${name} needs a value`);
    if (Object.hasOwn(flags, name)) throw new UsageError(`--${name} given more than once`);
    flags[name] = value;
    i += 1;
  }
  const engine = flags.engine ?? 'harness';
  if (engine === 'solo') throw new UsageError('--engine solo: Solo is manual evidence only — run the code-forge skill under Solo, not this script');
  if (!ENGINES.includes(engine)) throw new UsageError(`--engine must be ${ENGINES.join(' or ')}`);
  const coder = flags.coder ?? (engine === 'subprocess' ? 'spawn' : 'harness');
  if (!CODERS.includes(coder)) throw new UsageError(`--coder must be one of ${CODERS.join(', ')}`);
  if (coder === 'spawn' && engine !== 'subprocess') throw new UsageError('--coder spawn needs --engine subprocess');
  if (flags['tmp-root'] !== undefined && !path.isAbsolute(flags['tmp-root'])) throw new UsageError('--tmp-root must be an absolute path');
  const run = flags.run ?? defaultRunId();
  if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(run)) throw new UsageError('--run must match ^[a-z0-9][a-z0-9-]{0,63}$');
  const until = flags.until ?? 'report';
  if (!STEPS.includes(until)) throw new UsageError(`--until must be one of ${STEPS.join(', ')}`);
  const coderTimeout = flags['coder-timeout'] ?? String(DEFAULT_CODER_TIMEOUT_S);
  if (!/^[1-9]\d{0,5}$/.test(coderTimeout)) throw new UsageError('--coder-timeout must be a whole number of seconds (1..999999)');
  return { engine, coder, tmpRoot: flags['tmp-root'], run, until, coderTimeoutMs: Number(coderTimeout) * 1000, help };
}

/** @returns {string} `fu-<yyyymmdd>-<hhmmss>-<hex4>` */
function defaultRunId() {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
  return `fu-${stamp}-${randomBytes(2).toString('hex')}`;
}

/**
 * The §2 banner of `skill/references/degraded.md` (the text the skill prints once per run), for
 * this engine.
 * @param {string} engine
 * @returns {string}
 */
export function degradedBanner(engine) {
  const doc = readFileSync(DEGRADED_DOC, 'utf8');
  const section = doc.slice(doc.indexOf('## §2'));
  const open = section.indexOf('```\n');
  const close = section.indexOf('\n```', open + 4);
  if (!section.startsWith('## §2') || open < 0 || close < 0) throw new Error('degraded.md §2 has no banner block');
  return section.slice(open + 4, close).replace('<harness|subprocess>', engine);
}

/**
 * `process.env` minus every inherited `GIT_*` (a git hook's `GIT_DIR`/`GIT_INDEX_FILE` must never
 * redirect a child into another repository). Every child this script starts gets it.
 * @returns {Record<string, string>}
 */
export function scrubbedEnv() {
  /** @type {Record<string, string>} */
  const env = {};
  for (const [key, value] of Object.entries(process.env)) if (!key.startsWith('GIT_') && value !== undefined) env[key] = value;
  return env;
}

/** {@link scrubbedEnv} plus no system/global git config. */
function gitEnv() {
  return { ...scrubbedEnv(), GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: os.devNull };
}

/** @param {string} text @returns {any} the last line of `text` parsed as JSON, or null */
function lastJSON(text) {
  const line = text.trim().split('\n').at(-1) ?? '';
  try {
    return JSON.parse(line);
  } catch {
    return null;
  }
}

/**
 * The cost of one block from its run's ledger rows: every row that carries a token count and a
 * provider + level is priced with the static table (`src/ledger/prices.mjs`) — dollars are
 * estimates, tokens are facts. Rows of the run without a block (facts, author) are the block's
 * planning cost here, since this run has one block.
 * @param {Array<Record<string, any>>} rows - the run's rows
 * @param {string} block
 * @returns {{usd: number, tokens_in: number, tokens_out: number, by_role: Record<string, number>, priced_rows: number}}
 */
export function blockCost(rows, block) {
  let usd = 0;
  let tokensIn = 0;
  let tokensOut = 0;
  let priced = 0;
  /** @type {Record<string, number>} */
  const byRole = {};
  for (const row of rows) {
    if (row.block !== block && row.block !== null && row.block !== undefined) continue;
    const tIn = typeof row.tokens_in === 'number' ? row.tokens_in : 0;
    const tOut = typeof row.tokens_out === 'number' ? row.tokens_out : 0;
    if (row.event !== 'session' || typeof row.provider !== 'string' || typeof row.level !== 'string' || tIn + tOut === 0) continue;
    const cost = estimateCostUsd({ provider: row.provider, level: row.level, tokensIn: tIn, tokensOut: tOut });
    usd += cost;
    tokensIn += tIn;
    tokensOut += tOut;
    priced += 1;
    const role = typeof row.role === 'string' ? row.role : 'other';
    byRole[role] = Math.round(((byRole[role] ?? 0) + cost) * 10000) / 10000;
  }
  return { usd: Math.round(usd * 10000) / 10000, tokens_in: tokensIn, tokens_out: tokensOut, by_role: byRole, priced_rows: priced };
}

/**
 * @param {string[]} argv
 * @param {{stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [io]
 * @returns {Promise<number>}
 */
export async function main(argv, io = {}) {
  const stdout = io.stdout ?? process.stdout;
  const stderr = io.stderr ?? process.stderr;
  const say = (/** @type {string} */ s) => stdout.write(`${s}\n`);
  /** @type {ReturnType<typeof parseArgs>} */
  let opts;
  try {
    opts = parseArgs(argv);
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    stderr.write(`first-use: ${err.message}\n${USAGE}\n`);
    return 2;
  }
  if (opts.help) {
    say(USAGE);
    return 0;
  }
  const { engine, coder, run, until } = opts;

  // The copy lives in `<tmp-root>/first-use/<run>/`; `first-use/` is a temp root this process owns
  // (owner.json + pid registry), so every child `exec` starts is registered there and the next
  // sweep after this process ends removes the copies (§9.6).
  const container = runRoot('first-use', { root: tmpBase(opts.tmpRoot) });
  setRunRoot(container);
  const copy = path.join(container, run);
  if (existsSync(copy)) {
    stderr.write(`first-use: ${copy} already exists — pick another --run\n`);
    return 2;
  }

  /** @type {Record<string, any>} */
  const summary = { ok: false, run, engine, coder, copy, slug: null, steps: [] };
  let started = false;
  let blockOpen = false;
  const abort = new AbortController();
  let stepNo = 0;
  const step = (/** @type {string} */ name) => {
    stepNo += 1;
    summary.steps.push(name);
    say(`== ${stepNo}. ${name} ==`);
  };
  const done = (/** @type {string} */ name) => name === until;

  /** @param {string[]} args @param {{cwd?: string, ok?: number[]}} [o] */
  const cf = async (args, o = {}) => {
    if (abort.signal.aborted) throw new StepFailed(String(abort.signal.reason));
    const res = await exec([process.execPath, BIN, ...args], { cwd: o.cwd ?? copy, env: scrubbedEnv(), timeoutMs: CLI_TIMEOUT_MS });
    if (abort.signal.aborted) throw new StepFailed(String(abort.signal.reason));
    const ok = o.ok ?? [0];
    if (!ok.includes(res.code ?? -1)) {
      stderr.write(res.stderr);
      throw new StepFailed(`code-forge ${args.slice(0, 2).join(' ')} exited ${res.code}`);
    }
    return res;
  };
  /** @param {string[]} args */
  const git = async (args) => {
    const res = await exec(['git', '-c', 'user.name=code-forge first use', '-c', 'user.email=first-use@example.invalid', '-c', 'commit.gpgsign=false', ...args], {
      cwd: copy,
      env: gitEnv(),
      timeoutMs: GIT_TIMEOUT_MS,
    });
    if (res.result !== 'ok') throw new StepFailed(`git ${args[0]} failed (exit ${res.code})`);
    return res.stdout;
  };

  /** @returns {Promise<number>} */
  const body = async () => {
    // 1 — copy + git init + one base commit
    step('copy');
    cpSync(EXAMPLE_DIR, copy, { recursive: true, filter: (src) => !['node_modules', '.code-forge', '.git', '.env', '.gitignore'].includes(path.basename(src)) });
    renameSync(path.join(copy, SHIPPED_GITIGNORE), path.join(copy, '.gitignore'));
    await git(['init', '-q']);
    await git(['add', '-A']);
    await git(['commit', '-q', '-m', 'base: examples/node-lib']);
    const baseSha = (await git(['rev-parse', 'HEAD'])).trim();
    writeFileSync(path.join(copy, '.env'), readFileSync(path.join(copy, '.env.example'))); // untracked by design (.gitignore)
    say(`copy ${realpathSync(copy)} · base ${baseSha.slice(0, 12)}`);
    if (done('copy')) return 0;

    // 2 — init (non-interactive), then the engine banner once
    step('init');
    const initArgs = ['init', '--no-interaction', '--tools', 'current', '--harness', 'claude', '-p', '--engine', engine === 'harness' ? 'harness' : 'auto', '--no-jev', '--proof', 'copy_untracked=.env', '--skip-doctor'];
    const init = await cf(initArgs);
    const initJSON = lastJSON(init.stdout);
    if (initJSON && initJSON.ok !== true) throw new StepFailed('init did not report ok');
    const configFile = path.join(copy, '.code-forge.yml');
    if (engine === 'subprocess' || opts.tmpRoot !== undefined) {
      const doc = parseDocument(readFileSync(configFile, 'utf8'));
      // R2: subprocess is never automatic — this is the explicit config edit the stop text asks for
      if (engine === 'subprocess') doc.set('engine', 'subprocess');
      // the run roots (worker, facts, author) go where the copy is
      if (opts.tmpRoot !== undefined) doc.setIn(['tmp', 'root'], opts.tmpRoot);
      writeFileSync(configFile, doc.toString());
    }
    const config = parseYAML(readFileSync(configFile, 'utf8'));
    summary.slug = config?.project?.slug ?? null;
    say(`.code-forge.yml written · engine ${config?.engine} · slug ${summary.slug}${config?.tmp?.root ? ` · tmp.root ${config.tmp.root}` : ''}`);
    say(degradedBanner(engine).replace('<slug>', String(summary.slug)));
    if (done('init')) return 0;

    // 3 + 4 — the facts rule: the sheet exists before the author designs anything
    mkdirSync(path.join(copy, 'plans'), { recursive: true });
    cpSync(path.join(INPUTS_DIR, 'clamp.brief.md'), path.join(copy, 'plans', 'clamp.md'));
    cpSync(path.join(INPUTS_DIR, 'clamp.acceptance.yml'), path.join(copy, 'plans', 'clamp.acceptance.yml'));
    step('facts');
    const facts = lastJSON((await cf(['facts', '--brief', 'plans/clamp.md', '--run', run])).stdout);
    say(`facts sheet ${path.relative(copy, facts?.out ?? '')} · claims ${facts?.claims} · verified ${facts?.verified}`);
    if (done('facts')) return 0;

    step('author');
    const author = lastJSON((await cf(['author', '--job', 'plan', '--brief', 'plans/clamp.md', '--facts', 'plans/clamp.facts.md', '--run', run])).stdout);
    say(`plan draft ${path.relative(copy, author?.draft ?? '')} · questions ${author?.questions?.length ?? 0} · cost ${JSON.stringify(author?.cost ?? null)}`);
    if (done('author')) return 0;

    // 5 + 6 — the run (worker) and the block
    step('start');
    started = true;
    say((await cf(['run', 'start', '--run', run, '--engine', engine])).stdout.trimEnd());
    if (done('start')) return 0;

    step('open');
    const opened = await cf(['block', 'open', BLOCK, '--run', run, '--level', 'L1', '--owned', ...OWNED, '--acceptance', 'plans/clamp.acceptance.yml', '--brief', 'plans/clamp.plan.md', '--lines', '40']);
    blockOpen = true;
    say(opened.stdout.trimEnd());
    const pointer = opened.stdout.split('\n').find((l) => l.startsWith('BRIEF ')) ?? '';
    if (done('open')) return 0;

    // 7 — the coder
    step('code');
    const logFile = path.join(copy, '.code-forge', 'runs', run, `${BLOCK}.log`);
    mkdirSync(path.dirname(logFile), { recursive: true });
    await runCoder({ coder, copy, run, pointer, logFile, cf, say, signal: abort.signal, timeoutMs: opts.coderTimeoutMs });
    if (done('code')) return 0;

    // 8 — one review per owned file; the approval is the signed row the gate reads
    step('review');
    summary.approvals = [];
    for (const file of OWNED) {
      const queued = lastJSON((await cf(['review-file', file, '--block', BLOCK, '--run', run])).stdout);
      if (!queued?.ticket) throw new StepFailed(`review-file ${file}: ${queued?.status ?? 'no answer'}`);
      const waited = lastJSON((await cf(['review-file', '--wait', queued.ticket, '--max', '600s'])).stdout);
      const result = waited?.result ?? {};
      say(`review ${file} · ${waited?.status} · round ${result.round ?? '?'} ${result.kind ?? ''} · approved ${result.approved === true}`);
      if (waited?.status !== 'done' || result.approved !== true) {
        throw new StepFailed(`review of ${file} is not approved (${waited?.status}; ${waited?.fix_list?.length ?? 0} finding(s)${waited?.stop ? `; stopped ${waited.stop}` : ''})`);
      }
      summary.approvals.push(file);
    }
    if (done('review')) return 0;

    // 9 — proof, in the measurement export
    step('proof');
    const exp = lastJSON((await cf(['proof', 'export', BLOCK, '--run', run])).stdout);
    summary.export = { restored: exp?.restored ?? [], untracked: exp?.untracked ?? [] };
    say(`export ${exp?.dir} · restored ${summary.export.restored.join(', ')} · untracked ${summary.export.untracked.join(', ')}`);
    const gate = lastJSON((await cf(['gates', 'run', '--cwd', exp.dir], { ok: [0, 1] })).stdout);
    const gates = Array.isArray(gate?.results) ? gate.results.filter((/** @type {any} */ r) => r.skipped !== true) : [];
    summary.export_gates_ok = gates.length > 0 && gates.every((/** @type {any} */ r) => r.ok === true);
    say(`gates in the export · ${gates.map((/** @type {any} */ r) => `${r.gate} ${r.ok ? 'ok' : 'red'}`).join(' · ') || 'none ran'}`);
    if (summary.export_gates_ok !== true) throw new StepFailed('the example suite does not pass in the measurement export');
    summary.proofs = [];
    for (const test of acceptanceTests(path.join(copy, 'plans', 'clamp.acceptance.yml'))) {
      const res = await cf(['proof', 'red-green', BLOCK, '--run', run, '--test', test], { ok: [0, 1] });
      const proof = lastJSON(res.stdout);
      say(`${res.stderr.trimEnd()}\nproof ${test} · ${proof?.mechanism} · red ${proof?.red} (${proof?.red_kind}) · green ${proof?.green}`);
      summary.proofs.push({ test, mechanism: proof?.mechanism, red_kind: proof?.red_kind, proven: proof?.proven === true });
      if (proof?.proven !== true) throw new StepFailed(`red→green not proven for ${test}`);
    }
    await cf(['proof', 'export', BLOCK, '--run', run, '--remove']);
    if (done('proof')) return 0;

    // 10 — the gate, then what the completed block cost
    step('close');
    say((await cf(['block', 'close', BLOCK, '--run', run])).stdout.trimEnd());
    blockOpen = false;
    const rows = (await readAllRows(/** @type {string} */ (summary.slug))).filter((r) => r.run === run);
    summary.cost = blockCost(rows, BLOCK);
    const roles = Object.entries(summary.cost.by_role).map(([role, usd]) => `${role} $${usd}`).join(' · ');
    say(
      `cost per completed block: ${BLOCK} $${summary.cost.usd} (estimated from ${summary.cost.priced_rows} priced session row(s); ${roles || 'no priced rows'}) · tokens ${summary.cost.tokens_in} in / ${summary.cost.tokens_out} out` +
        (coder === 'scripted' ? ' · coder: scripted, no session' : coder === 'harness' ? ' · coder: harness subagent, its tokens are not in the ledger' : ''),
    );
    if (done('close')) return 0;

    // 11 — the report
    step('report');
    const report = await cf(['report', '--slug', /** @type {string} */ (summary.slug)]);
    stdout.write(report.stdout);
    summary.sections = report.stdout.split('\n').filter((l) => /^== [a-z0-9_]+ ==$/.test(l)).length;
    say(`report sections: ${summary.sections}`);
    return 0;
  };

  // SIGINT/SIGTERM while the run is active abort the current step (a wait ends at once, a CLI child
  // gets the signal forwarded by `exec`), so the `finally` below still stops the block and ends the run.
  const onSignal = (/** @type {NodeJS.Signals} */ signal) => abort.abort(`interrupted by ${signal}`);
  process.on('SIGINT', onSignal);
  process.on('SIGTERM', onSignal);
  let code;
  try {
    code = await body();
  } catch (err) {
    if (!(err instanceof StepFailed)) throw err;
    stderr.write(`first-use: FAILED at ${summary.steps.at(-1)}: ${err.message}\n`);
    code = 1;
  } finally {
    if (started && blockOpen) {
      // a failed run leaves no open block behind: `run end` refuses one, and the worker would outlive us
      const stopped = await exec([process.execPath, BIN, 'block', 'stop', BLOCK, '--run', run, '--reason', `first-use failed at ${summary.steps.at(-1)}`], { cwd: copy, env: scrubbedEnv(), timeoutMs: 60000 });
      say(`block stop · exit ${stopped.code}`);
    }
    if (started) {
      const ended = await exec([process.execPath, BIN, 'run', 'end', '--run', run], { cwd: copy, env: scrubbedEnv(), timeoutMs: 60000 });
      say(`run end · exit ${ended.code} · ${ended.stdout.trimEnd() || ended.stderr.trimEnd()}`);
      summary.run_ended = ended.code === 0;
    }
    process.off('SIGINT', onSignal);
    process.off('SIGTERM', onSignal);
  }
  summary.ok = code === 0;
  say(`FIRST-USE ${JSON.stringify(summary)}`);
  return code;
}

/**
 * The test ids the acceptance file names, in order (`<file>::<case>` or `<file>`), deduplicated.
 * @param {string} file
 * @returns {string[]}
 */
export function acceptanceTests(file) {
  const clauses = parseYAML(readFileSync(file, 'utf8'));
  if (!Array.isArray(clauses)) throw new StepFailed('the acceptance file is not a list of clauses');
  return [...new Set(clauses.flatMap((c) => (Array.isArray(c?.tests) ? c.tests.map(String) : [])))];
}

/**
 * The coder step.
 * @param {{coder: string, copy: string, run: string, pointer: string, logFile: string, cf: (args: string[], o?: {cwd?: string, ok?: number[]}) => Promise<import('../src/util/exec.mjs').ExecResult>, say: (s: string) => void, signal: AbortSignal, timeoutMs: number}} o
 */
async function runCoder({ coder, copy, run, pointer, logFile, cf, say, signal, timeoutMs }) {
  const sha8 = /sha=([0-9a-f]{8})/.exec(pointer)?.[1] ?? '????????';
  const lines = /lines=(\d+)/.exec(pointer)?.[1] ?? '?';
  if (coder === 'scripted') {
    appendFileSync(logFile, `ACK ${sha8} lines=${lines}\n`);
    for (const file of OWNED) {
      cpSync(path.join(INPUTS_DIR, 'coder', file), path.join(copy, file));
      appendFileSync(logFile, `wrote ${file}\n`);
    }
    appendFileSync(logFile, `===BLOCK ${BLOCK} COMPLETE===\n`);
    say(`scripted coder: ${OWNED.join(', ')} written · ACK ${sha8}`);
    return;
  }
  if (coder === 'spawn') {
    const res = await cf(['spawn', '--level', 'L1', '--role', 'coder', '--brief', 'plans/clamp.plan.md', '--run', run, '--block', BLOCK, '--cwd', copy, '--timeout', '1800'], { ok: [0, 1, 3] });
    appendFileSync(logFile, `${res.stdout}${res.stderr}`);
    if (res.code !== 0) throw new StepFailed(`the spawned coder exited ${res.code}`);
    say(`spawned coder done · ${res.stdout.trim().split('\n').at(-1)}`);
    return;
  }
  // harness: the orchestrator runs the Agent tool; this script waits for the sentinel in the log
  say(`harness coder: run Agent(subagent_type: general-purpose, model: <alias of L1>, prompt: <the line below>) from the harness`);
  say(`read the brief at the pointer; reply ACK ${sha8} lines=${lines} first; append a progress line to .code-forge/runs/${run}/${BLOCK}.log after every file, and your final ===BLOCK ${BLOCK} COMPLETE=== (or FAILED) line there too.`);
  say(pointer);
  say(`waiting up to ${timeoutMs / 1000}s for ===BLOCK ${BLOCK} COMPLETE=== in ${logFile} (Ctrl-C stops; the run is ended either way)`);
  await waitForSentinel({ logFile, signal, timeoutMs });
}

/**
 * Poll `logFile` until its last `===BLOCK B1 …===` line is COMPLETE (return) or anything else
 * (throw). Throws `StepFailed` when `signal` aborts or `timeoutMs` passes. Each poll sleeps with
 * `timers/promises` bound to `signal`, so no listener outlives its poll.
 * @param {{logFile: string, signal: AbortSignal, timeoutMs: number, pollMs?: number}} o
 * @returns {Promise<void>}
 */
export async function waitForSentinel({ logFile, signal, timeoutMs, pollMs = 1000 }) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (signal.aborted) throw new StepFailed(String(signal.reason));
    const log = existsSync(logFile) ? readFileSync(logFile, 'utf8') : '';
    const sentinel = log.split('\n').filter((l) => l.startsWith(`===BLOCK ${BLOCK} `)).at(-1);
    if (sentinel === `===BLOCK ${BLOCK} COMPLETE===`) return;
    if (sentinel) throw new StepFailed(`the coder reported ${sentinel}`);
    if (Date.now() >= deadline) throw new StepFailed(`no ===BLOCK ${BLOCK} COMPLETE=== line within ${timeoutMs / 1000}s`);
    try {
      await sleep(Math.min(pollMs, Math.max(0, deadline - Date.now())), undefined, { signal });
    } catch (err) {
      if (/** @type {Error} */ (err)?.name !== 'AbortError') throw err;
      // aborted: the check at the top of the loop throws StepFailed with the reason
    }
  }
}

const isMain = (() => {
  try {
    return process.argv[1] !== undefined && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url);
  } catch {
    return false;
  }
})();

if (isMain) process.exitCode = await main(process.argv.slice(2));
