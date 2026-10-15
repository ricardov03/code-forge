/**
 * `code-forge block open|attempt|rebase|claim|close|stop|waive|reopen` (plan §4.9). The skill calls
 * `block open` before every spawn in every engine; it prints the brief pointer (≤ 200 bytes).
 *
 *   block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file>
 *              [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <forecast>] [--kind code|docs|contract]
 *              [--tests <paths…>]
 *   block attempt <id> --run <r>
 *   block rebase <id> --run <r> [--head <ref>]
 *   block claim <id> <path> --run <r>
 *   block close <id> --run <r> [--worker-pid <pid>] [--remove-worktree]
 *   block stop <id> --run <r> --reason <text>
 *   block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]   (orchestrator --yes: evidence and a fresh judge)
 *   block reopen <id> --run <r> --file <path> --reason <text>   (human-only, B72; asks at a terminal, B80)
 *
 * B64a `--json` (every subcommand): on success stdout is exactly ONE JSON line instead of the
 * text — `{"block", "event", …}` with the facts the text prints (open: level, asked, source,
 * kind, attempt, base_sha, tests, worktree, brief; attempt: attempt; rebase (`rebased`): old_base,
 * new_base; claim (`claimed`): path; stop (`stopped`); waive (`waived`): finding, file, by;
 * close (`closed`): status, reason, close_event (the close result's own event word, as the text
 * prints it in parentheses, else null), and `worktree {removed, path, branch}` with
 * `--remove-worktree`). On
 * failure the exit code and stderr are unchanged and stdout stays empty, except a close the gate
 * refused (`closeBlock` said no), which also prints `{"block", "ok": false, "reason"}`. A usage
 * error, a thrown refusal, a cancelled `--no-require-reviews`, `block waive` or `block reopen` (or
 * one with no terminal, B80) and a failed `--remove-worktree`
 * print nothing on stdout. Without `--json` every output is byte-identical to before.
 *
 * B64b block end: a close that closes or stops the block and `block stop` write
 * `<runs>/<run>/blocks/<id>.done.json` after the end's rows (`state/block-end.mjs`), then, with
 * `hooks.on_block_end` in the run's snapshot, run that command outside the lock and add
 * `done_file` to `close`/`stop --json`. Without the key the output is unchanged.
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
 * B96 (E1): the tool builds the close report. After the review gate passed, the close prints
 * `reviewed <path> ticket <id>` for every file the gate accepted, sorted by path, before the
 * `reviewed_by_waiver` lines and the closed line; the id is the queue's ticket for that file at the
 * hash the gate accepted (`resolveTicket`: the tree of a worktree block is part of it). `--json`
 * and the done file get `reviewed: [{file, ticket, via}]` (`via`: approved, waiver or answered —
 * the gate's own verdict, `review/reviewed-lines.mjs`), only when there is a line. So `--report`
 * needs no ticket line: a missing hand line refuses only for a file the tool has no line for, and a
 * hand line that names another ticket prints `WARN report ticket mismatch <path>` on stderr while
 * the close goes on (D1). A gate-accepted file with no ticket prints `WARN no ticket for <path>`
 * (`WARN bad ticket for <path>` when the ticket is not 24 lowercase hex) and has no line; lines that
 * cannot be built at all give no line and `WARN reviewed lines unavailable: <message>` — then
 * `--report` needs a hand line for every file the gate accepted, as before B96. All four warnings
 * are held and print only when the block closes. A waived or external-review close is unchanged:
 * no line, no key.
 *
 * B58 (issue #11): `review.external.required: true` in the run's config snapshot (the owner set it
 * before `run start`; `run reload` refuses to change it) is the owner's non-interactive way out
 * when an outside reviewer reviews the block: the per-file review checks and the `--report` ticket
 * check are skipped, with no TTY and under an active autopilot grant, and a signed
 * `gate.reviews_waived {by: config, reviewer: <name>}` row is written just before `block.close`,
 * only when the block closes. Everything
 * else still refuses — `unowned_change`, `unproven`, `no_changes`, the transcript grep and
 * `closeBlock`'s own rows. With `--no-require-reviews` the human path runs exactly as without the key.
 *
 * B70 (issue #9 a): a file with no approval for its current hash whose real review (a signed
 * `review.round` at that hash) left findings that are all waived — `block waive`, or an autopilot
 * waiver its grant covered for a non-critical — passes as `reviewed_by_waiver`, never approved
 * (`checkBlockReviews`). When the block closes, one signed `gate.review_waived_all {file,
 * content_hash, findings, by}` row per such file is written just before `block.close` (after any
 * other close row); a refused close writes none. The close prints `block <id>: <file>
 * reviewed_by_waiver (<ids>; by <who>)` before the closed line, and `--json` adds
 * `reviewed_by_waiver: [{file, content_hash, status, findings, by}]` (only when there is one).
 * B71: a finding a signed `review.answered` row clears (another owned file resolved it, judged by
 * a reviewer) counts like a waived one, listed under `answered` next to `findings` in the close
 * row and the `--json` entry; the line then reads `(<ids>; by <who>; answered <ids>)`, or
 * `(answered <ids>)` when nothing was waived (`by` is then null).
 *
 * B63b: `context.require_coder_tests: true` in the run's config snapshot (immutable like
 * `review.external`; the live config is never read for it) adds one check after the report check:
 * `--report` must be given and readable, its last sentinel for the block must be COMPLETE, and a
 * `TESTS <command> exit=<n>` line must come before it with `n` 0 — else `coder report FAILED:
 * context.require_coder_tests needs --report` / `--report cannot be read` / `no ===BLOCK <id>
 * COMPLETE=== sentinel` / `no TESTS line` / `TESTS exit=<n>`. Waived reviews (the human flag or
 * `review.external`) do not lift it. On a pass a signed `coder.tests {block, command, exit}` row
 * (the command redacted) is one of `closeBlock`'s close rows, after any waiver row. Without the key
 * (or false) nothing is added.
 *
 * B47: `block open` without `--level` takes the level `autopilot level` chose for the block (the
 * latest `autopilot.level` row of the run for it whose MAC verifies, `autopilotLevel`); with
 * neither it is a usage error (`no --level given and no autopilot level recorded for <id>`). An
 * explicit `--level` always wins; a repeated or valueless `--level` is a usage error as before.
 *
 * B59: in a brief-mode run (`run start --mode brief`), `block open` without `--level` takes the
 * lane from the §3.5 rules (`fallbackLane`) over facts built from the owned entries, `--lines`
 * (estimated changed lines) and `proof.tiers.high.paths` of the run's config snapshot
 * (`laneFacts`), and writes one signed `decision {question: lane, source: rules}` row naming the
 * block, then the `dispatch` row (`openBlock`; neither when the open is refused). An explicit
 * `--level` still wins, with no decision row. Outside brief mode `--lines` stays the forecast it
 * always was, and no `--level` without an autopilot level is the usage error above.
 *
 * B60: `--tests <paths…>` names the block's test files (the same path rules as `--owned`); they are
 * recorded as the block's `test_files` and on the dispatch row, and a gate argv item that is
 * exactly `{test_files}` runs them (`gates run --run --block`). Without it the gate takes the owned
 * entries that look like tests.
 *
 * B62a: under `proof.isolation: worktree` (the run's config snapshot, `proof/worktree.mjs`) `block
 * open` makes the block's own worktree and branch before the block is recorded — or reuses the one
 * an earlier attempt of the same block made — and prints a `worktree <path> · branch <branch>` line
 * between the open line and the brief pointer. A refused `git worktree add` refuses the open (exit
 * 1) with nothing recorded. `block close --remove-worktree` runs `git worktree remove <path>` (no
 * `--force`; the branch is kept) only after the block closed, and writes a signed
 * `worktree.removed` or `worktree.remove_failed` row; a failed removal is exit 1 with the block
 * still closed. The flag on a block without a worktree, or on another subcommand, is a usage error
 * (exit 2). It is coder-forbidden (`CODER_ONLY_FORBIDDEN`).
 *
 * B62b: `block close` of a block with a worktree measures that tree (`blockTree`), not the main
 * one: the changed files (committed on the block's branch and uncommitted, against
 * `worktree.base_sha`), the file set and its content hashes, and the unowned changes all come from
 * the tree, by the same rules as for the main tree. A change in the main tree or in another
 * block's tree never counts for the block. A fresh tree holds nothing but the block's own work,
 * so every change in it is the block's: the `tree_at_open` stamps (taken in the main tree) do not
 * exempt anything there, and a file another block owns is not left to that block (its close
 * measures only its own tree) — a change in the tree to any file this block does not own is
 * `unowned_change`. The whole close uses one base, the tree's `base_sha`. A tree that is gone,
 * not a directory, without the workspace's subdirectory, or recorded malformed refuses the close
 * before anything is asked or written. The ledger, the signed result files and the high paths stay
 * in the main tree. Nothing is checked out, pushed or merged.
 *
 * B72 (issues #12, #15): `block reopen <id> --run <r> --file <path> --reason <text>` is the
 * human's restart of a file whose review STOPPED or whose state is LOST (`review/reopen.mjs`):
 * coder-forbidden like `block waive`. The done marker of the current content's ticket is removed
 * (the ticket of the block's worktree when it has one), then one signed `review.reopened {block,
 * file, from, stop_reason, at_seq, from_hash, to_hash, reason, by: human}` row is written, so the
 * next `review-file` runs a full round 1. Refused (exit 1, no row): `unknown_block` and
 * `reopen-block-not-open` (checked before anything is read), a `worktree-*` code (the block's tree
 * cannot be read), `reopen-config` (the run's config snapshot or `tmp.root` cannot be read),
 * `reopen-not-owned`, `reopen-tampered`, `reopen-not-stopped`, `reopen-same-content`,
 * `reopen-already-recorded`, `reopen-write-failed`; a missing `--file` or `--reason` is a usage
 * error (exit 2). It prints `block <id> reopened <file> (from
 * <from>[ <stop reason>]; by: human)`; `--json`: `{"block", "event": "reopened", "file", "from",
 * "stop_reason", "at_seq", "from_hash", "to_hash", "ticket_cleared", "by"}`.
 *
 * B80: without `--yes`, `block waive` and `block reopen` need the human at a terminal, through the same
 * confirm as `--no-require-reviews` (`confirmAtTerminal`). Right after the usage checks and the run
 * record read — before the autopilot expiry checks or anything else is written — stdin or stdout
 * not a TTY prints `humanVerbNoTTY` (exit 2). At a terminal, what the verb would refuse is refused before the
 * question: a `--file` or finding id with a control character or of the wrong form (`usage`, exit
 * 2, `checkFileForm` / `checkFindingId` — the verbs' own checks), an unknown block, for reopen a
 * block that is not open or a file it does not own (`reopen-not-owned`), for waive a file the block
 * does not own (`waive-not-owned`) or a finding id no review row of the block names for the file
 * (`waive-unknown-finding`; `proof` is exempt) — exit 1. A "no" or a cancel prints `block <id>
 * <verb>: cancelled — nothing was recorded` (exit 1); a "yes" runs the verb exactly as before (the
 * same signed row). Only `block waive` accepts `--yes` (B124a, evidence and a fresh L3 judge). An active autopilot grant
 * changes nothing here — autopilot waives through its own path (`autopilot waive`, `by:
 * autopilot`). The prompt checks only that stdin and stdout are terminals; a pseudo-terminal
 * passes it, so it does not prove a human. The coder deny-list entries stay.
 *
 * `--acceptance` is a YAML/JSON list of `{clause, tests: [test ids…]}`. `block close` runs B8's
 * rows of the gate (worker pin, MACs, orphans); the gate rows owned by later blocks join it
 * through `closeBlock`'s `extraChecks`.
 */

