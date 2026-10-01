/**
 * `code-forge logs` (block B27): read, summarize, clear and share the local error log
 * (`~/.code-forge/logs/errors.jsonl`, written by `bin/code-forge.mjs` through `util/error-log.mjs`).
 *
 *   logs [--last N] [--json]          newest first (default 10)
 *   logs summary [--days N] [--json]  counts by verb + kind over the last N days (default 30)
 *   logs clear [--yes]                delete the log after one yes
 *   logs path                         print the log path
 *   logs report [--last N] [--kind K] [--verb V] [--note "text"] [--dry-run] [--yes]
 *
 * `report` builds a GitHub issue (title + Markdown body) from the selected errors, scrubs it again,
 * runs a last secret check (a hit stops it; the matched text is never printed), then ALWAYS prints
 * what is shared, a cleaning summary and the full title and body before one yes (default no). It
 * sends with `gh issue create` (argv only) when `gh` is on PATH and signed in, else prints a
 * prefilled issue link. It never opens a browser. The repository comes from package.json.
 */

import { accessSync, constants as fsConstants, readFileSync, statSync } from 'node:fs';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { exec as realExec } from '../util/exec.mjs';
import { createScrubber, describeCounts, errorLogPath, mergeCounts, packageVersion, readErrorLog, readProjectSlug } from '../util/error-log.mjs';
import { writeSafe } from '../util/redact.mjs';
import { currentRunRoot } from '../util/tmp.mjs';
import { SECRET_LOOKING_PATTERNS } from '../config/secret-patterns.mjs';
import { intFlag, parseFlags } from '../state/cli-args.mjs';

const USAGE =
  'usage: code-forge logs [--last N] [--json] | logs summary [--days N] [--json] | logs clear [--yes] | logs path\n' +
  '       code-forge logs report [--last N] [--kind K] [--verb V] [--note "text"] [--dry-run] [--yes]\n';

const PACKAGE_JSON = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'package.json');

/** A prefilled issue link longer than this is cut (browsers and GitHub refuse very long URLs). */
export const MAX_URL_LENGTH = 7500;

/** Printed before every report preview. */
export const DISCLOSURE =
  'What you will share (public on GitHub): code-forge, Node and OS versions; command names and flag NAMES; exit codes; error types; error messages and crash reports after cleaning.\n' +
  'Never shared: flag values, file contents, your code, keys or tokens, your home folder, project folder or project name.\n';

export const CONFIRM_MESSAGE = 'This will be public on GitHub. Send it?';

const GH_TIMEOUT_MS = 60_000;

/** @typedef {import('../util/error-log.mjs').ErrorEntry} ErrorEntry */
/** @typedef {{write: (s: string) => unknown}} Out */

/**
 * @typedef {object} LogsDeps
 * @property {NodeJS.ProcessEnv} [env]
 * @property {string} [cwd]
 * @property {Out} [stdout]
 * @property {Out} [stderr]
 * @property {boolean} [isTTY]
 * @property {{confirm: (o: {message: string, initialValue?: boolean}) => Promise<any>, isCancel: (v: unknown) => boolean}} [ui]
 * @property {typeof realExec} [exec]
 * @property {(cmd: string) => boolean} [onPath] - whether `cmd` is an executable on PATH.
 * @property {() => Date} [now]
 * @property {any} [pkg] - the package.json object the issue repository is read from (default: this package's).
 * @property {{version: string, node: string, os: string}} [system] - the versions a report names.
 */

/**
 * Secret shapes the final report is checked for, after scrubbing. A hit stops the report.
 * @type {ReadonlyArray<{name: string, re: RegExp}>}
 */
export const REPORT_SECRET_RULES = Object.freeze([
  { name: 'API key (sk-)', re: /(?<![A-Za-z0-9_-])sk-[A-Za-z0-9_-]{10,}/ },
  { name: 'GitHub token', re: /(?<![A-Za-z0-9_])(?:gh[opsur]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})/ },
  { name: 'Slack token', re: /(?<![A-Za-z0-9_-])xox[abprs]-[A-Za-z0-9-]{10,}/ },
  { name: 'AWS access key', re: /(?<![A-Za-z0-9])AKIA[0-9A-Z]{16}(?![0-9A-Z])/ },
  { name: 'private key', re: /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----/ },
  { name: 'JWT', re: /(?<![A-Za-z0-9_-])eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/ },
  // copied without g/y: `.test()` must carry no `lastIndex` state between checks
  ...SECRET_LOOKING_PATTERNS.map((re) => ({ name: 'secret-shaped token', re: new RegExp(re.source, re.flags.replace(/[gy]/g, '')) })),
]);

