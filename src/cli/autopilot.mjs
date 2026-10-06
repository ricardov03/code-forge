/**
 * `code-forge autopilot start|status|stop|ask` (issue #5, blocks B45, B46). The OWNER (and the
 * orchestrator on the owner's behalf) runs these — a coder never may: `autopilot` is on the
 * coder-only forbidden list.
 *
 *   autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…>
 *                   [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]
 *   autopilot status --run <id> [--json]
 *   autopilot stop --run <id>
 *   autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--json]
 *
 * `ask` (B46) puts one question to the delegate (`src/autopilot/delegate.mjs`): exit 0 when the
 * answer is acted on, 3 when the question goes to the owner (answered but not acted, or the
 * session failed — one signed `autopilot.decision` row either way), 1 when the grant refuses
 * (no grant, expired, stopped, denied, not allowed: no session, no row), 2 for usage.
 * `start` grants a delegate the listed scopes until `--until` (at most 24 h ahead). It asks for
 * confirmation on a terminal; without one it is refused unless `--yes` is given. Scopes outside
 * the vocabulary, or on the fixed deny list, are refused by name (`src/autopilot/scopes.mjs`).
 * Every subcommand runs the lazy expiry check first, so an expired grant gets its one
 * `autopilot.expire` row here at the latest.
 */

import { activeGrantMessage, checkExpiry, expiredMessage, grantState, startGrant, stopGrant, validateGrantInput } from '../autopilot/grant.mjs';
import { askDelegate } from '../autopilot/delegate.mjs';
import { FIXED_DENY_SCOPES } from '../autopilot/scopes.mjs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { SessionError } from '../session/spawn.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun } from '../state/run.mjs';
import { writeSafe } from '../util/redact.mjs';

/**
 * @typedef {{
 *   stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown},
 *   now?: () => Date, isTTY?: boolean, ui?: any,
 *   writeRow?: (row: Record<string, any>) => Promise<unknown>,
 *   cwd?: string, session?: import('../session/spawn.mjs').SessionDeps,
 * }} AutopilotDeps - `cwd`: where `ask` loads the project config when the run has no snapshot;
 *   `session`: the spawner seams (`bins`, `env`, `exec`, …) for `ask` (tests: the fake CLIs).
 */

const SUBCOMMANDS = Object.freeze(['start', 'status', 'stop', 'ask']);

const FLAG_SPECS = Object.freeze({
  start: { values: ['run', 'until', 'delegate', 'allow', 'deny', 'budget', 'stop-at'], booleans: ['yes'] },
  status: { values: ['run'], booleans: ['json'] },
  stop: { values: ['run'] },
  ask: { values: ['run', 'scope', 'question', 'options', 'context-file'], booleans: ['json'] },
});

/** `--options a,b,…` → the trimmed, non-empty items. @param {unknown} text @returns {string[] | undefined} */
const optionList = (text) => (typeof text === 'string' ? text.split(',').map((s) => s.trim()).filter((s) => s.length > 0) : undefined);

/**
 * `autopilot ask`: one delegate question.
 * @param {string} runId @param {Record<string, any>} flags @param {AutopilotDeps} deps
 * @param {(s: string) => void} out
 * @returns {Promise<number>} 0 acted, 3 to the owner, 1 refused
 */
