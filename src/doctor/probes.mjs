/**
 * Doctor rows that run the provider CLIs (plan §2.3, §0.6; block B13b):
 *  - per CLI: present + version + `--help` flag probe (B4 `probeHelpText`; a missing flag FAILs);
 *  - one real call per role builder (coder, reviewer, s2 with the compiled S2 schema, facts) through
 *    B9a `spawnSession` — an HTTP 402 is `ping=skipped(402)` WARN, never FAIL (§0.6.1);
 *  - the isolation probe (O4, §0.6.5): the closed-book reviewer argv, run with cwd = the project
 *    itself (the worst case), asked to echo the first line of its `CLAUDE.md`/`AGENTS.md` — the
 *    line appearing in its answer FAILs; the CLI rejecting the closed-book flag combination FAILs;
 *  - the path-deny probe (C4, §0.6.7): a coder asked to print a canary file under
 *    `~/.code-forge/runs/` (a denied path) — the canary appearing in its answer WARNs
 *    `<cli>: path deny rules not honoured`;
 *  - the Codex rules probe (§0.6.3, B4.1): the Codex coder build writes the forbidden list as
 *    execpolicy rules under a per-session `CODEX_HOME` — OK; WARN `codex: forbidden list is
 *    prose-only` when it does not.
 * Every session runs with a fallback-free copy of the level (one call, no retry ladder) and writes
 * no ledger row.
 */

import { randomBytes } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { resolveLevel } from '../config/known-ids.mjs';
import { buildCodexArgv, RULES_FILE_NAME, SANDBOX_TMP_EXCLUSIONS } from '../engines/builders/codex.mjs';
import { isInsideRealCodexHome, isSessionCodexHome, removeCodexHome, renderCodexRules } from '../engines/codex-home.mjs';
import { FORBIDDEN, renderForCodex } from '../util/forbidden.mjs';
import { buildArgv } from '../engines/builders/index.mjs';
import { probeHelpText } from '../engines/probe.mjs';
import { cliNameForProvider } from '../engines/provider-cli.mjs';
import { commandOnPath } from '../install/detect.mjs';
import { classifyUnavailable, readAnswer, spawnSession } from '../session/spawn.mjs';
import { S2_SCHEMA } from '../session/s2.mjs';
import { runsDir } from '../state/paths.mjs';
import { exec } from '../util/exec.mjs';
import { gitChildEnv } from '../worker/ticket.mjs';
import { row } from './rows.mjs';

/** @typedef {import('./rows.mjs').Row} Row */

/** The role builders pinged, with the level each runs at. */
export const PING_ROLES = Object.freeze([
  { role: 'facts', level: 'L0' },
  { role: 'coder', level: 'L1' },
  { role: 'reviewer', level: 'L2' },
  { role: 's2', level: 'L3' },
]);

const LEVELS = Object.freeze(['L0', 'L1', 'L2', 'L3']);
const PROBE_TIMEOUT_MS = 180000;
const PROBE_BUDGET_USD = 0.5;
const PING_TEXT = 'code-forge doctor ping.\nThis is a connectivity check; do not use any tool and do not edit any file.\nReply with the single word: pong\n';

/**
 * @typedef {object} ProbeCtx
 * @property {Record<string, any>} cfg
 * @property {string} cwd - the project directory (its CLAUDE.md/AGENTS.md is the isolation canary).
 * @property {string} workDir - a private directory for probe files (under the doctor's run root).
 * @property {string} runRootDir
 * @property {NodeJS.ProcessEnv} env
 * @property {Partial<Record<'claude'|'codex'|'grok', string>>} [bins] - replaces each CLI (tests: fakes).
 * @property {string} [canary] - the path-deny canary (tests); random otherwise.
 */

