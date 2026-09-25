/**
 * Worker start/stop and signer rows (plan §2.3, §4.8, §8.6; block B13b).
 *
 * The probe makes a throwaway run in a throwaway git repo (the project's `.code-forge.yml` copied
 * in): B8 `startRun` generates the run key, B8 `signer` signs a row and verifies it (and refuses
 * a byte-flipped copy), B11 `launchWorker` starts `code-forge worker` detached and waits for its
 * queue announcement, then the worker is stopped (SIGTERM, SIGKILL of its group after 5 s). The
 * run's record, key, lock and temp root are removed afterwards: doctor leaves no run behind.
 *
 * The `signer` row is printed on every full run: `signer: same-user boundary only` (§8.6) — the
 * MAC is tamper evidence within ordinary tool use, never a boundary against a same-user process.
 */

import { copyFileSync, mkdirSync, readdirSync, rmSync } from 'node:fs';
import path from 'node:path';
import { runsDir } from '../state/paths.mjs';
import { startRun } from '../state/run.mjs';
import { loadKey, signRow, verifyRow } from '../state/signer.mjs';
import { exec } from '../util/exec.mjs';
import { isAlive, registerPid, unregisterPid } from '../util/reaper.mjs';
import { pidsDir, tmpBase } from '../util/tmp.mjs';
import { launchWorker } from '../worker/loop.mjs';
import { liveWorker } from '../worker/queue.mjs';
import { gitChildEnv } from '../worker/ticket.mjs';
import { row } from './rows.mjs';

/** @typedef {import('./rows.mjs').Row} Row */

export const SIGNER_ROW = Object.freeze(row('signer', 'INFO', 'signer', 'same-user boundary only'));

const STOP_WAIT_MS = 5000;

/** @param {() => boolean} cond @param {number} ms */
async function waitFor(cond, ms) {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    if (cond()) return true;
    await new Promise((r) => setTimeout(r, 50));
  }
  return cond();
}

/**
 * SIGTERM the worker, then SIGKILL its group when it is still alive after `STOP_WAIT_MS`.
 * @param {number} pid @returns {Promise<boolean>} true when it stopped on SIGTERM.
 */
async function stopWorker(pid) {
  try {
    process.kill(pid, 'SIGTERM');
  } catch {
    return true; // already gone
  }
  if (await waitFor(() => !isAlive(pid), STOP_WAIT_MS)) return true;
  try {
    process.kill(-pid, 'SIGKILL');
  } catch {
    // gone meanwhile
  }
  await waitFor(() => !isAlive(pid), 2000);
  return false;
}

/** @param {string} runId remove the probe run's record, key, lock and anything else it left in the runs dir. */
function removeRun(runId) {
  let names = [];
  try {
    names = readdirSync(runsDir());
  } catch {
    return;
  }
  for (const name of names) if (name === `${runId}.json` || name === `${runId}.key` || name === `${runId}.lock` || name.startsWith(`${runId}.json.`)) rmSync(path.join(runsDir(), name), { recursive: true, force: true });
}

/**
 * @param {{cfg: Record<string, any>, configFile: string, workDir: string, runRootDir: string, env: NodeJS.ProcessEnv, runId: string}} ctx
 * @returns {Promise<Row[]>} the `signer key` row, the `worker` row and the `signer` line.
 */
export async function probeWorkerAndSigner(ctx) {
  const { runId, env } = ctx;
  const repo = path.join(ctx.workDir, 'worker-repo');
  const rows = [];
  /** @type {number | null} */
  let pid = null;
  try {
    mkdirSync(repo, { recursive: true, mode: 0o700 });
    const init = await exec(['git', 'init', '-q'], { cwd: repo, env: gitChildEnv(env), timeoutMs: 20000 });
    if (init.result !== 'ok') throw Object.assign(new Error('git init failed'), { code: 'git-init' });
    copyFileSync(ctx.configFile, path.join(repo, '.code-forge.yml'));
    await startRun({ workspace: repo, project: 'doctor-probe', config: ctx.cfg, runId, writeRow: async () => {} });

    const key = await loadKey(runId);
    const signed = signRow({ event: 'doctor.probe', run: runId }, key);
    const flipped = { ...signed, event: 'doctor.probf' };
    const signerOk = verifyRow(signed, key).ok && verifyRow(flipped, key).reason === 'mismatch';
    rows.push(row('signer-key', signerOk ? 'OK' : 'FAIL', 'signer key', signerOk ? 'generated; a signed row verifies, a changed one is refused' : 'sign/verify round trip failed'));

    const started = await launchWorker({ runId, workspace: repo, env });
    pid = started.pid;
    registerPid(pidsDir(ctx.runRootDir), pid, 'code-forge worker'); // a killed doctor's worker is reaped by the next sweep
    const announced = liveWorker(started.repoRoot)?.pid === pid;
    const clean = await stopWorker(pid);
    unregisterPid(pidsDir(ctx.runRootDir), pid);
    const workerPid = pid;
    pid = null;
    const gone = liveWorker(started.repoRoot) === null;
    rows.push(announced && clean && gone
      ? row('worker', 'OK', 'worker', `pid ${workerPid} started, announced itself and stopped on SIGTERM`)
      : row('worker', 'FAIL', 'worker', `pid ${workerPid} start/stop incomplete (announced=${announced}, stopped on SIGTERM=${clean}, retracted=${gone})`));
  } catch (err) {
    if (!rows.some((r) => r.id === 'signer-key')) rows.push(row('signer-key', 'FAIL', 'signer key', `run key could not be generated (${err?.code ?? err?.name ?? 'error'})`));
    rows.push(row('worker', 'FAIL', 'worker', `could not start (${err?.code ?? err?.name ?? 'error'})`));
  } finally {
    if (pid !== null) await stopWorker(pid);
    removeRun(runId);
    const cfgRoot = typeof ctx.cfg?.tmp?.root === 'string' ? ctx.cfg.tmp.root : undefined;
    try {
      rmSync(path.join(tmpBase(cfgRoot), runId), { recursive: true, force: true }); // the worker's own run root
    } catch {
      // tmp.root invalid: the tmp row reports it
    }
    rmSync(repo, { recursive: true, force: true });
  }
  rows.push(SIGNER_ROW);
  return rows;
}
