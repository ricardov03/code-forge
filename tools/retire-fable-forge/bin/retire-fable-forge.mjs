#!/usr/bin/env node
/**
 * retire-fable-forge — retire the old /fable-forge Agent Skill (code-forge plan v1.3, B18/B21).
 *
 *   retire-fable-forge [--alias | --remove] [--yes] [--dry-run] [--home <dir>]
 *
 * Default (no mode, or --dry-run): list every install and print the plan; change nothing.
 * --alias : back up, then replace SKILL.md with a <= 5-line alias pointing at /code-forge.
 * --remove: back up, then delete the skill directory (or unlink the symlink; never its target).
 *
 * Standalone on purpose: it mirrors code-forge's skill paths (src/install/harnesses.mjs)
 * instead of importing them. Exit codes: 0 done / nothing to do, 1 refused or failed, 2 usage.
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';
import { fileURLToPath } from 'node:url';

export const SKILL = 'fable-forge';
/** `<home>/<marker>/skills` — Claude Code first, then the shared `.agents` root, then per-harness. */
export const USER_MARKERS = ['.claude', '.agents', '.codex', '.grok', '.gemini', '.cursor', '.copilot'];
/** Project-level skill roots, relative to the cwd. */
export const PROJECT_DIRS = ['.claude/skills', '.agents/skills'];

export const ALIAS_TEXT = [
  '---',
  'name: fable-forge',
  'description: RETIRED. /fable-forge is replaced by /code-forge; use /code-forge instead.',
  '---',
  '/fable-forge is retired. Do not run it: tell the user and use the /code-forge skill instead.',
  '',
].join('\n');

const USAGE = `usage: retire-fable-forge [--alias | --remove] [--yes] [--dry-run] [--home <dir>]

  (no flag)   list fable-forge installs and print the plan; change nothing
  --dry-run   same, even with --alias or --remove
  --alias     back up, then replace SKILL.md with a short alias to /code-forge
  --remove    back up, then delete the skill folder (a symlink is unlinked, never followed)
  --yes       do not ask; required when there is no TTY
  --home DIR  use DIR instead of $HOME (backups go to DIR/.code-forge-retired)`;

class UsageError extends Error {}

/** @param {string[]} argv */
export function parseArgs(argv) {
  const opts = { mode: null, yes: false, dryRun: false, home: null, help: false };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--alias' || a === '--remove') {
      const m = a.slice(2);
      if (opts.mode && opts.mode !== m) throw new UsageError('--alias and --remove are exclusive');
      opts.mode = m;
    } else if (a === '--yes' || a === '-y') opts.yes = true;
    else if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--help' || a === '-h') opts.help = true;
    else if (a === '--home') {
      const v = argv[++i];
      if (!v || v.startsWith('-')) throw new UsageError('--home needs a directory');
      opts.home = v;
    } else if (a.startsWith('--home=')) {
      const v = a.slice('--home='.length);
      if (!v) throw new UsageError('--home needs a directory');
      opts.home = v;
    } else throw new UsageError(`unknown argument: ${a}`);
  }
  return opts;
}

/** @param {string} p */
function lstatOrNull(p) {
  try {
    return fs.lstatSync(p);
  } catch {
    return null;
  }
}

/** @param {string} file */
function sha256OrNull(file) {
  try {
    return createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  } catch {
    return null;
  }
}

/**
 * @param {string} home
 * @param {string} cwd
 * @returns {{root: string, scope: string, slug: string}[]}
 */
export function skillRoots(home, cwd) {
  const roots = USER_MARKERS.map((m) => ({
    root: path.join(home, m, 'skills'),
    scope: 'user',
    slug: `user${m.replace('.', '-')}`,
  }));
  for (const d of PROJECT_DIRS) {
    const root = path.join(cwd, d);
    if (!roots.some((r) => r.root === root)) {
      roots.push({ root, scope: 'project', slug: `project-${d.split('/')[0].replace('.', '')}` });
    }
  }
  return roots;
}

/**
 * Find every fable-forge install. `kind` is `symlink` (with `target`), `copy` (a real folder whose
 * SKILL.md is byte-identical to another install's), `directory`, or `file` (not a folder).
 * @param {string} home
 * @param {string} cwd
 */