import { statSync } from 'node:fs';
import { readFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { parse as parseYAML } from 'yaml';
import { approvalCheckText, expiryChecks } from '../autopilot/limits.mjs';
import { fallbackLane } from '../decide/fallback-rules.mjs';
import { laneFacts } from '../decide/lane-facts.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { attemptBlock, claimPath, closeBlock, openBlock, rebaseBlock, repoTop, stopBlock } from '../state/block.mjs';
import { lastGate } from '../state/brief-status.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { configInForce, externalReviewer, requireCoderTests } from '../state/config-snapshot.mjs';
import { StateError } from '../state/paths.mjs';
import { readRun, writeSigned } from '../state/run.mjs';
import { loadKey, verifyRow } from '../state/signer.mjs';
import { PROOF_FINDING, blockFileSet, changedFiles, openFindings, ownsAt, resultFilesFor, reviewedFiles, reviewGateCheck, unownedChanges, waiveFinding } from '../review/gate-check.mjs';
import { agentWaivers, waiveAsAgent } from '../review/agent-waiver.mjs';
import { checkExpiry } from '../autopilot/grant.mjs';
import { redact, writeSafe } from '../util/redact.mjs';
import { blockTree, prepareWorktree, removeWorktree, worktreeSettings } from '../proof/worktree.mjs';
import { DEFAULT_CONFIG_FILENAME, loadProjectConfig } from '../config/load.mjs';
import { validateConfig } from '../config/validate.mjs';
import { migrateConfig } from '../config/migrate.mjs';
import { exec } from '../util/exec.mjs';
import { contentHash, gitChildEnv } from '../worker/ticket.mjs';
import { clearDone, resolveTicket } from '../worker/queue.mjs';
import { loadState } from '../worker/review-state.mjs';
import { openBlockEntry, reopenFile } from '../review/reopen.mjs';
import { checkFileForm, checkFindingId } from '../review/verb-forms.mjs';
import { runRootPath } from '../util/tmp.mjs';
import { missingReviewTickets, parseSentinel } from '../engines/sentinel.mjs';
import { reportMismatches, reviewedLines } from '../review/reviewed-lines.mjs';
import { blockEndHook, runBlockEndHook, writeDoneFile } from '../state/block-end.mjs';
import { blockOpenFindings } from '../review/open-findings.mjs';
import { hookForbidden, scanTranscript } from '../util/forbidden.mjs';
import { prepareAcceptHit, writeAcceptedHit } from '../review/accept-hit.mjs';

/**
 * B72: `block reopen` — the file's content is read where the block works (its worktree, else the
 * main tree), its state is read (never moved) from the code-forge home or the run's old temp root,
 * the checks, the done marker's removal and then the signed row are `reopenFile`'s.
 * @param {{runId: string, id: string, file: string, reason: string, record: Record<string, any>, writeRow: (row: Record<string, any>) => Promise<unknown>}} opts
 * @returns {Promise<{row: Record<string, any>, cleared: boolean}>}
 */
async function reopenBlockFile({ runId, id, file, reason, record, writeRow }) {
  // the stable block codes come first, before any tree, config, key or ledger read
  const entry = openBlockEntry(record, runId, id);
  const real = async (/** @type {string} */ p) => realpath(p).catch(() => path.resolve(p));
  const mainTop = await real(await repoTop(record.workspace));
  const wt = await blockTree(record.workspace, entry).catch((thrown) => {
    if (typeof thrown?.code === 'string' && thrown.code.startsWith('worktree-')) throw new StateError(thrown.code, `block ${id}: ${thrown.message}, so the file's content cannot be read; nothing was recorded`);
    throw thrown;
  });
  const contentRoot = wt === null ? mainTop : wt.top;
  const wsPrefix = path.relative(mainTop, await real(record.workspace)).split(path.sep).join('/');
  // the state is read where the worker keeps it: an unreadable snapshot or tmp.root is refused,
  // never guessed (a wrong root would read as "no state"); only an absent tmp.root (undefined or
  // null) means the default root, any other non-string is refused (B79)
  /** @type {string} */
  let runRootDir;
  try {
    const root = configInForce(record)?.tmp?.root;
    if (root !== undefined && root !== null && typeof root !== 'string') throw new TypeError('tmp.root is not a string');
    runRootDir = runRootPath(runId, typeof root === 'string' ? { root } : {});
  } catch {
    throw new StateError('reopen-config', `reopen-config: the run's config snapshot or its tmp.root cannot be read, so the review state of ${file} cannot be found; nothing was recorded`);
  }
  const key = await loadKey(runId);
  const rows = await readAllRows(record.project);
  const tree = contentRoot === mainTop ? {} : { tree: contentRoot };
  const { row, cleared } = await reopenFile({
    runId,
    id,
    file,
    reason,
    record,
    rows,
    key,
    writeRow,
    writeSigned,
    io: {
      owns: ownsAt(wsPrefix),
      loadState: (where) => loadState({ .../** @type {any} */ (where), runRootDir, contentRoot, migrate: false }),
      contentHash: (f) => contentHash(contentRoot, f),
      // C11 (synchronous): the cached result of the current content's ticket would repeat the stop
      clearDone: (f) => clearDone({ repoRoot: mainTop, run: runId, block: id, file: f, ...tree }).removed,
    },
  });
  return { row, cleared };
}

const USAGE =
  'usage: code-forge block open <id> --run <r> [--level L<n>] --owned <paths…> --acceptance <file> [--brief <file>] [--attempt <n>] [--base <sha>] [--lines <n>] [--kind code|docs|contract] [--tests <paths…>]\n' +
  '       code-forge block attempt|rebase <id> --run <r> · block close <id> --run <r> [--transcript <file>] [--report <file>] [--no-require-reviews] [--remove-worktree] · block claim <id> <path> --run <r> · block stop <id> --run <r> --reason <text>\n' +
  '       code-forge block waive <id> <finding-id> --run <r> --file <path> --reason <text> [--yes]\n' +
  '       code-forge block reopen <id> --run <r> --file <path> --reason <text>\n' +
  '       code-forge block accept-hit <id> --run <r> --rule <rule-id> --line <n> --transcript <path> --reason <text>\n' +
  '       every block verb takes --json: one JSON line on stdout ({"block","event",…}) instead of the text\n';

const SUBCOMMANDS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive', 'reopen', 'accept-hit'];

/** Refuse likely shell quoting slips before any row is written.
 * @param {string} workspace @param {string[]} entries @param {string} label
 */
function checkWhitespacePaths(workspace, entries, label) {
  for (const entry of entries) {
    if (!/\s/u.test(entry)) continue;
    let stat;
    try {
      stat = statSync(path.join(workspace, entry));
    } catch {
      // A missing or unreadable path cannot establish a literal whitespace-containing name.
    }
    if (!stat?.isFile() && !stat?.isDirectory()) {
      throw new StateError(label === 'owned files' ? 'bad-owned' : 'bad-tests', `${label}: invalid path ${JSON.stringify(entry)}: contains whitespace but names no existing file or directory`);
    }
  }
}
const FLAG_VALUES = ['run', 'level', 'acceptance', 'brief', 'attempt', 'base', 'lines', 'head', 'worker-pid', 'reason', 'file', 'transcript', 'kind', 'report', 'rule', 'line'];

/**
 * @param {string[]} args
 * @param {{
 *   stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}, probe?: import('../state/run.mjs').StartTimeProbe, now?: () => Date,
 *   isTTY?: boolean, stdinIsTTY?: boolean, stdoutIsTTY?: boolean,
 *   ui?: {confirm: (o: {message: string, initialValue?: boolean}) => Promise<unknown>, isCancel: (v: unknown) => boolean},
 *   removeBlockTree?: typeof removeBlockTree,
 *   resolveTicket?: typeof resolveTicket, reviewedLines?: typeof reviewedLines,
 *   waiverJudge?: import('../review/agent-waiver.mjs').WaiverJudge,
 * }} [deps] - `now` (B48): the clock of the autopilot expiry checks. `stdinIsTTY` / `stdoutIsTTY`
 *   (default `process.stdin.isTTY` / `process.stdout.isTTY`; `isTTY` sets both) and `ui` (default
 *   `@clack/prompts`): the human's confirmation (B52 `--no-require-reviews`, B80 `block waive` and
 *   `block reopen`), which needs both to be TTYs.
 *   `removeBlockTree` (B64b, tests only): the `--remove-worktree` step, default the real one.
 *   `resolveTicket` / `reviewedLines` (B96, tests only): the queue's ticket rule and the reviewed
 *   lines builder of the close, default the real ones.
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
    const { flags, positionals } = parseFlags(rest, { values: FLAG_VALUES, multi: ['owned', 'tests'], booleans: ['no-require-reviews', 'remove-worktree', 'json', ...(sub === 'waive' ? ['yes'] : [])] });
    // B64a: --json prints ONE JSON line on success instead of the text lines (`emit`, one object
    // per verb, built where the text is); without it every output is exactly as before (`say` is `out`)
    const json = flags.json === true;
    const say = json ? (/** @type {string} */ _s) => {} : out;
    const emit = (/** @type {Record<string, unknown>} */ obj) => out(`${JSON.stringify(obj)}\n`);
    // B60: --tests names exact paths (a file or a directory — PHPUnit and Pest take directories).
    // `*` and `?` are refused: a glob would reach the gate unexpanded. `[` and `{` are allowed,
    // since real paths use them (`app/[id]/page.test.tsx`) and argv never reaches a shell. A
    // leading "-" would be read as an option.
    for (const t of sub === 'open' && Array.isArray(flags.tests) ? flags.tests : []) {
      if (/[*?]/.test(t)) throw new StateError('usage', `--tests takes exact paths (a file or a directory); "*" and "?" are refused: ${JSON.stringify(t)}`);
      if (t.startsWith('-')) throw new StateError('usage', `--tests: a path may not start with "-": ${JSON.stringify(t)}`);
    }
    const [id, extra] = positionals;
    const runId = typeof flags.run === 'string' ? flags.run : undefined;
    if (!id || !runId) throw new StateError('usage', 'a block id and --run are required');
    if (sub === 'open' && (typeof flags.acceptance !== 'string' || !Array.isArray(flags.owned) || (flags.level !== undefined && typeof flags.level !== 'string'))) {
      throw new StateError('usage', 'block open needs --level, --owned and --acceptance');
    }
    if (sub !== 'close' && flags['remove-worktree'] === true) throw new StateError('usage', '--remove-worktree is a block close flag');
    if (sub === 'claim' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block claim needs exactly one path');
    if ((sub === 'stop' || sub === 'waive' || sub === 'reopen') && typeof flags.reason !== 'string') throw new StateError('usage', `block ${sub} needs --reason`);
    if (sub === 'waive' && (!extra || positionals.length !== 2)) throw new StateError('usage', 'block waive needs exactly one finding id');
    if (sub === 'reopen' && positionals.length !== 1) throw new StateError('usage', 'block reopen takes one block id (the file goes in --file)');
    if (sub === 'reopen' && typeof flags.file !== 'string') throw new StateError('usage', 'block reopen needs --file <path>');
    if (sub === 'waive' && typeof flags.file !== 'string') throw new StateError('usage', 'block waive needs --file <path> (a finding id is scoped to one file)');
    if (sub === 'accept-hit') {
      const { record, row } = await prepareAcceptHit({ runId, id, flags, positionals });
      const asked = await confirmHumanVerb(sub, runId, id, { rule: row.rule, line: row.line, file: /** @type {string} */ (flags.transcript) }, record, deps);
      if (asked === 'no-tty') { err(`${humanVerbNoTTY(sub)}\n`); return 2; }
      if (asked === 'cancelled') { err(`block ${id} ${sub}: cancelled — nothing was recorded\n`); return 1; }
      await writeAcceptedHit({ runId, row, writeRow: (signed) => appendRow(signed, { slug: record.project }) });
      say(`block ${id} accepted ${row.rule} transcript line ${row.line} (by: human)\n`);
      if (json) emit({ block: id, event: 'rule_break.accepted', rule: row.rule, line: row.line, reason: row.reason, by: 'human' });
      return 0;
    }
    const record = await readRun(runId);
    if (sub === 'open') {
      checkWhitespacePaths(record.workspace, /** @type {string[]} */ (flags.owned), 'owned files');
      if (Array.isArray(flags.tests)) checkWhitespacePaths(record.workspace, flags.tests, 'test files');
    }
    // B80: waive and reopen are the human's — asked at a terminal before anything is written (not
    // even the autopilot expiry rows below); no terminal is exit 2, a "no" is exit 1
    if ((sub === 'waive' && flags.yes !== true) || sub === 'reopen') {
      const asked = await confirmHumanVerb(sub, runId, id, { finding: extra, file: /** @type {string} */ (flags.file) }, record, deps);
      if (asked === 'no-tty') {
        err(`${humanVerbNoTTY(sub)}\n`);
        return 2;
      }
      if (asked === 'cancelled') {
        err(`block ${id} ${sub}: cancelled — nothing was recorded\n`);
        return 1;
      }
    }
    const writeRow =(/** @type {Record<string, any>} */ row) => appendRow(row, { slug: record.project });
    // Agent refusals precede even the lazy expiry checks: no rows on refusal.
    if (sub === 'waive' && flags.yes === true) {
      await waiveAsAgent({ runId, id, finding: extra, file: /** @type {string} */ (flags.file), reason: /** @type {string} */ (flags.reason), record, writeRow, judge: deps.waiverJudge });
      say(`block ${id} waived ${extra} in ${flags.file} (by: agent)\n`);
      if (json) emit({ block: id, event: 'waived', finding: extra, file: flags.file, by: 'agent' });
      return 0;
    }
    // B64b: the block-end signal — after an end's rows (and outside the run lock) the done file,
    // then the owner's `hooks.on_block_end` from the run-start snapshot. Without the hook key the
    // done file is the only addition: no row, and every output stays exactly as before.
    const hook = blockEndHook(record);
    // B96: `reviewed` (the close's tool lines) joins the done file only when there is one
    const ended = async (/** @type {'closed' | 'stopped'} */ status, /** @type {string} */ event, /** @type {string | null} */ reason, /** @type {import('../review/reviewed-lines.mjs').ReviewedLine[]} */ reviewed = []) => {
      // ruling (e): `ended` never throws — any failure here is the done-file warning and a null
      // `doneFile`, so the caller's try/finally still reaches the hook
      /** @type {string | null} */
      let doneFile = null;
      try {
        doneFile = await writeDoneFile({
          record,
          id,
          status,
          event,
          reason: typeof reason === 'string' ? redact(reason) : null,
          openFindings: (rows) => blockOpenFindings(record, rows, id),
          err,
          ...(reviewed.length > 0 ? { reviewed } : {}),
        });
      } catch (thrown) {
        const code = /** @type {any} */ (thrown)?.code;
        err(`warning: done file not written: ${typeof code === 'string' && /^[A-Z0-9_]{1,32}$/.test(code) ? code : 'UNKNOWN'}\n`);
        doneFile = null;
      }
      return {
        json: hook === null ? {} : { done_file: doneFile },
        runHook: async () => {
          if (hook === null) return;
          const top = await repoTop(record.workspace).catch(() => record.workspace);
          await runBlockEndHook({ hook, runId, id, status, doneFile: doneFile ?? '', repoTop: top, writeRow, err });
        },
      };
    };
    // B48: every block command runs the lazy autopilot checks (grant expiry, expired approvals);
    // a failed check is a warning, never a reason to stop the block command
    try {
      err(approvalCheckText(await expiryChecks(runId, (deps.now ?? (() => new Date()))(), { writeRow })));
    } catch (thrown) {
      err(`autopilot: WARN the expiry check failed (${thrown?.message ?? String(thrown)})\n`);
    }

    if (sub === 'open') {
      // an explicit --level wins; B59: else in a brief-mode run the rules lane; B47: else the level
      // autopilot chose for the block (a signed autopilot.level row)
      /** @type {{lane: string, facts: ReturnType<typeof laneFacts>} | null} */
      let rules = null;
      /** @type {string | null} */
      let level;
      // B59: only the rules lane parses --lines this early; every other open parses it where it
      // always was (after the level check and --attempt), so its errors keep their pre-B59 order
      /** @type {number | null | undefined} */
      let lines;
      if (typeof flags.level === 'string') {
        level = flags.level;
      } else if (record.mode === 'brief') {
        lines = intFlag(flags.lines, 'lines') ?? null;
        rules = rulesLane(record, /** @type {string[]} */ (flags.owned), lines);
        level = rules.lane;
      } else {
        level = await autopilotLevel(runId, id, record);
      }
      if (level === null) throw new StateError('usage', `no --level given and no autopilot level recorded for ${id}`);
      const acceptance = parseYAML(await readFile(/** @type {string} */ (flags.acceptance), 'utf8'), { prettyErrors: false });
      let brief;
      if (typeof flags.brief === 'string') {
        const absolute = await realpath(flags.brief);
        const relative = path.relative(record.workspace, absolute);
        if (relative.startsWith('..') || path.isAbsolute(relative)) throw new StateError('usage', '--brief must be a file inside the workspace');
        brief = { path: relative, content: await readFile(absolute) };
      }
      // B62a: the run's isolation, from its config snapshot (fixed for the run)
      const isolated = worktreeSettings(record);
      const { block, pointer, worktreeReused } = await openBlock({
        runId,
        id,
        level,
        owned: /** @type {string[]} */ (flags.owned),
        acceptance,
        attempt: intFlag(flags.attempt, 'attempt'),
        base: typeof flags.base === 'string' ? flags.base : undefined,
        lines: lines === undefined ? (intFlag(flags.lines, 'lines') ?? null) : lines,
        brief,
        ...(rules ? { decision: { question: 'lane', answer: rules.lane, source: 'rules', facts: rules.facts } } : {}),
        ...(typeof flags.kind === 'string' ? { kind: flags.kind } : {}),
        ...(Array.isArray(flags.tests) ? { tests: flags.tests } : {}),
        cfg: await openConfig(record.workspace),
        writeRow,
        ...(isolated ? { worktree: (/** @type {{baseSha: string, previous: any}} */ ctx) => prepareWorktree({ cwd: record.workspace, settings: isolated, runId, id, ...ctx }) } : {}),
      });
      const raised = block.level !== level ? ` (${block.kind} floor, asked ${level})` : '';
      const chosen = typeof flags.level === 'string' ? '' : rules ? ' (rules lane)' : ' (autopilot level)';
      const testCount = Array.isArray(block.test_files) ? block.test_files.length : 0;
      const tests = testCount > 0 ? ` · ${testCount} test file${testCount === 1 ? '' : 's'}` : '';
      say(`block ${id} open · ${block.level}${raised}${chosen} · ${block.kind} · attempt ${block.attempt} · base ${block.base_sha.slice(0, 12)}${tests}\n`);
      if (block.worktree) say(`worktree ${block.worktree.path} · branch ${block.worktree.branch}${worktreeReused ? ' · reused' : ''}\n`);
      say(`${pointer ?? 'brief: none given (--brief <file>)'}\n`);
      if (json) {
        emit({
          block: id,
          event: 'open',
          level: block.level,
          asked: level ?? null,
          source: typeof flags.level === 'string' ? 'flag' : rules ? 'rules' : 'autopilot',
          kind: block.kind,
          attempt: block.attempt,
          base_sha: block.base_sha,
          tests: testCount,
          worktree: block.worktree ? { path: block.worktree.path, branch: block.worktree.branch, reused: Boolean(worktreeReused) } : null,
          brief: pointer ?? null,
        });
      }
      return 0;
    }
    if (sub === 'attempt') {
      const n = await attemptBlock({ runId, id, writeRow });
      say(`block ${id} attempt ${n}\n`);
      if (json) emit({ block: id, event: 'attempt', attempt: n });
      return 0;
    }
    if (sub === 'rebase') {
      const moved = await rebaseBlock({ runId, id, head: typeof flags.head === 'string' ? flags.head : undefined, writeRow });
      say(`block ${id} rebased ${moved.old_base.slice(0, 12)} → ${moved.new_base.slice(0, 12)}\n`);
      if (json) emit({ block: id, event: 'rebased', old_base: moved.old_base, new_base: moved.new_base });
      return 0;
    }
    if (sub === 'claim') {
      await claimPath({ runId, id, file: extra, writeRow });
      say(`block ${id} claimed ${extra}\n`);
      if (json) emit({ block: id, event: 'claimed', path: extra });
      return 0;
    }
    if (sub === 'stop') {
      await stopBlock({ runId, id, reason: /** @type {string} */ (flags.reason), writeRow });
      const end = await ended('stopped', 'block.stop', /** @type {string} */ (flags.reason));
      try {
        say(`block ${id} stopped\n`);
        if (json) emit({ block: id, event: 'stopped', ...end.json });
        return 0;
      } finally {
        await end.runHook();
      }
    }
    if (sub === 'waive') {
      await waiveFinding({ runId, id, finding: extra, reason: /** @type {string} */ (flags.reason), file: /** @type {string} */ (flags.file), writeRow });
      say(`block ${id} waived ${extra} in ${flags.file} (by: human)\n`);
      if (json) emit({ block: id, event: 'waived', finding: extra, file: flags.file, by: 'human' });
      return 0;
    }
    if (sub === 'reopen') {
      const file = /** @type {string} */ (flags.file);
      const { row, cleared } = await reopenBlockFile({ runId, id, file, reason: /** @type {string} */ (flags.reason), record, writeRow });
      const fromText = row.stop_reason == null ? row.from : `${row.from} ${row.stop_reason}`;
      say(`block ${id} reopened ${file} (from ${fromText}; by: human)\n`);
      if (json) emit({ block: id, event: 'reopened', file, from: row.from, stop_reason: row.stop_reason, at_seq: row.at_seq, from_hash: row.from_hash, to_hash: row.to_hash, ticket_cleared: cleared, by: 'human' });
      return 0;
    }
    if (sub === 'close') {
      const entry = record.blocks?.[id];
      if (!entry) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
      // B52: a block that is not open is refused here, with or without --no-require-reviews, by
      // the same check and message as `closeBlock` — and the owner is never asked about it
      if (entry.status !== 'open') throw new StateError('no-block', `block ${id} is not open in run ${runId}`);
      // B62a: removing a tree the block does not have is a usage error, before anything runs
      const removeTree = flags['remove-worktree'] === true;
      if (removeTree && !entry.worktree) throw new StateError('usage', `block ${id} has no worktree (proof.isolation is not worktree); --remove-worktree does not apply`);
      // B62b: the block's tree is where its work is measured — a tree that is gone refuses the close
      // before anything is asked or written
      const wt = await blockTree(record.workspace, entry).catch((thrown) => {
        if (thrown?.code === 'worktree-missing') {
          throw new StateError('worktree-missing', `block ${id}: its worktree ${entry.worktree.path} is gone (removed or moved), so its work cannot be measured — restore it on branch ${entry.worktree.branch} (git worktree prune, then git worktree add ${entry.worktree.path} ${entry.worktree.branch}) and close again; nothing was recorded`);
        }
        if (typeof thrown?.code === 'string' && thrown.code.startsWith('worktree-')) throw new StateError(thrown.code, `block ${id}: ${thrown.message}, so its work cannot be measured; nothing was recorded`);
        throw thrown;
      });
      // ONE base for the whole close: a worktree block is measured against the commit its tree was
      // made from (B62a keeps it equal to the block's base_sha: a re-open keeps it, a rebase is refused)
      const base = wt !== null ? wt.base_sha : entry.base_sha;
      const reviewsWaived = flags['no-require-reviews'] === true;
      // B52: the human's way out is confirmed by the human, at a terminal, before anything is written
      if (reviewsWaived) {
        const asked = await confirmNoReviews(runId, id, deps);
        if (asked === 'cancelled') {
          err(`block ${id} close: cancelled — the review checks stay on\n`);
          return 1;
        }
      }
      // B58: the owner's other way out — `review.external.required` in the run's config snapshot
      // (set before `run start`, immutable): no TTY, allowed under autopilot, the human flag wins
      const external = reviewsWaived ? null : externalReviewer(record);
      const rows = (await readAllRows(record.project)).filter((row) => row.run === runId);
      // B52: ONE path base and ONE listing, memoised as a promise and shared by every check. The
      // base is the repository top level: the worker signs every review row with a repo-root-
      // relative `file` and keeps its queue and signed result files under `<top>/.code-forge/`
      // (`repoRootOf`), and `tree_at_open` is stamped there too. An owned entry matches a path
      // as given or, for a workspace in a subdirectory, relative to the workspace (`ownsAt`).
      // A failure here is the usual `git-failed` refusal. B62b: for a block with a worktree the
      // listing and the content are the tree's (`top`); `mainTop` keeps the run's state.
      /** @type {Promise<{top: string, mainTop: string, where: string, inTree: boolean, wsPrefix: string, owns: (owned: ReadonlyArray<string>, file: string) => boolean, changed: string[], fileSet: Array<{file: string, content_hash: string}>, reviewed: string[]}> | undefined} */
      let treeOnce;
      const tree = () =>
        (treeOnce ??= (async () => {
          const mainTop = await repoTop(record.workspace);
          const inTree = wt !== null;
          const top = inTree ? wt.top : mainTop;
          const where = inTree ? `the worktree ${wt.path}` : 'the workspace';
          // both sides as realpaths: git prints the top resolved (/private/var on macOS) while the
          // run record may keep a symlinked spelling (/var)
          const real = async (/** @type {string} */ p) => realpath(p).catch(() => path.resolve(p));
          const wsPrefix = path.relative(await real(mainTop), await real(record.workspace)).split(path.sep).join('/');
          const owns = ownsAt(wsPrefix);
          const isMine = (/** @type {string} */ f) => owns(entry.owned_files, f, entry.owned_kinds);
          const changed = await changedFiles({ repoRoot: top, base });
          // the file set is the diff since base ∪ every owned file with review rows of this block —
          // work the diff cannot see (committed before `block open`, done in another worktree) is
          // still checked, at its content in the workspace (in the block's worktree when it has one)
          const reviewed = reviewedFiles(rows, id).filter(isMine);
          const fileSet = await blockFileSet({ repoRoot: top, base, owned: entry.owned_files, matchOwned: isMine, changed: [...new Set([...changed, ...reviewed])].sort() });
          return { top, mainTop, where, inTree, wsPrefix, owns, changed, fileSet, reviewed };
        })());
      // B58: the config waiver row is written by `closeBlock` under the run lock, just before
      // `block.close` and only when every other check passed — never by a refused close
      /** @type {Record<string, any>[]} */
      const closeRows = external === null ? [] : [{ event: 'gate.reviews_waived', block: id, by: 'config', reviewer: external }];
      /** @type {import('../review/gate-check.mjs').WaivedAll[]} */
      let waivedAll = [];
      /** @type {import('../review/accept-hit.mjs').AcceptedHit[]} */
      let acceptedHits = [];
      // B70: a file whose review left only waived findings passes as `reviewed_by_waiver`; its signed
      // `gate.review_waived_all` row joins the close rows (written just before `block.close`, only
      // when the block closes — a refused close writes none)
      // B96: whether the review gate passed — set from the gate's own result (below), so the files
      // it checked are the ones it accepted and the tool can give their reviewed lines
      let reviewsPassed = false;
      const onWaivedAll = (/** @type {import('../review/gate-check.mjs').WaivedAll[]} */ files) => {
        // idempotent: the waived rows are rebuilt from the final list, never appended per call
        // (closeBlock reads closeRows only after every extra check passed)
        waivedAll = files;
        const others = closeRows.filter((r) => r.event !== 'gate.review_waived_all');
        closeRows.splice(0, closeRows.length, ...others, ...files.map((f) => ({ event: 'gate.review_waived_all', block: id, file: f.file, content_hash: f.content_hash, findings: f.findings, by: f.by, ...(f.answered?.length ? { answered: f.answered } : {}) })));
      };
      const gate = reviewGateCheck(async () => {
        const transcript = await findTranscript(record, id, typeof flags.transcript === 'string' ? flags.transcript : null);
        if (transcript === null) {
          await writeSigned(runId, writeRow, { event: 'gate.transcript_missing', block: id });
          err(`WARN block ${id}: no coder transcript found (--transcript, run record, .code-forge/runs/${runId}/${id}.log); the transcript grep did not run\n`);
        }
        // the human's way out of the per-file review check leaves a signed audit row before the close
        if (reviewsWaived) await writeSigned(runId, writeRow, { event: 'gate.reviews_waived', block: id, by: 'human' });
        // B20: the high paths come first — a base the workspace does not know refuses the close here
        const highPaths = await highPathsAt(record.workspace, base);
        const { top, mainTop, where, inTree, wsPrefix, owns, changed, fileSet, reviewed } = await tree();
        const isMine = (/** @type {string} */ f) => owns(entry.owned_files, f, entry.owned_kinds);
        const key = await loadKey(runId);
        // B52: an empty diff with review rows is never silent — name the likely cause
        if (reviewed.length > 0 && !changed.some(isMine)) {
          const likely = inTree ? 'done outside the block\'s worktree (in the main checkout or another tree)' : 'committed before block open or done in another worktree';
          err(`WARN block ${id}: no owned file differs from the block base in ${where}, but ${reviewed.length} owned file(s) have review rows — the work was likely ${likely}; they are checked at their content in ${where}\n`);
        }
        // B52: only code-forge's own state is exempt — `<top>/.code-forge/`, where the worker keeps
        // its queue and signed results — never a `--transcript` or `--report` path in the source
        // tree. For a workspace in a subdirectory, `<workspace>/.code-forge/` is source tree like any
        // other path, except the two files code-forge itself keeps there at fixed names: this run's
        // record mirror and this block's default transcript (`.code-forge/runs/<run>.json`,
        // `.code-forge/runs/<run>/<id>.log`)
        const wsRel = (/** @type {string} */ p) => path.posix.join(wsPrefix, p);
        const ownState = new Set([wsRel(`.code-forge/runs/${runId}.json`), wsRel(`.code-forge/runs/${runId}/${id}.log`)]);
        // B62b: a block's own tree started clean at its base and no other block works in it — no
        // stamp from the main tree and no other block's ownership exempts a change there
        const scope = await unownedChanges({ top, changed: changed.filter((f) => !ownState.has(f)), id, blocks: record.blocks, rows, key, isMine, owns, stateDir: '.code-forge', ownTree: inTree });
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
          resultFiles: await resultFilesFor({ repoRoot: mainTop, runId, block: id, rows }),
          files: reviewsWaived || external !== null ? [] : fileSet,
          // B52: nothing changed and nothing reviewed is never a pass (the owner's way out is
          // --no-require-reviews); B58: `review.external` does not lift it
          noChanges: !reviewsWaived && fileSet.length === 0,
          // B20: high-tier files need a proven red→green row; `--no-require-reviews` does not lift it
          proofFiles: fileSet,
          highPaths,
        };
      }, { onWaivedAll, onAcceptedHits: (hits) => { acceptedHits = hits; } });
      // B96: `reviewsPassed` comes from the gate's own result, not from the onWaivedAll call. Each
      // extra check runs at most once per close (`closeBlock`'s one loop, src/state/block.mjs:562,
      // and `closeBlock` is called once below), so the gate's one pass is its final one.
      const reviews = async () => {
        const result = await gate();
        reviewsPassed = result.ok;
        return result;
      };
      // B96: the stderr warnings of the reviewed lines (`WARN no ticket for <path>`, `WARN bad
      // ticket for <path>`, `WARN reviewed lines unavailable: <message>`, `WARN report ticket
      // mismatch <path>`): kept here and printed only when the block closes, before the reviewed
      // lines — a refused close prints none
      /** @type {string[]} */
      const lineWarnings = [];
      // B96: the tool's reviewed lines — one per file the review gate accepted, with the queue's
      // ticket for that file at the hash the gate accepted (`resolveTicket`, the queue's own rule:
      // the same path check, tree resolution and ticket id); a file whose content moved since the
      // gate hashed it, or whose ticket cannot be given, has no line (fail closed, `WARN no ticket
      // for <path>`), and so has a file whose ticket is not 24 lowercase hex (`WARN bad ticket for
      // <path>`). None when the reviews were waived (the human flag or `review.external`) or the
      // gate has not passed — and that empty answer is never memoised: the lines are built once,
      // from the gate's pass (its file set and hashes, its waivedAll). It never throws: an error
      // gives no lines and one held `WARN reviewed lines unavailable: <message>`, so the done file
      // and the on_block_end hook of a closed block always follow.
      /** @type {Promise<import('../review/reviewed-lines.mjs').ReviewedLine[]> | undefined} */
      let linesOnce;
      const toolLines = async () => {
        if (!reviewsPassed || reviewsWaived || external !== null) return [];
        linesOnce ??= (async () => {
          try {
            const { mainTop, top, inTree, fileSet } = await tree();
            const resolve = deps.resolveTicket ?? resolveTicket;
            const ticketOf = (/** @type {{file: string, content_hash: string}} */ f) => {
              try {
                const t = resolve({ repoRoot: mainTop, run: runId, block: id, file: f.file, tree: inTree ? top : null });
                return t.content_hash === f.content_hash ? t.ticket : null;
              } catch {
                return null;
              }
            };
            return await (deps.reviewedLines ?? reviewedLines)({
              files: fileSet,
              waivedAll,
              ticketOf,
              onNoTicket: (file) => lineWarnings.push(`WARN no ticket for ${file}\n`),
              onBadTicket: (file) => lineWarnings.push(`WARN bad ticket for ${file}\n`),
            });
          } catch (thrown) {
            lineWarnings.push(`WARN reviewed lines unavailable: ${thrown instanceof Error ? thrown.message : String(thrown)}\n`);
            return [];
          }
        })();
        return linesOnce;
      };
      // B36 → B96: the coder's report. The tool gives the reviewed lines, so a missing hand line is
      // a refusal only for a file the tool has no line for; a hand line that names another ticket
      // is a warning and the close goes on (D1: the tool lines are the record)
      const report = async () => {
        if (typeof flags.report !== 'string' || reviewsWaived || external !== null) return { ok: true };
        let text;
        try {
          text = await readFile(flags.report, 'utf8');
        } catch {
          return { ok: false, reason: 'coder report FAILED: --report cannot be read' };
        }
        const { fileSet } = await tree();
        const lines = await toolLines();
        const given = new Set(lines.map((l) => l.file));
        const missing = missingReviewTickets(text, fileSet.map((f) => f.file)).filter((f) => !given.has(f));
        if (missing.length > 0) return { ok: false, reason: `coder report FAILED: no review-file ticket id for ${missing.join(', ')}` };
        for (const file of reportMismatches(text, lines)) lineWarnings.push(`WARN report ticket mismatch ${file}\n`);
        return { ok: true };
      };
      /** @type {Array<() => Promise<{ok: boolean, reason?: string}>>} */
      const extraChecks = [reviews, report];
      // B63b: `context.require_coder_tests` in the run's config snapshot (never the live config)
      // needs the coder's green test run — waived reviews do not lift it. It runs FIRST: it only
      // reads the report, so a refusal here leaves no row (not even the human waiver row the review
      // check writes). Absent or false: nothing is added, the close is exactly as before.
      if (requireCoderTests(record)) {
        extraChecks.unshift(async () => {
          const tests = await coderTests(flags.report, id);
          if (!tests.ok) return tests;
          // signed under the run lock by `closeBlock`, after the waiver row, only when the block closes
          closeRows.push({ event: 'coder.tests', block: id, command: redact(tests.command), exit: tests.exit });
          return { ok: true };
        });
      }
      // B64b: the hook command is the orchestrator's, never a coder's — a coder transcript that runs
      // it refuses the close. Only with the hook configured (the list is otherwise unchanged); it
      // only reads the transcript, so it runs first and a refusal here leaves no row.
      if (hook !== null) {
        extraChecks.unshift(async () => {
          const text = await findTranscript(record, id, typeof flags.transcript === 'string' ? flags.transcript : null);
          const [hit] = text === null ? [] : scanTranscript(text, hookForbidden(hook.argv));
          return hit ? { ok: false, reason: `rule_break transcript line ${hit.line}: ${hit.id}` } : { ok: true };
        });
      }
      const result = await closeBlock({ runId, id, rows, writeRow, livePid: intFlag(flags['worker-pid'], 'worker-pid'), probe, extraChecks, closeRows });
      const agentRows = agentWaivers(rows, id, await loadKey(runId));
      // B64b: the block ended (closed, or stopped by a worker pin event or ledger.tamper) — its rows
      // are written and the run lock released; the done file is written before the tree is removed
      // B96: the tool's reviewed lines of a block that closed (also kept in its done file)
      const reviewedJson = result.ok ? await toolLines() : [];
      const end = result.status === 'closed' || result.status === 'stopped' ? await ended(result.status, result.event ?? (result.status === 'stopped' ? 'block.stop' : 'block.close'), result.reason ?? null, reviewedJson) : null;
      // B64b ruling (e): the hook runs exactly once for this end, after everything else the verb
      // does — also when a later step (the output, `--remove-worktree`) throws; it never changes
      // the exit code or stdout (`runHook` never throws)
      try {
        // B96: the reviewed lines (sorted by path) come first, then the reviewed_by_waiver lines
        // B70: each file that passed as reviewed_by_waiver is named before the closed line
        // B71: an answered finding is named as answered; a file with only answered findings has no `by`
        if (result.ok) {
          if (configInForce(record)?.gates?.test != null && lastGate(rows.filter((row) => row.block === id)).result !== 'pass') {
            err(`WARN block ${id}: no green gates run for this block; run gates run --run ${runId} --block ${id} before close\n`);
          }
          for (const w of lineWarnings) err(w);
          for (const l of reviewedJson) say(`reviewed ${l.file} ticket ${l.ticket}\n`);
          for (const f of waivedAll) {
            const parts = f.findings.length > 0 ? [`${f.findings.join(', ')}; by ${f.by}`] : [];
            if (f.answered?.length) parts.push(`answered ${f.answered.join(', ')}`);
            say(`block ${id}: ${f.file} reviewed_by_waiver (${parts.join('; ')})\n`);
          }
          for (const w of agentRows) say(`block ${id}: agent waiver ${w.file} ${w.finding} reason: ${w.reason}; verdict: ${w.verdict}\n`);
          for (const hit of acceptedHits) say(`accepted rule_break ${hit.rule} transcript line ${hit.line}: ${hit.reason} (by: human)\n`);
        }
        (result.ok ? say : err)(`block ${id} ${result.status}${result.event ? ` (${result.event})` : ''}${result.reason ? `: ${result.reason}` : ''}\n`);
        // B64a: a refused close also names itself on stdout under --json (its stderr is unchanged)
        if (json && !result.ok) emit({ block: id, ok: false, reason: result.reason ?? null, ...(end?.json ?? {}) });
        const closedJson = {
          block: id,
          event: result.status,
          status: result.status,
          reason: result.reason ?? null,
          close_event: result.event ?? null,
          ...(end?.json ?? {}),
          // B70: only when a file passed as reviewed_by_waiver (otherwise the output is unchanged)
          ...(result.ok && waivedAll.length > 0 ? { reviewed_by_waiver: waivedAll.map((f) => ({ file: f.file, content_hash: f.content_hash, status: f.status, findings: f.findings, by: f.by, ...(f.answered?.length ? { answered: f.answered } : {}) })) } : {}),
          // B96: only when the tool gave reviewed lines (a waived or external close is unchanged)
          ...(reviewedJson.length > 0 ? { reviewed: reviewedJson } : {}),
          ...(agentRows.length > 0 ? { agent_waivers: agentRows } : {}),
          ...(acceptedHits.length > 0 ? { accepted_hits: acceptedHits } : {}),
        };
        if (!result.ok || !removeTree) {
          if (json && result.ok) emit(closedJson);
          return result.ok ? 0 : 1;
        }
        const removed = await (deps.removeBlockTree ?? removeBlockTree)(runId, id, entry.worktree, record.workspace, writeRow, say, err);
        // a failed removal is exit 1 with its stderr text and nothing on stdout, like every failure
        if (json && removed === 0) {
          // built from the block's own tree entry; `removed` is what removeBlockTree reported (0 = removed)
          const treeJson = entry.worktree ? { removed: removed === 0, path: entry.worktree.path, branch: entry.worktree.branch } : null;
          emit({ ...closedJson, worktree: treeJson });
        }
        return removed;
      } finally {
        await end?.runHook();
      }
    }
  } catch (thrown) {
    err(`block ${sub ?? ''}: ${thrown?.message ?? String(thrown)}\n`);
    return thrown instanceof StateError && thrown.code === 'usage' ? 2 : 1;
  }
  err(USAGE);
  return 2;
}

