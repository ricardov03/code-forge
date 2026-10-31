/**
 * `code-forge doctor [--quick] [--json] [--cwd <dir>]` (plan §2.3; block B13b).
 *
 * Prints one row per check (`<STATUS> <label>: <detail>`) on stdout, or with `--json` ONE JSON
 * document `{ok, counts, rows}`; a one-line summary goes to stderr. Exit 0 when no row FAILs,
 * 1 when one does, 2 on a usage error. The full run adopts a private temp root for its probes
 * (every child it spawns is registered there) and removes it before exiting.
 */

import { randomBytes } from 'node:crypto';
import { rmSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig, projectRootFor } from '../config/load.mjs';
import { runDoctor } from '../doctor/index.mjs';
import { counts, exitCode, renderJSON, renderText } from '../doctor/rows.mjs';
import { parseFlags } from '../state/cli-args.mjs';
import { configInForce } from '../state/config-snapshot.mjs';
import { readRun } from '../state/run.mjs';
import { daemonEnabled, daemonRequest, nativePath, runNative } from '../util/native.mjs';
import { writeSafe } from '../util/redact.mjs';
import { runRoot, setRunRoot } from '../util/tmp.mjs';
import { liveWorker } from '../worker/queue.mjs';

const USAGE = 'usage: code-forge doctor [--quick] [--json] [--cwd <dir>]\n';

/**
 * @param {string[]} args
 * @param {import('../doctor/index.mjs').DoctorDeps & {stdout?: {write: (s: string) => unknown}, stderr?: {write: (s: string) => unknown}}} [deps]
 * @param {boolean} [adopt] - the real verb: adopt one temp root so exec registers every probe
 *   child there (in-process callers keep their own run root).
 * @returns {Promise<number>}
 */
export async function runDoctorCli(args, deps = {}, adopt = false) {
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  let parsed;
  try {
    parsed = parseFlags(args, { values: ['cwd'], booleans: ['quick', 'json'] });
  } catch (err) {
    writeSafe(stderr, `doctor: ${err.message}\n${USAGE}`);
    return 2;
  }
  if (parsed.positionals.length > 0) {
    writeSafe(stderr, USAGE);
    return 2;
  }
  const { flags } = parsed;
  const cwd = path.resolve(typeof flags.cwd === 'string' ? flags.cwd : process.cwd());
  /** @type {string | undefined} */
  let root;
  if (adopt && !flags.quick) {
    const loaded = await loadProjectConfig(cwd);
    const tmpRoot = loaded.ok && typeof loaded.config?.tmp?.root === 'string' ? loaded.config.tmp.root : undefined;
    try {
      root = runRoot(`doctor-${process.pid}-${randomBytes(3).toString('hex')}`, { root: tmpRoot });
      setRunRoot(root);
    } catch {
      root = undefined; // runDoctor reports the missing temp root as a FAIL row
    }
  }
  try {
    const rows = await runDoctor({ cwd, quick: flags.quick === true, ...(root ? { runRootDir: root } : {}) }, deps);
    if (!flags.quick) {
      const binary = nativePath();
      let nativeDetail = 'not found, using Node';
      let nativeOK = false;
      if (binary) {
        const result = await runNative(['version']);
        try {
          const version = result?.code === 0 ? JSON.parse(result.stdout) : null;
          if (typeof version?.version === 'string') {
            nativeOK = true;
            nativeDetail = `${binary}, version ${version.version}`;
          } else nativeDetail = `${binary}: unavailable, using Node`;
        } catch { nativeDetail = `${binary}: unavailable, using Node`; }
      }
      rows.push({ id: 'native', status: nativeOK ? 'OK' : 'INFO', label: 'native binary', detail: nativeDetail });
      let daemon = 'off';
      try {
        const loaded = await loadProjectConfig(cwd);
        const worker = liveWorker(projectRootFor(cwd));
        const config = (worker ? configInForce(await readRun(worker.run)) : null)
          ?? (loaded.ok ? loaded.config : { native: { daemon: false } });
        if (daemonEnabled(config)) {
          daemon = worker && await daemonRequest(worker.run, { method: 'ping' }, { config }) ? 'live' : 'fallback';
        }
      } catch { /* an unreadable config or worker leaves native acceleration off */ }
      rows.push({ id: 'native_daemon', status: 'INFO', label: 'native daemon', detail: daemon });
    }
    writeSafe(stdout, flags.json ? renderJSON(rows) : renderText(rows));
    const c = counts(rows);
    writeSafe(stderr, `doctor: ${c.OK} ok, ${c.WARN} warn, ${c.FAIL} fail\n`);
    return exitCode(rows);
  } finally {
    if (root) {
      setRunRoot(null);
      rmSync(root, { recursive: true, force: true });
    }
  }
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function doctor(args) {
  return runDoctorCli(args, {}, true);
}
