import { spawn } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, readdir, readlink, realpath, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import { fileURLToPath } from 'node:url';
import { signRow, verifyRow } from '../../src/state/signer.mjs';
import { announceWorker } from '../../src/worker/queue.mjs';

const CLI = fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url));

/** @typedef {{repo: string, home: string}} Fixture */
/** @typedef {{where: string, field: string, node: any, go: any}} Diff */

/** Run argv without a shell; a signal and a spawn failure must never count as success.
 * @param {string | string[]} command @param {string[]} args @param {string} cwd
 * @param {NodeJS.ProcessEnv} env @param {string | Buffer} [stdin]
 * @returns {Promise<{code: number | null, signal: string | null, stdout: string, stderr: string}>}
 */
function execute(command, args, cwd, env, stdin = '') {
  return new Promise((resolve, reject) => {
    const argv = typeof command === 'string' ? [command] : command;
    const child = spawn(argv[0], [...argv.slice(1), ...args], { cwd, env, stdio: ['pipe', 'pipe', 'pipe'] });
    const stdout = [], stderr = [];
    child.stdout.on('data', (chunk) => stdout.push(chunk));
    child.stderr.on('data', (chunk) => stderr.push(chunk));
    child.on('error', reject);
    child.stdin.on('error', (err) => { if (/** @type {NodeJS.ErrnoException} */ (err).code !== 'EPIPE') reject(err); });
    child.on('close', (code, signal) => resolve({ code, signal, stdout: Buffer.concat(stdout).toString('utf8'), stderr: Buffer.concat(stderr).toString('utf8') }));
    child.stdin.end(stdin);
  });
}

// Do not inherit a caller's git routing, Node preloads, native overrides, or error-log path.
function childEnv(home, tmp, extra = {}) {
  const env = { PATH: process.env.PATH, ...extra, HOME: home, TMPDIR: tmp, CODE_FORGE_KEY_BACKEND: 'file', CODE_FORGE_NO_ERROR_LOG: '1' };
  return env;
}

/** A private git fixture, a real CLI-started run, and an open B1 owning sample.mjs.
 * The test process stands in for an idle worker: review-file queues, but no model runs.
 * Call cleanup() after use. Custom fixtures need only supply {repo, home}.
 * @param {{env?: Record<string, string>}} [options]
 */
export async function makeParityFixture({ env = {} } = {}) {
  const root = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-parity-fixture-')));
  const repo = path.join(root, 'repo'), home = path.join(root, 'home');
  const cleanup = () => rm(root, { recursive: true, force: true });
  try {
    await mkdir(repo); await mkdir(home); await mkdir(path.join(root, 'tmp'));
    const vars = childEnv(home, path.join(root, 'tmp'), env);
    const checked = async (command, args) => {
      const result = await execute(command, args, repo, vars);
      if (result.code !== 0) throw new Error(`fixture command failed (${result.code}): ${result.stderr}`);
      return result;
    };
    await checked('git', ['init', '-q', '-b', 'main']);
    await writeFile(path.join(repo, '.code-forge.yml'), 'version: 1\nproject: {slug: parity-fixture}\nprovider: anthropic\nengine: harness\nlevels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-sonnet-5}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\nproof: {isolation: export}\n');
    await writeFile(path.join(repo, '.gitignore'), '.code-forge/\n');
    await writeFile(path.join(repo, 'sample.mjs'), 'export const value = 1;\n');
    await writeFile(path.join(repo, 'acceptance.json'), JSON.stringify([{ clause: 'sample works', tests: ['sample'] }]));
    await checked('git', ['add', '.']);
    await checked('git', ['-c', 'user.name=Parity Fixture', '-c', 'user.email=parity@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture']);
    const started = await checked([process.execPath, CLI], ['run', 'start', '--worker-pid', String(process.pid)]);
    const runId = /^run (\S+) started/m.exec(started.stdout)?.[1];
    if (!runId) throw new Error(`fixture run id missing: ${started.stdout}`);
    await checked([process.execPath, CLI], ['block', 'open', 'B1', '--run', runId, '--level', 'L1', '--owned', 'sample.mjs', '--acceptance', 'acceptance.json']);
    announceWorker(repo, { pid: process.pid, run: runId });
    return { repo, home, runId, block: 'B1', cleanup };
  } catch (err) { await cleanup(); throw err; }
}