async function runAsk(runId, flags, deps, out) {
  if (typeof flags.scope !== 'string' || flags.scope.trim().length === 0) throw new StateError('usage', 'autopilot ask needs --scope <scope>');
  if (typeof flags.question !== 'string' || flags.question.trim().length === 0) throw new StateError('usage', 'autopilot ask needs --question <text>');
  const options = optionList(flags.options);
  // `--options ","` parses to nothing: refused here, never sent on as an open (no-options) question
  if (options !== undefined && options.length === 0) throw new StateError('usage', '--options needs at least 2 distinct options, e.g. --options waive,fix');
  /** @type {string | undefined} */
  let context;
  if (typeof flags['context-file'] === 'string') {
    try {
      context = await readFile(path.resolve(deps.cwd ?? process.cwd(), flags['context-file']), 'utf8');
    } catch {
      throw new StateError('usage', `--context-file ${flags['context-file']} cannot be read`); // the path only, never content
    }
  }
  /** @type {Record<string, any> | undefined} */
  let cfg;
  try {
    const loaded = await loadProjectConfig(deps.cwd ?? process.cwd());
    cfg = loaded.ok ? loaded.config : undefined; // only used when the run has no config snapshot
  } catch {
    cfg = undefined;
  }
  const res = await askDelegate(
    { runId, scope: flags.scope.trim(), question: flags.question, options, context, cfg },
    { ...(deps.session ?? {}), stderr: deps.stderr ?? deps.session?.stderr, writeRow: deps.writeRow, ...(deps.now ? { now: deps.now } : {}) },
  );
  const code = res.acted ? 0 : res.row === null ? 1 : 3;
  const answer = res.answer;
  if (flags.json) {
    out(
      `${JSON.stringify({
        run: runId,
        scope: flags.scope,
        grant_id: res.grant_id,
        answered: res.answered,
        acted: res.acted,
        to_owner: res.to_owner,
        reason: res.reason,
        decision: answer?.decision ?? null,
        confidence: answer?.confidence ?? null,
        within_scope: answer?.within_scope ?? null,
        escalate: answer?.escalate ?? null,
        answer_reason: answer?.reason ?? null,
      })}\n`,
    );
    return code;
  }
  if (code === 1) {
    out(`autopilot ask run ${runId}: refused (${res.reason}) — no session; the question goes to the owner\n`);
    return code;
  }
  const head = res.acted ? 'acted' : `to the owner (${res.reason})`;
  out(`autopilot ask run ${runId}: ${head}\n`);
  if (answer) out(`  decision  ${answer.decision} · confidence ${answer.confidence} · within scope ${answer.within_scope} · escalate ${answer.escalate}\n  reason    ${answer.reason}\n`);
  return code;
}

/**
 * Time left as `<h>h <m>m` (whole minutes, rounded down).
 * @param {number} ms @returns {string}
 */
