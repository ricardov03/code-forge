import { spawn } from 'node:child_process';
import { accessSync, constants, statSync } from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import pkg from '../../package.json' with { type: 'json' };
import { exec } from './exec.mjs';

/** @returns {string | null} the optional executable shipped for this host. */
export function nativePath() {
  if (process.env.CODE_FORGE_NATIVE === '0') return null;
  const binary = fileURLToPath(new URL(`../../native/bin/${process.platform}-${process.arch}/code-forge-native`, import.meta.url));
  try {
    if (!statSync(binary).isFile()) return null;
    accessSync(binary, constants.X_OK);
    return binary;
  } catch { return null; }
}

/**
 * @param {string[]} args
 * @returns {Promise<import('./exec.mjs').ExecResult | null>} null requests the Node fallback.
 */
export async function runNative(args) {
  const binary = nativePath();
  return binary ? exec([binary, ...args], { timeoutMs: 30_000 }) : null;
}

/** @param {Record<string, any> | null} [config] @param {NodeJS.ProcessEnv} [env] */
export function daemonEnabled(config, env = process.env) {
  return env.CODE_FORGE_NATIVE !== '0' && config?.native?.daemon !== false;
}

/**
 * One connection per call. Ping negotiates the package version before sending the method;
 * no socket is opened when disabled. Protocol and transport failures request Node fallback.
 * @param {string} run
 * @param {{method: string, block?: string, max_ms?: number}} request
 * @param {{config?: Record<string, any> | null, timeoutMs?: number}} [opts]
 * @returns {Promise<Record<string, any> | null>}
 */
export async function daemonRequest(run, request, { config, timeoutMs = request.method === 'wait-block' ? (request.max_ms ?? 0) + 2500 : 6500 } = {}) {
  if (!daemonEnabled(config) || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(run)) return null;
  return new Promise((resolve) => {
    let settled = false;
    let negotiated = false;
    let buffer = '';
    const socket = net.createConnection(path.join(os.homedir(), '.code-forge', 'runs', run, 'native.sock'));
    socket.setEncoding('utf8');
    const finish = (/** @type {Record<string, any> | null} */ value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.destroy();
      resolve(value);
    };
    let timer = setTimeout(() => finish(null), Math.min(2000, timeoutMs));
    const deadline = (/** @type {number} */ ms) => {
      clearTimeout(timer);
      timer = setTimeout(() => finish(null), ms);
    };
    socket.on('error', () => finish(null));
    socket.on('close', () => finish(null));
    socket.on('connect', () => {
      deadline(Math.min(2000, timeoutMs));
      socket.write('{"method":"ping"}\n');
    });
    socket.on('data', (chunk) => {
      buffer += chunk;
      if (buffer.length > 8 * 1024 * 1024) return finish(null);
      const end = buffer.indexOf('\n');
      if (end < 0) return;
      let answer;
      try { answer = JSON.parse(buffer.slice(0, end)); } catch { return finish(null); }
      buffer = buffer.slice(end + 1);
      if (!answer || typeof answer !== 'object' || Array.isArray(answer) || answer.ok === false) return finish(null);
      if (!negotiated) {
        if (answer.ok !== true || answer.version !== pkg.version || answer.run !== run) return finish(null);
        if (request.method === 'ping') return finish(answer);
        negotiated = true;
        deadline(timeoutMs);
        socket.write(`${JSON.stringify(request)}\n`);
      } else {
        const { stats, ...result } = answer;
        if (request.method === 'wait-block' && (result.block !== request.block || !Array.isArray(result.files) || typeof result.done !== 'boolean'
          || !result.files.every((/** @type {any} */ file) => file && typeof file.file === 'string' && typeof file.status === 'string'))) return finish(null);
        if (request.method === 'status' && (!Array.isArray(result.runs) || result.runs.length !== 1 || result.runs[0]?.run_id !== run || !Array.isArray(result.runs[0]?.blocks))) return finish(null);
        finish(result);
      }
    });
  }).catch(() => null);
}

/**
 * The daemon serializes startup with its own OS lock. A loser exits harmlessly;
 * only this child is stopped, never a daemon another worker already owns.
 * @param {string} run @param {Record<string, any> | null} config
 * @param {NodeJS.ProcessEnv} [env]
 * @returns {Promise<() => Promise<void>>}
 */
export async function startNativeDaemon(run, config, env = process.env) {
  const noop = async () => {};
  if (!daemonEnabled(config, env)) return noop;
  const binary = nativePath();
  if (!binary || await daemonRequest(run, { method: 'ping' }, { config })) return noop;
  let child;
  try { child = spawn(binary, ['serve', '--home', os.homedir(), '--run', run], { stdio: 'ignore', env }); }
  catch { return noop; }
  let ended = false;
  const exited = new Promise((resolve) => {
    child.once('error', () => { ended = true; resolve(undefined); });
    child.once('exit', () => { ended = true; resolve(undefined); });
  });
  for (let i = 0; i < 100 && !ended; i++) {
    if (await daemonRequest(run, { method: 'ping' }, { config, timeoutMs: 100 })) break;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  /** @type {Promise<void> | undefined} */
  let stopping;
  return () => {
    stopping ??= (async () => {
      if (ended) return;
      child.kill('SIGTERM');
      const timer = setTimeout(() => child.kill('SIGKILL'), 2000);
      timer.unref();
      try { await exited; } finally { clearTimeout(timer); }
    })();
    return stopping;
  };
}
