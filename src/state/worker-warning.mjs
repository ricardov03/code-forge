import { workerRegistry } from '../worker/queue.mjs';
import { repoRootOf } from '../worker/ticket.mjs';
import { checkWorkerPin, normalizeStartTime, processStartTime } from './run.mjs';

/** @typedef {{pid: number, start_time: string, run: string}} WarningWorker */
/** @typedef {{live: boolean, worker: WarningWorker | null}} RegistryEntry */

/**
 * Read-only advice about live workers serving a run without a matching process identity pin.
 * Failure to discover the repository, read its registry or probe a pin never breaks status.
 * @param {Record<string, any>} record
 * @param {{repoRoot?: string, registry?: (root: string) => RegistryEntry[] | Promise<RegistryEntry[]>, probe?: import('./run.mjs').StartTimeProbe}} [opts]
 * @returns {Promise<string[]>}
 */
export async function unpinnedWorkerWarnings(record, { repoRoot, registry = workerRegistry, probe = processStartTime } = {}) {
  try {
    if (record.status === 'ended') return [];
    const root = repoRoot ?? await repoRootOf(record.workspace);
    const pins = record.workers ?? (record.worker ? [record.worker] : []);
    const unpinned = (await registry(root)).filter(({ live, worker }) => live && worker?.run === record.run_id
      && !pins.some((pin) => pin.pid === worker.pid
        && normalizeStartTime(pin.started_at) === normalizeStartTime(worker.start_time)));
    if (unpinned.length === 0) return [];

    let suffix = '';
    if (unpinned.length === 1 && pins.length > 0) {
      const check = await checkWorkerPin(record, { probe });
      if (!check.ok) suffix = ` (pinned pid ${pins[0].pid} is ${check.event === 'worker.replaced' ? 'replaced' : 'down'})`;
    }
    return unpinned.map(({ worker }) => `warning: live worker ${worker.pid} is not pinned: run start --reattach --run ${record.run_id}`
      + (unpinned.length > 1 ? ` --worker-pid ${worker.pid}` : suffix));
  } catch {
    return [];
  }
}