/**
 * B62a: `block close --remove-worktree`, after the block closed: `git worktree remove <path>` (no
 * `--force`, the branch is kept), then a signed `worktree.removed {block, path, branch}` or
 * `worktree.remove_failed {block, path, reason}` row. A failed removal (a tree with modified or
 * untracked files) leaves the tree where it is and the block closed, and is exit 1.
 * @param {string} runId @param {string} id @param {{path: string, branch: string, base_sha: string}} worktree
 * @param {string} workspace @param {(row: Record<string, any>) => Promise<unknown>} writeRow
 * @param {(s: string) => unknown} out @param {(s: string) => unknown} err
 * @returns {Promise<number>}
 */
async function removeBlockTree(runId, id, worktree, workspace, writeRow, out, err) {
  const failed = await removeWorktree({ cwd: workspace, worktree });
  if (failed === null) {
    await writeSigned(runId, writeRow, { event: 'worktree.removed', block: id, path: worktree.path, branch: worktree.branch });
    out(`block ${id} worktree removed: ${worktree.path} (branch ${worktree.branch} kept)\n`);
    return 0;
  }
  await writeSigned(runId, writeRow, { event: 'worktree.remove_failed', block: id, path: worktree.path, reason: failed });
  err(`block ${id} worktree NOT removed: ${failed} — the block stays closed; the tree stays at ${worktree.path} (commit or clean it, then git worktree remove it)\n`);
  return 1;
}

