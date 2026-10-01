/**
 * `code-forge tools` (block B26): see and install the recommended tools of the shared table
 * (`src/install/tools.mjs`).
 *
 *   tools [--json]                               one line per tool; exit 0 always
 *   tools install [<id>...] [--yes] [--dry-run]  install the missing (or the named) tools
 *
 * `install` shows the plan (each command as argv, joined for display only), asks ONE yes (default
 * no) unless `--yes`; without a terminal and without `--yes` it prints the plan, installs nothing
 * and exits 2. Each install runs through B0 `exec` (argv, never a shell); a failure does not stop
 * the others; presence is re-checked afterwards. The final table lists the tools acted on (and,
 * with named ids, "already installed"); a failure says why: `failed (exit n | signal S | timed out
 * | did not start)`. Exit 0 when every requested install succeeded, 1 when any failed or the yes
 * was refused ("cancelled — nothing installed", exit 1, as in init), 2 on a usage error. Manual tools are never "failed"
 * and no browser is ever opened.
 */

import { exec as realExec } from '../util/exec.mjs';
import { writeSafe } from '../util/redact.mjs';
import { detectTool, failureReason, installPlan, TOOL_IDS, TOOLS, toolById, toolVersion } from '../install/tools.mjs';
import { parseFlags } from '../state/cli-args.mjs';

const USAGE = 'usage: code-forge tools [--json] | tools install [<id>...] [--yes] [--dry-run]\n  (install asks one yes, default no; answering no prints "cancelled — nothing installed" and exits 1)\n';

/** An install may take a while (npm, brew); a hung one is killed after this. */
const INSTALL_TIMEOUT_MS = 600_000;

/** @typedef {{write: (s: string) => unknown}} Out */

/**
 * @typedef {object} ToolsDeps
 * @property {NodeJS.ProcessEnv} [env]
 * @property {Out} [stdout]
 * @property {Out} [stderr]
 * @property {boolean} [isTTY] - whether stdin is a terminal (default `process.stdin.isTTY`).
 * @property {{confirm: (o: {message: string, initialValue?: boolean}) => Promise<any>, isCancel: (v: unknown) => boolean}} [ui] - default `@clack/prompts`.
 * @property {typeof realExec} [exec] - runs `--version` and the installs (tests: a fake).
 * @property {NodeJS.Platform} [platform]
 * @property {(p: string) => boolean} [exists] - the Solo app bundle check (tests: a fake).
 */

/**
 * @typedef {object} ToolStatus
 * @property {string} id
 * @property {'installed'|'missing'} status
 * @property {string|null} version
 * @property {string[]|null} install - the argv that would install it (missing only).
 * @property {string|null} hint - the manual hint (missing only, when there is no argv).
 * @property {string|null} note
 */

/** @param {ToolsDeps} deps */
function toolEnv(deps) {
  const env = deps.env ?? process.env;
  return { pathEnv: env.PATH ?? '', platform: deps.platform ?? process.platform, home: env.HOME ?? '', ...(deps.exists ? { exists: deps.exists } : {}) };
}

/**
 * @param {ToolsDeps} deps
 * @returns {Promise<ToolStatus[]>}
 */
export async function toolStatuses(deps = {}) {
  const opts = toolEnv(deps);
  const exec = deps.exec ?? realExec;
  const env = deps.env ?? process.env;
  /** @type {ToolStatus[]} */
  const out = [];
  for (const tool of TOOLS) {
    if (await detectTool(tool, opts)) {
      out.push({ id: tool.id, status: 'installed', version: await toolVersion(tool, { exec, env }), install: null, hint: null, note: tool.note });
    } else {
      const plan = await installPlan(tool, opts);
      out.push({ id: tool.id, status: 'missing', version: null, install: plan.kind === 'run' ? plan.argv : null, hint: plan.kind === 'manual' ? plan.hint : null, note: null });
    }
  }
  return out;
}

/** @param {ToolStatus} s @returns {string} one line, no newline */
export function renderStatus(s) {
  if (s.status === 'installed') return `${s.id.padEnd(7)} installed  ${s.version ?? 'version unknown'}${s.note ? ` — ${s.note}` : ''}`;
  return `${s.id.padEnd(7)} missing    ${s.install ? `install: ${s.install.join(' ')}` : `manual: ${s.hint}`}`;
}

/**
 * @param {string[]} args
 * @param {ToolsDeps} [deps]
 * @returns {Promise<number>}
 */
