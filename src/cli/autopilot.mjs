/**
 * `code-forge autopilot start|status|stop|ask|waive|round|level|approve` (issue #5, blocks B45–B48). The OWNER (and the
 * orchestrator on the owner's behalf) runs these — a coder never may: `autopilot` is on the
 * coder-only forbidden list.
 *
 *   autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…>
 *                   [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes]
 *   autopilot status --run <id> [--json]
 *   autopilot stop --run <id>
 *   autopilot ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>]
 *                 [--block <id>] [--file <path>] [--finding <id>] [--json]
 *   autopilot waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id>
 *   autopilot round --run <id> --block <id> --file <path> --decision <id>
 *   autopilot level --run <id> --block <id> --plan <file> --decision <id>
 *   autopilot approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset>
 *
 * B48 (`src/autopilot/limits.mjs`): `approve` is the OWNER's temporary config change: it writes
 * the workspace `.code-forge.yml`, reloads the run the way `run reload` does, and the old value
 * comes back at `--until` (lazily: at the next autopilot or block command or worker drain), with a
 * signed `autopilot.approve` and `autopilot.restore` row. It runs only at a terminal, after the
 * owner confirms; there is deliberately NO `--yes`: a flag can be passed by any process — the
 * orchestrator, a script, the delegate's own tooling — and `approve` is the one way to relax a
 * limit while autopilot runs, so an unattended caller must never be able to approve. Fixed keys
 * (`IMMUTABLE_KEYS`) are refused. Every subcommand first restores expired approvals. `status`
 * shows the spend per budget category since the grant started and the paused categories.
 * B47 (`src/autopilot/actions.mjs`): `waive`, `round` and `level` are delegated actions — each is
 * refused (exit 1, nothing written) unless the grant allows its scope (`waive:<severity>`,
 * `round:extra`, `model:choose`) AND `--decision` names the delegate's acted decision for it (an
 * `autopilot ask` with `--block` [`--file` `--finding`] under the same grant and scope, at most
 * 30 min old) whose answer is the action's fixed option word: ask a waive with `--options
 * waive,fix` (it needs `waive`), a round with `--options allow,deny` (needs `allow`), a level with
 * the candidate levels, e.g. `--options L1,L2` (needs the level `autopilot level` sets). Any other
 * answer, even acted, authorises nothing. `waive` reads the finding's severity from the signed review
 * rows (critical or unknown ⇒ refused; `proof` never) and opens or comments on one tracked issue
 * in the project's repo; without `gh` the waiver still stands (`issue: none`). `level` prints the
 * chosen coder level; `block open` without `--level` uses it.
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
import { chooseCoderLevel, grantExtraRound, waiveForOwner } from '../autopilot/actions.mjs';
import { approvalCheckText, approveChange, checkApprovals, spendByCategory, validateApproval } from '../autopilot/limits.mjs';
import { BUDGET_CATEGORIES, FIXED_DENY_SCOPES } from '../autopilot/scopes.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { redact } from '../util/redact.mjs';
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
 *   actions?: import('../autopilot/actions.mjs').ActionDeps,
 * }} AutopilotDeps - `cwd`: where `ask` loads the project config when the run has no snapshot;
 *   `session`: the spawner seams (`bins`, `env`, `exec`, …) for `ask` (tests: the fake CLIs);
 *   `actions`: the B47 seams (`exec`, `env`, `onPath` for gh; `readRows`).
 */

const SUBCOMMANDS = Object.freeze(['start', 'status', 'stop', 'ask', 'waive', 'round', 'level', 'approve']);

const FLAG_SPECS = Object.freeze({
  start: { values: ['run', 'until', 'delegate', 'allow', 'deny', 'budget', 'stop-at'], booleans: ['yes'] },
  status: { values: ['run'], booleans: ['json'] },
  stop: { values: ['run'] },
  ask: { values: ['run', 'scope', 'question', 'options', 'context-file', 'block', 'file', 'finding'], booleans: ['json'] },
  waive: { values: ['run', 'block', 'file', 'finding', 'severity', 'reason', 'decision'] },
  round: { values: ['run', 'block', 'file', 'decision'] },
  level: { values: ['run', 'block', 'plan', 'decision'] },
  approve: { values: ['run', 'key', 'value', 'until'] },
});

/** Why `approve` takes no `--yes` (the refusal text). */
export const APPROVE_NO_YES =
  'autopilot approve: --yes is not accepted — only the owner approves a limit change, at a terminal; a flag could be passed by the orchestrator or a script, so approve always asks';

/** The refusal without a terminal. */
export const APPROVE_NO_TTY = 'autopilot approve: no terminal to confirm; only the owner approves a change, in a terminal (approve has no --yes)';