/** Every state file, including binary files, dotfiles and symlink targets. */
async function stateFiles(fixture) {
  const files = new Map();
  async function walk(dir, prefix) {
    let entries;
    try { entries = await readdir(dir, { withFileTypes: true }); }
    catch (err) { if (err.code === 'ENOENT') return; throw err; }
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      const full = path.join(dir, entry.name), name = `${prefix}/${entry.name}`;
      if (entry.isDirectory()) await walk(full, name);
      else if (entry.isSymbolicLink()) files.set(name, { kind: 'symlink', value: await readlink(full) });
      else files.set(name, { kind: 'file', value: await readFile(full) });
    }
  }
  for (const key of ['home', 'repo']) await walk(path.join(fixture[key], '.code-forge'), `${key}/.code-forge`);
  return files;
}

/** JSON markers also use suffixes such as .done. Keys and non-JSON stay byte comparisons. */
function decode(name, bytes) {
  if (name.endsWith('.key')) return bytes;
  const text = bytes.toString('utf8');
  if (!Buffer.from(text).equals(bytes)) return bytes;
  try {
    if (name.endsWith('.jsonl')) return text.trimEnd().split('\n').filter((line) => line.trim()).map((line) => JSON.parse(line));
    return JSON.parse(text);
  } catch { /* malformed state is still compared, as raw bytes */ }
  return bytes;
}

function replacePaths(text, from, to) {
  for (const key of ['repo', 'home']) text = text.replaceAll(from[key], to[key]);
  return text;
}

async function rowKey(row, home) {
  if (typeof row.run !== 'string' || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(row.run)) return null;
  try { return await readFile(path.join(home, '.code-forge', 'runs', `${row.run}.key`)); }
  catch (err) { if (err.code === 'ENOENT') return null; throw err; }
}

/** Relocate state references, re-signing only originally verified rows whose paths moved. */
async function copyFixture(fixture, root) {
  const copy = { repo: path.join(root, 'repo'), home: path.join(root, 'home') };
  await mkdir(root);
  await cp(fixture.repo, copy.repo, { recursive: true, verbatimSymlinks: true });
  await cp(fixture.home, copy.home, { recursive: true, verbatimSymlinks: true });
  async function relocate(value) {
    if (typeof value === 'string') return replacePaths(value, fixture, copy);
    if (value === null || typeof value !== 'object') return value;
    if (Array.isArray(value)) return Promise.all(value.map(relocate));
    const moved = Object.fromEntries(await Promise.all(Object.entries(value).map(async ([k, v]) => [k, await relocate(v)])));
    if (Object.hasOwn(value, 'mac') && !isDeepStrictEqual(value, moved)) {
      const key = await rowKey(value, copy.home);
      if (key && verifyRow(value, key).ok) return signRow(moved, key);
    }
    return moved;
  }
  for (const [name, entry] of await stateFiles(copy)) {
    if (entry.kind !== 'file') continue;
    const value = decode(name, entry.value);
    if (Buffer.isBuffer(value)) continue;
    const moved = await relocate(value);
    if (isDeepStrictEqual(value, moved)) continue;
    const body = name.endsWith('.jsonl') ? moved.map((row) => JSON.stringify(row)).join('\n') + '\n' : JSON.stringify(moved) + '\n';
    const [area, ...parts] = name.split('/');
    await writeFile(path.join(copy[area], ...parts), body);
  }
  return copy;
}

