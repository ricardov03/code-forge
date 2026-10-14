import { execFileSync } from 'node:child_process';
import { realpathSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { TEST_HOME_PREFIX } from './isolate.mjs';

const BIN = realpathSync(fileURLToPath(new URL('../../bin/code-forge.mjs', import.meta.url)));

/** @param {string} suite */
export function requireSuiteMarker(suite) {
  if (suite === '') throw new Error('CODE_FORGE_TEST_SUITE must be non-empty before taking the snapshot or checking workers');
}

/** Resolve existing ancestors too: a leaked worker's HOME may already have been removed.
 * @param {string} value @returns {string} */
function realPath(value) {
  try { return realpathSync(value); } catch {
    const parent = path.dirname(value);
    return parent === value ? value : path.join(realPath(parent), path.basename(value));
  }
}

/**
 * Only actual workers from this checkout, with this suite's marker and a temp HOME under its
 * test base. Read environments internally; diagnostics contain only pid/run, never env values.
 * A denied process listing throws: a no-leak check must not silently pass without evidence.
 * @param {string} suite @param {string} base
 * @returns {Array<{pid: number, run: string}>}
 */
export function suiteWorkers(suite, base) {
  requireSuiteMarker(suite);
  let output;
  try {
    output = execFileSync('ps', ['eww', '-Ao', 'pid=,command='], { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024, stdio: ['ignore', 'pipe', 'pipe'] });
  } catch (err) {
    const status = Number.isInteger(err.status) ? err.status : 'unknown';
    const code = typeof err.code === 'string' && /^[A-Z0-9_]+$/.test(err.code) ? err.code : 'unknown';
    throw new Error(`worker process listing failed (exit=${status}, code=${code})`);
  }
  const realBase = realPath(base);
  const workers = [];
  for (const line of output.split('\n')) {
    const command = line.trim().split(/\s+/);
    const bin = command.findIndex((part) => path.isAbsolute(part) && path.basename(part) === 'code-forge.mjs' && realPath(part) === BIN);
    if (bin < 2 || command[bin + 1] !== 'worker') continue;
    const runFlag = command.indexOf('--run', bin + 2);
    if (!command.includes(`CODE_FORGE_TEST_SUITE=${suite}`)) continue;
    const home = command.find((part) => part.startsWith('HOME='))?.slice(5);
    if (!home || !realPath(home).startsWith(realBase + path.sep + TEST_HOME_PREFIX)) continue;
    const run = runFlag >= 0 && /^[a-z0-9][a-z0-9-]{0,63}$/.test(command[runFlag + 1] ?? '') ? command[runFlag + 1] : '?';
    workers.push({ pid: Number(command[0]), run });
  }
  return workers.sort((a, b) => a.pid - b.pid);
}