export function formatLeft(ms) {
  const minutes = Math.max(0, Math.floor(ms / 60000));
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** @param {Record<string, number>} caps @returns {string} */
const capsText = (caps) => {
  const keys = Object.keys(caps).sort();
  return keys.length === 0 ? 'no caps' : keys.map((k) => `${k} ${caps[k].toFixed(2)} USD`).join(', ');
};

/**
 * @param {string} runId @param {import('../autopilot/grant.mjs').Grant | null} grant @param {Date} now
 * @param {string} [checked] - the state `checkExpiry` answered (`run-ended` overrides the grant's own)
 * @returns {Record<string, any>}
 */
function statusData(runId, grant, now, checked) {
  if (grant === null) {
    return { run: runId, state: 'none', grant_id: null, delegate: null, until: null, time_left_s: null, scopes: [], deny: [], fixed_deny: [...FIXED_DENY_SCOPES], caps: {}, stop_at: null, link: null, started_at: null, stopped_at: null, expired_at: null };
  }
  const state = checked === 'run-ended' ? 'run-ended' : grantState(grant, now);
  const left = state === 'active' ? Math.floor((Date.parse(grant.until) - now.getTime()) / 1000) : 0;
  return {
    run: runId,
    state,
    grant_id: grant.grant_id,
    delegate: grant.delegate,
    until: grant.until,
    time_left_s: left,
    scopes: grant.scopes,
    deny: grant.deny,
    fixed_deny: [...FIXED_DENY_SCOPES],
    caps: grant.caps,
    stop_at: grant.stop_at,
    link: grant.link ?? null,
    started_at: grant.started_at,
    stopped_at: grant.stopped_at,
    expired_at: grant.expired_at,
  };
}

/** @param {Record<string, any>} d @returns {string} */
function statusText(d) {
  if (d.state === 'none') return `autopilot run ${d.run}: no grant\n`;
  const head =
    d.state === 'active'
      ? `active · ${formatLeft(d.time_left_s * 1000)} left (until ${d.until})`
      : d.state === 'stopped'
        ? `stopped at ${d.stopped_at} (was until ${d.until})`
        : d.state === 'run-ended'
          ? `the run has ended; the grant no longer acts (was until ${d.until})`
          : `expired at ${d.until}`;
  return (
    `autopilot run ${d.run}: ${head}\n` +
    `  grant     ${d.grant_id}\n` +
    `  delegate  ${d.delegate}\n` +
    `  allow     ${d.scopes.join(', ')}\n` +
    `  deny      ${[...d.deny, ...d.fixed_deny].join(', ')}\n` +
    `  budget    ${capsText(d.caps)} · stop at ${Math.round(d.stop_at * 100)}%\n` +
    `  link      ${d.link ?? 'none'}\n`
  );
}

/**
 * @param {string[]} args @param {AutopilotDeps} [deps]
 * @returns {Promise<number>}
 */
export async function runAutopilot(args, deps = {}) {
  const { stdout = process.stdout, stderr = process.stderr, now: clock = () => new Date(), writeRow } = deps;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const [sub, ...rest] = args;
  if (!SUBCOMMANDS.includes(sub)) {
    err(USAGE);
    return 2;
  }
  try {
    const { flags, positionals } = parseFlags(rest, FLAG_SPECS[/** @type {keyof typeof FLAG_SPECS} */ (sub)]);
    if (positionals.length > 0) throw new StateError('usage', `unexpected argument ${JSON.stringify(positionals[0])}`);
    if (typeof flags.run !== 'string') throw new StateError('usage', `autopilot ${sub} needs --run <id>`);
    const runId = flags.run;
    if (sub === 'ask') return await runAsk(runId, flags, deps, out);
    const now = clock();

    if (sub === 'status') {
      const { grant, state } = await checkExpiry(runId, now, { writeRow });
      const data = statusData(runId, grant, now, state);
      out(flags.json ? `${JSON.stringify(data, null, 2)}\n` : statusText(data));
      return 0;
    }
    if (sub === 'stop') {
      // the expiry check first: an expired grant gets its one expire row and no stop row
      const checked = await checkExpiry(runId, now, { writeRow });
      if (checked.state === 'expired') throw new StateError('grant-expired', expiredMessage(runId, checked.grant.until));
      const grant = await stopGrant({ runId, now, writeRow });
      out(`autopilot run ${runId}: grant ${grant.grant_id} stopped\n`);
      return 0;
    }
    // start
    const input = { until: flags.until, delegate: flags.delegate, allow: flags.allow, deny: flags.deny, budget: flags.budget, stopAt: flags['stop-at'] };
    const valid = validateGrantInput(input, now); // refuse bad input before asking anyone
    if ((await readRun(runId)).status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const before = await checkExpiry(runId, now, { writeRow });
    if (before.state === 'active') {
      throw new StateError('grant-active', activeGrantMessage(runId, before.grant.until));
    }
    if (!flags.yes) {
      if (!(deps.isTTY ?? process.stdin.isTTY === true)) {
        err('autopilot start: no terminal to confirm; the owner starts autopilot — pass --yes to start it without a prompt\n');
        return 2;
      }
      const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
      const go = await ui.confirm({ message: `Start autopilot for run ${runId} until ${valid.until}?`, initialValue: false });
      if (ui.isCancel(go) || go !== true) {
        out('cancelled — no grant\n');
        return 1;
      }
    }
    // the prompt may have waited: `--until` is checked again (future, at most 24 h) against now
    const startedAt = flags.yes ? now : clock();
    const grant = await startGrant({ runId, input, now: startedAt, writeRow });
    out(
      `autopilot run ${runId}: grant ${grant.grant_id} active until ${grant.until} (${formatLeft(Date.parse(grant.until) - startedAt.getTime())})\n` +
        `  delegate ${grant.delegate} · allow ${grant.scopes.join(', ')} · budget ${capsText(grant.caps)} · stop at ${Math.round(grant.stop_at * 100)}%\n`,
    );
    return 0;
  } catch (thrown) {
    err(`autopilot ${sub}: ${thrown?.message ?? String(thrown)}\n`);
    // `ask` only: an unknown --scope and a malformed question/options are usage; start/status/stop keep B45's codes
    const askUsage = sub === 'ask' && ((thrown instanceof StateError && thrown.code === 'unknown-scope') || (thrown instanceof SessionError && thrown.code === 'usage'));
    const usageError = (thrown instanceof StateError && thrown.code === 'usage') || askUsage;
    return usageError ? 2 : 1;
  }
}

const USAGE =
  'usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--json]\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function autopilot(args) {
  return runAutopilot(args);
}