/**
 * B63b: the coder's green test run, read from its final report — the block's LAST sentinel must
 * be COMPLETE, with a `TESTS <command> exit=0` line before it (`parseSentinel`).
 * @param {unknown} reportFlag - the `--report` value (absent, or `true` for a valueless flag).
 * @param {string} id
 * @returns {Promise<{ok: true, command: string, exit: number} | {ok: false, reason: string}>}
 */
async function coderTests(reportFlag, id) {
  if (typeof reportFlag !== 'string') return { ok: false, reason: 'coder report FAILED: context.require_coder_tests needs --report' };
  let text;
  try {
    text = await readFile(reportFlag, 'utf8');
  } catch {
    return { ok: false, reason: 'coder report FAILED: --report cannot be read' };
  }
  const sentinel = parseSentinel(text, id);
  if (sentinel?.status !== 'complete') return { ok: false, reason: `coder report FAILED: no ===BLOCK ${id} COMPLETE=== sentinel` };
  // Keep diagnostics in the same segment as parseSentinel: any block's sentinel ends it.
  let malformed = null;
  let lastMalformed = null;
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim();
    const end = parseSentinel(line);
    if (end) {
      if (end.blockId === id) lastMalformed = malformed;
      malformed = null;
    } else if (line.startsWith('TESTS ') && !parseSentinel(`${line}\n===BLOCK ${id} COMPLETE===`, id)?.tests) {
      malformed = line.slice(0, 120);
    }
  }
  if (lastMalformed !== null) return { ok: false, reason: `coder report FAILED: TESTS line malformed: ${lastMalformed}` };
  if (!sentinel.tests) return { ok: false, reason: 'coder report FAILED: no TESTS line' };
  if (sentinel.tests.exit !== 0) return { ok: false, reason: `coder report FAILED: TESTS exit=${sentinel.tests.exit}` };
  return { ok: true, command: sentinel.tests.command, exit: 0 };
}