/** @param {object} res - a refused action result. @returns {string} its message. */
const refusal = (res) => /** @type {{message: string}} */ (res).message;

/**
 * `autopilot waive|round|level` (B47): one delegated action.
 * @param {'waive' | 'round' | 'level'} sub @param {string} runId @param {Record<string, any>} flags
 * @param {AutopilotDeps} deps @param {(s: string) => void} out @param {(s: string) => void} err
 * @returns {Promise<number>} 0 done, 1 refused
 */
async function runAction(sub, runId, flags, deps, out, err) {
  /** @param {string} name */
  const need = (name) => {
    if (typeof flags[name] !== 'string' || flags[name].trim().length === 0) throw new StateError('usage', `autopilot ${sub} needs --${name}`);
    return /** @type {string} */ (flags[name]);
  };
  const actionDeps = { ...(deps.actions ?? {}), writeRow: deps.writeRow, ...(deps.now ? { now: deps.now } : {}) };
  if (sub === 'waive') {
    const req = { runId, block: need('block'), file: need('file'), finding: need('finding'), severity: need('severity'), reason: need('reason'), decisionId: need('decision') };
    const res = await waiveForOwner(req, actionDeps);
    if (!res.ok) {
      err(`autopilot waive: refused — ${refusal(res)}\n`);
      return 1;
    }
    const issue =
      res.issue !== null
        ? `issue ${res.issue} (${res.issueAction})`
        : `issue none (${res.issueFailure}; the waiver still stands${res.issueRowWritten ? '' : '; the autopilot.issue_failed row could not be written'})`;
    out(`autopilot waive run ${runId}: waived ${req.finding} (${res.row.severity}) in ${res.row.file}, block ${req.block} (by: autopilot, grant ${res.row.grant_id}) · ${issue}\n`);
    return 0;
  }
  if (sub === 'round') {
    const res = await grantExtraRound({ runId, block: need('block'), file: need('file'), decisionId: need('decision') }, actionDeps);
    if (!res.ok) {
      err(`autopilot round: refused — ${refusal(res)}\n`);
      return 1;
    }
    out(`autopilot round run ${runId}: one extra fix round for ${res.row.file} in block ${res.row.block} (grant ${res.row.grant_id})\n`);
    return 0;
  }
  const plan = path.resolve(deps.cwd ?? process.cwd(), need('plan'));
  const res = await chooseCoderLevel({ runId, block: need('block'), plan, decisionId: need('decision') }, actionDeps);
  if (!res.ok) {
    err(`autopilot level: refused — ${refusal(res)}\n`);
    return 1;
  }
  out(`autopilot level run ${runId}: block ${res.row.block} codes at ${res.row.level} (lane ${res.row.lane}, plan ${res.row.plan_level}, grant ${res.row.grant_id}); block open without --level uses it\n`);
  return 0;
}

/**
 * `autopilot approve` (B48): validate, ask the owner at a terminal (never without one, never
 * `--yes`), then apply the change through the `run reload` path.
 * @param {string} runId @param {Record<string, any>} flags @param {AutopilotDeps} deps
 * @param {(s: string) => void} out @param {(s: string) => void} err
 * @returns {Promise<number>} 0 approved, 1 refused or cancelled, 2 usage or no terminal
 */
