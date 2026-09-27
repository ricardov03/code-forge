#!/usr/bin/env node
/**
 * The release tool (B23): bump every version location, move the CHANGELOG's `[Unreleased]`
 * section under the new version, run the checks, commit `Release vX.Y.Z` and create the annotated
 * tag `vX.Y.Z`. It NEVER pushes and NEVER publishes: it prints the exact next commands instead.
 *
 *   npm run release -- <patch|minor|major|x.y.z> [--dry-run] [--date YYYY-MM-DD] [--no-checks]
 *                      [--allow-branch <name>]
 *   node scripts/release.mjs notes <vX.Y.Z> [--out <file>]   (the GitHub release body; CI uses it)
 *
 * Steps: 1 preflight (clean tree, branch, up to date with origin, `[Unreleased]` has entries, the
 * new version beats the current one and npm's latest, the tag is free locally and on origin),
 * 2 bump, 3 CHANGELOG, 4 checks (any failure restores every changed file byte-for-byte),
 * 5 commit + tag, 6 next steps. `--dry-run` runs the preflight, prints the plan and the diffs, and
 * writes nothing (it never fetches either: origin is read with `git ls-remote`).
 *
 * Runs in the current directory (npm runs scripts from the package root). Every child is an argv
 * array, never a shell string; git runs without the repository-picking `GIT_*` variables a hook
 * may export. Exit 0 on success, 1 on a refusal or a failed step, 2 on usage.
 */

import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { findSection, findUnreleased, parseSubsections } from './changelog.mjs';

const USAGE =
  'usage: npm run release -- <patch|minor|major|x.y.z> [--dry-run] [--date YYYY-MM-DD] [--no-checks] [--allow-branch <name>]';

/** Git variables that pick a repository (git hooks export them); stripped from every git call. */
const REPO_ENV = ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX', 'GIT_OBJECT_DIRECTORY',
  'GIT_COMMON_DIR', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_IMPLICIT_WORK_TREE'];

export class Refusal extends Error {}
export class UsageError extends Error {}

// ── arguments and versions ──────────────────────────────────────────────────────────────────

/** @param {string[]} argv */
export function parseArgs(argv) {
  const opts = { bump: '', dryRun: false, date: '', checks: true, allowBranch: '' };
  for (let i = 0; i < argv.length; i += 1) {
    const a = argv[i];
    if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--no-checks') opts.checks = false;
    else if (a === '--date' || a === '--allow-branch') {
      const v = argv[i + 1];
      if (!v || v.startsWith('-')) throw new UsageError(`${a} needs a value`);
      if (a === '--date') opts.date = v;
      else opts.allowBranch = v;
      i += 1;
    } else if (a.startsWith('-')) throw new UsageError(`unknown option ${a}`);
    else if (opts.bump) throw new UsageError(`one version argument only (got ${opts.bump} and ${a})`);
    else opts.bump = a;
  }
  if (!opts.bump) throw new UsageError('missing <patch|minor|major|x.y.z>');
  if (opts.date && !/^\d{4}-\d{2}-\d{2}$/.test(opts.date)) throw new UsageError('--date must be YYYY-MM-DD');
  return opts;
}

/** @param {string} v @returns {[number, number, number] | null} */
export function parseVersion(v) {
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(v);
  return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null;
}

/** @param {string} a @param {string} b @returns {number} */
export function compareVersions(a, b) {
  const x = parseVersion(a);
  const y = parseVersion(b);
  for (let i = 0; i < 3; i += 1) if (x[i] !== y[i]) return x[i] - y[i];
  return 0;
}

/** @param {string} current @param {string} bump @returns {string} */
export function nextVersion(current, bump) {
  const c = parseVersion(current);
  if (!c) throw new Refusal(`package.json version "${current}" is not x.y.z`);
  if (bump === 'major') return `${c[0] + 1}.0.0`;
  if (bump === 'minor') return `${c[0]}.${c[1] + 1}.0`;
  if (bump === 'patch') return `${c[0]}.${c[1]}.${c[2] + 1}`;
  const explicit = bump.replace(/^v/, '');
  if (!parseVersion(explicit)) throw new UsageError(`"${bump}" is not patch, minor, major or x.y.z`);
  return explicit;
}

// ── field-exact file edits ──────────────────────────────────────────────────────────────────

