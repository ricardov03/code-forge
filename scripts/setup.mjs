#!/usr/bin/env node
// B463: check the Go binary this package ships for the host OS/arch.
//   code-forge setup      (the Go binary serves it; bin/code-forge runs this file when the binary
//                          is missing, not executable or crashes, and src/cli/setup.mjs imports it)
// Picks the @codedology/code-forge-<platform>-<arch> package's binary (under or beside this
// package), else native/bin/<platform>-<arch>/code-forge-native (B606), restores lost exec bits, runs
// `<binary> internal version` and compares its version to package.json. Success prints one OK
// line (exit 0). A failure prints the problem and one `fix:` line (exit 1). Usage errors exit 2.
// Local checks only: no network. npm runs no install script: customers run `code-forge setup`.
import { spawnSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, readFileSync, realpathSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const USAGE = 'usage: code-forge setup\n';
const TARGETS = ['darwin-arm64', 'darwin-x64', 'linux-x64', 'linux-arm64'];
const HELP = new Set(['--help', '-h']);
const PKG = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

class Problem extends Error {
  /** @param {string} problem @param {string} fix */
  constructor(problem, fix) {
    super(problem);
    this.fix = fix;
  }
}

/**
 * B606: the same lookup as bin/code-forge and src/util/native.mjs, kept here so this file runs on
 * its own: the platform package under this package, then beside it, then native/bin/<target>; a
 * git checkout (native/go.mod) uses its own build only. Missing names the first candidate.
 * @param {string} pkg @param {string} target
 */
function findBinary(pkg, target) {
  const dev = path.join(pkg, 'native', 'bin', target, 'code-forge-native');
  const candidates = existsSync(path.join(pkg, 'native', 'go.mod')) ? [dev] : [
    path.join(pkg, 'node_modules', '@codedology', `code-forge-${target}`, 'bin', 'code-forge-native'),
    path.join(path.dirname(path.resolve(pkg)), `code-forge-${target}`, 'bin', 'code-forge-native'),
    dev,
  ];
  return candidates.find((c) => existsSync(c) && statSync(c).isFile()) ?? candidates[0];
}

/** @param {string} pkg @returns {string} the OK line */
function check(pkg) {
  const { name, version } = JSON.parse(readFileSync(path.join(pkg, 'package.json'), 'utf8'));
  const target = `${process.platform}-${process.arch}`;
  const binary = findBinary(pkg, target);
  const sourceCheckout = existsSync(path.join(pkg, 'native', 'go.mod'));
  const reinstall = sourceCheckout ? 'node scripts/build-native.mjs --host' : `npm install -g ${name}@${version}`;
  if (!TARGETS.includes(target)) {
    throw new Problem(`no Go binary is built for ${target} (built: ${TARGETS.join(', ')}); code-forge runs on Node here`, 'use macOS or Linux on x64 or arm64 for the Go binary');
  }
  if (!existsSync(binary) || !statSync(binary).isFile()) throw new Problem(`Go binary is missing: ${binary}`, reinstall);
  try {
    accessSync(binary, constants.X_OK);
  } catch {
    try {
      chmodSync(binary, 0o755);
      accessSync(binary, constants.X_OK);
    } catch {
      throw new Problem(`Go binary is not executable and its mode could not be fixed: ${binary}`, `chmod 755 '${binary}'`);
    }
  }
  const run = spawnSync(binary, ['internal', 'version'], { encoding: 'utf8', timeout: 10000 });
  let reported;
  try { reported = JSON.parse(run.stdout).version; } catch { reported = undefined; }
  if (run.error || run.status !== 0 || typeof reported !== 'string') {
    const why = run.error ? run.error.message : run.signal ? `signal ${run.signal}` : `exit ${run.status}`;
    const detail = (run.stderr || '').trim().split('\n')[0];
    throw new Problem(`Go binary failed to run (${why})${detail ? `: ${detail}` : ''}: ${binary}`, reinstall);
  }
  if (reported !== version) throw new Problem(`Go binary reports v${reported}, package is v${version}: ${binary}`, reinstall);
  return `code-forge setup: OK Go binary ${target} v${version} (${binary})\n`;
}

/**
 * Run the check and write its result. `--help`/`-h` in the first two arguments prints USAGE
 * (exit 0), like the router's help rule; any other argument is a usage error (exit 2).
 * @param {string[]} args
 * @param {{pkg?: string, stdout?: NodeJS.WritableStream, stderr?: NodeJS.WritableStream}} [io]
 * @returns {number} the exit code
 */
export function runSetup(args, { pkg = PKG, stdout = process.stdout, stderr = process.stderr } = {}) {
  if (HELP.has(args[0]) || HELP.has(args[1])) {
    stdout.write(USAGE);
    return 0;
  }
  if (args.length) {
    stderr.write(USAGE);
    return 2;
  }
  try {
    stdout.write(check(pkg));
    return 0;
  } catch (err) {
    if (!(err instanceof Problem)) throw err;
    stderr.write(`code-forge setup: ${err.message}\nfix: ${err.fix}\n`);
    return 1;
  }
}

if (process.argv[1] && pathToFileURL(realpathSync(process.argv[1])).href === import.meta.url) {
  process.exitCode = runSetup(process.argv.slice(2));
}