function compare(node, go, where, field, diffs) {
  if (isDeepStrictEqual(node, go)) return;
  if (Buffer.isBuffer(node) || Buffer.isBuffer(go)) {
    diffs.push({ where, field, node: Buffer.isBuffer(node) ? node.toString('hex') : node, go: Buffer.isBuffer(go) ? go.toString('hex') : go });
  } else if (node !== null && go !== null && typeof node === 'object' && typeof go === 'object' && Array.isArray(node) === Array.isArray(go)) {
    for (const key of [...new Set([...Object.keys(node), ...Object.keys(go)])].sort()) {
      const next = Array.isArray(node) ? `${field}[${key}]` : `${field}.${key}`;
      if (!Object.hasOwn(node, key) || !Object.hasOwn(go, key)) diffs.push({ where, field: next, node: node[key], go: go[key] });
      else compare(node[key], go[key], where, next, diffs);
    }
  } else diffs.push({ where, field, node, go });
}

const CLOCK_RUN = /\br-\d{8}-\d{6}-[a-f0-9]{6}\b/g;
const ISO_TIME = /\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b/g;
const isPid = (field) => /^(?:pid|pids)$|_pids?$|Pids?$/.test(field);
const isDuration = (field) => /^(?:duration|elapsed|ms)$|_ms$|Ms$/.test(field);

function normalizeText(text, copy) {
  text = replacePaths(text, copy, { repo: '<repo>', home: '<home>' });
  // TMPDIR is also private to the copy; preserve its suffix and all surrounding text.
  text = text.replaceAll(path.dirname(copy.repo) + path.sep, '<copy>/');
  text = text.replace(ISO_TIME, (stamp) => Number.isFinite(Date.parse(stamp)) ? '<timestamp>' : stamp);
  text = text.replace(CLOCK_RUN, '<clock-run>');
  // Structured fields in CLI JSON retain whitespace, ordering and all other output bytes.
  text = text.replace(/("([^"\n]+)"\s*:\s*)(\d+(?:\.\d+)?)(?=\s*[,}\]])/g, (all, prefix, field) =>
    isPid(field) ? `${prefix}<pid>` : isDuration(field) ? `${prefix}<duration>` : all);
  return text.replace(/\bpid(\s+|\s*[=:]\s*)\d+\b/gi, 'pid$1<pid>');
}

function normalize(value, copy, field = '') {
  if (Buffer.isBuffer(value)) {
    const text = value.toString('utf8');
    // Invalid UTF-8 must not collapse distinct binary files to replacement characters.
    return Buffer.from(text).equals(value) ? Buffer.from(normalizeText(text, copy)) : value;
  }
  if (typeof value === 'string') return normalizeText(value, copy);
  if (typeof value === 'number' && Number.isFinite(value)) {
    if (isPid(field) && Number.isInteger(value) && value > 0) return '<pid>';
    if (isDuration(field) && value >= 0) return '<duration>';
  }
  if (Array.isArray(value)) return value.map((entry) => normalize(entry, copy, field));
  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, normalize(entry, copy, key)]));
  return value;
}

/** MACs may differ only when BOTH original rows verify and every other normalized field agrees. */
async function normalizeSignatures(a, b, left, right, node, go) {
  if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object' || Buffer.isBuffer(a) || Buffer.isBuffer(b)) return;
  if (typeof a.mac === 'string' && typeof b.mac === 'string') {
    const { mac: _a, ...bodyA } = left, { mac: _b, ...bodyB } = right;
    if (isDeepStrictEqual(bodyA, bodyB)) {
      const keyA = await rowKey(a, node.home), keyB = await rowKey(b, go.home);
      if (keyA?.length === 32 && keyB?.length === 32 && verifyRow(a, keyA).ok && verifyRow(b, keyB).ok) {
        left.mac = '<verified-signature>'; right.mac = '<verified-signature>';
      }
    }
  }
  for (const key of Object.keys(a)) {
    if (Object.hasOwn(b, key)) await normalizeSignatures(a[key], b[key], left[key], right[key], node, go);
  }
}