/**
 * Set fields in a JSON text, keeping its indentation and its trailing newline.
 * @param {string} text
 * @param {Array<{ keys: string[], value: string }>} edits
 * @returns {string}
 */
export function setJsonFields(text, edits) {
  const data = JSON.parse(text);
  for (const { keys, value } of edits) {
    let obj = data;
    for (const k of keys.slice(0, -1)) obj = obj?.[k];
    const last = keys.at(-1);
    if (!obj || typeof obj[last] !== 'string') throw new Refusal(`no string field ${keys.join('.')}`);
    obj[last] = value;
  }
  const indentMatch = /\n([ \t]+)"/.exec(text);
  const indent = indentMatch ? indentMatch[1] : 2;
  const newline = text.endsWith('\n') ? '\n' : '';
  // Re-serializing is only field-exact when the file is already in JSON.stringify's own layout;
  // otherwise it would reformat unrelated lines (inline arrays, escapes), so refuse instead.
  if (JSON.stringify(JSON.parse(text), null, indent) + newline !== text) {
    throw new Refusal(`a JSON file is not in canonical JSON.stringify layout (indent ${JSON.stringify(indent)}); reformat it first so the release only changes the version`);
  }
  return JSON.stringify(data, null, indent) + newline;
}

/** @param {string} text @param {string[]} keys @returns {unknown} */
function getJsonField(text, keys) {
  let obj = JSON.parse(text);
  for (const k of keys) obj = obj?.[k];
  return obj;
}

/**
 * Set the shim's `PINNED_VERSION="x.y.z"` line by a line edit: exactly one line may start with
 * `PINNED_VERSION="`, it must hold `current`, and only that line changes.
 * @param {string} text @param {string} current @param {string} next @param {string} file
 * @returns {string}
 */
export function setPinnedVersion(text, current, next, file = 'skill/scripts/forge') {
  const lines = text.split('\n');
  const at = lines.flatMap((l, i) => (l.startsWith('PINNED_VERSION="') ? [i] : []));
  if (at.length !== 1) throw new Refusal(`${file}: expected exactly 1 PINNED_VERSION="…" line, found ${at.length}`);
  if (lines[at[0]] !== `PINNED_VERSION="${current}"`) {
    throw new Refusal(`${file}: the PINNED_VERSION line is ${lines[at[0]]}, package.json says ${current}`);
  }
  lines[at[0]] = `PINNED_VERSION="${next}"`;
  return lines.join('\n');
}

/**
 * Every place that carries the package version. `required` files must exist; the others are
 * bumped only when present (a `package-lock.json`, say).
 */
const LOCATIONS = [
  { file: 'package.json', required: true, json: [['version']] },
  { file: 'npm-shrinkwrap.json', required: false, json: [['version'], ['packages', '', 'version']] },
  { file: 'package-lock.json', required: false, json: [['version'], ['packages', '', 'version']] },
  { file: '.claude-plugin/plugin.json', required: false, json: [['version']] },
  { file: 'skill/scripts/forge', required: false, shim: true },
];

/**
 * Read every present location and check it carries `current`.
 * @param {string} root @param {string} current
 * @returns {Array<{ file: string, label: string, before: string, bump: (v: string) => string }>}
 */
function readLocations(root, current) {
  const found = [];
  for (const loc of LOCATIONS) {
    const abs = path.join(root, loc.file);
    if (!existsSync(abs)) {
      if (loc.required) throw new Refusal(`${loc.file} not found in ${root}`);
      continue;
    }
    const before = readFileSync(abs, 'utf8');
    if (loc.shim) {
      setPinnedVersion(before, current, current, loc.file);
      found.push({ file: loc.file, label: 'PINNED_VERSION', before,
        bump: (v) => setPinnedVersion(before, current, v, loc.file) });
      continue;
    }
    for (const keys of loc.json) {
      const got = getJsonField(before, keys);
      if (got !== current) throw new Refusal(`${loc.file}: ${fieldLabel(keys)} is ${JSON.stringify(got)}, package.json says ${current}`);
    }
    found.push({ file: loc.file, label: loc.json.map(fieldLabel).join(', '), before,
      bump: (v) => setJsonFields(before, loc.json.map((keys) => ({ keys, value: v }))) });
  }
  return found;
}