/** @param {string} s @returns {number} Shannon entropy in bits per character */
export function entropy(s) {
  /** @type {Map<string, number>} */
  const freq = new Map();
  for (const ch of s) freq.set(ch, (freq.get(ch) ?? 0) + 1);
  let bits = 0;
  for (const n of freq.values()) {
    const p = n / s.length;
    bits -= p * Math.log2(p);
  }
  return bits;
}

/**
 * A 32+ character run of `[A-Za-z0-9+/_=-]` with at least two of upper/lower/digit and entropy of
 * at least 4 bits per character (a hex hash tops out at 4 and is not flagged; a random token is).
 * @param {string} line
 * @returns {boolean}
 */
function hasHighEntropyRun(line) {
  for (const m of line.matchAll(/[A-Za-z0-9+/_=-]{32,}/g)) {
    const run = m[0];
    const classes = [/[A-Z]/, /[a-z]/, /[0-9]/].filter((re) => re.test(run)).length;
    if (classes >= 2 && entropy(run) >= 4) return true;
  }
  return false;
}

/**
 * The first secret-shaped text in `text` (rule name and 1-based line), or null. The matched text
 * itself is never returned.
 * @param {string} text
 * @returns {{rule: string, line: number}|null}
 */
export function findSecret(text) {
  const lines = text.split('\n');
  for (let i = 0; i < lines.length; i += 1) {
    for (const r of REPORT_SECRET_RULES) if (r.re.test(lines[i])) return { rule: r.name, line: i + 1 };
    if (hasHighEntropyRun(lines[i])) return { rule: 'high-entropy string', line: i + 1 };
  }
  return null;
}

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