export function detect(home, cwd) {
  const found = [];
  /** @type {Map<string, any>} real skills-root path -> first install found under it */
  const byRealRoot = new Map();
  for (const r of skillRoots(home, cwd)) {
    const p = path.join(r.root, SKILL);
    const st = lstatOrNull(p);
    if (!st) continue;
    // A skills root that is itself a symlink (e.g. ~/.codex/skills -> ~/.claude/skills) reaches
    // the same folder twice: keep the first path, record the other as an alias of it.
    const realRoot = fs.realpathSync(path.dirname(p));
    const first = byRealRoot.get(realRoot);
    if (first) {
      first.alsoVia.push(p);
      continue;
    }
    const rootSt = lstatOrNull(r.root);
    /** @type {any} */
    const item = {
      ...r,
      path: p,
      kind: 'directory',
      target: null,
      copyOf: null,
      rootLink: rootSt?.isSymbolicLink() ? fs.readlinkSync(r.root) : null,
      alsoVia: [],
    };
    byRealRoot.set(realRoot, item);
    if (st.isSymbolicLink()) {
      item.kind = 'symlink';
      item.target = fs.readlinkSync(p);
    } else if (!st.isDirectory()) item.kind = 'file';
    item.sha = sha256OrNull(path.join(p, 'SKILL.md'));
    found.push(item);
  }
  for (const it of found) {
    if (it.kind !== 'directory' || !it.sha) continue;
    const twin = found.find((o) => o !== it && o.sha === it.sha);
    if (twin) {
      it.kind = 'copy';
      it.copyOf = twin.path;
    }
  }
  return found;
}

/** @param {any} it */
export function describe(it) {
  let base = it.kind;
  if (it.kind === 'symlink') base = `symlink -> ${it.target}`;
  else if (it.kind === 'copy') base = `copy (same SKILL.md as ${it.copyOf})`;
  if (it.rootLink) base += ` (skills root is a symlink -> ${it.rootLink})`;
  for (const v of it.alsoVia) base += `\n      same folder, also reached via ${v} (symlinked skills root)`;
  return base;
}

/**
 * Safety checks. Returns the reason to refuse, or null.
 * @param {any} it
 * @param {string[]} allowedRoots
 */
export function refusal(it, allowedRoots) {
  const parent = path.dirname(it.path);
  if (path.basename(it.path) !== SKILL || !allowedRoots.includes(parent)) {
    return `path is not exactly <skills root>/${SKILL}`;
  }
  if (it.kind === 'file') return 'not a folder';
  let text;
  try {
    text = fs.readFileSync(path.join(it.path, 'SKILL.md'), 'utf8');
  } catch {
    return 'no SKILL.md';
  }
  const name = frontmatterName(text);
  if (name === 'code-forge') return 'SKILL.md is code-forge; never touched';
  if (name !== SKILL) return `SKILL.md frontmatter has no "name: ${SKILL}"`;
  return null;
}

/** @param {string} text */
export function frontmatterName(text) {
  const lines = text.split(/\r?\n/);
  if (lines[0].trim() !== '---') return null;
  for (let i = 1; i < lines.length; i++) {
    if (lines[i].trim() === '---') return null;
    const m = /^name:\s*["']?([^"'\s]+)["']?\s*$/.exec(lines[i]);
    if (m) return m[1];
  }
  return null;
}