/** @param {string[]} keys */
const fieldLabel = (keys) => keys.map((k) => (k === '' ? '[""]' : k)).join('.').replace('.[', '[');

// ── CHANGELOG ───────────────────────────────────────────────────────────────────────────────

/**
 * Move `## [Unreleased]` under `## [version] — date`, add a fresh empty `[Unreleased]` above it,
 * and update compare links only when the file already has an `[Unreleased]: …/compare/…` link.
 * @param {string} text @param {string} version @param {string} date
 * @returns {{ text: string, notes: string }}
 */
export function releaseChangelog(text, version, date) {
  const lines = text.split('\n');
  const sec = findUnreleased(lines);
  if (!sec) throw new Refusal('CHANGELOG.md has no "## [Unreleased]" heading: add an entry with npm run changelog -- added "…"');
  const { start, end } = sec;
  const body = lines.slice(start + 1, end);
  if (!parseSubsections(lines, start + 1, end).some((s) => s.bullets.length)) {
    throw new Refusal('CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"');
  }
  const notes = body.join('\n').trim();
  const out = [...lines.slice(0, start), '## [Unreleased]', '', `## [${version}] — ${date}`, ...body, ...lines.slice(end)];

  const linkIdx = out.findIndex((l) => /^\[Unreleased\]:\s*\S+\/compare\/\S+\.\.\.HEAD\s*$/i.test(l));
  if (linkIdx >= 0) {
    const m = /^\[Unreleased\]:\s*(\S+)\/compare\/(\S+)\.\.\.HEAD\s*$/i.exec(out[linkIdx]);
    out.splice(linkIdx, 1, `[Unreleased]: ${m[1]}/compare/v${version}...HEAD`, `[${version}]: ${m[1]}/compare/${m[2]}...v${version}`);
  }
  return { text: out.join('\n'), notes };
}

/**
 * A small line diff for display. Files whose line count is unchanged (the version fields) show
 * each changed line on its own; otherwise the changed region between the common prefix and
 * suffix is shown (the CHANGELOG head).
 * @param {string} a @param {string} b @param {number} [context]
 * @returns {string[]}
 */
export function lineDiff(a, b, context = 1) {
  const x = a.split('\n');
  const y = b.split('\n');
  const out = [];
  if (x.length === y.length) {
    x.forEach((line, i) => {
      if (line === y[i]) return;
      if (i > 0) out.push(`  ${x[i - 1]}`);
      out.push(`- ${line}`, `+ ${y[i]}`);
    });
    return out;
  }
  let p = 0;
  while (p < x.length && p < y.length && x[p] === y[p]) p += 1;
  let s = 0;
  while (s < x.length - p && s < y.length - p && x[x.length - 1 - s] === y[y.length - 1 - s]) s += 1;
  for (let i = Math.max(0, p - context); i < p; i += 1) out.push(`  ${x[i]}`);
  for (let i = p; i < x.length - s; i += 1) out.push(`- ${x[i]}`);
  for (let i = p; i < y.length - s; i += 1) out.push(`+ ${y[i]}`);
  for (let i = x.length - s; i < Math.min(x.length, x.length - s + context); i += 1) out.push(`  ${x[i]}`);
  return out;
}

/** Today in the local time zone, YYYY-MM-DD. */
function localToday() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
}

// ── processes ───────────────────────────────────────────────────────────────────────────────

/**
 * Run an argv array (never a shell).
 * @param {string} cmd @param {string[]} args
 * @param {{ cwd: string, timeout?: number, env?: NodeJS.ProcessEnv }} opts
 */
function run(cmd, args, opts) {
  const r = spawnSync(cmd, args, { cwd: opts.cwd, env: opts.env ?? process.env, encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024, timeout: opts.timeout });
  return { code: r.error ? -1 : r.status ?? -1, stdout: r.stdout ?? '', stderr: r.stderr ?? '', error: r.error };
}

function gitEnv() {
  const env = { ...process.env, GIT_TERMINAL_PROMPT: '0' };
  for (const k of REPO_ENV) delete env[k];
  return env;
}

/** @param {string} cwd @param {string[]} args @param {number} [timeout] */
function git(cwd, args, timeout) {
  return run('git', args, { cwd, env: gitEnv(), timeout });
}

