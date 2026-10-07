/**
 * `code-forge block open|attempt|rebase|claim|close|stop` (plan §4.9). The skill calls
 * `block open` before every spawn in every engine; it prints the brief pointer (≤ 200 bytes).
 *
 *   block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file>
 *              [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <forecast>] [--kind code|docs|contract]
 *   block attempt <id> --run <r>
 *   block rebase <id> --run <r> [--head <ref>]
 *   block claim <id> <path> --run <r>
 *   block close <id> --run <r> [--worker-pid <pid>]
 *   block stop <id> --run <r> --reason <text>
 *   block waive <id> <finding-id> --run <r> --file <path> --reason <text>   (human-only, §4.9)
 *
 * `block close [--transcript <file>] [--report <file>] [--no-require-reviews]` also runs B12b's review rows
 * (`review/gate-check.mjs`): a signed `review.approved` row for the current content hash of every
 * changed owned file (deletions included) that no later review of the same content made stale
 * (B52: `not_approved <file> <finding>` per open finding of that later review, unless waived — a
 * critical only by the human's `block waive`) — `--no-require-reviews` is the human's way out, is
 * forbidden to coders (`mergeForbidden`), needs the human to confirm at a terminal (B52: stdin
 * or stdout not a TTY ⇒ exit 2, an active autopilot grant ⇒ refused, there is no `--yes`; a block
 * that is not open is refused first, exactly as without the flag), lifts `no_changes`, and leaves a signed
 * `gate.reviews_waived {by: human}` row before the close (never a silent skip; a declined prompt
 * prints `block <id> close: cancelled …`). B52: a changed or new file no block of the run
 * accounts for is `unowned_change <file>` (`unownedChanges`, listed from the repository top
 * level; not lifted by `--no-require-reviews`; only the repository top-level `.code-forge/` is
 * exempt — plus, for a workspace in a subdirectory, this run's record mirror and this block's
 * default transcript there — never a `--transcript` or `--report` path in the source tree; a
 * closed block's file still exactly at the content its gate approved is accounted for); a file another OPEN block of the run owns
 * is left to that block's gate with one `WARN` line per file (the close cannot tell which coder
 * wrote it); an old ledger's `review.result` rows are judged by their signed result files
 * (`resultFilesFor`). B52 (field run): the file set is the diff since base ∪ every owned file
 * with review rows of the block (`reviewedFiles`; an owned entry with no glob owns the paths below
 * it, `ownsAt`), so work committed before `block open` or done in another worktree is still
 * checked (with a WARN naming the likely cause when the diff shows none of it); an empty set is
 * `no_changes`; late findings ruled; `review_cap` findings waived; no
 * `rule_break`, plus the transcript grep. The transcript is `--transcript`, else the block's
 * `transcript` in the run record, else `.code-forge/runs/<run>/<block>.log`; none ⇒ a signed
 * `gate.transcript_missing` row and a WARN line (never a silent pass, never a refusal). An id the
 * run does not know is `unknown_block` before any check runs. B20: every changed owned file of
 * tier `high` (§7.1: `proof.tiers.high.paths`, a recorded `risk ≥ 2`, or `security_sensitive`)
 * needs a signed, proven red→green `proof` row (`checkBlockProof`), else `unproven <file>`; the
 * human's way out is `block waive <id> proof --file <path>`. The high paths are the union of the
 * `.code-forge.yml` at the block's base and the current one (`highPathsAt`); either that exists
 * but does not load or validate refuses the close (a typo must never drop a high path), and a
 * base that is missing or not a commit of the workspace refuses it too (`git-failed`).
 *
 * B36: `--report <file>` is the coder's final report. The check is enforced when `--report` is
 * given (the orchestrator always passes it, `skill/references/code.md` §3): a changed owned file
 * whose `review-file` ticket id the report does not name refuses the close with `coder report
 * FAILED: no review-file ticket id for <files>` (skipped under `--no-require-reviews`, the human's
 * way out). It runs after the review rows and reuses their file set (listed once).
 *
 * B47: `block open` without `--level` takes the level `autopilot level` chose for the block (the
 * latest `autopilot.level` row of the run for it whose MAC verifies, `autopilotLevel`); with
 * neither it is a usage error (`no --level given and no autopilot level recorded for <id>`). An
 * explicit `--level` always wins; a repeated or valueless `--level` is a usage error as before.
 *
 * `--acceptance` is a YAML/JSON list of `{clause, tests: [test ids…]}`. `block close` runs B8's
 * rows of the gate (worker pin, MACs, orphans); the gate rows owned by later blocks join it
 * through `closeBlock`'s `extraChecks`.
 */

import { readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { parse as parseYAML } from 'yaml';
import { approvalCheckText, expiryChecks } from '../autopilot/limits.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { attemptBlock, claimPath, closeBlock, openBlock, rebaseBlock, repoTop, stopBlock } from '../state/block.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { loadKey, verifyRow } from '../state/signer.mjs';
import { blockFileSet, changedFiles, ownsAt, resultFilesFor, reviewedFiles, reviewGateCheck, unownedChanges, waiveFinding } from '../review/gate-check.mjs';
import { checkExpiry } from '../autopilot/grant.mjs';
import { writeSafe } from '../util/redact.mjs';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { migrateConfig } from '../config/migrate.mjs';
import { exec } from '../util/exec.mjs';
import { gitChildEnv } from '../worker/ticket.mjs';
import { missingReviewTickets } from '../engines/sentinel.mjs';

const USAGE =
  'usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract]\n' +
  '       code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>\n' +
  '       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text>\n';

const SUBCOMMANDS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive'];
const FLAG_VALUES = ['run', 'level', 'acceptance', 'brief', 'attempt', 'base', 'lines', 'head', 'worker-pid', 'reason', 'file', 'transcript', 'kind', 'report'];

/**
 * @param {string[]} args
 * @param {{
 *   stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, probe?: import('../state/run.mjs').StartTimeProbe, now?: () => Date,
 *   isTTY?: boolean, stdinIsTTY?: boolean, stdoutIsTTY?: boolean,
 *   ui?: {confirm: (o: {message: string, initialValue?: boolean}) => Promise<unknown>, isCancel: (v: unknown) => boolean},
 * }} [deps] - `now` (B48): the clock of the autopilot expiry checks. `stdinIsTTY` / `stdoutIsTTY`
 *   (default `process.stdin.isTTY` / `process.stdout.isTTY`; `isTTY` sets both) and `ui` (default
 *   `@clack/prompts`): B52's confirmation of `--no-require-reviews`, which needs both to be TTYs.
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
    const { flags, positionals } = parseFlags(rest, { values: FLAG_VALUES, multi: ['owned'], booleans: ['no-require-reviews'] });
    const [id, extra] = positionals;
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    if (!id || !runId) throw new StateError('usage', 'a block id and --run are required');
    if (sub === 'open' && (typeof flags.acceptance !== 'string' || !Array.isArray(flags.owned) || (flags.level !== undefined && typeof flags.level !== 'string'))) {
      throw new StateError('usage', 'block open needs --level, --owned and --acceptance');
    }
    if (sub === 'claim' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block claim needs exactly one path');
    if ((sub === 'stop' || sub === 'waive') && typeof flags.reason !== 'string') throw new StateError('usage', `block ${sub} needs --reason`);
    if (sub === 'waive' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block waive needs exactly one finding id');
    if (sub === 'waive' && typeof flags.file !== 'string') throw new StateError('usage', 'block waive needs --file <path> (a finding id is scoped to one file)');
    const record = await readRun(runId);
    const writeRow = (/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });
    // B48: every block command runs the lazy autopilot checks (grant expiry, expired approvals);
    // a failed check is a warning, never a reason to stop the block command
    try {
      err(approvalCheckText(await expiryChecks(runId, (deps.now ?? (() => new Date()))(), { writeRow })));
    } catch (thrown) {
      err(`autopilot: WARN the expiry check failed (${thrown?.message ?? String(thrown)})\n`);
    }

    if (sub === 'open') {
      // B47: no --level ⇒ the level autopilot chose for the block (a signed autopilot.level row)
      const level = typeof flags.level === 'string' ? flags.level : await autopilotLevel(runId, id, record);
      if (level === null) throw new StateError('usage', `no --level given and no autopilot level recorded for ${id}`);
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
        level,
        owned: /** @type {string[]} */ (flags.owned),
        acceptance,
        attempt: intFlag(flags.attempt, 'attempt'),
        base: typeof flags.base === 'string' ? flags.base : undefined,
        lines: intFlag(flags.lines, 'lines') ?? null,
        brief,
        ...(typeof flags.kind === 'string' ? { kind: flags.kind } : {}),
        cfg: await openConfig(record.workspace),
        writeRow,
      });
      const raised = block.level !== level ? ` (${block.kind} floor, asked ${level})` : '';
      const chosen = typeof flags.level === 'string' ? '' : ' (autopilot level)';
      out(`block ${id} open · ${block.level}${raised}${chosen} · ${block.kind} · attempt ${block.attempt} · base ${block.base_sha.slice(0, 12)}\n`);
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
    if (sub === 'waive') {
      await waiveFinding({ runId, id, finding: extra, reason: /** @type {string} */ (flags.reason), file: /** @type {string} */ (flags.file), writeRow });
      out(`block ${id} waived ${extra} in ${flags.file} (by: human)\n`);
      return 0;
    }
    if (sub === 'close') {
      const entry = record.blocks?.[id];
      if (!entry) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
      // B52: a block that is not open is refused here, with or without --no-require-reviews, by
      // the same check and message as `closeBlock` — and the owner is never asked about it
      if (entry.status !== 'open') throw new StateError('no-block', `block ${id} is not open in run ${runId}`);
      const reviewsWaived = flags['no-require-reviews'] === true;
      // B52: the human's way out is confirmed by the human, at a terminal, before anything is written
      if (reviewsWaived) {
        const asked = await confirmNoReviews(runId, id, deps);
        if (asked === 'cancelled') {
          err(`block ${id} close: cancelled — the review checks stay on\n`);
          return 1;
        }
      }
      const rows = (await readAllRows(record.project)).filter((row) => row.run === runId);
      // B52: ONE path base and ONE listing, memoised as a promise and shared by every check. The
      // base is the repository top level: the worker signs every review row with a repo-root-
      // relative `file` and keeps its queue and signed result files under `<top>/.code-forge/`
      // (`repoRootOf`), and `tree_at_open` is stamped there too. An owned entry matches a path
      // as given or, for a workspace in a subdirectory, relative to the workspace (`ownsAt`).
      // A failure here is the usual `git-failed` refusal.
      /** @type {Promise<{top: string, wsPrefix: string, owns: (owned: ReadonlyArray<string>, file: string) => boolean, changed: string[], fileSet: Array<{file: string, content_hash: string}>, reviewed: string[]}> | undefined} */
      let treeOnce;
      const tree = () =>
        (treeOnce ??= (async () => {
          const top = await repoTop(record.workspace);
          // both sides as realpaths: git prints the top resolved (/private/var on macOS) while the
          // run record may keep a symlinked spelling (/var)
          const real = async (/** @type {string} */ p) => realpath(p).catch(() => path.resolve(p));
          const wsPrefix = path.relative(await real(top), await real(record.workspace)).split(path.sep).join('/');
          const owns = ownsAt(wsPrefix);
          const isMine = (/** @type {string} */ f) => owns(entry.owned_files, f);
          const changed = await changedFiles({ repoRoot: top, base: entry.base_sha });
          // the file set is the diff since base ∪ every owned file with review rows of this block —
          // work the diff cannot see (committed before `block open`, done in another worktree) is
          // still checked, at its content in the workspace
          const reviewed = reviewedFiles(rows, id).filter(isMine);
          const fileSet = await blockFileSet({ repoRoot: top, base: entry.base_sha, owned: entry.owned_files, matchOwned: isMine, changed: [...new Set([...changed, ...reviewed])].sort() });
          return { top, wsPrefix, owns, changed, fileSet, reviewed };
        })());
      const reviews = reviewGateCheck(async () => {
        const transcript = await findTranscript(record, id, typeof flags.transcript === 'string' ? flags.transcript : null);
        if (transcript === null) {
          await writeSigned(runId, writeRow, { event: 'gate.transcript_missing', block: id });
          err(`WARN block ${id}: no coder transcript found (--transcript, run record, .code-forge/runs/${runId}/${id}.log); the transcript grep did not run\n`);
        }
        // the human's way out of the per-file review check leaves a signed audit row before the close
        if (reviewsWaived) await writeSigned(runId, writeRow, { event: 'gate.reviews_waived', block: id, by: 'human' });
        // B20: the high paths come first — a base the workspace does not know refuses the close here
        const highPaths = await highPathsAt(record.workspace, entry.base_sha);
        const { top, wsPrefix, owns, changed, fileSet, reviewed } = await tree();
        const isMine = (/** @type {string} */ f) => owns(entry.owned_files, f);
        const key = await loadKey(runId);
        // B52: an empty diff with review rows is never silent — name the likely cause
        if (reviewed.length > 0 && !changed.some(isMine)) {
          err(`WARN block ${id}: no owned file differs from the block base in the workspace, but ${reviewed.length} owned file(s) have review rows — the work was likely committed before block open or done in another worktree; they are checked at their content in the workspace\n`);
        }
        // B52: only code-forge's own state is exempt — `<top>/.code-forge/`, where the worker keeps
        // its queue and signed results — never a `--transcript` or `--report` path in the source
        // tree. For a workspace in a subdirectory, `<workspace>/.code-forge/` is source tree like any
        // other path, except the two files code-forge itself keeps there at fixed names: this run's
        // record mirror and this block's default transcript (`.code-forge/runs/<run>.json`,
        // `.code-forge/runs/<run>/<id>.log`)
        const wsRel = (/** @type {string} */ p) => path.posix.join(wsPrefix, p);
        const ownState = new Set([wsRel(`.code-forge/runs/${runId}.json`), wsRel(`.code-forge/runs/${runId}/${id}.log`)]);
        const scope = await unownedChanges({ top, changed: changed.filter((f) => !ownState.has(f)), id, blocks: record.blocks, rows, key, isMine, owns, stateDir: '.code-forge' });
        if (scope === null) err(`WARN block ${id}: opened before code-forge recorded the tree at block open; unowned changes were not checked\n`);
        for (const { file, block: other } of scope?.siblings ?? []) err(`WARN block ${id}: ${file} changed and is owned by open block ${other}; not checked here (its own gate reviews it)\n`);
        return {
          block: id,
          runId,
          rows,
          key,
          transcript,
          // B52: a change no block of the run owns is refused — `--no-require-reviews` does not lift it
          unowned: scope?.unowned ?? [],
          // B52: an old ledger's review.result rows are judged by their signed result files
          resultFiles: await resultFilesFor({ repoRoot: top, runId, block: id, rows }),
          files: reviewsWaived ? [] : fileSet,
          // B52: nothing changed and nothing reviewed is never a pass (the owner's way out is --no-require-reviews)
          noChanges: !reviewsWaived && fileSet.length === 0,
          // B20: high-tier files need a proven red→green row; `--no-require-reviews` does not lift it
          proofFiles: fileSet,
          highPaths,
        };
      });
      // B36: the coder's report names a review-file ticket for every changed file, or it is FAILED
      const report = async () => {
        if (typeof flags.report !== 'string' || reviewsWaived) return { ok: true };
        let text;
        try {
          text = await readFile(flags.report, 'utf8');
        } catch {
          return { ok: false, reason: 'coder report FAILED: --report cannot be read' };
        }
        const { fileSet } = await tree();
        const missing = missingReviewTickets(text, fileSet.map((f) => f.file));
        return missing.length === 0 ? { ok: true } : { ok: false, reason: `coder report FAILED: no review-file ticket id for ${missing.join(', ')}` };
      };
      const result = await closeBlock({ runId, id, rows, writeRow, livePid: intFlag(flags['worker-pid'], 'worker-pid'), probe, extraChecks: [reviews, report] });
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

/** B52: the refusal when `--no-require-reviews` has no terminal to ask the human at. */
export const NO_REVIEWS_NO_TTY = '--no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)';

/**
 * B52: `--no-require-reviews` lifts every per-file review check, so it is the human's alone — the
 * same rule as `autopilot approve`: a flag can be passed by any process (the orchestrator, a
 * script, the delegate's tooling), so the human confirms at a terminal and there is NO `--yes`.
 * No terminal — stdin AND stdout must both be TTYs (a piped stdout means nobody may see the
 * question) — ⇒ `usage` (exit 2); an active autopilot grant on the run ⇒ `autopilot-active`
 * (exit 1; the owner stops the grant first) — nothing is written either way. `deps.stdinIsTTY` /
 * `deps.stdoutIsTTY` inject each side; `deps.isTTY` injects both at once.
 * @param {string} runId @param {string} id
 * @param {{isTTY?: boolean, stdinIsTTY?: boolean, stdoutIsTTY?: boolean, ui?: {confirm: (o: {message: string, initialValue?: boolean}) => Promise<unknown>, isCancel: (v: unknown) => boolean}, now?: () => Date}} deps
 * @returns {Promise<'confirmed' | 'cancelled'>}
 * @throws {StateError} `usage`, `autopilot-active`
 */
async function confirmNoReviews(runId, id, deps) {
  const stdinTTY = deps.stdinIsTTY ?? deps.isTTY ?? process.stdin.isTTY === true;
  const stdoutTTY = deps.stdoutIsTTY ?? deps.isTTY ?? process.stdout.isTTY === true;
  if (!(stdinTTY && stdoutTTY)) throw new StateError('usage', NO_REVIEWS_NO_TTY);
  const grant = await checkExpiry(runId, (deps.now ?? (() => new Date()))());
  if (grant.state === 'active') {
    throw new StateError('autopilot-active', `--no-require-reviews is refused while an autopilot grant is active on run ${runId} (stop it first: code-forge autopilot stop --run ${runId})`);
  }
  const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
  const go = await ui.confirm({ message: `Close block ${id} of run ${runId} WITHOUT the per-file review checks? Only the owner may say yes.`, initialValue: false });
  return ui.isCancel(go) || go !== true ? 'cancelled' : 'confirmed';
}

/**
 * B47: the level `autopilot level` chose for the block — the latest `autopilot.level` row of this
 * run for it whose MAC verifies — or null when there is none.
 * @param {string} runId @param {string} id @param {Record<string, any>} record
 * @returns {Promise<string | null>}
 */
async function autopilotLevel(runId, id, record) {
  const rows = (await readAllRows(record.project)).filter((r) => r?.event === 'autopilot.level' && r.run === runId && r.block === id && typeof r.level === 'string');
  if (rows.length === 0) return null;
  const key = await loadKey(runId);
  const valid = rows.filter((r) => verifyRow(r, key).ok);
  return valid.length === 0 ? null : valid[valid.length - 1].level;
}

/**
 * The project config for `block open`'s coder floor (B34): the loaded `.code-forge.yml`, or
 * undefined when there is none or it does not load — the floor then takes its default (L1), the
 * safe side.
 * @param {string} workspace @returns {Promise<Record<string, any> | undefined>}
 */
async function openConfig(workspace) {
  try {
    const loaded = await loadProjectConfig(workspace);
    return loaded.ok && loaded.config ? loaded.config : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The block's coder transcript: `--transcript`, else the run record's `blocks.<id>.transcript`
 * (workspace-relative), else `.code-forge/runs/<run>/<id>.log`; null when none exists.
 * @param {Record<string, any>} record @param {string} id @param {string | null} given
 * @returns {Promise<string | null>}
 */
async function findTranscript(record, id, given) {
  if (given !== null) return readFile(given, 'utf8');
  const recorded = record.blocks?.[id]?.transcript;
  const candidates = [
    ...(typeof recorded === 'string' && recorded.length > 0 ? [path.resolve(record.workspace, recorded)] : []),
    path.join(record.workspace, '.code-forge', 'runs', record.run_id, `${id}.log`),
  ];
  for (const file of candidates) {
    try {
      return await readFile(file, 'utf8');
    } catch (thrown) {
      if (/** @type {any} */ (thrown)?.code !== 'ENOENT') throw thrown;
    }
  }
  return null;
}

/**
 * The key paths of a config's validation errors (never values).
 * @param {Record<string, any>} config @param {string} where
 * @returns {string[]} `proof.tiers.high.paths`
 * @throws {StateError} `bad-config`
 */
function validHighPaths(config, where) {
  const result = validateConfig(config);
  if (!result.valid) {
    const at = [...new Set(result.errors.map((e) => (typeof e.path === 'string' ? e.path : e.rule)))].join(', ');
    throw new StateError('bad-config', `${where} is invalid (${result.errors.length} error(s) at ${at}) — run code-forge validate`);
  }
  const paths = config?.proof?.tiers?.high?.paths;
  return Array.isArray(paths) ? paths.filter((p) => typeof p === 'string') : [];
}

/**
 * B20: `proof.tiers.high.paths` for the close — the UNION of the `.code-forge.yml` committed at
 * the block's base (read with `git show <base>:.code-forge.yml`, argv only, GIT_* stripped) and
 * the one in the working tree, so a coder that edits or deletes the high paths in its block can
 * only ever add to them. Fails closed on the base: a base that is not a non-empty string, or that
 * `git rev-parse --verify` does not know as a commit, refuses the close (`git-failed`), and so does
 * any git call that times out, fails to spawn or exits non-zero. The base config counts as absent
 * ONLY when a clean `git ls-tree <base> -- .code-forge.yml` lists nothing; the current config may
 * be absent too. A side that exists but does not load or validate refuses the close (key paths
 * only, never values).
 * @param {string} workspace @param {string | undefined} base
 * @returns {Promise<string[]>}
 * @throws {StateError} `bad-config`, `git-failed`
 */
export async function highPathsAt(workspace, base) {
  if (typeof base !== 'string' || base.length === 0 || base.startsWith('-')) throw new StateError('git-failed', 'the block has no base commit — the close cannot read the base config');
  const paths = new Set();
  const loaded = await loadProjectConfig(workspace);
  if (loaded.ok && loaded.config) for (const p of validHighPaths(loaded.config, DEFAULT_CONFIG_FILENAME)) paths.add(p);
  else if (loaded.error !== 'not-found') throw new StateError('bad-config', `${DEFAULT_CONFIG_FILENAME} does not load (${loaded.error ?? 'no config'}) — run code-forge validate`);

  const git = { cwd: workspace, env: gitChildEnv(), timeoutMs: 30000 };
  const known = await exec(['git', 'rev-parse', '--verify', '--quiet', `${base}^{commit}`], git);
  if (known.result !== 'ok') throw new StateError('git-failed', 'the block base is not a commit of the workspace — the close cannot read the base config');
  const listed = await exec(['git', 'ls-tree', base, '--', DEFAULT_CONFIG_FILENAME], git);
  if (listed.result !== 'ok') throw new StateError('git-failed', `git could not check for ${DEFAULT_CONFIG_FILENAME} at the block base`);
  if (listed.stdout.trim().length === 0) return [...paths]; // a clean git said the base has no config
  const spec = `${base}:${DEFAULT_CONFIG_FILENAME}`;
  const shown = await exec(['git', 'show', spec], git);
  if (shown.result !== 'ok') throw new StateError('git-failed', `git could not read ${DEFAULT_CONFIG_FILENAME} at the block base`);
  const where = `${DEFAULT_CONFIG_FILENAME} at the block base`;
  let parsed;
  try {
    parsed = migrateConfig(parseYAML(shown.stdout, { prettyErrors: false }));
  } catch {
    throw new StateError('bad-config', `${where} does not load — run code-forge validate`);
  }
  for (const p of validHighPaths(parsed, where)) paths.add(p);
  return [...paths];
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function block(args) {
  return runBlock(args);
}