/** @param {string} s */
function q(s) {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/**
 * tar the install (a symlink is stored as the link itself), then verify the archive lists.
 * @param {any} it
 * @param {string} backupDir
 * @param {string} stamp
 */
export function backup(it, backupDir, stamp) {
  fs.mkdirSync(backupDir, { recursive: true });
  const tgz = path.join(backupDir, `${SKILL}-${stamp}-${it.slug}.tgz`);
  const root = path.dirname(it.path);
  execFileSync('tar', ['-czf', tgz, '-C', root, SKILL], { stdio: ['ignore', 'pipe', 'pipe'] });
  const listing = execFileSync('tar', ['-tzf', tgz], { encoding: 'utf8' })
    .split('\n')
    .map((l) => l.replace(/\/$/, ''))
    .filter(Boolean);
  const want = it.kind === 'symlink' ? SKILL : `${SKILL}/SKILL.md`;
  if (!listing.includes(want)) throw new Error(`backup ${tgz} does not list ${want}`);
  return tgz;
}

/**
 * @param {any} it
 * @param {string} tgz
 * @param {string} mode
 */
export function restoreCommand(it, tgz, mode) {
  const root = path.dirname(it.path);
  const clear = mode === 'alias' && it.kind === 'symlink' ? `rm -r ${q(it.path)} && ` : '';
  return `${clear}mkdir -p ${q(root)} && tar -xzf ${q(tgz)} -C ${q(root)}`;
}

/**
 * @param {any} it
 * @param {string} mode
 */
function apply(it, mode) {
  if (it.kind === 'symlink') {
    fs.unlinkSync(it.path); // the link only; its target is never opened for writing
    if (mode === 'alias') {
      fs.mkdirSync(it.path);
      fs.writeFileSync(path.join(it.path, 'SKILL.md'), ALIAS_TEXT, { flag: 'wx' });
    }
    return;
  }
  if (mode === 'alias') {
    // SKILL.md may itself be a symlink: unlink the entry (never follow it), then create a new file.
    const md = path.join(it.path, 'SKILL.md');
    if (lstatOrNull(md)) fs.unlinkSync(md);
    fs.writeFileSync(md, ALIAS_TEXT, { flag: 'wx' });
  } else fs.rmSync(it.path, { recursive: true });
}

/** @param {string} home */
export function memoryMentions(home) {
  const out = [];
  const projects = path.join(home, '.claude', 'projects');
  let dirs = [];
  try {
    dirs = fs.readdirSync(projects);
  } catch {
    return out;
  }
  for (const d of dirs.sort()) {
    const f = path.join(projects, d, 'memory', 'MEMORY.md');
    let text;
    try {
      text = fs.readFileSync(f, 'utf8');
    } catch {
      continue;
    }
    text.split(/\r?\n/).forEach((line, i) => {
      if (/fable-forge/i.test(line)) out.push(`${f}:${i + 1}: ${line.trim()}`);
    });
  }
  return out;
}

/** @param {string} prompt */
async function confirm(prompt) {
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  try {
    return /^y(es)?$/i.test((await rl.question(prompt)).trim());
  } finally {
    rl.close();
  }
}

/**
 * @param {string[]} argv
 * @param {{cwd?: string, env?: NodeJS.ProcessEnv, log?: (s: string) => void, isTTY?: boolean, now?: Date}} [io]
 * @returns {Promise<number>}
 */
export async function main(argv, io = {}) {
  const log = io.log ?? ((s) => process.stdout.write(`${s}\n`));
  let opts;
  try {
    opts = parseArgs(argv);
  } catch (e) {
    if (!(e instanceof UsageError)) throw e;
    log(`retire-fable-forge: ${e.message}\n${USAGE}`);
    return 2;
  }
  if (opts.help) {
    log(USAGE);
    return 0;
  }
  const env = io.env ?? process.env;
  const home = path.resolve(opts.home ?? env.HOME ?? os.homedir());
  const cwd = path.resolve(io.cwd ?? process.cwd());
  const roots = skillRoots(home, cwd);
  const found = detect(home, cwd);
  const mentions = memoryMentions(home);

  if (found.length === 0) {
    log('fable-forge: nothing to retire (no install found).');
    printMentions(mentions, log);
    return 0;
  }
  log(`fable-forge installs (${found.length}):`);
  const refused = [];
  for (const it of found) {
    const why = refusal(
      it,
      roots.map((r) => r.root),
    );
    log(`  [${it.scope}] ${it.path}  ${describe(it)}${why ? `  REFUSED: ${why}` : ''}`);
    if (why) refused.push(it);
  }
  printMentions(mentions, log);

  const backupDir = path.join(home, '.code-forge-retired');
  if (!opts.mode || opts.dryRun) {
    const action = {
      alias: 'replace SKILL.md with a 5-line alias to /code-forge',
      remove: 'delete it (a symlink is unlinked, its target kept)',
    }[opts.mode ?? ''] ?? 'alias it (--alias) or delete it (--remove)';
    log(`\nplan: back up each install to ${backupDir}/, then ${action}.`);
    log('dry run: nothing changed. Re-run with --alias or --remove, plus --yes.');
    return refused.length ? 1 : 0;
  }
  if (refused.length) {
    log(`refused: ${refused.length} install(s) failed the safety checks; nothing changed.`);
    return 1;
  }
  if (!opts.yes) {
    const tty = io.isTTY ?? Boolean(process.stdin.isTTY && process.stdout.isTTY);
    if (!tty) {
      log('refused: --yes is required when there is no TTY; nothing changed.');
      return 1;
    }
    if (!(await confirm(`--${opts.mode} ${found.length} install(s)? [y/N] `))) {
      log('refused: not confirmed; nothing changed.');
      return 1;
    }
  }

  const stamp = (io.now ?? new Date()).toISOString().replace(/[:.]/g, '-');
  let failed = 0;
  for (const it of found) {
    try {
      const tgz = backup(it, backupDir, stamp);
      log(`backup: ${tgz} (verified)`);
      apply(it, opts.mode);
      log(`${opts.mode === 'alias' ? 'aliased' : it.kind === 'symlink' ? 'unlinked' : 'removed'}: ${it.path}`);
      log(`restore: ${restoreCommand(it, tgz, opts.mode)}`);
    } catch (e) {
      failed++;
      log(`failed: ${it.path}: ${/** @type {Error} */ (e).message}`);
    }
  }
  return failed ? 1 : 0;
}

/**
 * @param {string[]} mentions
 * @param {(s: string) => void} log
 */
function printMentions(mentions, log) {
  if (!mentions.length) return;
  log(`\nmemory lines that mention fable-forge (not edited):`);
  for (const m of mentions) log(`  ${m}`);
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).then(
    (code) => {
      process.exitCode = code;
    },
    (e) => {
      process.stderr.write(`retire-fable-forge: ${e.message}\n`);
      process.exitCode = 1;
    },
  );
}
