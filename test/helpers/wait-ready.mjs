import { setTimeout as delay } from 'node:timers/promises';

/**
 * @template T
 * @param {() => T | Promise<T>} probe
 * @param {{ timeoutMs?: number, intervalMs?: number, stop?: () => boolean }} [options]
 */
export async function waitReady(probe, { timeoutMs = 15000, intervalMs = 20, stop } = {}) {
  const started = Date.now();
  const deadline = started + timeoutMs;
  let reason = 'timeout';
  while (Date.now() < deadline) {
    if (stop?.()) {
      reason = 'stopped';
      break;
    }
    try {
      const answer = await probe();
      if (answer !== false && answer !== undefined) return answer;
    } catch {}
    if (stop?.()) {
      reason = 'stopped';
      break;
    }
    const remaining = deadline - Date.now();
    if (remaining > 0) await delay(Math.min(intervalMs, remaining));
  }
  throw new Error(`waitReady ${reason} after ${Date.now() - started} ms`);
}