/** B52: the refusal when `--no-require-reviews` has no terminal to ask the human at. */
export const NO_REVIEWS_NO_TTY = '--no-require-reviews needs the human at a terminal to confirm; the block stays open (there is no --yes)';

/**
 * B80: the refusal when `block waive` or `block reopen` has no terminal to ask the human at.
 * @param {'waive' | 'reopen' | 'accept-hit'} verb @returns {string}
 */
export const humanVerbNoTTY = (verb) => verb === 'waive'
  ? 'block waive needs the human at a terminal to confirm; nothing was recorded (--yes works only for a recorded warning or nit finding, with evidence)'
  : `block ${verb} needs the human at a terminal to confirm; nothing was recorded (there is no --yes)`;

/**
 * @typedef {{isTTY?: boolean, stdinIsTTY?: boolean, stdoutIsTTY?: boolean, ui?: {confirm: (o: {message: string, initialValue?: boolean}) => Promise<unknown>, isCancel: (v: unknown) => boolean}, now?: () => Date}} ConfirmDeps
 */

/**
 * The ONE human confirm of the `block` verb (B52 `--no-require-reviews`, B80 `block waive` and
 * `block reopen`): a flag or a verb can be run by any process (the orchestrator, a script, a
 * coder), so the human answers a yes/no question at a terminal and there is NO `--yes`. No
 * terminal — stdin AND stdout must both be TTYs (a piped stdout means nobody may see the
 * question) — is `no-tty`, decided before anything is read or asked. `before` runs between the
 * terminal check and the question (a refusal it throws means the human is never asked).
 * `deps.stdinIsTTY` / `deps.stdoutIsTTY` inject each side, `deps.isTTY` both at once, `deps.ui`
 * the prompt (default `@clack/prompts`). A "no" or a cancel (Ctrl-C) is `cancelled`.
 * @param {ConfirmDeps} deps @param {string} message
 * @param {() => Promise<unknown> | unknown} [before]
 * @returns {Promise<'no-tty' | 'confirmed' | 'cancelled'>}
 */
