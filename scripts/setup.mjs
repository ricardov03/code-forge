#!/usr/bin/env node
// B463: check the Go binary this package ships for the host OS/arch.
//   code-forge setup [--postinstall]      (the bin/code-forge launcher runs this file)
// Picks native/bin/<platform>-<arch>/code-forge-native, restores lost exec bits, runs
// `<binary> internal version` and compares its version to package.json. Success prints one OK
// line (exit 0). A failure prints the problem and one `fix:` line (exit 1). --postinstall (npm
// postinstall) prints the same text as a warning and always exits 0, so an install never fails.
// Local checks only: no network.
import { spawnSync } from 'node:child_process';
import { accessSync, chmodSync, constants, existsSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const TARGETS = ['darwin-arm64', 'darwin-x64', 'linux-x64', 'linux-arm64'];
const args = process.argv.slice(2);
const postinstall = args.length === 1 && args[0] === '--postinstall';
if (args.length && !postinstall) {
  process.stderr.write('usage: code-forge setup [--postinstall]\n');
  process.exit(2);
}

const pkg = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const { name, version } = JSON.parse(readFileSync(path.join(pkg, 'package.json'), 'utf8'));
const target = `${process.platform}-${process.arch}`;
const binary = path.join(pkg, 'native', 'bin', target, 'code-forge-native');
const sourceCheckout = existsSync(path.join(pkg, 'native', 'go.mod'));
const reinstall = sourceCheckout ? 'node scripts/build-native.mjs --host' : `npm install -g ${name}@${version}`;

/** @param {string} problem @param {string} fix */
function fail(problem, fix) {
  process.stderr.write(`code-forge setup: ${postinstall ? 'warning: ' : ''}${problem}\nfix: ${fix}\n`);
  process.exit(postinstall ? 0 : 1);
}

if (!TARGETS.includes(target)) {
  fail(`no Go binary is built for ${target} (built: ${TARGETS.join(', ')}); code-forge runs on Node here`, 'use macOS or Linux on x64 or arm64 for the Go binary');
}
if (!existsSync(binary) || !statSync(binary).isFile()) fail(`Go binary is missing: ${binary}`, reinstall);
try {
  accessSync(binary, constants.X_OK);
} catch {
  try {
    chmodSync(binary, 0o755);
    accessSync(binary, constants.X_OK);
  } catch {
    fail(`Go binary is not executable and its mode could not be fixed: ${binary}`, `chmod 755 '${binary}'`);
  }
}

const run = spawnSync(binary, ['internal', 'version'], { encoding: 'utf8', timeout: 10000 });
let reported;
try { reported = JSON.parse(run.stdout).version; } catch { reported = undefined; }
if (run.error || run.status !== 0 || typeof reported !== 'string') {
  const why = run.error ? run.error.message : run.signal ? `signal ${run.signal}` : `exit ${run.status}`;
  const detail = (run.stderr || '').trim().split('\n')[0];
  fail(`Go binary failed to run (${why})${detail ? `: ${detail}` : ''}: ${binary}`, reinstall);
}
if (reported !== version) fail(`Go binary reports v${reported}, package is v${version}: ${binary}`, reinstall);
process.stdout.write(`code-forge setup: OK Go binary ${target} v${version} (${binary})\n`);