export async function runTools(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);

  if (args[0] === 'install') return runInstall(args.slice(1), deps, out, err);

  let parsed;
  try {
    parsed = parseFlags(args, { booleans: ['json'] });
  } catch (e) {
    err(`tools: ${e.message}\n${USAGE}`);
    return 2;
  }
  if (parsed.positionals.length > 0) {
    err(`tools: unknown subcommand ${JSON.stringify(parsed.positionals[0])}\n${USAGE}`);
    return 2;
  }
  const statuses = await toolStatuses(deps);
  if (parsed.flags.json) {
    out(`${JSON.stringify({ tools: statuses })}\n`);
  } else {
    for (const s of statuses) out(`${renderStatus(s)}\n`);
  }
  return 0;
}

/**
 * @param {string[]} args @param {ToolsDeps} deps
 * @param {(s: string) => void} out @param {(s: string) => void} err
 * @returns {Promise<number>}
 */
async function runInstall(args, deps, out, err) {
  let parsed;
  try {
    parsed = parseFlags(args, { booleans: ['yes', 'dry-run'] });
  } catch (e) {
    err(`tools: ${e.message}\n${USAGE}`);
    return 2;
  }
  const ids = [...new Set(parsed.positionals)];
  const unknown = ids.filter((id) => !TOOL_IDS.includes(id));
  if (unknown.length > 0) {
    err(`tools: unknown tool id(s): ${unknown.join(', ')} — valid ids: ${TOOL_IDS.join(', ')}\n${USAGE}`);
    return 2;
  }
  const opts = toolEnv(deps);
  const env = deps.env ?? process.env;
  const exec = deps.exec ?? realExec;
  const named = ids.length > 0;
  const wanted = named ? TOOLS.filter((t) => ids.includes(t.id)) : TOOLS;

  /** @type {Array<{id: string, result: string}>} */
  const table = [];
  /** @type {Array<{id: string, argv: string[]}>} */
  const runs = [];
  /** @type {Array<{id: string, hint: string}>} */
  const manual = [];
  for (const tool of wanted) {
    if (await detectTool(tool, opts)) {
      if (named) table.push({ id: tool.id, result: 'already installed' });
      continue;
    }
    const plan = await installPlan(tool, opts);
    if (plan.kind === 'run') runs.push({ id: tool.id, argv: plan.argv });
    else manual.push({ id: tool.id, hint: plan.hint });
  }

  if (runs.length === 0 && manual.length === 0) {
    out(named ? '' : 'all recommended tools are installed\n');
    printTable(out, table);
    return 0;
  }

  out('plan:\n');
  for (const r of runs) out(`  ${r.id}: ${r.argv.join(' ')}\n`);
  for (const m of manual) out(`  ${m.id}: manual — ${m.hint}\n`);

  if (parsed.flags['dry-run']) {
    out('dry run: nothing installed\n');
    return 0;
  }
  if (runs.length > 0 && !parsed.flags.yes) {
    if (!(deps.isTTY ?? process.stdin.isTTY === true)) {
      err('tools: no terminal to confirm; nothing installed — pass --yes to install the plan above\n');
      return 2;
    }
    const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
    const go = await ui.confirm({ message: `Run ${runs.length} install command(s)?`, initialValue: false });
    if (ui.isCancel(go) || go !== true) {
      out('cancelled — nothing installed\n');
      return 1;
    }
  }

  let failed = 0;
  for (const r of runs) {
    out(`\n$ ${r.argv.join(' ')}\n`);
    let res;
    try {
      res = await exec(r.argv, { timeoutMs: INSTALL_TIMEOUT_MS, env });
    } catch (e) {
      // a spawn that throws reads "did not start"; its message is never printed
      res = { result: 'failed', code: null, signal: null, stdout: '', stderr: '', timedOut: false };
    }
    if (res.stdout) out(res.stdout.endsWith('\n') ? res.stdout : `${res.stdout}\n`);
    if (res.stderr) err(res.stderr.endsWith('\n') ? res.stderr : `${res.stderr}\n`);
    if (res.result === 'ok') {
      const tool = /** @type {NonNullable<ReturnType<typeof toolById>>} */ (toolById(r.id));
      const present = await detectTool(tool, opts);
      table.push({ id: r.id, result: present ? 'installed' : 'installed (not on PATH yet — open a new shell)' });
    } else {
      failed += 1;
      table.push({ id: r.id, result: `failed (${failureReason(res)})` });
    }
  }
  for (const m of manual) table.push({ id: m.id, result: `skipped: manual — ${m.hint}` });
  out('\n');
  printTable(out, table);
  return failed > 0 ? 1 : 0;
}

/** @param {(s: string) => void} out @param {Array<{id: string, result: string}>} table */
function printTable(out, table) {
  for (const t of table) out(`${t.id.padEnd(7)} ${t.result}\n`);
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function tools(args) {
  return runTools(args);
}