async function confirmAtTerminal(deps, message, before) {
  const stdinTTY = deps.stdinIsTTY ?? deps.isTTY ?? process.stdin.isTTY === true;
  const stdoutTTY = deps.stdoutIsTTY ?? deps.isTTY ?? process.stdout.isTTY === true;
  if (!(stdinTTY && stdoutTTY)) return 'no-tty';
  if (before) await before();
  const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
  const go = await ui.confirm({ message, initialValue: false });
  return ui.isCancel(go) || go !== true ? 'cancelled' : 'confirmed';
}

/**
 * B52: `--no-require-reviews` lifts every per-file review check, so it is the human's alone — the
 * same rule as `autopilot approve` (`confirmAtTerminal`). No terminal ⇒ `usage` (exit 2); an
 * active autopilot grant on the run ⇒ `autopilot-active` (exit 1; the owner stops the grant
 * first) — nothing is written either way.
 * @param {string} runId @param {string} id @param {ConfirmDeps} deps
 * @returns {Promise<'confirmed' | 'cancelled'>}
 * @throws {StateError} `usage`, `autopilot-active`
 */
async function confirmNoReviews(runId, id, deps) {
  const asked = await confirmAtTerminal(deps, `Close block ${id} of run ${runId} WITHOUT the per-file review checks? Only the owner may say yes.`, async () => {
    const grant = await checkExpiry(runId, (deps.now ?? (() => new Date()))());
    if (grant.state === 'active') {
      throw new StateError('autopilot-active', `--no-require-reviews is refused while an autopilot grant is active on run ${runId} (stop it first: code-forge autopilot stop --run ${runId})`);
    }
  });
  if (asked === 'no-tty') throw new StateError('usage', NO_REVIEWS_NO_TTY);
  return asked;
}

