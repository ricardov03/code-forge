/**
 * `code-forge queue` (B352): the machine-wide FIFO test queue lives in the Go binary
 * (native/testslot). This verb hands the whole command line to it, so the Node router lists the
 * verb and `node bin/code-forge.mjs queue ...` works the same as the Go entry.
 */

import { spawn } from 'node:child_process';
import { nativePath } from '../util/native.mjs';

/** Set on the Go child: an old binary without the route hands back to Node, which stops here. */
const HANDOVER_GUARD = 'CODE_FORGE_QUEUE_HANDOVER';

/**
 * @param {string[]} args
 * @returns {Promise<number>}
 */
export default async function queue(args) {
  const binary = nativePath();
  if (!binary || process.env[HANDOVER_GUARD] === '1') {
    process.stderr.write('queue: the test queue needs the code-forge native binary for this host (node scripts/build-native.mjs --host)\n');
    return 1;
  }
  const child = spawn(binary, ['queue', ...args], { stdio: 'inherit', env: { ...process.env, [HANDOVER_GUARD]: '1' } });
  // The Go side forwards SIGINT/SIGTERM to its command; Node only waits for it.
  const ignore = () => {};
  process.on('SIGINT', ignore);
  process.on('SIGTERM', ignore);
  try {
    return await new Promise((resolve) => {
      child.on('error', (err) => {
        process.stderr.write(`queue: ${err.message}\n`);
        resolve(1);
      });
      child.on('exit', (code, signal) => resolve(code ?? (signal ? 128 + (signalNumber(signal) ?? 1) : 1)));
    });
  } finally {
    process.off('SIGINT', ignore);
    process.off('SIGTERM', ignore);
  }
}

/** @param {string} signal @returns {number | undefined} */
function signalNumber(signal) {
  return { SIGHUP: 1, SIGINT: 2, SIGQUIT: 3, SIGKILL: 9, SIGTERM: 15 }[signal];
}

export const USAGE = 'usage: code-forge queue [status] [--json]\n' +
  '       code-forge queue run [--kind full|changed] [--label X] -- <cmd> [args]\n' +
  '  The machine-wide FIFO test queue ($HOME/.code-forge/queue). Gates take a slot by themselves;\n' +
  '  wrap any other test command in `queue run`. CODE_FORGE_TEST_QUEUE=off disables it.\n';

export const SUMMARY = 'Show the machine-wide test queue or run a test command inside a queue slot.';