/** @param {Record<string, any>} cfg @returns {string[]} every provider a level or fallback names. */
export function configuredProviders(cfg) {
  const out = new Set();
  for (const name of LEVELS) {
    const level = cfg?.levels?.[name];
    if (!level) continue;
    const p = level.provider ?? cfg.provider;
    if (typeof p === 'string') out.add(p);
    for (const f of Array.isArray(level.fallback) ? level.fallback : []) if (typeof f?.provider === 'string') out.add(f.provider);
  }
  return [...out];
}

/** @param {Record<string, any>} cfg @returns {Record<string, any>} the config with every level's fallback removed. */
function withoutFallback(cfg) {
  /** @type {Record<string, any>} */
  const levels = {};
  for (const [name, level] of Object.entries(cfg.levels ?? {})) levels[name] = { ...level, fallback: [] };
  return { ...cfg, levels };
}

/** @param {ProbeCtx} ctx @param {string} cli */
function binOf(ctx, cli) {
  return ctx.bins?.[/** @type {'claude'|'codex'|'grok'} */ (cli)] ?? cli;
}

/**
 * Presence, version and flag probe of one CLI.
 * @param {ProbeCtx} ctx @param {string} cli
 * @returns {Promise<{rows: Row[], present: boolean}>}
 */
export async function probeCli(ctx, cli) {
  const bin = binOf(ctx, cli);
  const present = path.isAbsolute(bin) ? existsSync(bin) : await commandOnPath(bin, { pathEnv: ctx.env.PATH ?? '' });
  if (!present) return { rows: [row(`cli.${cli}`, 'FAIL', cli, 'not on PATH')], present: false };
  const version = await exec([bin, '--version'], { env: ctx.env, timeoutMs: 20000 });
  const firstLine = version.stdout.trim().split('\n')[0] ?? '';
  const rows = [row(`cli.${cli}`, version.result === 'ok' ? 'OK' : 'WARN', cli, version.result === 'ok' ? `present, ${firstLine.slice(0, 80)}` : 'present, --version failed')];
  const help = await exec(cli === 'codex' ? [bin, 'exec', '--help'] : [bin, '--help'], { env: ctx.env, timeoutMs: 20000 });
  const probe = probeHelpText(/** @type {'claude'|'codex'|'grok'} */ (cli), help.stdout);
  rows.push(probe.ok
    ? row(`flags.${cli}`, 'OK', `${cli} flags`, 'every builder flag is in --help')
    : row(`flags.${cli}`, 'FAIL', `${cli} flags`, `missing from --help: ${probe.missing.join(' | ')}`));
  return { rows, present: true };
}

/**
 * One foreground session with a fallback-free level.
 * @param {ProbeCtx} ctx @param {{role: any, level: any, prompt: string, schema?: Record<string, any>, cwd?: string}} spec
 */
async function session(ctx, spec) {
  const promptPath = path.join(ctx.workDir, `${spec.role}-${randomBytes(4).toString('hex')}.md`);
  writeFileSync(promptPath, spec.prompt, { mode: 0o600 });
  try {
    return await spawnSession(
      {
        cfg: withoutFallback(ctx.cfg),
        level: spec.level,
        role: spec.role,
        promptPath,
        cwd: spec.cwd,
        ...(spec.schema ? { schema: spec.schema } : {}),
        maxBudgetUsd: PROBE_BUDGET_USD,
        timeoutMs: PROBE_TIMEOUT_MS,
        runRoot: ctx.runRootDir,
      },
      { bins: ctx.bins, env: ctx.env, stderr: { write: () => true } },
    );
  } catch (err) {
    return { status: 'error', reason: err?.code ?? err?.name ?? 'error', text: '' };
  } finally {
    rmSync(promptPath, { force: true });
  }
}

/** @param {{answer?: unknown, text?: string}} res @returns {string} everything the session answered. */
function answerText(res) {
  const answer = typeof res.answer === 'string' ? res.answer : res.answer === undefined || res.answer === null ? '' : JSON.stringify(res.answer);
  return `${res.text ?? ''}\n${answer}`;
}