/**
 * B80: `block waive` and `block reopen` are the human's alone (D1 = a): the human confirms at a
 * terminal through `confirmAtTerminal`, exactly like `--no-require-reviews`, but an active
 * autopilot grant changes nothing (autopilot waives through its own path, `by: autopilot`). The
 * pre-checks (`reopenPrecheck`, `waivePrecheck`) run first, at a terminal, so the human is never
 * asked about a waive or reopen the verb would refuse that way.
 * @param {'waive' | 'reopen' | 'accept-hit'} verb @param {string} runId @param {string} id
 * @param {{finding?: string, file: string, rule?: string, line?: number}} what @param {Record<string, any>} record @param {ConfirmDeps} deps
 * @returns {Promise<'no-tty' | 'confirmed' | 'cancelled'>}
 */
function confirmHumanVerb(verb, runId, id, what, record, deps) {
  const message =
    verb === 'waive'
      ? `Waive finding ${what.finding} in ${what.file} of block ${id} (run ${runId})? Only the owner may say yes.`
      : verb === 'accept-hit'
        ? `Accept false transcript hit ${what.rule} at line ${what.line} in ${what.file} of block ${id} (run ${runId})? Only the owner may say yes.`
        : `Reopen the review of ${what.file} in block ${id} (run ${runId})? Only the owner may say yes.`;
  return confirmAtTerminal(deps, message, async () => {
    if (verb === 'reopen') await reopenPrecheck(record, runId, id, what.file);
    else if (verb === 'waive') await waivePrecheck(record, runId, id, /** @type {string} */ (what.finding), what.file);
  });
}