/** @param {string} cwd @param {string[]} args */
function gitOk(cwd, args) {
  const r = git(cwd, args, 60000);
  if (r.code !== 0) throw new Refusal(`git ${args.join(' ')} failed: ${(r.stderr || r.stdout).trim()}`);
  return r.stdout;
}

/** @param {string} name @returns {boolean} */
function onPath(name) {
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    if (!dir) continue;
    try {
      if (statSync(path.join(dir, name)).isFile()) return true;
    } catch { /* not here */ }
  }
  return false;
}

/** @param {string} msg */
const warn = (msg) => process.stdout.write(`WARN  ${msg}\n`);
/** @param {string} msg */
const ok = (msg) => process.stdout.write(`ok    ${msg}\n`);

// ── preflight ───────────────────────────────────────────────────────────────────────────────

/**
 * @param {string} root
 * @param {{ branch: string, allowBranch: string, tag: string, current: string, next: string, name: string }} p
 * @param {(msg: string) => void} fail - throws a Refusal, or (dry run) records it and lets the next check run
 */
function preflight(root, p, fail) {
  const inside = git(root, ['rev-parse', '--is-inside-work-tree']);
  if (inside.code !== 0 || inside.stdout.trim() !== 'true') throw new Refusal(`${root} is not a git work tree`);

  const status = gitOk(root, ['status', '--porcelain', '--untracked-files=normal']);
  if (status.trim()) fail(`the working tree is not clean:\n${status.trimEnd()}`);
  else ok('working tree is clean');

  const wanted = p.allowBranch || 'main';
  if (p.branch !== wanted) fail(`on branch "${p.branch}", expected "${wanted}" (use --allow-branch ${p.branch} to release from it)`);
  else ok(`on branch ${p.branch}`);

  const remote = git(root, ['ls-remote', '--heads', 'origin', `refs/heads/${p.branch}`], 60000);
  const remoteSha = remote.code === 0 ? remote.stdout.split(/\s/)[0] : '';
  const head = gitOk(root, ['rev-parse', 'HEAD']).trim();
  if (remote.code !== 0) fail(`cannot read origin: ${remote.stderr.trim()}`);
  else if (!remoteSha) fail(`origin has no branch ${p.branch}: push it first`);
  else if (remoteSha === head) ok(`up to date with origin/${p.branch}`);
  else if (git(root, ['cat-file', '-e', `${remoteSha}^{commit}`]).code !== 0) {
    fail(`${p.branch} is behind origin/${p.branch} (origin has commits you do not have): pull first`);
  } else if (git(root, ['merge-base', '--is-ancestor', head, remoteSha]).code === 0) {
    fail(`${p.branch} is behind origin/${p.branch}: pull first`);
  } else if (git(root, ['merge-base', '--is-ancestor', remoteSha, head]).code !== 0) {
    fail(`${p.branch} and origin/${p.branch} have diverged: reconcile them first`);
  } else warn(`${p.branch} is ahead of origin/${p.branch}: the push will also send those commits`);

  const view = run('npm', ['view', p.name, 'version'], { cwd: root, timeout: 30000 });
  const published = view.stdout.trim();
  if (compareVersions(p.next, p.current) <= 0) fail(`the new version ${p.next} is not greater than the current ${p.current}`);
  else if (view.code === 0 && parseVersion(published)) {
    if (compareVersions(p.next, published) <= 0) fail(`the new version ${p.next} is not greater than the latest on npm (${published})`);
    else ok(`${p.next} > ${p.current} (package.json) and > ${published} (npm)`);
  } else if (/E404/.test(view.stderr)) {
    ok(`${p.next} > ${p.current}; ${p.name} is not on npm yet`);
  } else {
    warn(`could not read the latest version from npm (${(view.stderr || view.error?.message || 'no output').trim().split('\n')[0]}); compared with package.json only`);
  }

  const remoteTag = git(root, ['ls-remote', '--tags', 'origin', `refs/tags/${p.tag}`], 60000);
  if (git(root, ['rev-parse', '-q', '--verify', `refs/tags/${p.tag}`]).code === 0) fail(`the tag ${p.tag} already exists locally`);
  else if (remoteTag.code !== 0) fail(`cannot read origin's tags: ${remoteTag.stderr.trim()}`);
  else if (remoteTag.stdout.trim()) fail(`the tag ${p.tag} already exists on origin`);
  else ok(`tag ${p.tag} is free (local and origin)`);
}

