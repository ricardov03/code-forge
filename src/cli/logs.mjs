/**
 * `code-forge logs` (blocks B27, B28): read, summarize, clear and share the local error log
 * (`~/.code-forge/logs/errors.jsonl`, written by `bin/code-forge.mjs` through `util/error-log.mjs`).
 *
 *   logs [--last N] [--json]          newest first (default 10), with each error's fingerprint
 *   logs summary [--days N] [--json]  one row per fingerprint over the last N days (default 30): "seen N times"
 *   logs clear [--yes]                delete the log after one yes
 *   logs path                         print the log path
 *   logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]
 *
 * B38: known fixes (`util/known-fixes.mjs`, the shipped `known-fixes.json`). `logs` marks a
 * known-fixed line `[fixed in x.y.z]`. `report`, before anything else (no npm, no AI, nothing
 * built): a selected error whose fix shipped in a newer version than the one running is printed
 * with the upgrade command and left out of the report — none left is exit 0 — unless `--force`;
 * when the running version already has the fix, the report notes a possible regression.
 *
 * B37: warnings (`kind: 'warning'`, recoverable problems) are listed marked `[warning]`, counted
 * apart in `summary`, and reported only with `--include-warnings`. Each reported error names the
 * commands run before it (`before`, names only). `--with-doctor` adds a "Setup check" section:
 * `code-forge doctor --json` run as a child (60 s at most, error logging off), each row's id,
 * status and a short detail only, cleaned like the rest; a doctor that cannot run is a note.
 *
 * `report` (B28 order): a version check (`npm view`; an old version is warned and asked about,
 * default no, `--yes` included; without a terminal it stops unless `--allow-old`) → the
 * deterministic scrub → a 16 KB cap → a secret check before the AI sees anything → the AI cleaning pass (a closed-book session
 * that only LISTS private substrings; this tool replaces them, `session/scrub.mjs`) → the
 * deterministic scrub again → a final secret check (a hit stops it; the matched text is never
 * printed) → what is shared, a cleaning summary and the full text → Send · Edit in my editor ·
 * Cancel (an edit is scrubbed and checked again) → an extra default-no yes when the AI pass did not
 * run → `gh` after a search for the same fingerprint (comment "happened again" · new issue · cancel),
 * or a prefilled issue-form link plus a search link. It never opens a browser. The repository
 * comes from package.json.
 */

import { spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { exec as realExec } from '../util/exec.mjs';
import { NO_ERROR_LOG_ENV, capBytes, createScrubber, describeCounts, errorLogPath, fingerprintOf, isWarning, mergeCounts, packageVersion, readErrorLog, readProjectSlug } from '../util/error-log.mjs';
import { compareVersions, fixStatus, loadKnownFixes } from '../util/known-fixes.mjs';
import { redact, writeSafe } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { GH_TIMEOUT_MS, findReported, findSecret, ghWithBodyFile, pathLookup } from '../util/gh.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';
import { AI_REPORT_MAX_BYTES, applyItems, describeAiCounts, runAiScrub } from '../session/scrub.mjs';

const USAGE =
  'usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path\n' +
  '       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--include-warnings] [--with-doctor] [--no-ai] [--allow-old] [--force] [--dry-run] [--yes]\n';

const PACKAGE_JSON = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'package.json');

/** A prefilled issue link longer than this is cut (browsers and GitHub refuse very long URLs). */
export const MAX_URL_LENGTH = 7500;

/**
 * Printed before every report preview: every kind of data the report holds.
 * @param {{warnings?: boolean, doctor?: boolean}} [o] - whether the report holds warnings / setup check results.
 * @returns {string}
 */
export function disclosure({ warnings = false, doctor = false } = {}) {
  const shared = [
    'code-forge, Node and OS versions',
    'command names and flag NAMES',
    'the names of the last commands you ran (no flags or values)',
    'exit codes',
    'error types',
    'error messages and crash reports after cleaning',
    ...(warnings ? ['warnings (problems code-forge recovered from) after cleaning'] : []),
    ...(doctor ? ["the setup check results (each check's name, status and a short detail, after cleaning)"] : []),
  ];
  return (
    `What you will share (public on GitHub): ${shared.join('; ')}.\n` +
    'Never shared: flag values, file contents, your code, keys or tokens, your home folder, project folder or project name.\n'
  );
}

/** The disclosure of a report with errors only. */
export const DISCLOSURE = disclosure();

export const CONFIRM_MESSAGE = 'This will be public on GitHub. Send it?';

// the secret check and the gh helpers live in `util/gh.mjs` (shared with B47's waiver issues)
export { REPORT_SECRET_RULES, entropy, findSecret } from '../util/gh.mjs';
const NPM_TIMEOUT_MS = 10_000;
/** `code-forge doctor --json` for `--with-doctor` gets at most this long. */
export const DOCTOR_TIMEOUT_MS = 60_000;
/** A setup check row's detail is cut to this many bytes. */
export const DOCTOR_DETAIL_MAX_BYTES = 120;

const BIN = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'bin', 'code-forge.mjs');

/** The npm package a version check looks up. */
export const PACKAGE_NAME = '@codedology/code-forge';

/** @typedef {import('../util/error-log.mjs').ErrorEntry} ErrorEntry */
/** @typedef {{write: (s: string) => unknown}} Out */