async function runApprove(runId, flags, deps, out, err) {
  const clock = deps.now ?? (() => new Date());
  const valid = await validateApproval({ runId, key: flags.key, value: flags.value, until: flags.until, now: clock() });
  if (!(deps.isTTY ?? process.stdin.isTTY === true)) {
    err(`${APPROVE_NO_TTY}\n`);
    return 2;
  }
  const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
  const go = await ui.confirm({
    message: `Approve ${valid.key} = ${JSON.stringify(redact(valid.value))} for run ${runId} until ${valid.until}? The old value comes back then.`,
    initialValue: false,
  });
  if (ui.isCancel(go) || go !== true) {
    out('cancelled — nothing changed\n');
    return 1;
  }
  // the prompt may have waited: --until is checked again against now
  const now = clock();
  const again = await validateApproval({ runId, key: flags.key, value: flags.value, until: flags.until, now });
  const approval = await approveChange({ runId, key: again.key, segs: again.segs, value: again.value, until: again.until, now, writeRow: deps.writeRow });
  out(`autopilot approve run ${runId}: ${approval.key} changed until ${approval.until} (approval ${approval.approval_id}); the old value comes back then\n`);
  return 0;
}

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
  /** @type {Record<string, string>} */
  const subject = {};
  for (const name of ['block', 'file', 'finding']) if (typeof flags[name] === 'string') subject[name] = flags[name];
  const res = await askDelegate(
    { runId, scope: flags.scope.trim(), question: flags.question, options, context, cfg, ...(Object.keys(subject).length > 0 ? { subject } : {}) },
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
        decision_id: res.row?.decision_id ?? null,
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
  const head = res.acted ? `acted · decision ${res.row?.decision_id}` : `to the owner (${res.reason})`;
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
 * B48: spend per category against its cap, e.g. `coding 4.50 of 20.00 USD, review 0.00 USD (no cap)`.
 * @param {Record<string, number>} spend @param {Record<string, number>} caps @returns {string}
 */
const spendText = (spend, caps) =>
  BUDGET_CATEGORIES.map((c) => {
    const spent = (spend[c] ?? 0).toFixed(2);
    return typeof caps[c] === 'number' ? `${c} ${spent} of ${caps[c].toFixed(2)} USD` : `${c} ${spent} USD (no cap)`;
  }).join(', ');

/**
 * @param {string} runId @param {import('../autopilot/grant.mjs').Grant | null} grant @param {Date} now
 * @param {string} [checked] - the state `checkExpiry` answered (`run-ended` overrides the grant's own)
 * @param {Record<string, number>} [spend] - B48: USD per budget category since the grant started.
 * @returns {Record<string, any>}
 */
function statusData(runId, grant, now, checked, spend = {}) {
  if (grant === null) {
    return { run: runId, state: 'none', grant_id: null, delegate: null, until: null, time_left_s: null, scopes: [], deny: [], fixed_deny: [...FIXED_DENY_SCOPES], caps: {}, stop_at: null, spend: {}, paused: [], link: null, started_at: null, stopped_at: null, expired_at: null };
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
    spend,
    paused: Object.keys(grant.paused ?? {}).sort(),
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
    `  spend     ${spendText(d.spend, d.caps)} · paused ${d.paused.length === 0 ? 'none' : d.paused.join(', ')}\n` +
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
  if (sub === 'approve' && rest.some((a) => a === '--yes' || a.startsWith('--yes='))) {
    err(`${APPROVE_NO_YES}\n`);
    return 2;
  }
  try {
    const { flags, positionals } = parseFlags(rest, FLAG_SPECS[/** @type {keyof typeof FLAG_SPECS} */ (sub)]);
    if (positionals.length > 0) throw new StateError('usage', `unexpected argument ${JSON.stringify(positionals[0])}`);
    if (typeof flags.run !== 'string') throw new StateError('usage', `autopilot ${sub} needs --run <id>`);
    const runId = flags.run;
    // B48: ONE writer for every grant / approval / restore / expiry row of this command (signed by
    // the state layer); the run's own ledger unless the caller passed one
    const writer = writeRow ?? (async (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: (await readRun(runId)).project }));
    // B48: every subcommand first restores the approvals whose window is over
    err(approvalCheckText(await checkApprovals(runId, clock, { writeRow: writer })));
    if (sub === 'approve') return await runApprove(runId, flags, { ...deps, writeRow: writer }, out, err);
    if (sub === 'ask') return await runAsk(runId, flags, deps, out);
    if (sub === 'waive' || sub === 'round' || sub === 'level') return await runAction(sub, runId, flags, deps, out, err);
    const now = clock();

    if (sub === 'status') {
      const { grant, state } = await checkExpiry(runId, now, { writeRow: writer });
      const spend = grant === null ? {} : spendByCategory(await readAllRows((await readRun(runId)).project), runId, grant);
      const data = statusData(runId, grant, now, state, spend);
      out(flags.json ? `${JSON.stringify(data, null, 2)}\n` : statusText(data));
      return 0;
    }
    if (sub === 'stop') {
      // the expiry check first: an expired grant gets its one expire row and no stop row
      const checked = await checkExpiry(runId, now, { writeRow: writer });
      if (checked.state === 'expired') throw new StateError('grant-expired', expiredMessage(runId, checked.grant.until));
      const grant = await stopGrant({ runId, now, writeRow: writer });
      out(`autopilot run ${runId}: grant ${grant.grant_id} stopped\n`);
      return 0;
    }
    // start
    const input = { until: flags.until, delegate: flags.delegate, allow: flags.allow, deny: flags.deny, budget: flags.budget, stopAt: flags['stop-at'] };
    const valid = validateGrantInput(input, now); // refuse bad input before asking anyone
    if ((await readRun(runId)).status !== 'active') throw new StateError('run-ended', `run ${runId} has ended`);
    const before = await checkExpiry(runId, now, { writeRow: writer });
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
    const grant = await startGrant({ runId, input, now: startedAt, writeRow: writer });
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
  'usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset>\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function autopilot(args) {
  return runAutopilot(args);
}
