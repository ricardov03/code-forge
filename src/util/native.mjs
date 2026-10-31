import { accessSync, constants, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
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
