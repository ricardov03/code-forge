/** Opt-in commit watch. Read immutable git objects, never the coder's working bytes. */
import { randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { reviewFileLoop } from '../config/validate.mjs';
import { snapshotFor } from '../state/config-snapshot.mjs';
import { readRun } from '../state/run.mjs';
import { ownsAt } from '../review/gate-check.mjs';
import { exec } from '../util/exec.mjs';
import { blockTickets, enqueueHash, isDone, queueDir, supersedeTicket } from './queue.mjs';
import { assertRowPath, DELETED_HASH, gitChildEnv, hashBytes } from './ticket.mjs';

const SHA = /^[0-9a-f]{40,64}$/;
const BLOCK = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/**
 * One scheduler tick, also exposed directly for deterministic race tests.
 * The boot config gates ALL watch work; absent/wait/overlap never reads the run or spawns git.
 * @param {{repoRoot: string, runId: string, cfg: Record<string, any>, readRun?: typeof readRun,
 *   writeRow: (row: Record<string, any>) => Promise<unknown>, exec?: typeof exec,
 *   running?: ReadonlySet<string>, hooks?: {afterHead?: (head: string) => Promise<unknown>, beforeQueue?: () => Promise<unknown>}}} opts
 */
export async function watchTick({ repoRoot, runId, cfg, readRun: loadRun = readRun, writeRow, exec: execute = exec, running = new Set(), hooks = {} }) {
  if (reviewFileLoop(cfg) !== 'watch') return;
  const record = await loadRun(runId);
  if (record.status !== 'active' || reviewFileLoop(snapshotFor(record, '')?.config ?? cfg) !== 'watch') return;
  const env = gitChildEnv();
  for (const [block, entry] of Object.entries(record.blocks ?? {})) {
    if (entry.status !== 'open' || !BLOCK.test(block)) continue;
    const stateFile = path.join(queueDir(repoRoot), 'watch', `${block}.json`);
    let tree = null;
    let root = repoRoot;
    let state = {};
    try { state = JSON.parse(readFileSync(stateFile, 'utf8')) ?? {}; } catch { /* restart at base */ }
    const base = entry.base_sha;
    /** @param {Record<string, any>} next */
    const save = (next) => {
      mkdirSync(path.dirname(stateFile), { recursive: true });
      const tmp = `${stateFile}.${process.pid}.${randomBytes(4).toString('hex')}.tmp`;
      try {
        writeFileSync(tmp, `${JSON.stringify(next)}\n`, { mode: 0o600 });
        renameSync(tmp, stateFile);
        state = next;
      } finally { rmSync(tmp, { force: true }); }
    };
    /** @param {string} reason */
    const skip = async (reason) => {
      const skipped = Array.isArray(state.skipped) ? state.skipped : [];
      if (!skipped.includes(reason)) {
        await writeRow({ event: 'watch.skipped', block, reason });
        save({ ...state, skipped: [...skipped, reason] });
      }
    };
    try {
      if (entry.worktree) {
        root = realpathSync(entry.worktree.path);
        tree = root;
      }
      if (state.run !== runId || state.base !== base || state.tree !== tree) state = { run: runId, base, tree, skipped: [] };
      /** @param {string[]} argv */
      const git = (argv) => execute(['git', ...argv], { cwd: root, env, timeoutMs: 10000 });
      const headResult = await git(['rev-parse', '--verify', 'HEAD^{commit}']);
      const head = headResult.stdout.trim();
      if (headResult.result !== 'ok' || !SHA.test(head)) { await skip('unreadable_head'); continue; }
      const earlier = blockTickets({ repoRoot, run: runId, block, tree });
      await hooks.afterHead?.(head);
      if (typeof base !== 'string' || !SHA.test(base) || (await git(['merge-base', '--is-ancestor', base, head])).result !== 'ok') {
        await skip('not_descendant'); continue;
      }
      const last = typeof state.sha === 'string' && SHA.test(state.sha) ? state.sha : base;
      if (last !== base && (await git(['merge-base', '--is-ancestor', last, head])).result !== 'ok') {
        await skip('rewound');
        const { sha: _sha, ...restart } = state;
        save(restart);
        continue;
      }
      // A healthy tick ends the skip streak: no new commit here, or a queued commit below. An
      // unreadable commit keeps the streak, so it writes one watch.skipped row, not one per tick.
      if (last === head) { if (state.skipped?.length) save({ ...state, skipped: [] }); continue; }
      const diff = await git(['diff', '--name-only', '-z', '--no-renames', last, head, '--']);
      if (diff.result !== 'ok') { await skip('unreadable_head'); continue; }
      const owns = ownsAt(tree ? '' : path.relative(repoRoot, record.workspace).split(path.sep).join('/'));
      const files = diff.stdout.split('\0').filter(Boolean);
      const owned = files.filter((file) => owns(entry.owned_files, file, entry.owned_kinds));
      if (owned.length !== files.length) await skip('unowned_path');
      // Stage every hash before publishing anything: an unreadable object queues nothing.
      const hashes = [];
      let unreadable = false;
      for (const raw of owned) {
        const file = assertRowPath(raw);
        const listed = await git(['ls-tree', '-z', head, '--', file]);
        if (listed.result !== 'ok') { unreadable = true; break; }
        if (!listed.stdout) { hashes.push({ file, content_hash: DELETED_HASH }); continue; }
        if (!/^100(?:644|755) blob /.test(listed.stdout)) { unreadable = true; break; }
        const shown = await execute(['git', 'show', `${head}:${file}`], { cwd: root, env, timeoutMs: 10000, stdoutEncoding: 'base64' });
        if (shown.result !== 'ok') { unreadable = true; break; }
        hashes.push({ file, content_hash: hashBytes(Buffer.from(shown.stdout, 'base64')) });
      }
      if (unreadable) { await skip('unreadable_head'); continue; }
      await hooks.beforeQueue?.();
      for (const { file, content_hash } of hashes) {
        const current = enqueueHash({ repoRoot, run: runId, block, file, content_hash, tree });
        for (const old of earlier) {
          if (old.file === file && old.ticket !== current.ticket && !running.has(old.ticket) && !isDone(repoRoot, old.ticket)) supersedeTicket(repoRoot, old.ticket);
        }
      }
      save({ ...state, run: runId, base, tree, sha: head, skipped: [] });
    } catch {
      // A bad path, missing tree or failed git read is a skip, never a block failure.
      await skip('unreadable_head');
    }
  }
}
