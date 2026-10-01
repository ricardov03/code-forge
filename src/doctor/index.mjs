/**
 * `code-forge doctor` (plan §2.3; block B13b): every row, in print order. `--quick` = the three
 * rows the skill's preflight needs (config, harness links, Jev key resolution); the full run adds
 * the PATH/Solo/gate rows, the live Jev call, ledger, `tmp`, the provider CLI probes (presence,
 * version, flags, one call per role builder, isolation, path deny, Codex rules), the worker
 * start/stop, the signer key and the `signer: same-user boundary only` line.
 */

import { randomBytes } from 'node:crypto';
import { mkdirSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { cliNameForProvider } from '../engines/provider-cli.mjs';
import { newRunId } from '../state/paths.mjs';
import { runRoot } from '../util/tmp.mjs';
import { checkCommands, checkConfig, checkJev, checkKeys, checkLedger, checkLinks, checkTmp, checkTools } from './local.mjs';
import { configuredProviders, pingRoles, probeCli, probeCodexRules, probeIsolation, probePathDeny } from './probes.mjs';
import { row } from './rows.mjs';
import { probeWorkerAndSigner, SIGNER_ROW } from './worker-probe.mjs';

/** @typedef {import('./rows.mjs').Row} Row */

/**
 * @typedef {object} DoctorDeps
 * @property {NodeJS.ProcessEnv} [env] - the env probes and spawned CLIs see (default `process.env`).
 * @property {Partial<Record<'claude'|'codex'|'grok', string>>} [bins] - replaces each provider CLI (tests: fakes).
 * @property {any} [store] - B2 key store (default: the production chain).
 * @property {any} [opRead]
 * @property {typeof import('../decide/jev-client.mjs').askJev} [askJev]
 * @property {string} [canary] - the path-deny canary (tests).
 * @property {NodeJS.Platform} [platform] - the recommended-tools row (default `process.platform`).
 * @property {(p: string) => boolean} [toolExists] - the recommended-tools row's Solo app check (tests).
 */

/** @param {Record<string, any> | null} cfg @returns {string} a new, private run root for the probes. */
function doctorRoot(cfg) {
  const root = typeof cfg?.tmp?.root === 'string' ? cfg.tmp.root : undefined;
  return runRoot(`doctor-${process.pid}-${Date.now()}-${randomBytes(3).toString('hex')}`, { root });
}

/**
 * @param {{cwd?: string, quick?: boolean, runRootDir?: string}} opts - `runRootDir`: the caller's
 *   run root (the CLI adopts one); otherwise the doctor makes one and removes it afterwards.
 * @param {DoctorDeps} [deps]
 * @returns {Promise<Row[]>}
 */
export async function runDoctor(opts = {}, deps = {}) {
  const env = deps.env ?? process.env;
  const cwd = path.resolve(opts.cwd ?? process.cwd());
  const checked = await checkConfig(cwd);
  const cfg = checked.cfg;
  const links = await checkLinks(env.HOME || os.homedir());
  const keys = await checkKeys(cfg, { env, store: deps.store, opRead: deps.opRead });
  /** @type {Row[]} */
  const rows = [...checked.rows, ...links, ...keys.rows];
  if (opts.quick) return rows;

  if (cfg) {
    rows.push(...(await checkCommands(cfg, env.PATH ?? '')));
    rows.push(...(await checkJev(cfg, keys.key, { askJev: deps.askJev })));
  }
  const toolOpts = {
    pathEnv: env.PATH ?? '',
    platform: deps.platform ?? process.platform,
    home: env.HOME || os.homedir(),
    ...(deps.toolExists ? { exists: deps.toolExists } : {}),
  };
  rows.push(...(await checkTools(toolOpts)));
  rows.push(...(await checkLedger()));
  rows.push(...checkTmp(cfg));
  if (!cfg) {
    rows.push(row('probes', 'FAIL', 'probes', 'skipped: no valid config'));
    rows.push(SIGNER_ROW);
    return rows;
  }

  const ownRoot = opts.runRootDir === undefined;
  let runRootDir;
  try {
    runRootDir = opts.runRootDir ?? doctorRoot(cfg);
  } catch (err) {
    rows.push(row('probes', 'FAIL', 'probes', `no temp root (${err?.message ?? 'error'})`));
    rows.push(SIGNER_ROW);
    return rows;
  }
  const workDir = path.join(runRootDir, 'doctor');
  mkdirSync(workDir, { recursive: true, mode: 0o700 });
  try {
    const ctx = { cfg, cwd, workDir, runRootDir, env, bins: deps.bins, canary: deps.canary };
    const missing = new Set();
    for (const provider of configuredProviders(cfg)) {
      const cli = cliNameForProvider(provider);
      if (!cli) {
        rows.push(row(`cli.${provider}`, 'FAIL', provider, 'no CLI mapping for this provider'));
        missing.add(provider);
        continue;
      }
      const probed = await probeCli(ctx, cli);
      rows.push(...probed.rows);
      if (!probed.present) missing.add(provider);
    }
    rows.push(...(await pingRoles(ctx, missing)));
    const reviewerProvider = cfg.levels?.L2?.provider ?? cfg.provider;
    const coderProvider = cfg.levels?.L1?.provider ?? cfg.provider;
    rows.push(...(missing.has(reviewerProvider) ? [row('isolation', 'FAIL', 'isolation', 'skipped (reviewer CLI missing)')] : await probeIsolation(ctx)));
    rows.push(...(missing.has(coderProvider) ? [row('path-deny', 'WARN', 'path deny', 'skipped (coder CLI missing)')] : await probePathDeny(ctx)));
    if (configuredProviders(cfg).includes('openai')) rows.push(...probeCodexRules(ctx));
    rows.push(...(await probeWorkerAndSigner({ cfg, configFile: checked.file, workDir, runRootDir, env, runId: newRunId() })));
  } finally {
    if (ownRoot) rmSync(runRootDir, { recursive: true, force: true });
    else rmSync(workDir, { recursive: true, force: true });
  }
  return rows;
}