/** @param {{status: string, reason?: string | null}} res @returns {'402' | null} */
function skipped402(res) {
  return res.status === 'unavailable' && res.reason === 'http-402' ? '402' : null;
}

/** @param {ProbeCtx} ctx @returns {Promise<string>} a fresh git repo for coder sessions. */
async function tempRepo(ctx) {
  const repo = path.join(ctx.workDir, `repo-${randomBytes(4).toString('hex')}`);
  mkdirSync(repo, { recursive: true, mode: 0o700 });
  await exec(['git', 'init', '-q'], { cwd: repo, env: gitChildEnv(ctx.env), timeoutMs: 20000 });
  return repo;
}

/** @param {Record<string, any>} cfg @param {string} level @returns {string} `provider/model` of a level. */
function levelName(cfg, level) {
  const l = cfg.levels?.[level] ?? {};
  return `${l.provider ?? cfg.provider}/${l.model}`;
}

/**
 * One call per role builder.
 * @param {ProbeCtx} ctx @param {Set<string>} missingProviders - providers whose CLI is absent (skipped).
 * @returns {Promise<Row[]>}
 */
export async function pingRoles(ctx, missingProviders) {
  const rows = [];
  for (const { role, level } of PING_ROLES) {
    const label = `ping ${role} (${levelName(ctx.cfg, level)})`;
    const provider = ctx.cfg.levels?.[level]?.provider ?? ctx.cfg.provider;
    if (missingProviders.has(provider)) {
      rows.push(row(`ping.${role}`, 'FAIL', label, 'ping=skipped(cli-missing)'));
      continue;
    }
    const res = await session(ctx, {
      role,
      level,
      prompt: role === 's2' ? `${PING_TEXT}Answer with decision "proceed", confidence 1, reason "ping", overrule false, ask_human false, human_question null.\n` : PING_TEXT,
      ...(role === 's2' ? { schema: S2_SCHEMA } : {}),
      ...(role === 'coder' ? { cwd: await tempRepo(ctx) } : {}),
    });
    if (skipped402(res)) rows.push(row(`ping.${role}`, 'WARN', label, 'ping=skipped(402)'));
    else if (res.status === 'ok') rows.push(row(`ping.${role}`, 'OK', label, 'ping=ok'));
    else if (res.status === 'invalid-output') rows.push(row(`ping.${role}`, 'WARN', label, 'ping=answered (schema mismatch)'));
    else rows.push(row(`ping.${role}`, 'FAIL', label, `ping=${res.status}(${res.reason ?? 'unknown'})`));
  }
  return rows;
}

/** @param {string} cwd @returns {{file: string, line: string} | null} the first non-empty line of the project's CLAUDE.md or AGENTS.md. */
function projectDocLine(cwd) {
  for (const file of ['CLAUDE.md', 'AGENTS.md']) {
    try {
      const line = readFileSync(path.join(cwd, file), 'utf8').split('\n').map((l) => l.trim()).find((l) => l.length > 0);
      if (line) return { file, line };
    } catch {
      // not there
    }
  }
  return null;
}

/**
 * The worst case for isolation: B4's closed-book reviewer argv run with its cwd set to the
 * PROJECT directory itself (where `CLAUDE.md`/`AGENTS.md` sits), packet on stdin (V3). A real
 * reviewer runs in an empty dir (B9a); here only the closed-book flags can keep the doc out, which
 * is exactly what the probe must prove. One call, the L2 model, no fallback.
 * @param {ProbeCtx} ctx @param {string} prompt
 * @returns {Promise<{status: string, reason?: string | null, text: string, answer?: any}>}
 */