// ── checks ──────────────────────────────────────────────────────────────────────────────────

/** @param {string} root @param {string} name @param {string} version @returns {string | null} a failure, or null */
function runChecks(root, name, version) {
  /** @param {string} label @param {ReturnType<typeof run>} r */
  const failed = (label, r) => {
    const tail = `${r.stdout}${r.stderr}`.trimEnd().split('\n').slice(-30).join('\n');
    return `${label} failed (exit ${r.code})${tail ? `:\n${tail}` : ''}`;
  };
  process.stdout.write('check npm test …\n');
  let r = run('npm', ['test'], { cwd: root });
  if (r.code !== 0) return failed('npm test', r);
  ok('npm test');

  r = run('npm', ['run', 'typecheck'], { cwd: root });
  if (r.code !== 0) return failed('npm run typecheck', r);
  ok('npm run typecheck');

  r = run('npm', ['pack', '--dry-run', '--json'], { cwd: root });
  if (r.code !== 0) return failed('npm pack --dry-run', r);
  let pack;
  try {
    pack = JSON.parse(r.stdout)[0];
  } catch {
    return 'npm pack --dry-run --json printed no JSON';
  }
  if (pack?.name !== name || pack?.version !== version) {
    return `npm pack --dry-run: the tarball is ${pack?.name}@${pack?.version}, expected ${name}@${version}`;
  }
  ok(`npm pack --dry-run: ${name}@${version}, ${pack.entryCount ?? pack.files?.length} files`);

  r = run('npm', ['ls', '--json'], { cwd: root });
  let tree;
  try {
    tree = JSON.parse(r.stdout);
  } catch {
    return failed('npm ls --json (no JSON)', r);
  }
  const bad = [];
  /** @param {any} node @param {string} at */
  const walk = (node, at) => {
    for (const [dep, info] of Object.entries(node?.dependencies ?? {})) {
      const i = /** @type {any} */ (info);
      if (i?.missing) bad.push(`${at}${dep} (missing)`);
      if (i?.invalid) bad.push(`${at}${dep} (invalid)`);
      walk(i, `${at}${dep} > `);
    }
  };
  walk(tree, '');
  if (bad.length || (Array.isArray(tree.problems) && tree.problems.length)) {
    return `npm ls: ${bad.length} invalid or missing: ${bad.join(', ') || tree.problems.join('; ')}`;
  }
  ok('npm ls: 0 invalid, 0 missing');

  if (onPath('claude')) {
    r = run('claude', ['plugin', 'validate', '.'], { cwd: root, timeout: 120000 });
    if (r.code !== 0) return failed('claude plugin validate .', r);
    ok('claude plugin validate .');
  } else {
    warn('the claude CLI is not on PATH: "claude plugin validate ." was skipped (the publish workflow runs it)');
  }
  return null;
}

// ── next steps ──────────────────────────────────────────────────────────────────────────────

/** @param {string} root @returns {'set' | 'not set' | 'unknown'} */
function npmTokenState(root) {
  if (!onPath('gh')) return 'unknown';
  const r = run('gh', ['secret', 'list'], { cwd: root, timeout: 30000 });
  if (r.code !== 0) return 'unknown';
  return r.stdout.split('\n').some((l) => l.split(/\s+/)[0] === 'NPM_TOKEN') ? 'set' : 'not set';
}

// ── release notes ─────────────────────────────────────────────────────────────────────────

/**
 * `owner/repo` from a package.json `repository` (string or `{ url }`) on GitHub, else null.
 * @param {unknown} repository @returns {string | null}
 */