/** @param {string} pathEnv @returns {(cmd: string) => boolean} */
function pathLookup(pathEnv) {
  return (cmd) =>
    pathEnv.split(path.delimiter).some((dir) => {
      if (!dir) return false;
      try {
        const f = path.join(dir, cmd);
        accessSync(f, fsConstants.X_OK);
        return statSync(f).isFile();
      } catch {
        return false;
      }
    });
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
 * The issue title and Markdown body for `entries` (newest first).
 * @param {ErrorEntry[]} entries
 * @param {{version: string, node: string, os: string}} system
 * @param {string|null} note
 * @returns {{title: string, body: string}}
 */
export function buildReport(entries, system, note) {
  const pairs = new Set(entries.map((e) => `${e.verb}\u0000${e.kind}`));
  const title = pairs.size === 1 ? `[error report] ${entries[0].verb} ${entries[0].kind}` : `[error report] ${entries.length} errors`;
  const out = [
    '## code-forge error report',
    '',
    `- code-forge: ${system.version}`,
    `- Node: ${system.node}`,
    `- OS: ${system.os}`,
    '',
  ];
  if (note) out.push('### Note', '', note, '');
  out.push('### Summary', '', '| # | when | command | exit | kind |', '|---|---|---|---|---|');
  entries.forEach((e, i) => out.push(`| ${i + 1} | ${cell(e.ts)} | ${cell(command(e))} | ${cell(e.exit)} | ${cell(e.kind)} |`));
  out.push('', '### Errors');
  entries.forEach((e, i) => {
    out.push(
      '',
      `#### ${i + 1}. ${command(e)}: ${e.kind}`,
      '',
      `- when: ${e.ts}`,
      `- flags: ${Array.isArray(e.flags) && e.flags.length > 0 ? e.flags.map((f) => `\`${f}\``).join(' ') : 'none'}`,
      `- exit: ${e.exit}`,
      `- kind: ${e.kind}`,
      `- code-forge ${e.version}, Node ${e.node}, ${e.platform} ${e.arch}`,
      '',
      'message:',
      '',
      fenced(e.message ?? '(none)'),
    );
    if (e.stack) out.push('', 'stack:', '', fenced(e.stack));
  });
  return { title, body: `${out.join('\n')}\n` };
}

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
    report: { values: ['last', 'kind', 'verb', 'note'], booleans: ['dry-run', 'yes'] },
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
    out(flags.json ? `${JSON.stringify(name === 'summary' ? { days: n ?? 30, counts: [] } : { errors: [] })}\n` : 'no errors logged\n');
    return 0;
  }
  const newest = [...entries].reverse();

  if (name === 'list') {
    const shown = newest.slice(0, n ?? 10);
    if (flags.json) out(`${JSON.stringify({ errors: shown })}\n`);
    else for (const e of shown) out(`${e.ts}  ${command(e)}  ${e.exit}  ${e.kind}  ${firstLine(e.message)}\n`);
    return 0;
  }

  if (name === 'summary') {
    const days = n ?? 30;
    const since = (deps.now ?? (() => new Date()))().getTime() - days * 86_400_000;
    /** @type {Map<string, {verb: string, kind: string, count: number}>} */
    const counts = new Map();
    for (const e of newest) {
      const t = Date.parse(e.ts);
      if (!(t >= since)) continue;
      const key = `${e.verb}\u0000${e.kind}`;
      const c = counts.get(key) ?? { verb: e.verb, kind: e.kind, count: 0 };
      c.count += 1;
      counts.set(key, c);
    }
    const rows = [...counts.values()].sort((a, b) => b.count - a.count || a.verb.localeCompare(b.verb) || a.kind.localeCompare(b.kind));
    if (flags.json) {
      out(`${JSON.stringify({ days, counts: rows })}\n`);
    } else if (rows.length === 0) {
      out(`no errors in the last ${days} days\n`);
    } else {
      out(`errors in the last ${days} days:\n`);
      for (const r of rows) out(`${String(r.count).padStart(5)}  ${r.verb}  ${r.kind}\n`);
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
 * @param {ErrorEntry[]} newest @param {number} last
 * @param {Record<string, string|string[]|true>} flags @param {LogsDeps} deps
 * @param {(s: string) => void} out @param {(s: string) => void} err
 */
async function report(newest, last, flags, deps, out, err) {
  const env = deps.env ?? process.env;
  const home = env.HOME ?? '';
  const kind = typeof flags.kind === 'string' ? flags.kind : null;
  const verb = typeof flags.verb === 'string' ? flags.verb : null;
  const selected = newest.filter((e) => (kind === null || e.kind === kind) && (verb === null || e.verb === verb)).slice(0, last);
  if (selected.length === 0) {
    out('no matching errors to report\n');
    return 0;
  }
  const repo = issueRepo(deps.pkg);
  if (repo === null) {
    err('logs: package.json names no GitHub repository to report to\n');
    return 1;
  }
  const cwd = deps.cwd ?? process.cwd();
  const scrubber = createScrubber({ home, cwd, slug: await readProjectSlug(cwd) });
  const note = typeof flags.note === 'string' ? flags.note : null;
  const built = buildReport(selected, deps.system ?? currentSystem(), note);
  const title = scrubber.scrub(built.title);
  const body = scrubber.scrub(built.body);
  const cleaned = mergeCounts([...selected.map((e) => e.cleaned), scrubber.counts()]);

  out(DISCLOSURE);
  const hit = findSecret(`${title}\n${body}`);
  if (hit !== null) {
    err(`Possible secret found in the report (${hit.rule}, report line ${hit.line}; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n`);
    return 1;
  }
  out(`${describeCounts(cleaned)}\n\nTitle: ${title}\n\n${body}\n`);

  if (flags['dry-run']) {
    out('dry run: not sent\n');
    return 0;
  }
  if (!flags.yes) {
    if (!(deps.isTTY ?? process.stdin.isTTY === true)) {
      err('logs: no terminal to confirm; not sent — pass --yes to send the report above\n');
      return 2;
    }
    const ui = deps.ui ?? /** @type {any} */ (await import('@clack/prompts'));
    const go = await ui.confirm({ message: CONFIRM_MESSAGE, initialValue: false });
    if (ui.isCancel(go) || go !== true) {
      out('not sent\n');
      return 1;
    }
  }

  const exec = deps.exec ?? realExec;
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
    const sent = await sendWithGh(exec, env, repo, title, body, out);
    if (sent === null) return 0;
    out(`gh failed (${sent}); here is a link instead\n`);
    return printLink(repo, title, body, home, deps, out, true);
  }
  return printLink(repo, title, body, home, deps, out, false);
}

/**
 * @param {typeof realExec} exec @param {NodeJS.ProcessEnv} env
 * @param {string} repo @param {string} title @param {string} body
 * @param {(s: string) => void} out
 * @returns {Promise<string|null>} null when sent; else why gh failed (`exit n`, never its output).
 */
async function sendWithGh(exec, env, repo, title, body, out) {
  /** @type {string|null} */
  let dir = null;
  try {
    let res;
    try {
      const root = currentRunRoot();
      await mkdir(root, { recursive: true });
      dir = await mkdtemp(path.join(root, 'report-'));
      const bodyFile = path.join(dir, 'body.md');
      await writeFile(bodyFile, body, { encoding: 'utf8', mode: 0o600 });
      const base = ['gh', 'issue', 'create', '--repo', repo, '--title', title, '--body-file', bodyFile];
      res = await ghRun(exec, [...base, '--label', 'bug'], env);
      // gh's stderr is matched to decide the retry, never printed
      if (res.result !== 'ok' && /label/i.test(res.stderr)) res = await ghRun(exec, base, env);
    } catch {
      return 'could not write the report file';
    }
    if (res.result !== 'ok') return res.code !== null ? `exit ${res.code}` : res.timedOut ? 'timed out' : 'did not finish';
    const url = res.stdout.split('\n').map((l) => l.trim()).filter((l) => /^https:\/\//.test(l)).pop();
    out(`sent: ${url ?? '(gh printed no issue URL)'}\n`);
    return null;
  } finally {
    if (dir !== null) await rm(dir, { recursive: true, force: true }).catch(() => {});
  }
}

/**
 * @param {typeof realExec} exec @param {string[]} argv @param {NodeJS.ProcessEnv} env
 * @returns {Promise<{result: string, code: number|null, timedOut: boolean, stdout: string, stderr: string}>}
 */
async function ghRun(exec, argv, env) {
  try {
    return await exec(argv, { env, timeoutMs: GH_TIMEOUT_MS });
  } catch {
    return { result: 'failed', code: null, timedOut: false, stdout: '', stderr: '' };
  }
}

/** @param {string} repo @param {string} title @param {string} body */
export function issueUrl(repo, title, body) {
  return `https://github.com/${repo}/issues/new?title=${encodeURIComponent(title)}&body=${encodeURIComponent(body)}&labels=bug`;
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
 * Print a prefilled issue link; a body too long for a link is cut and saved in full.
 * @param {string} repo @param {string} title @param {string} body @param {string} home
 * @param {LogsDeps} deps @param {(s: string) => void} out
 * @param {boolean} alwaysSave - save the full body even when it fits (the gh fallback).
 */
async function printLink(repo, title, body, home, deps, out, alwaysSave) {
  let url = issueUrl(repo, title, body);
  if (url.length > MAX_URL_LENGTH) {
    const file = await saveReport(body, home, deps);
    const tail = file ? `\n\n(report cut; full report saved at ~/.code-forge/logs/${file.name})\n` : '\n\n(report cut)\n';
    const chars = Array.from(body);
    let lo = 0;
    let hi = chars.length;
    while (lo < hi) {
      const mid = Math.ceil((lo + hi) / 2);
      if (issueUrl(repo, title, chars.slice(0, mid).join('') + tail).length <= MAX_URL_LENGTH) lo = mid;
      else hi = mid - 1;
    }
    url = issueUrl(repo, title, chars.slice(0, lo).join('') + tail);
    out(file ? `The report is too long for a link: it was cut. Full report saved at ${file.saved}\n` : 'The report is too long for a link: it was cut.\n');
  } else if (alwaysSave) {
    const file = await saveReport(body, home, deps);
    if (file) out(`Full report saved at ${file.saved}\n`);
  }
  out(`Open this link to file the issue:\n${url}\n`);
  return 0;
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function logs(args) {
  return runLogs(args);
}