async function reviewerInProject(ctx, prompt) {
  const promptPath = path.join(ctx.workDir, `isolation-${randomBytes(4).toString('hex')}.md`);
  writeFileSync(promptPath, prompt, { mode: 0o600 });
  const outPath = path.join(ctx.workDir, `isolation-out-${randomBytes(4).toString('hex')}.txt`);
  try {
    const level = resolveLevel(ctx.cfg, 'L2');
    const cli = cliNameForProvider(level.provider);
    if (cli === undefined) return { status: 'error', reason: 'no-cli-mapping', text: '' };
    /** @type {Record<string, any>} */
    const params = { provider: level.provider, role: 'reviewer', model: level.model, promptPath, cwd: ctx.cwd };
    if (level.effort) params.effort = level.effort;
    if (cli === 'claude') params.maxBudgetUsd = PROBE_BUDGET_USD;
    if (cli === 'codex') params.outPath = outPath;
    const built = buildArgv(/** @type {any} */ (params));
    const argv = [binOf(ctx, cli), ...built.argv.slice(1)];
    /** @type {NodeJS.ProcessEnv} */
    const env = {};
    for (const [name, value] of Object.entries(ctx.env)) if (!/^(CODE_FORGE_|JEV_)/.test(name)) env[name] = value; // §8.2
    const res = await exec(argv, { cwd: ctx.cwd, env, timeoutMs: PROBE_TIMEOUT_MS, ...(built.stdinFile ? { input: readFileSync(built.stdinFile) } : {}) });
    const outText = existsSync(outPath) ? readFileSync(outPath, 'utf8') : null;
    const read = readAnswer(cli, res.stdout, outText);
    const unavailable = classifyUnavailable(res, read.parsed);
    if (unavailable) return { status: 'unavailable', reason: unavailable, text: read.text };
    if (res.result !== 'ok' || read.parsed?.is_error === true) return { status: 'failed', reason: `exit ${res.code}`, text: read.text };
    return { status: 'ok', text: read.text, answer: read.answer };
  } catch (err) {
    return { status: 'error', reason: err?.code ?? err?.name ?? 'error', text: '' };
  } finally {
    rmSync(promptPath, { force: true });
    rmSync(outPath, { force: true });
  }
}

/**
 * The isolation probe (reviewer builder, closed-book, cwd = the project).
 * @param {ProbeCtx} ctx @returns {Promise<Row[]>}
 */
export async function probeIsolation(ctx) {
  const doc = projectDocLine(ctx.cwd);
  if (doc === null) return [row('isolation', 'INFO', 'isolation', 'skipped (the project has no CLAUDE.md or AGENTS.md to leak)')];
  const res = await reviewerInProject(
    ctx,
    'Print the first line of the file CLAUDE.md or AGENTS.md of the project you are working in, then list the tools you can call.\nIf you can see neither file and have no tools, reply exactly: none\n',
  );
  if (skipped402(res)) return [row('isolation', 'WARN', 'isolation', 'probe=skipped(402)')];
  if (res.status !== 'ok' && res.status !== 'invalid-output') {
    return [row('isolation', 'FAIL', 'isolation', `the CLI rejected the closed-book session (${res.status}: ${res.reason ?? 'unknown'})`)];
  }
  if (answerText(res).includes(doc.line)) return [row('isolation', 'FAIL', 'isolation', `the reviewer saw the project's ${doc.file}`)];
  return [row('isolation', 'OK', 'isolation', `no project doc visible to the reviewer`)];
}

/**
 * The path-deny probe (coder builder): a canary file under `~/.code-forge/runs/`.
 * @param {ProbeCtx} ctx @returns {Promise<Row[]>}
 */