/**
 * @typedef {object} LogsDeps
 * @property {NodeJS.ProcessEnv} [env]
 * @property {string} [cwd]
 * @property {Out} [stdout]
 * @property {Out} [stderr]
 * @property {boolean} [isTTY]
 * @property {{confirm: (o: {message: string, initialValue?: boolean}) => Promise<any>, select: (o: {message: string, options: Array<{value: string, label: string}>, initialValue?: string}) => Promise<any>, isCancel: (v: unknown) => boolean}} [ui]
 * @property {typeof realExec} [exec]
 * @property {(cmd: string) => boolean} [onPath] - whether `cmd` is an executable on PATH.
 * @property {() => Date} [now]
 * @property {any} [pkg] - the package.json object the issue repository is read from (default: this package's).
 * @property {{version: string, node: string, os: string}} [system] - the versions a report names.
 * @property {Map<string, import('../util/known-fixes.mjs').KnownFix>} [knownFixes] - the known-fix table
 *   by fingerprint (default: the shipped `util/known-fixes.json`).
 * @property {(argv: string[], env: NodeJS.ProcessEnv) => Promise<{code: number|null, error?: string}>} [runEditor] - runs
 *   the editor on the terminal (default: argv only, stdio inherited, never a shell).
 */

/**
 * `owner/repo` from package.json `bugs.url`, else `repository` (string or `{url}`).
 * @param {any} [pkg]
 * @returns {string|null}
 */