/**
 * Run a verb in independent repo + HOME copies. goCommand is an executable string or an
 * argv prefix (e.g. [process.execPath, CLI]); verbArgs are appended without shell parsing.
 * env and stdin are supplied to both commands; HOME/TMPDIR stay private. Copies are removed
 * even on errors. Diffs identify process streams or home/repo-relative state files and JSON
 * paths ($.field, $[row].field); raw files use $. Missing files use $file.
 * @param {string[]} verbArgs
 * @param {{fixture: Fixture, goCommand: string | string[], env?: Record<string, string>, stdin?: string | Buffer}} options
 * @returns {Promise<{equal: boolean, diffs: Diff[]}>}
 */
export async function runParity(verbArgs, { fixture, goCommand, env = {}, stdin = '' }) {
  const root = await realpath(await mkdtemp(path.join(os.tmpdir(), 'cf-parity-')));
  try {
    const node = await copyFixture(fixture, path.join(root, 'node'));
    const go = await copyFixture(fixture, path.join(root, 'go'));
    const diffs = [];
    const runCopy = async (copy, command) => {
      await mkdir(path.join(copy.home, 'tmp'), { recursive: true });
      return execute(command, verbArgs.map((arg) => replacePaths(arg, fixture, copy)), copy.repo, childEnv(copy.home, path.join(copy.home, 'tmp'), env), stdin);
    };
    const nodeResult = await runCopy(node, [process.execPath, CLI]);
    const goResult = await runCopy(go, goCommand);
    for (const field of ['code', 'signal', 'stdout', 'stderr']) compare(normalize(nodeResult[field], node), normalize(goResult[field], go), 'process', field, diffs);
    const left = await stateFiles(node), right = await stateFiles(go);
    const index = (files, copy) => {
      const groups = new Map();
      for (const [name, entry] of files) {
        const normalized = normalizeText(name, copy);
        if (!groups.has(normalized)) groups.set(normalized, []);
        groups.get(normalized).push({ ...entry, name });
      }
      return groups;
    };
    const leftIndex = index(left, node), rightIndex = index(right, go);
    for (const name of [...new Set([...leftIndex.keys(), ...rightIndex.keys()])].sort()) {
      const leftGroup = leftIndex.get(name) ?? [], rightGroup = rightIndex.get(name) ?? [];
      const pairs = [];
      // Match unchanged identities first. Keep ALL remaining files even when several volatile
      // names normalize to the same name; a Map overwrite would silently lose extra files.
      for (let i = leftGroup.length - 1; i >= 0; i--) {
        const j = rightGroup.findIndex((entry) => entry.name === leftGroup[i].name);
        if (j >= 0) pairs.push([leftGroup.splice(i, 1)[0], rightGroup.splice(j, 1)[0]]);
      }
      while (leftGroup.length || rightGroup.length) pairs.push([leftGroup.shift(), rightGroup.shift()]);
      for (const [a, b] of pairs) {
        if (!a || !b) { diffs.push({ where: name, field: '$file', node: a ? 'present' : undefined, go: b ? 'present' : undefined }); continue; }
        if (a.kind !== b.kind) { diffs.push({ where: name, field: '$type', node: a.kind, go: b.kind }); continue; }
        const rawA = a.kind === 'file' ? decode(a.name, a.value) : a.value;
        const rawB = b.kind === 'file' ? decode(b.name, b.value) : b.value;
        const normA = normalize(rawA, node), normB = normalize(rawB, go);
        await normalizeSignatures(rawA, rawB, normA, normB, node, go);
        compare(normA, normB, name, '$', diffs);
      }
    }
    return { equal: diffs.length === 0, diffs };
  } finally { await rm(root, { recursive: true, force: true }); }
}