export function githubRepo(repository) {
  const url = typeof repository === 'string' ? repository : /** @type {any} */ (repository)?.url;
  const m = /github\.com[/:]([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/.exec(String(url ?? '')) ?? /^(?:github:)?([\w.-]+)\/([\w.-]+)$/.exec(String(url ?? ''));
  return m ? `${m[1]}/${m[2]}` : null;
}

/**
 * The GitHub release body for `version`: its CHANGELOG section, then the npm and changelog links.
 * @param {string} changelog @param {{ name: string, repository?: unknown }} pkg @param {string} version - x.y.z
 * @returns {string | null} null when the CHANGELOG has no section for `version`
 */
export function releaseNotes(changelog, pkg, version) {
  const lines = changelog.split('\n');
  const sec = findSection(lines, version);
  if (!sec) return null;
  const body = lines.slice(sec.start + 1, sec.end).join('\n').trim();
  const links = [`npm: https://www.npmjs.com/package/${pkg.name}/v/${version}`];
  const repo = githubRepo(pkg.repository);
  if (repo) links.push(`Full changelog: https://github.com/${repo}/blob/v${version}/CHANGELOG.md`);
  return `${body ? `${body}\n\n` : ''}${links.join('\n')}\n`;
}

/**
 * `release.mjs notes <vX.Y.Z> [--out <file>]`: print (or write) the release body.
 * @param {string[]} argv @param {string} root @returns {number}
 */
function notesMain(argv, root) {
  let version = '';
  let out = '';
  for (let i = 0; i < argv.length; i += 1) {
    if (argv[i] === '--out' && argv[i + 1]) {
      out = argv[i + 1];
      i += 1;
    } else if (!version && !argv[i].startsWith('-')) version = argv[i].replace(/^v/, '');
    else {
      process.stderr.write(`usage: node scripts/release.mjs notes <vX.Y.Z> [--out <file>]\n`);
      return 2;
    }
  }
  if (!parseVersion(version)) {
    process.stderr.write(`usage: node scripts/release.mjs notes <vX.Y.Z> [--out <file>]\n`);
    return 2;
  }
  const pkg = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
  const notes = releaseNotes(readFileSync(path.join(root, 'CHANGELOG.md'), 'utf8'), pkg, version);
  if (notes === null) {
    process.stderr.write(`REFUSED: CHANGELOG.md has no "## [${version}]" section\n`);
    return 1;
  }
  if (out) {
    writeFileSync(path.resolve(root, out), notes);
    process.stdout.write(`release notes for v${version} written to ${out}\n`);
  } else process.stdout.write(notes);
  return 0;
}

// ── main ────────────────────────────────────────────────────────────────────────────────────

/** @param {string[]} argv @param {string} root @returns {number} exit code */
export function main(argv, root = process.cwd()) {
  if (argv[0] === 'notes') return notesMain(argv.slice(1), root);
  let opts;
  try {
    opts = parseArgs(argv);
  } catch (e) {
    process.stderr.write(`${e.message}\n${USAGE}\n`);
    return 2;
  }
  /** @type {Array<{ file: string, before: string, after: string }>} */
  let written = [];
  const restore = () => {
    for (const w of written) writeFileSync(path.join(root, w.file), w.before);
  };
  try {
    const pkgText = readFileSync(path.join(root, 'package.json'), 'utf8');
    const pkg = JSON.parse(pkgText);
    const { name, version: current } = pkg;
    const next = nextVersion(current, opts.bump);
    const tag = `v${next}`;
    const date = opts.date || localToday();
    const branch = gitOk(root, ['rev-parse', '--abbrev-ref', 'HEAD']).trim();

    process.stdout.write(`release ${name}: ${current} → ${next}${opts.dryRun ? ' (dry run: nothing is written)' : ''}\n\n== 1. preflight ==\n`);
    const locations = readLocations(root, current);
    const changelogPath = path.join(root, 'CHANGELOG.md');
    if (!existsSync(changelogPath)) throw new Refusal('CHANGELOG.md not found');
    const changelogBefore = readFileSync(changelogPath, 'utf8');
    const { text: changelogAfter, notes } = releaseChangelog(changelogBefore, next, date);
    ok('CHANGELOG.md [Unreleased] has entries');
    /** @type {string[]} */
    const refusals = [];
    preflight(root, { branch, allowBranch: opts.allowBranch, tag, current, next, name }, (msg) => {
      if (!opts.dryRun) throw new Refusal(msg);
      refusals.push(msg);
      process.stdout.write(`FAIL  ${msg}\n`);
    });

    const plan = [
      ...locations.map((l) => ({ file: l.file, label: l.label, before: l.before, after: l.bump(next) })),
      { file: 'CHANGELOG.md', label: `[Unreleased] → [${next}] — ${date}`, before: changelogBefore, after: changelogAfter },
    ];

    process.stdout.write('\n== 2. bump + 3. CHANGELOG ==\n');
    for (const f of plan) {
      process.stdout.write(`${f.file}: ${f.label}${f.file === 'CHANGELOG.md' ? '' : `  ${current} → ${next}`}\n`);
      const diff = lineDiff(f.before, f.after, 3);
      if (opts.dryRun || f.file === 'CHANGELOG.md') for (const l of diff) process.stdout.write(`    ${l}\n`);
    }

    if (opts.dryRun) {
      process.stdout.write('\n== plan (dry run) ==\n');
      process.stdout.write(`would run the checks: ${opts.checks ? 'npm test, npm run typecheck, npm pack --dry-run, npm ls --json, claude plugin validate .' : 'none (--no-checks)'}\n`);
      process.stdout.write(`would commit "Release ${tag}" (${plan.length} files) and create the annotated tag ${tag}\n`);
      process.stdout.write(`would print: git push origin ${branch} --follow-tags\n`);
      process.stdout.write('dry run: 0 files changed, 0 commits, 0 tags\n');
      if (refusals.length) {
        process.stderr.write(`\nREFUSED: a real run would stop at ${refusals.length} preflight failure(s):\n${refusals.map((r) => `  - ${r.split('\n')[0]}`).join('\n')}\n`);
        return 1;
      }
      return 0;
    }

    for (const f of plan) {
      writeFileSync(path.join(root, f.file), f.after);
      written.push(f);
    }

    process.stdout.write('\n== 4. checks ==\n');
    if (opts.checks) {
      const failure = runChecks(root, name, next);
      if (failure) {
        restore();
        process.stderr.write(`\nFAILED: ${failure}\nrolled back ${written.length} files; nothing was committed or tagged\n`);
        return 1;
      }
    } else {
      warn('!!! --no-checks: npm test, typecheck, npm pack, npm ls and plugin validate were NOT run !!!');
    }

    process.stdout.write('\n== 5. commit + tag ==\n');
    const files = plan.map((f) => f.file);
    const add = git(root, ['add', '--', ...files]);
    const commit = add.code === 0 ? git(root, ['commit', '-m', `Release ${tag}`, '--', ...files]) : add;
    if (commit.code !== 0) {
      restore();
      git(root, ['add', '--', ...files]); // the restored files equal HEAD again, so the index is clean
      process.stderr.write(`\nFAILED: git commit: ${(commit.stderr || commit.stdout).trim()}\nrolled back ${written.length} files\n`);
      return 1;
    }
    written = [];
    ok(`committed "Release ${tag}" (${files.length} files)`);

    const tmp = mkdtempSync(path.join(os.tmpdir(), 'code-forge-release-'));
    const notesFile = path.join(tmp, `release-notes-${tag}.md`);
    writeFileSync(notesFile, releaseNotes(changelogAfter, pkg, next) ?? `${notes}\n`);
    const tagMsgFile = path.join(tmp, 'tag-message.txt');
    writeFileSync(tagMsgFile, `Release ${tag}\n\n${notes}\n`);
    const tagged = git(root, ['tag', '-a', tag, '--cleanup=verbatim', '-F', tagMsgFile]);
    if (tagged.code !== 0) {
      process.stderr.write(`\nFAILED: git tag: ${tagged.stderr.trim()}\nthe commit stays; tag it by hand: git tag -a ${tag} --cleanup=verbatim -F ${tagMsgFile}\n`);
      return 1;
    }
    ok(`annotated tag ${tag}`);

    const token = npmTokenState(root);
    process.stdout.write(`\n== 6. next steps (nothing was pushed or published) ==\n`);
    process.stdout.write(`  git push origin ${branch} --follow-tags\n`);
    process.stdout.write(`then publish, one of:\n`);
    process.stdout.write(`  - automatic: the tag triggers .github/workflows/publish.yml if the NPM_TOKEN secret is set (NPM_TOKEN: ${token});\n`);
    process.stdout.write(`    after the npm publish it also creates the GitHub release ${tag} from CHANGELOG.md\n`);
    process.stdout.write(`  - manual: npm publish, then create the GitHub release (notes already written to ${notesFile}):\n`);
    process.stdout.write(`      gh release create ${tag} --title ${tag} --notes-file ${notesFile} --verify-tag\n`);
    return 0;
  } catch (e) {
    if (written.length) restore();
    if (e instanceof Refusal || e instanceof UsageError) {
      process.stderr.write(`\nREFUSED: ${e.message}\n`);
      return e instanceof UsageError ? 2 : 1;
    }
    throw e;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
