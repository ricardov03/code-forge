#!/usr/bin/env node
/** Maintainer release automation stays in Node, alongside release.mjs and gen-site-data.mjs. */
import { spawnSync } from 'node:child_process';
import { existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, realpathSync, rmdirSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { stripVTControlCharacters } from 'node:util';
import { parse } from 'yaml';

const USAGE = 'usage: npm run release:site -- [--site <dir>] [--wait-min <minutes>] [--deploy-dir <path>] [--dry-run]';
const DATA = 'src/data/site-data.json';
/** The site's build:site merges the marketing pages and /docs into this folder; `dist` alone has no /docs. */
const DEPLOY_DIR = 'docs-site/site/.pages-out';
/** @param {string} dir */
const deployArgv = (dir) => ['wrangler', 'pages', 'deploy', dir, '--project-name', 'code-forge-ai', '--branch', 'main'];

/** A deploy folder must be relative to the site root and stay inside it. @param {string} value */
function deployDir(value) {
  if (path.isAbsolute(value) || path.win32.isAbsolute(value)) throw new Error(`--deploy-dir must be relative to the site root: ${value}`);
  const parts = value.split(/[\\/]+/);
  if (parts.includes('..')) throw new Error(`--deploy-dir must stay inside the site root: ${value}`);
  const dir = path.posix.normalize(parts.filter(Boolean).join('/'));
  if (dir === '.' || dir === '') throw new Error('--deploy-dir must name a folder below the site root');
  return dir;
}

/** After the gates: the deploy folder must exist inside the tree and hold index.html. @param {string} tree @param {string} dir */
function checkDeployDir(tree, dir) {
  const full = path.join(tree, dir);
  const index = path.join(full, 'index.html');
  const missing = `Deploy folder ${dir} is missing or has no index.html after the build; no commit or deploy; the old site remains live`;
  if (!existsSync(full) || !statSync(full).isDirectory()) throw new Error(missing);
  const realTree = realpathSync(tree);
  const real = realpathSync(full);
  if (real !== realTree && !real.startsWith(realTree + path.sep)) throw new Error(`Deploy folder ${dir} resolves outside the site tree; no commit or deploy`);
  if (!existsSync(index) || !statSync(index).isFile()) throw new Error(missing);
}

/** @param {string[]} args */
function options(args) {
  const opts = { site: '../code-forge-site', waitMin: 20, dryRun: false, deployDir: DEPLOY_DIR };
  for (let i = 0; i < args.length; i++) {
    const flag = args[i];
    if (flag === '--dry-run') opts.dryRun = true;
    else if (flag === '--site' || flag === '--wait-min' || flag === '--deploy-dir') {
      const value = args[++i];
      if (!value || value.startsWith('--')) throw new Error(`${flag} needs a value\n${USAGE}`);
      if (flag === '--site') opts.site = value;
      else if (flag === '--deploy-dir') opts.deployDir = deployDir(value);
      else {
        opts.waitMin = Number(value);
        if (!Number.isFinite(opts.waitMin) || opts.waitMin <= 0) throw new Error('--wait-min must be a positive number');
      }
    } else throw new Error(`unknown option ${flag}\n${USAGE}`);
  }
  return opts;
}

/** Strip repository selectors inherited from git hooks from every child. */
function childEnv() {
  const env = { ...process.env, GIT_TERMINAL_PROMPT: '0' };
  for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_PREFIX', 'GIT_OBJECT_DIRECTORY',
    'GIT_COMMON_DIR', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_IMPLICIT_WORK_TREE']) delete env[key];
  return env;
}

/** @param {string} cwd @param {string[]} argv @param {number} [timeout] @param {NodeJS.ProcessEnv} [env] */
function run(cwd, argv, timeout = 60000, env = childEnv()) {
  return spawnSync(argv[0], argv.slice(1), { cwd, env, encoding: 'utf8', timeout,
    maxBuffer: 32 * 1024 * 1024 });
}

/** @param {string} cwd @param {string[]} argv @param {number} [timeout] */
function checked(cwd, argv, timeout) {
  const r = run(cwd, argv, timeout);
  if (r.error || r.status !== 0) throw new Error(`${argv.join(' ')} failed: ${r.error?.message || r.stderr || r.stdout}`);
  return r.stdout;
}

/** @param {string} root @param {string} name @param {string} version @param {number} waitMin */
async function waitForNpm(root, name, version, waitMin) {
  const deadline = Date.now() + waitMin * 60000;
  for (;;) {
    const remaining = deadline - Date.now();
    if (remaining <= 0) break;
    const r = run(root, ['npm', 'view', `${name}@${version}`, 'version'], Math.max(1, Math.min(30000, remaining)));
    if (!r.error && r.status === 0 && r.stdout.trim() === version) {
      process.stdout.write(`npm serves ${name}@${version}\n`);
      return;
    }
    const left = deadline - Date.now();
    if (left <= 0) break;
    process.stdout.write(`Waiting for npm to serve ${name}@${version} (poll every 30 s)\n`);
    await new Promise((resolve) => setTimeout(resolve, Math.min(30000, left)));
  }
  throw new Error(`Timed out after ${waitMin} min waiting for npm to serve ${name}@${version}; nothing changed`);
}

/** @param {unknown} value @param {string} name @returns {string[]} */
function argvOf(value, name) {
  if (!Array.isArray(value) || value.length === 0 || value.some((v) => typeof v !== 'string' || !v)) {
    throw new Error(`Invalid site gate ${name}: expected a nonempty argv array`);
  }
  return value;
}

/** Read the committed configuration from the detached tree, including every custom gate. @param {string} tree */
function siteGates(tree) {
  const config = parse(readFileSync(path.join(tree, '.code-forge.yml'), 'utf8'));
  const gates = config?.gates;
  if (!gates || typeof gates !== 'object') throw new Error('Site .code-forge.yml must define gates');
  if (gates.custom != null && !Array.isArray(gates.custom)) throw new Error('Invalid site gates.custom: expected an array');
  const custom = (gates.custom ?? []).map((gate) => {
    if (!gate || typeof gate.name !== 'string' || !gate.name) throw new Error('Invalid site custom gate: missing name');
    if (gate.env != null && (typeof gate.env !== 'object' || Array.isArray(gate.env)
      || Object.values(gate.env).some((v) => typeof v !== 'string'))) throw new Error(`Invalid site gate ${gate.name}: env must contain strings`);
    return { name: gate.name, argv: argvOf(gate.argv, gate.name), env: gate.env ?? {} };
  });
  if (gates.test != null) {
    // An omitted file selection means the full suite, as in the normal gate runner.
    const argv = argvOf(gates.test, 'test').filter((arg) => arg !== '{test_files}' && arg !== '{changed_files}');
    custom.push({ name: 'test', argv: argvOf(argv, 'test'), env: {} });
  }
  const timeout = gates.timeout_s == null ? 300000 : Number(gates.timeout_s) * 1000;
  if (!Number.isFinite(timeout) || timeout <= 0) throw new Error('Invalid site gates.timeout_s');
  return { custom, timeout };
}

/** @param {string} value */
const quote = (value) => `'${value.replaceAll("'", "'\\''")}'`;

/** Link installed dependencies, keeping root and scoped cache directories local to the tree.
 * @param {string} source @param {string} target
 */
function linkModules(source, target) {
  mkdirSync(target);
  for (const entry of readdirSync(source, { withFileTypes: true })) {
    if (entry.name.startsWith('.') && entry.name !== '.bin') continue;
    const from = path.join(source, entry.name);
    const to = path.join(target, entry.name);
    if (entry.name.startsWith('@') && entry.isDirectory()) linkModules(from, to);
    else symlinkSync(from, to);
  }
}

async function main() {
  try {
    const opts = options(process.argv.slice(2));
    const root = process.cwd();
    const { name, version } = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
    if (typeof name !== 'string' || !name || !/^\d+\.\d+\.\d+$/.test(version)) throw new Error('package.json needs a name and an x.y.z version');
    const tag = `v${version}`;
    checked(root, ['git', 'rev-parse', '--verify', `refs/tags/${tag}^{commit}`]);
    await waitForNpm(root, name, version, opts.waitMin);

    const data = checked(root, [process.execPath, 'scripts/gen-site-data.mjs', '--tag', tag]);
    JSON.parse(data);
    const site = realpathSync(path.resolve(root, opts.site));
    if (realpathSync(checked(site, ['git', 'rev-parse', '--show-toplevel']).trim()) !== site) throw new Error('--site must name the site repository root');
    const head = checked(site, ['git', 'rev-parse', 'HEAD^{commit}']).trim();
    const branch = `release-site/${tag}`;
    const ref = run(site, ['git', 'show-ref', '--verify', '--quiet', `refs/heads/${branch}`]);
    if (ref.status !== 1) throw new Error(`Cannot create new branch ${branch}: it already exists or git could not check it`);
    const parent = path.join(site, '.code-forge-tmp');
    const tree = path.join(parent, `release-site-${version}`);
    const hadParent = existsSync(parent);
    if (hadParent && (!lstatSync(parent).isDirectory() || lstatSync(parent).isSymbolicLink())) throw new Error(`${parent} must be a real directory`);
    if (existsSync(tree)) throw new Error(`Temporary worktree already exists: ${tree}`);
    mkdirSync(parent, { recursive: true });
    let added = false;
    try {
      checked(site, ['git', '-c', 'core.hooksPath=/dev/null', 'worktree', 'add', '--detach', tree, head]);
      added = true;
      mkdirSync(path.dirname(path.join(tree, DATA)), { recursive: true });
      writeFileSync(path.join(tree, DATA), data);
      const { custom, timeout } = siteGates(tree);
      // Every tracked package (the root and nested ones such as docs-site/site) needs its own dependencies.
      const packages = checked(tree, ['git', 'ls-files', '-z', '--', 'package.json', '*/package.json'])
        .split('\0').filter(Boolean).map((file) => path.dirname(file))
        .filter((dir) => !dir.split('/').includes('node_modules'));
      for (const dir of packages) {
        const target = path.join(tree, dir, 'node_modules');
        const source = path.join(site, dir, 'node_modules');
        if (existsSync(target)) continue;
        if (existsSync(source)) linkModules(source, target);
        else checked(path.join(tree, dir), ['npm', 'ci'], 600000);
      }
      const failures = [];
      for (const gate of [{ name: 'build:site', argv: ['npm', 'run', 'build:site'], env: {} }, ...custom]) {
        process.stdout.write(`Running gate: ${gate.name}\n`);
        const r = run(tree, gate.argv, timeout, { ...childEnv(), CI: '1', npm_config_yes: 'false', ...gate.env });
        process.stdout.write(r.stdout ?? '');
        process.stderr.write(r.stderr ?? '');
        if (r.error || r.status !== 0) {
          failures.push(gate.name);
          process.stderr.write(`Gate failed: ${gate.name}${r.error ? ` (${r.error.message})` : ` (exit ${r.status})`}\n`);
        }
      }
      if (failures.length) throw new Error(`Site gates failed: ${failures.join(', ')}; no commit or deploy; the old site remains live`);
      checkDeployDir(tree, opts.deployDir);
      const deploy = deployArgv(opts.deployDir);
      if (opts.dryRun) {
        process.stdout.write(`Would commit ${DATA} on ${branch}\nWould deploy: npx ${deploy.join(' ')}\n`);
      } else {
        checked(tree, ['git', 'switch', '-c', branch]);
        checked(tree, ['git', 'add', '--', DATA]);
        checked(tree, ['git', '-c', 'core.hooksPath=/dev/null', 'commit', '--only', '-m', `Release site ${tag}`, '--', DATA]);
        const output = checked(tree, ['npx', ...deploy], 600000);
        process.stdout.write(output);
        const url = stripVTControlCharacters(output).match(/https:\/\/[^\s<>"']+/)?.[0];
        if (url) process.stdout.write(`Deploy URL: ${url}\n`);
      }
      process.stdout.write(`Owner next steps (nothing was pushed):\ngit -C ${quote(site)} merge ${branch}\ngit -C ${quote(site)} push origin main\n`);
    } finally {
      if (added) checked(site, ['git', 'worktree', 'remove', '--force', tree]);
      if (!hadParent) rmdirSync(parent);
    }
    return 0;
  } catch (error) {
    process.stderr.write(`release:site: ${error.message}\n`);
    return 1;
  }
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) process.exitCode = await main();