/**
 * Does the block own `file`, by the rule both verbs use (`ownsAt` with the workspace's prefix
 * inside its repo)?
 * @param {Record<string, any>} record @param {Record<string, any>} entry @param {string} file
 * @returns {Promise<boolean>}
 */
async function blockOwnsFile(record, entry, file) {
  const real = async (/** @type {string} */ p) => realpath(p).catch(() => path.resolve(p));
  const mainTop = await real(await repoTop(record.workspace));
  const wsPrefix = path.relative(mainTop, await real(record.workspace)).split(path.sep).join('/');
  const owned = Array.isArray(entry.owned_files) ? entry.owned_files : [];
  return ownsAt(wsPrefix)(owned, file, entry.owned_kinds);
}

/**
 * B80: the checks a reopen must pass BEFORE the human is asked, in `reopenFile`'s order: the
 * `--file` form (`checkFileForm`, the reopen rule: `usage`), the block (`openBlockEntry`:
 * `unknown_block`, `reopen-block-not-open`) and ownership (`reopen-not-owned`, the same message).
 * The state checks that read the key and the ledger stay the verb's.
 * @param {Record<string, any>} record @param {string} runId @param {string} id @param {string} file
 * @returns {Promise<void>}
 * @throws {StateError}
 */
async function reopenPrecheck(record, runId, id, file) {
  checkFileForm(file, { normalized: true });
  const entry = openBlockEntry(record, runId, id);
  if (!(await blockOwnsFile(record, entry, file))) throw new StateError('reopen-not-owned', `reopen-not-owned: block ${id} does not own ${file}; nothing was recorded`);
}

/**
 * B80: the checks a waive must pass BEFORE the human is asked, so the human is never asked about a
 * waive the verb would refuse, in `waiveFinding`'s order: the finding id and `--file` have the
 * forms `waiveFinding` takes (`checkFindingId`, `checkFileForm`: `usage`, the same helpers); the
 * block is in the run (`unknown_block`); the block owns the file
 * (`waive-not-owned`, the reopen's ownership rule through `ownsAt`); and, except for the proof
 * finding (`proof`, which no review row names), a review row of this run and block for the file
 * names the id (`waive-unknown-finding`): `review.triage` / `review.late_finding` `finding`, a
 * `review.result` `open` id (as the gate reads it, `openFindings`) or a `review.cap` `open` id.
 * @param {Record<string, any>} record @param {string} runId @param {string} id
 * @param {string} finding @param {string} file
 * @returns {Promise<void>}
 * @throws {StateError}
 */
async function waivePrecheck(record, runId, id, finding, file) {
  checkFindingId(finding);
  checkFileForm(file, { normalized: false });
  const entry = record.blocks?.[id];
  if (!entry) throw new StateError('unknown_block', `block ${id} is not in run ${runId}`);
  if (!(await blockOwnsFile(record, entry, file))) throw new StateError('waive-not-owned', `waive-not-owned: block ${id} does not own ${file}; nothing was recorded`);
  if (finding === PROOF_FINDING) return;
  const named = (await readAllRows(record.project)).some((r) => {
    if (r?.run !== runId || r.block !== id || r.file !== file) return false;
    if (r.event === 'review.triage' || r.event === 'review.late_finding') return r.finding === finding;
    if (r.event === 'review.result') return Array.isArray(r.open) && openFindings(r.open).some((o) => o.id === finding);
    if (r.event === 'review.cap') return Array.isArray(r.open) && r.open.includes(finding);
    return false;
  });
  if (!named) throw new StateError('waive-unknown-finding', `waive-unknown-finding: no review row of block ${id} names finding ${finding} in ${file}; nothing was recorded`);
}

/**
 * B59: the rules lane for a brief-mode block — `fallbackLane` over the facts of its owned entries,
 * the `--lines` estimate and the snapshot's `proof.tiers.high.paths` (none for a record from
 * before snapshots).
 * @param {Record<string, any>} record @param {string[]} owned @param {number | null} lines
 * @returns {{lane: string, facts: ReturnType<typeof laneFacts>}}
 */
function rulesLane(record, owned, lines) {
  const high = configInForce(record)?.proof?.tiers?.high?.paths;
  const facts = laneFacts({ owned, lines, highPaths: high });
  return { lane: fallbackLane(facts), facts };
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