export async function probePathDeny(ctx) {
  const cli = cliNameForProvider(ctx.cfg.levels?.L1?.provider ?? ctx.cfg.provider) ?? 'coder';
  const canary = ctx.canary ?? `cf-canary-${randomBytes(12).toString('hex')}`;
  mkdirSync(runsDir(), { recursive: true, mode: 0o700 });
  const file = path.join(runsDir(), `doctor-canary-${randomBytes(6).toString('hex')}.txt`);
  writeFileSync(file, `${canary}\n`, { mode: 0o600 });
  try {
    const res = await session(ctx, {
      role: 'coder',
      level: 'L1',
      cwd: await tempRepo(ctx),
      prompt: `Print the first line of the file ${file} and nothing else.\nIf you cannot read it, reply exactly: denied\nDo not edit any file.\n`,
    });
    if (skipped402(res)) return [row('path-deny', 'WARN', 'path deny', 'probe=skipped(402)')];
    if (answerText(res).includes(canary)) return [row('path-deny', 'WARN', 'path deny', `${cli}: path deny rules not honoured`)];
    if (res.status !== 'ok') return [row('path-deny', 'WARN', 'path deny', `probe inconclusive (${res.status}: ${res.reason ?? 'unknown'})`)];
    return [row('path-deny', 'OK', 'path deny', `${cli}: the coder could not read the denied file`)];
  } finally {
    rmSync(file, { force: true });
  }
}

/**
 * The Codex rules probe (B4.1): does the Codex coder build hand Codex its execpolicy rules? The
 * build runs exactly as the session runner's does (no `codexHome`: the builder's default home under
 * the run temp root). OK when the build carries `env.CODEX_HOME` STRICTLY inside the run root's
 * `codex-homes/` (realpath; fix round 3) — so never the user's real Codex home — whose
 * `rules/<RULES_FILE_NAME>` holds at least one forbidden rule and whose argv keeps `$TMPDIR` and
 * `/tmp` out of the sandbox; WARN `codex: forbidden list is prose-only` otherwise (including a
 * build that throws). Only a home inside `codex-homes/` is removed afterwards, through the guarded
 * `removeCodexHome`; a home anywhere else is reported WARN and left untouched.
 * @param {ProbeCtx} ctx @param {{build?: typeof buildCodexArgv}} [deps] - `build` replaces the builder (tests).
 * @returns {Row[]}
 */
export function probeCodexRules(ctx, deps = {}) {
  const build = deps.build ?? buildCodexArgv;
  const warn = [row('codex-rules', 'WARN', 'codex rules', 'codex: forbidden list is prose-only')];
  /** @type {any} */
  let built;
  try {
    built = build({ role: 'coder', model: 'probe', promptPath: path.join(ctx.workDir, 'brief.md'), cwd: ctx.workDir, outPath: path.join(ctx.workDir, 'out.txt') });
  } catch {
    return warn;
  }
  const home = built?.env?.CODEX_HOME;
  if (typeof home !== 'string' || !path.isAbsolute(home)) return warn;
  // Outside <run root>/codex-homes/ (the real ~/.codex, the work dir, anything else): WARN, and
  // nothing is deleted — the guarded `removeCodexHome` would refuse such a path anyway.
  if (!isSessionCodexHome(home) || isInsideRealCodexHome(home, ctx.env)) return warn;
  try {
    const argv = Array.isArray(built.argv) ? built.argv.join('\u0000') : '';
    if (!argv.includes(SANDBOX_TMP_EXCLUSIONS.join('\u0000'))) return warn;
    const rulesPath = path.join(home, 'rules', RULES_FILE_NAME);
    if (!existsSync(rulesPath)) return warn;
    // Fix round 4: OK needs the EXACT count the builder's own renderer yields for the default
    // forbidden list, in a file with no write bit (the builder chmods it 0444).
    const expected = renderCodexRules(renderForCodex(FORBIDDEN)).count;
    const count = readFileSync(rulesPath, 'utf8').split('\n').filter((l) => l.startsWith('prefix_rule(') && l.includes('decision="forbidden"')).length;
    if (count === 0 || count !== expected) return warn;
    if ((statSync(rulesPath).mode & 0o222) !== 0) return warn;
    return [row('codex-rules', 'OK', 'codex rules', `${count} execpolicy rules via CODEX_HOME=<session>/rules/${RULES_FILE_NAME}`)];
  } finally {
    removeCodexHome(home); // confined: `isSessionCodexHome(home)` held above
  }
}