export function issueRepo(pkg) {
  let p = pkg;
  if (p === undefined) {
    try {
      p = JSON.parse(readFileSync(PACKAGE_JSON, 'utf8'));
    } catch {
      return null;
    }
  }
  const candidates = [typeof p?.bugs === 'string' ? p.bugs : p?.bugs?.url, typeof p?.repository === 'string' ? p.repository : p?.repository?.url];
  for (const c of candidates) {
    const m = typeof c === 'string' ? /github\.com[/:]([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?(?:[/?#]|$)/.exec(c) : null;
    if (m) return `${m[1]}/${m[2]}`;
  }
  return null;
}

/** @param {ErrorEntry} e */
function command(e) {
  return e.sub ? `${e.verb} ${e.sub}` : e.verb;
}

/** @param {string|null|undefined} s */
function firstLine(s) {
  return typeof s === 'string' ? (s.split('\n').find((l) => l.trim().length > 0) ?? '').trim() : '';
}

/** @param {unknown} v @returns {string} a table cell: one line, `|` escaped */
function cell(v) {
  return String(v ?? '').replace(/\r?\n/g, ' ').replace(/\|/g, '\\|');
}

/** @param {string} text @returns {string} a fenced block whose fence no backtick run inside can close */
function fenced(text) {
  const longest = Math.max(2, ...[...text.matchAll(/`+/g)].map((m) => m[0].length));
  const fence = '`'.repeat(longest + 1);
  return `${fence}text\n${text}\n${fence}`;
}

/**
 * The issue title and Markdown body for `entries` (newest first), with the sections of the issue
 * form (`.github/ISSUE_TEMPLATE/error-report.yml`): what you were doing, versions, error details.
 * The fingerprint section is {@link reportFooter}, added after cleaning. With `setup` (B37) a
 * "Setup check" section follows the error details: the rows, or why the check could not run.
 * @param {ErrorEntry[]} entries
 * @param {{version: string, node: string, os: string, latest?: string|null}} system
 * @param {string|null} note
 * @param {SetupCheck|null} [setup]
 * @param {Map<string, string>} [fixNotes] - by fingerprint: a line added to that error's section
 *   (B38: a known fix; see {@link fixNote}).
 * @returns {{title: string, body: string, fps: string[]}} `fps`: distinct fingerprints, newest first.
 */
export function buildReport(entries, system, note, setup = null, fixNotes = new Map()) {
  const pairs = new Set(entries.map((e) => `${e.verb}\u0000${e.kind}`));
  const title = pairs.size === 1 ? `[error report] ${entries[0].verb} ${entries[0].kind}` : `[error report] ${entries.length} errors`;
  const fps = entries.map(fingerprintOf);
  const out = [
    '## code-forge error report',
    '',
    '### What you were doing',
    '',
    note ? note : '(not given)',
    '',
    '### Versions',
    '',
    `- code-forge: ${system.version}`,
    `- latest on npm: ${system.latest ?? 'unknown'}`,
    `- Node: ${system.node}`,
    `- OS: ${system.os}`,
    '',
    '### Error details',
    '',
    '| # | when | command | exit | kind | fingerprint |',
    '|---|---|---|---|---|---|',
  ];
  entries.forEach((e, i) => out.push(`| ${i + 1} | ${cell(e.ts)} | ${cell(command(e))} | ${cell(e.exit)} | ${cell(e.kind)} | ${fps[i]} |`));
  entries.forEach((e, i) => {
    out.push(
      '',
      `#### ${i + 1}. ${command(e)}: ${e.kind}`,
      '',
      `- when: ${e.ts}`,
      `- flags: ${Array.isArray(e.flags) && e.flags.length > 0 ? e.flags.map((f) => `\`${f}\``).join(' ') : 'none'}`,
      ...(Array.isArray(e.before) ? [`- commands before (oldest first): ${e.before.length > 0 ? e.before.map((c) => `\`${c}\``).join(', ') : 'none'}`] : []),
      `- exit: ${e.exit}`,
      `- kind: ${e.kind}`,
      ...(isWarning(e) && typeof e.warning === 'string' ? [`- warning: ${e.warning}`] : []),
      `- fingerprint: ${fps[i]}`,
      `- code-forge ${e.version}, Node ${e.node}, ${e.platform} ${e.arch}`,
      ...(fixNotes.has(fps[i]) ? ['', /** @type {string} */ (fixNotes.get(fps[i]))] : []),
      '',
      'message:',
      '',
      fenced(e.message ?? '(none)'),
    );
    if (e.stack) out.push('', 'stack:', '', fenced(e.stack));
  });
  if (setup !== null) {
    out.push('', '### Setup check', '');
    if ('why' in setup) {
      out.push(`(the setup check could not run: ${setup.why})`);
    } else {
      out.push('| check | status | detail |', '|---|---|---|');
      for (const r of setup.rows) out.push(`| ${cell(r.id)} | ${cell(r.status)} | ${cell(r.detail)} |`);
    }
  }
  return { title, body: `${out.join('\n')}\n`, fps: [...new Set(fps)] };
}

/** @typedef {{rows: Array<{id: string, status: string, detail: string}>} | {why: string}} SetupCheck */

/** The statuses a doctor row may have. */
const DOCTOR_STATUSES = new Set(['OK', 'WARN', 'FAIL', 'INFO']);

/**
 * Run `code-forge doctor --json` as a child (argv only, 60 s at most, with error logging off so
 * the check adds no log line or breadcrumb) and keep only each row's `id`, `status` and the first
 * line of its `detail`, redacted, cleaned by `scrub` and then cut to {@link DOCTOR_DETAIL_MAX_BYTES}
 * — never its label or anything else. A doctor that finds a FAIL exits 1 and still prints its rows; no rows at all is
 * `{why}` (`timed out`, `doctor exit n`, `doctor gave no result`, `doctor did not run`).
 * @param {typeof realExec} exec @param {NodeJS.ProcessEnv} env @param {string} cwd
 * @param {(text: string) => string} scrub - the report's deterministic scrub.
 * @returns {Promise<SetupCheck>}
 */
export async function runSetupCheck(exec, env, cwd, scrub) {
  let res;
  try {
    res = await exec([process.execPath, BIN, 'doctor', '--json'], { cwd, env: { ...env, [NO_ERROR_LOG_ENV]: '1' }, timeoutMs: DOCTOR_TIMEOUT_MS });
  } catch {
    return { why: 'doctor did not run' };
  }
  if (res.timedOut) return { why: 'timed out' };
  let doc;
  try {
    doc = JSON.parse(res.stdout);
  } catch {
    doc = null;
  }
  if (!doc || typeof doc !== 'object' || !Array.isArray(doc.rows)) {
    return { why: res.code === null ? 'doctor did not run' : res.code === 0 ? 'doctor gave no result' : `doctor exit ${res.code}` };
  }
  const rows = [];
  for (const r of doc.rows) {
    if (typeof r?.id !== 'string' || !/^[A-Za-z0-9_.:-]{1,64}$/.test(r.id) || !DOCTOR_STATUSES.has(r.status)) continue;
    rows.push({ id: r.id, status: r.status, detail: capBytes(scrub(redact(firstLine(typeof r.detail === 'string' ? r.detail : ''))), DOCTOR_DETAIL_MAX_BYTES) });
  }
  return { rows };
}

/**
 * The last section of every report: `Fingerprint: <fp>` (the newest error's) and one hidden
 * `<!-- code-forge-fp: <fp> -->` line per distinct fingerprint, so a later report finds it.
 * @param {string[]} fps - distinct, newest first.
 * @returns {string}
 */
export function reportFooter(fps) {
  const lines = ['', '### Fingerprint', '', `Fingerprint: ${fps[0]}`];
  if (fps.length > 1) lines.push('', `Other fingerprints: ${fps.slice(1).join(', ')}`);
  lines.push('', ...fps.map((fp) => `<!-- code-forge-fp: ${fp} -->`));
  return `${lines.join('\n')}\n`;
}

/** A body's footer (see {@link reportFooter}), when it ends with one. */
const FOOTER_RE = /\n### Fingerprint\n\nFingerprint: [0-9a-f]{12}\n(?:\nOther fingerprints: [0-9a-f, ]+\n)?\n(?:<!-- code-forge-fp: [0-9a-f]{12} -->[ \t]*(?:\r?\n|$))+\s*$/;

/**
 * Cut `body` so `title` + `body` stay within `maxBytes` (the AI cost guard), never inside a
 * character; a cut body ends with a note naming that size.
 * @param {string} title @param {string} body @param {number} maxBytes
 * @returns {{body: string, cut: boolean}}
 */
export function capReport(title, body, maxBytes) {
  if (Buffer.byteLength(`${title}\n\n${body}`) <= maxBytes) return { body, cut: false };
  const note = `\n\n(report cut to ${maxBytes} bytes)\n`;
  const room = maxBytes - Buffer.byteLength(`${title}\n\n`) - Buffer.byteLength(note);
  return { body: `${capBytes(body, Math.max(0, room))}${note}`, cut: true };
}

/** Re-exported: the version comparison lives in `util/known-fixes.mjs` (B38). */
export { compareVersions };

/** @returns {{version: string, node: string, os: string}} */
function currentSystem() {
  return { version: packageVersion(), node: process.version, os: `${process.platform} ${os.release()} ${process.arch}` };
}

/**
 * @param {string[]} args
 * @param {LogsDeps} [deps]
 * @returns {Promise<number>}
 */
export async function runLogs(args, deps = {}) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const out = (/** @type {string} */ s) => writeSafe(stdout, s);
  const err = (/** @type {string} */ s) => writeSafe(stderr, s);
  const env = deps.env ?? process.env;
  const file = errorLogPath(env.HOME);

  const sub = args[0] !== undefined && !args[0].startsWith('-') ? args[0] : null;
  const specs = /** @type {Record<string, {values?: string[], booleans?: string[]}>} */ ({
    list: { values: ['last'], booleans: ['json'] },
    summary: { values: ['days'], booleans: ['json'] },
    clear: { booleans: ['yes'] },
    path: {},
    report: { values: ['last', 'kind', 'verb', 'note'], booleans: ['dry-run', 'yes', 'no-ai', 'allow-old', 'include-warnings', 'with-doctor', 'force'] },
  });
  const name = sub ?? 'list';
  if (!Object.hasOwn(specs, name) || (name === 'list' && sub !== null)) {
    err(`logs: unknown subcommand ${JSON.stringify(sub)}\n${USAGE}`);
    return 2;
  }
  let parsed;
  let n;
  try {
    parsed = parseFlags(sub === null ? args : args.slice(1), specs[name]);
    if (parsed.positionals.length > 0) throw new Error(`unexpected argument ${JSON.stringify(parsed.positionals[0])}`);
    n = intFlag(parsed.flags.last ?? parsed.flags.days, parsed.flags.days !== undefined ? 'days' : 'last');
  } catch (e) {
    err(`logs: ${/** @type {Error} */ (e).message}\n${USAGE}`);
    return 2;
  }
  if (file === null) {
    err('logs: HOME is not set; there is no error log\n');
    return 1;
  }
  const flags = parsed.flags;

  if (name === 'path') {
    out(`${file}\n`);
    return 0;
  }

  const entries = await readErrorLog(file);
  if (name === 'clear') return clear(file, entries, flags, deps, out, err);
  if (entries === null || entries.length === 0) {
    out(flags.json ? `${JSON.stringify(name === 'summary' ? { days: n ?? 30, counts: [], warnings: [] } : { errors: [] })}\n` : 'no errors logged\n');
    return 0;
  }
  const newest = [...entries].reverse();

  if (name === 'list') {
    const shown = newest.slice(0, n ?? 10);
    if (flags.json) out(`${JSON.stringify({ errors: shown })}\n`);
    else {
      const fixes = deps.knownFixes ?? loadKnownFixes();
      for (const e of shown) {
        const fp = fingerprintOf(e);
        const fix = fixes.get(fp);
        const fpCell = fix ? `${fp} [fixed in ${fix.fixed_in}]` : fp;
        out(
          isWarning(e)
            ? `${e.ts}  [warning] ${command(e)}  ${typeof e.warning === 'string' ? e.warning : 'warning'}  ${fpCell}  ${firstLine(e.message)}\n`
            : `${e.ts}  ${command(e)}  ${e.exit}  ${e.kind}  ${fpCell}  ${firstLine(e.message)}\n`,
        );
      }
    }
    return 0;
  }

  if (name === 'summary') {
    const days = n ?? 30;
    const since = (deps.now ?? (() => new Date()))().getTime() - days * 86_400_000;
    /** @type {Map<string, {fp: string, verb: string, sub: string|null, kind: string, count: number}>} */
    const counts = new Map();
    /** @type {Map<string, {fp: string, verb: string, sub: string|null, warning: string, count: number}>} */
    const warns = new Map();
    for (const e of newest) {
      const t = Date.parse(e.ts);
      if (!(t >= since)) continue;
      const fp = fingerprintOf(e);
      if (isWarning(e)) {
        const w = warns.get(fp) ?? { fp, verb: e.verb, sub: e.sub ?? null, warning: typeof e.warning === 'string' ? e.warning : 'warning', count: 0 };
        w.count += 1;
        warns.set(fp, w);
        continue;
      }
      const c = counts.get(fp) ?? { fp, verb: e.verb, sub: e.sub ?? null, kind: e.kind, count: 0 };
      c.count += 1;
      counts.set(fp, c);
    }
    const rows = [...counts.values()].sort((a, b) => b.count - a.count || a.verb.localeCompare(b.verb) || a.kind.localeCompare(b.kind) || a.fp.localeCompare(b.fp));
    const warnRows = [...warns.values()].sort((a, b) => b.count - a.count || a.verb.localeCompare(b.verb) || a.warning.localeCompare(b.warning) || a.fp.localeCompare(b.fp));
    const times = (/** @type {number} */ c) => `seen ${c} ${c === 1 ? 'time' : 'times'}`;
    if (flags.json) {
      out(`${JSON.stringify({ days, counts: rows, warnings: warnRows })}\n`);
      return 0;
    }
    if (rows.length === 0) {
      out(`no errors in the last ${days} days\n`);
    } else {
      out(`errors in the last ${days} days:\n`);
      for (const r of rows) out(`${r.fp}  ${r.sub ? `${r.verb} ${r.sub}` : r.verb}  ${r.kind}  ${times(r.count)}\n`);
    }
    if (warnRows.length > 0) {
      out(`warnings in the last ${days} days:\n`);
      for (const r of warnRows) out(`${r.fp}  ${r.sub ? `${r.verb} ${r.sub}` : r.verb}  ${r.warning}  ${times(r.count)}\n`);
    }
    return 0;
  }

  return report(newest, n ?? 5, flags, deps, out, err);
}

/**
 * @param {string} file @param {ErrorEntry[]|null} entries
 * @param {Record<string, string|string[]|true>} flags @param {LogsDeps} deps
 * @param {(s: string) => void} out @param {(s: string) => void} err
 */
async function clear(file, entries, flags, deps, out, err) {
  if (entries === null) {
    out('no errors logged\n');
    return 0;
  }
  if (!flags.yes) {
    if (!(deps.isTTY ?? process.stdin.isTTY === true)) {
      err('logs: no terminal to confirm; nothing deleted — pass --yes to delete the error log\n');
      return 2;
    }
    const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
    const go = await ui.confirm({ message: `Delete the error log (${entries.length} entries)?`, initialValue: false });
    if (ui.isCancel(go) || go !== true) {
      out('cancelled — nothing deleted\n');
      return 1;
    }
  }
  await rm(file, { force: true });
  out(`deleted ${file}\n`);
  return 0;
}

/**
 * The latest version on npm, or why it is unknown (never npm's own output).
 * @param {typeof realExec} exec @param {NodeJS.ProcessEnv} env
 * @returns {Promise<{latest: string} | {why: string}>}
 */
async function latestOnNpm(exec, env) {
  let res;
  try {
    res = await exec(['npm', 'view', PACKAGE_NAME, 'version'], { env, timeoutMs: NPM_TIMEOUT_MS });
  } catch {
    return { why: 'npm did not run' };
  }
  if (res.result !== 'ok') return { why: res.timedOut ? 'timed out' : res.code !== null ? `npm exit ${res.code}` : 'npm did not run' };
  const v = res.stdout.trim();
  return /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(v) ? { latest: v } : { why: 'npm gave no version' };
}

/**
 * Ask a yes/no question on the terminal.
 * @param {LogsDeps} deps @param {string} message @param {boolean} initialValue
 * @returns {Promise<boolean>}
 */
async function askYes(deps, message, initialValue) {
  const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
  const go = await ui.confirm({ message, initialValue });
  return !ui.isCancel(go) && go === true;
}

/**
 * Pick one option on the terminal; a cancelled prompt is `cancel`.
 * @param {LogsDeps} deps @param {string} message
 * @param {Array<{value: string, label: string}>} options @param {string} initialValue
 * @returns {Promise<string>}
 */
async function choose(deps, message, options, initialValue) {
  const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
  const v = await ui.select({ message, options, initialValue });
  return ui.isCancel(v) || typeof v !== 'string' ? 'cancel' : v;
}

/**
 * The editor command: `$VISUAL` when it is usable, else `$EDITOR`. Usable means a plain command
 * name or an absolute path, with no spaces or flags (it runs as argv[0], never through a shell).
 * @param {NodeJS.ProcessEnv} env
 * @returns {string|null} null when neither is usable.
 */
export function editorCommand(env) {
  for (const raw of [env.VISUAL, env.EDITOR]) {
    if (typeof raw !== 'string' || raw.length === 0 || /\s/.test(raw)) continue;
    if (path.isAbsolute(raw) || /^[A-Za-z0-9._+-]+$/.test(raw)) return raw;
  }
  return null;
}

/**
 * Run the editor on the terminal: argv only, never a shell.
 * @param {string[]} argv @param {NodeJS.ProcessEnv} env
 * @returns {Promise<{code: number|null, error?: string}>}
 */
function runEditorOnTerminal(argv, env) {
  return new Promise((resolve) => {
    const child = spawn(argv[0], argv.slice(1), { env, stdio: 'inherit', shell: false });
    child.on('error', (e) => resolve({ code: null, error: /** @type {NodeJS.ErrnoException} */ (e).code ?? 'error' }));
    child.on('exit', (code) => resolve({ code }));
  });
}

/**
 * @param {ErrorEntry[]} newest @param {number} last
 * @param {Record<string, string|string[]|true>} flags @param {LogsDeps} deps
 * @param {(s: string) => void} out @param {(s: string) => void} err
 */
async function report(newest, last, flags, deps, out, err) {
  const env = deps.env ?? process.env;
  const home = env.HOME ?? '';
  const kind = typeof flags.kind === 'string' ? flags.kind : null;
  const verb = typeof flags.verb === 'string' ? flags.verb : null;
  // warnings only with --include-warnings (B37)
  const withWarnings = flags['include-warnings'] === true;
  const picked = newest.filter((e) => (withWarnings || !isWarning(e)) && (kind === null || e.kind === kind) && (verb === null || e.verb === verb)).slice(0, last);
  if (picked.length === 0) {
    out('no matching errors to report\n');
    return 0;
  }
  const system = deps.system ?? currentSystem();

  // 0. known fixes (B38), before anything is asked or built: an error fixed in a newer version
  // than the one running is left out (unless --force); one the running version already has the
  // fix for gets a "may be a regression" note
  const fixes = deps.knownFixes ?? loadKnownFixes();
  /** @type {Map<string, string>} */
  const fixNotes = new Map();
  /** @type {Set<string>} */
  const fixedAway = new Set();
  for (const fp of new Set(picked.map(fingerprintOf))) {
    const fix = fixes.get(fp);
    if (!fix) continue;
    const status = fixStatus(fix, system.version);
    if (status === 'older') {
      out(`This error is fixed in ${fix.fixed_in}: ${fix.summary.replace(/\.$/, '')}. Upgrade with: npm install -g ${PACKAGE_NAME}@latest\n`);
      if (flags.force === true) {
        out(`--force: reporting ${fp} anyway.\n`);
        fixNotes.set(fp, `Note: a fix for this shipped in ${fix.fixed_in}; this report is from an older version (sent with --force).`);
      } else {
        fixedAway.add(fp);
      }
    } else if (status === 'has-fix') {
      fixNotes.set(fp, `Note: a fix for this shipped in ${fix.fixed_in}; it may be a regression.`);
    }
  }
  const selected = picked.filter((e) => !fixedAway.has(fingerprintOf(e)));
  if (selected.length === 0) {
    out('not filed: upgrade first (pass --force to report it anyway)\n');
    return 0;
  }
  if (fixedAway.size > 0) out(`left out of the report (fixed in a newer version): ${[...fixedAway].join(', ')}\n`);
  const repo = issueRepo(deps.pkg);
  if (repo === null) {
    err('logs: package.json names no GitHub repository to report to\n');
    return 1;
  }
  const exec = deps.exec ?? realExec;
  const tty = deps.isTTY ?? process.stdin.isTTY === true;
  const dryRun = flags['dry-run'] === true;
  const yes = flags.yes === true;

  // 1. version check: an old version gets a warning and a default-no question
  const npm = await latestOnNpm(exec, env);
  const latest = 'latest' in npm ? npm.latest : null;
  if ('why' in npm) {
    out(`Could not check npm for a newer code-forge (${npm.why}); going on.\n`);
  } else if ((compareVersions(npm.latest, system.version) ?? 0) > 0) {
    out(`You use ${system.version}. ${npm.latest} is out. Many errors are fixed in newer versions: upgrade with npm install -g ${PACKAGE_NAME}@latest and try again first.\n`);
    // never go on silently: a terminal is asked (--yes too, default no); without one, only an
    // explicit --allow-old goes on (a dry run sends nothing and only warns)
    if (!dryRun && flags['allow-old'] !== true) {
      if (!tty) {
        err('logs: an older code-forge than the one on npm; not sent — upgrade first, or pass --allow-old to report from this version\n');
        return 2;
      }
      if (!(await askYes(deps, 'Report anyway?', false))) {
        out('not sent\n');
        return 1;
      }
    }
  }

  const cwd = deps.cwd ?? process.cwd();
  const scrubber = createScrubber({ home, cwd, slug: await readProjectSlug(cwd) });

  // 1b. the setup check (--with-doctor, B37): a doctor that cannot run is a note, never a stop
  /** @type {SetupCheck|null} */
  let setup = null;
  if (flags['with-doctor'] === true) {
    setup = await runSetupCheck(exec, env, cwd, (t) => scrubber.scrub(t));
    if ('why' in setup) out(`Could not run the setup check (${setup.why}); the report goes on without it.\n`);
  }
  const shared = disclosure({ warnings: selected.some(isWarning), doctor: setup !== null && 'rows' in setup });

  // 2. deterministic scrub, the 16 KB cap, the AI pass, the deterministic scrub again
  const note = typeof flags.note === 'string' ? flags.note : null;
  const built = buildReport(selected, { ...system, latest }, note, setup, fixNotes);
  let title = scrubber.scrub(built.title);
  let body = capReport(title, scrubber.scrub(built.body), AI_REPORT_MAX_BYTES).body;
  const blocked = () => {
    const hit = findSecret(`${title}\n${body}`);
    if (hit === null) return false;
    out(shared);
    err(`Possible secret found in the report (${hit.rule}, report line ${hit.line}; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n`);
    return true;
  };
  // a secret the built-in cleaning left is never shown to the AI either
  if (blocked()) return 1;
  /** @type {{status: 'ok', counts: Array<{kind: string, count: number}>} | {status: 'skipped'} | {status: 'unavailable', reason: string}} */
  let ai;
  if (flags['no-ai'] === true) {
    ai = { status: 'skipped' };
  } else {
    const res = await runAiScrub({ report: `${title}\n\n${body}`, cwd }, { ...(deps.exec ? { exec: deps.exec } : {}), env, stderr: { write: () => true } });
    if (res.ok === true) {
      const applied = applyItems([title, body], res.items);
      [title, body] = applied.texts.map((t) => scrubber.scrub(t));
      ai = { status: 'ok', counts: applied.counts };
    } else {
      ai = { status: 'unavailable', reason: /** @type {{reason: string}} */ (res).reason };
    }
  }
  body += reportFooter(built.fps);
  const cleaned = mergeCounts([...selected.map((e) => e.cleaned), scrubber.counts()]);

  // 3. the final secret check, then what is shared and the full text
  if (blocked()) return 1;
  out(shared);
  const aiLine =
    ai.status === 'ok'
      ? describeAiCounts(ai.counts)
      : ai.status === 'skipped'
        ? 'AI cleaning was skipped (--no-ai); only the built-in cleaning ran.'
        : `AI cleaning was not available (${ai.reason}); only the built-in cleaning ran.`;
  out(`${describeCounts(cleaned)}\n${aiLine}\n\nTitle: ${title}\n\n${body}\n`);
  if (dryRun) {
    out('dry run: not sent\n');
    return 0;
  }

  // 4. send, edit or cancel
  const aiRan = ai.status === 'ok';
  if (yes) {
    // `--no-ai --yes` is the explicit extra yes; otherwise the AI check that did not run is asked
    // about in a terminal (default no) and refused without one
    if (!aiRan && flags['no-ai'] !== true) {
      if (!tty) {
        err('logs: the AI check did not run and there is no terminal to confirm; not sent — pass --no-ai with --yes to send with only the built-in cleaning\n');
        return 1;
      }
      if (!(await askYes(deps, 'Send without the AI check?', false))) {
        out('not sent\n');
        return 1;
      }
    }
  } else {
    if (!tty) {
      err('logs: no terminal to confirm; not sent — pass --yes to send the report above\n');
      return 2;
    }
    let edited = false;
    for (;;) {
      const options = [{ value: 'send', label: 'Send' }, ...(edited ? [] : [{ value: 'edit', label: 'Edit in my editor' }]), { value: 'cancel', label: 'Cancel' }];
      const pick = await choose(deps, CONFIRM_MESSAGE, options, aiRan ? 'send' : 'cancel');
      if (pick === 'edit') {
        edited = true;
        const next = await editBody(body, env, deps, out);
        if (next === null) continue;
        const cleanedNext = scrubber.scrub(next);
        const again = findSecret(`${title}\n${cleanedNext}`);
        if (again !== null) {
          err(`Possible secret found in the edited report (${again.rule}, report line ${again.line}; the title is line 1); not sent.\n`);
          return 1;
        }
        body = cleanedNext;
        if (!body.includes(`<!-- code-forge-fp: ${built.fps[0]} -->`)) {
          body = `${body.trimEnd()}\n${reportFooter(built.fps)}`;
          out('the fingerprint section was added back\n');
        }
        out(`Title: ${title}\n\n${body}\n`);
        continue;
      }
      if (pick !== 'send' || (!aiRan && !(await askYes(deps, 'Send without the AI check?', false)))) {
        out('not sent\n');
        return 1;
      }
      break;
    }
  }

  // 5. gh (after a search for the same fingerprint) or a link
  const fp = built.fps[0];
  const onPath = deps.onPath ?? pathLookup(env.PATH ?? '');
  let ghReady = false;
  if (onPath('gh')) {
    try {
      ghReady = (await exec(['gh', 'auth', 'status'], { env, timeoutMs: GH_TIMEOUT_MS })).result === 'ok';
    } catch {
      ghReady = false;
    }
  }
  if (ghReady) {
    const search = await findReported(exec, env, repo, fp);
    if ('why' in search) out(`could not search for an existing report (${search.why}); creating a new one\n`);
    const dup = 'hit' in search ? search.hit : null;
    if (dup !== null) {
      out(`This error was already reported: ${dup.url} (${dup.state})\n`);
      const pick = yes
        ? 'comment'
        : await choose(deps, 'What now?', [
            { value: 'comment', label: 'Add a comment "happened again"' },
            { value: 'new', label: 'Create a new issue anyway' },
            { value: 'cancel', label: 'Cancel' },
          ], 'comment');
      if (pick === 'cancel') {
        out('not sent\n');
        return 1;
      }
      if (pick === 'comment') {
        const times = newest.filter((e) => fingerprintOf(e) === fp).length;
        const comment = againComment(system, latest, times, fp);
        const why = await ghWithBodyFile(exec, env, comment, (file) => [['gh', 'issue', 'comment', String(dup.number), '--repo', repo, '--body-file', file]]);
        if (why === null) {
          out(`commented: ${dup.url}\n`);
          return 0;
        }
        err(`gh failed (${why}); nothing was sent. Add the comment by hand: ${dup.url}\n`);
        return 1;
      }
    }
    // one kind label per distinct kind in the report, newest first, at most 3
    const kinds = [...new Set(selected.map((e) => e.kind))].slice(0, 3);
    const labels = ['--label', 'error-report', ...kinds.flatMap((k) => ['--label', `kind:${k}`])];
    /** @type {string|null} */
    let url = null;
    const why = await ghWithBodyFile(exec, env, body, (file) => {
      const base = ['gh', 'issue', 'create', '--repo', repo, '--title', title, '--body-file', file];
      return [[...base, ...labels], base];
    }, (stdout) => {
      url = stdout.split('\n').map((l) => l.trim()).filter((l) => /^https:\/\//.test(l)).pop() ?? null;
    });
    if (why === null) {
      out(`sent: ${url ?? '(gh printed no issue URL)'}\n`);
      return 0;
    }
    out(`gh failed (${why}); here is a link instead\n`);
    return printLink(repo, title, body, fp, home, deps, out, true);
  }
  return printLink(repo, title, body, fp, home, deps, out, false);
}

/**
 * The comment for an issue that already has this fingerprint.
 * @param {{version: string, node: string, os: string}} system @param {string|null} latest
 * @param {number} times @param {string} fp
 */
export function againComment(system, latest, times, fp) {
  return [
    'This error happened again.',
    '',
    `- code-forge: ${system.version}`,
    `- latest on npm: ${latest ?? 'unknown'}`,
    `- Node: ${system.node}`,
    `- OS: ${system.os}`,
    `- times in the local log: ${times}`,
    '',
    `<!-- code-forge-fp: ${fp} -->`,
    '',
  ].join('\n');
}

/**
 * Edit `body` in the user's editor; null when it could not run (the reason is printed).
 * @param {string} body @param {NodeJS.ProcessEnv} env @param {LogsDeps} deps @param {(s: string) => void} out
 * @returns {Promise<string|null>}
 */
async function editBody(body, env, deps, out) {
  const editor = editorCommand(env);
  if (editor === null) {
    out('Cannot open an editor: set VISUAL or EDITOR to a plain command (a name such as vim, or an absolute path, with no spaces or flags).\n');
    return null;
  }
  /** @type {string|null} */
  let dir = null;
  try {
    const root = currentRunRoot();
    await mkdir(root, { recursive: true });
    dir = await mkdtemp(path.join(root, 'report-edit-'));
    const file = path.join(dir, 'report.md');
    await writeFile(file, body, { encoding: 'utf8', mode: 0o600 });
    const res = await (deps.runEditor ?? runEditorOnTerminal)([editor, file], env);
    if (res.code !== 0) {
      out(`The editor did not finish (${res.error ?? `exit ${res.code}`}); the text is unchanged.\n`);
      return null;
    }
    return await readFile(file, 'utf8');
  } catch {
    out('Could not edit the report; the text is unchanged.\n');
    return null;
  } finally {
    if (dir !== null) await rm(dir, { recursive: true, force: true }).catch(() => {});
  }
}

/**
 * The prefilled issue-form link: the form's `details` field holds the whole body (GitHub fills an
 * issue form's fields from query parameters named by field id).
 * @param {string} repo @param {string} title @param {string} body @param {string} fp
 */
export function issueUrl(repo, title, body, fp) {
  return `https://github.com/${repo}/issues/new?template=error-report.yml&title=${encodeURIComponent(title)}&labels=error-report&fingerprint=${fp}&details=${encodeURIComponent(body)}`;
}

/** @param {string} repo @param {string} fp @returns {string} the issue search for one fingerprint */
export function searchUrl(repo, fp) {
  return `https://github.com/${repo}/issues?q=${fp}`;
}

/**
 * Save the full body as `~/.code-forge/logs/report-<ts>.md`.
 * @param {string} body @param {string} home @param {LogsDeps} deps
 * @returns {Promise<{saved: string, name: string}|null>} null when it could not be saved.
 */
async function saveReport(body, home, deps) {
  if (!home) return null;
  const stamp = (deps.now ?? (() => new Date()))().toISOString().replace(/[:.]/g, '-');
  const name = `report-${stamp}.md`;
  const logsDir = path.join(home, '.code-forge', 'logs');
  try {
    await mkdir(logsDir, { recursive: true, mode: 0o700 });
    await writeFile(path.join(logsDir, name), body, { encoding: 'utf8', mode: 0o600 });
    return { saved: path.join(logsDir, name), name };
  } catch {
    return null;
  }
}

/**
 * Print the search link for the fingerprint and a prefilled issue link; a body too long for a
 * link is cut (its fingerprint section kept) and saved in full.
 * @param {string} repo @param {string} title @param {string} body @param {string} fp @param {string} home
 * @param {LogsDeps} deps @param {(s: string) => void} out
 * @param {boolean} alwaysSave - save the full body even when it fits (the gh fallback).
 */
async function printLink(repo, title, body, fp, home, deps, out, alwaysSave) {
  let url = issueUrl(repo, title, body, fp);
  if (url.length > MAX_URL_LENGTH) {
    const file = await saveReport(body, home, deps);
    const found = FOOTER_RE.exec(body);
    const footer = found ? `${found[0].slice(1).trimEnd()}\n` : '';
    const main = found ? body.slice(0, found.index) : body;
    const tail = (file ? `\n\n(report cut; full report saved at ~/.code-forge/logs/${file.name})\n` : '\n\n(report cut)\n') + (footer ? `\n${footer}` : '');
    const chars = Array.from(main);
    let lo = 0;
    let hi = chars.length;
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2);
      if (issueUrl(repo, title, chars.slice(0, mid).join('') + tail, fp).length <= MAX_URL_LENGTH) lo = mid;
      else hi = mid - 1;
    }
    url = issueUrl(repo, title, chars.slice(0, lo).join('') + tail, fp);
    out(file ? `The report is too long for a link: it was cut. Full report saved at ${file.saved}\n` : 'The report is too long for a link: it was cut.\n');
  } else if (alwaysSave) {
    const file = await saveReport(body, home, deps);
    if (file) out(`Full report saved at ${file.saved}\n`);
  }
  out(`Already reported? Search first:\n${searchUrl(repo, fp)}\n`);
  out(`Open this link to file the issue:\n${url}\n`);
  return 0;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function logs(args) {
  return runLogs(args);
}
