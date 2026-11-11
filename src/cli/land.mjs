import { copyFile, lstat, mkdir, mkdtemp, readdir, readFile, realpath, rm, stat, symlink } from 'node:fs/promises';
import path from 'node:path';
import { loadProjectConfig } from '../config/load.mjs';
import { doneFilePath } from '../state/block-end.mjs';
import { exec } from '../util/exec.mjs';
import { writeSafe } from '../util/redact.mjs';

/**
 * Callbacks supplied by the block CLI keep land independent of its dispatch and gate handlers.
 * @typedef {object} LandDeps
 * @property {{write: (s: string) => unknown}} [stdout]
 * @property {{write: (s: string) => unknown}} [stderr]
 * @property {(...args: any[]) => Promise<number>} runBlock
 * @property {(...args: any[]) => Promise<number>} runReviewFile
 * @property {(...args: any[]) => Promise<number>} runGatesVerb
 * @property {(...args: any[]) => Promise<any>} autoClaimChanges
 * @property {(...args: any[]) => Promise<any>} blockTree
 * @property {(...args: any[]) => Promise<string>} repoTop
 * @property {(...args: any[]) => any} configInForce
 * @property {() => NodeJS.ProcessEnv} gitChildEnv
 * @property {(text: string) => any} parseTapPassed
 * @property {(...args: any[]) => Promise<any>} appendRow
 * @property {(row: Record<string, any>, key: Buffer) => Record<string, any>} signRow
 * @property {(runId: string) => Promise<Buffer>} loadKey
 */

/** Quote a displayed command for a POSIX shell; execution always uses argv.
 * @param {string[]} argv @returns {string}
 */
export function landCommand(argv) {
  return argv.map((arg) => /^[\w./:@=-]+$/.test(arg) ? arg : `'${arg.replaceAll("'", "'\\''")}'`).join(' ');
}


export const USAGE = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n";

export const SUMMARY = "Provide the block landing implementation used by block land.";

const mergeGateHint = 'the gate ran in a temporary tree with tracked files only; git-ignored files the suite needs go in gates.merge_gate_links';

/** @param {string} root @param {string} name @returns {Promise<string>} */
async function mergeGatePath(root, name) {
  const refuse = () => new Error('gates.merge_gate_links paths must stay inside the repository (no absolute paths or ..)');
  if (!name || path.isAbsolute(name) || /[\\\0:]/.test(name) || name.split('/').some((part) => part === '..' || part === '.git') || path.normalize(name) === '.') throw refuse();
  root = await realpath(root);
  let ancestor = path.join(root, name);
  const tail = [];
  while (true) {
    try { await lstat(ancestor); break; } catch (err) { if (err.code !== 'ENOENT') throw err; }
    tail.unshift(path.basename(ancestor));
    ancestor = path.dirname(ancestor);
  }
  const resolved = path.join(await realpath(ancestor), ...tail);
  const rel = path.relative(root, resolved);
  if (rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) throw refuse();
  return resolved;
}

/** Copy a source already checked by mergeGatePath. Its children are any names on disk (a:b, a
 * nested .git), so they are checked for containment only, under the resolved repository root.
 * @param {string} realRoot realpath of the main checkout @param {string} source @param {string} destination @param {Set<string>} [ancestors] */
async function copyMergeGatePath(realRoot, source, destination, ancestors = new Set()) {
  source = await realpath(source);
  const rel = path.relative(realRoot, source);
  if (!rel || rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) throw new Error('gates.merge_gate_links paths must stay inside the repository');
  if (ancestors.has(source)) throw new Error('gates.merge_gate_links contains a directory cycle');
  const info = await stat(source);
  if (info.isDirectory()) {
    await mkdir(destination, { mode: info.mode });
    const next = new Set([...ancestors, source]);
    for (const name of await readdir(source)) await copyMergeGatePath(realRoot, path.join(source, name), path.join(destination, name), next);
  } else if (info.isFile()) await copyFile(source, destination);
  else throw new Error('gates.merge_gate_links copies require regular files or directories');
}

/** Fallback-only preparation; the native land path owns normal product execution.
 * @param {string} main @param {string} temporary @param {string} workspace
 * @param {{write: (s: string) => unknown}} stderr @param {NodeJS.ProcessEnv} env
 */
async function prepareMergeGate(main, temporary, workspace, stderr, env) {
  const loaded = await loadProjectConfig(workspace);
  if (!loaded.ok) throw new Error(loaded.message);
  const gates = loaded.config?.gates ?? {};
  const links = gates.merge_gate_links ?? [];
  const setup = gates.merge_gate_setup;
  if (!Array.isArray(links) || links.some((name) => typeof name !== 'string' || !name)) throw new Error('gates.merge_gate_links must be an array of nonempty paths');
  if (setup != null && (!Array.isArray(setup) || setup.length === 0 || setup.some((arg) => typeof arg !== 'string' || !arg || arg.includes('\0')))) throw new Error('gates.merge_gate_setup must be an argv array');
  const seen = new Set();
  const realMain = await realpath(main);
  for (const name of links) {
    const source = await mergeGatePath(main, name);
    if (seen.has(source)) continue;
    seen.add(source);
    const destination = await mergeGatePath(temporary, name);
    for (const root of [main, temporary]) {
      const tracked = await exec(['git', 'ls-files', '-z', '--', `:(literal)${name}`], { cwd: root, env, timeoutMs: 120000 });
      if (tracked.result !== 'ok' || tracked.stdout) throw new Error('gates.merge_gate_links must not include tracked paths; they need no link');
    }
    try { await stat(source); } catch (err) {
      if (err.code !== 'ENOENT') throw err;
      writeSafe(stderr, `warning: gates.merge_gate_links source missing: ${name}\n`);
      continue;
    }
    try { await lstat(destination); throw new Error('gates.merge_gate_links destination already exists'); } catch (err) { if (err.code !== 'ENOENT') throw err; }
    await mkdir(path.dirname(destination), { recursive: true });
    if (process.env.CODE_FORGE_LAND_COPY_LINKS !== '1') {
      try { await symlink(source, destination); continue; } catch (err) { if (!['EPERM', 'EACCES', 'ENOTSUP', 'ENOSYS'].includes(err.code)) throw err; }
    }
    await copyMergeGatePath(realMain, source, destination);
  }
  if (setup != null) {
    const result = await exec(setup, { cwd: workspace, env: { ...env, CI: '1', npm_config_yes: 'false' }, timeoutMs: 300000 });
    if (result.result !== 'ok') throw new Error('gates.merge_gate_setup failed');
  }
}
/** Join, gates and close share their existing CLI handlers, without spawning the CLI.
 * @param {{id: string, runId: string, record: Record<string, any>, report: string, transcript: string, maxMs: number, merge: boolean, noMergeGate: boolean, json: boolean}} options
 * @param {LandDeps} deps
 * @returns {Promise<number>}
 */
export async function landBlock({ id, runId, record, report, transcript, maxMs, merge, noMergeGate, json }, deps) {
  const { autoClaimChanges, blockTree, runReviewFile, runGatesVerb, runBlock, repoTop, configInForce, gitChildEnv, parseTapPassed, appendRow, signRow, loadKey } = deps;
  const stdout = deps.stdout ?? process.stdout;
  const stderr = deps.stderr ?? process.stderr;
  const emit = (/** @type {Record<string, any>} */ value) => writeSafe(stdout, `${JSON.stringify(value)}\n`);
  let step = 'join';
  let joinMs = 0, gatesMs = 0;
  /** @type {number | null} */
  let mergeGateMs = null;
  let next = landCommand(['code-forge', 'review-file', '--wait-block', id, '--run', runId]);
  const stop = (/** @type {string} */ reason) => {
    if (step === 'merge-gate' && !reason.includes(mergeGateHint)) reason += `\n${mergeGateHint}`;
    if (step === 'join' && reason.split('\n')[0] === 'wait_timeout') {
      next = landCommand(['code-forge', 'block', 'land', id, '--run', runId, '--report', report, '--transcript', transcript,
        '--max', `${Math.max(1800, Math.ceil(maxMs / 500))}s`, ...(merge ? ['--merge'] : []), ...(noMergeGate ? ['--no-merge-gate'] : []), ...(process.env.CODE_FORGE_LAND_COPY_LINKS === '1' ? ['--copy-links'] : []), ...(json ? ['--json'] : [])]);
    }
    writeSafe(stderr, `block ${id} land stopped at ${step}: ${reason.trim()}\nnext: ${next}\n`);
    if (json) emit({ block: id, event: 'refused', step, reason: reason.trim(), next });
    return 1;
  };
  /** @param {(streams: {stdout: {write: (s: string) => void}, stderr: {write: (s: string) => void}}) => Promise<number>} run */
  const capture = async (run) => {
    let output = '';
    let error = '';
    const code = await run({ stdout: { write: (s) => { output += s; } }, stderr: { write: (s) => { error += s; } } });
    /** @type {Record<string, any> | null} */
    let data = null;
    try { data = JSON.parse(output); } catch { /* A thrown refusal may only write stderr. */ }
    return { code, output, error, data };
  };
  try {
    const entry = record.blocks?.[id];
    if (!entry) return stop(`unknown block in run ${runId}`);
    if (entry.status !== 'open' && entry.status !== 'closed') return stop(`block is ${entry.status}, not open`);
    const tree = await blockTree(record.workspace, entry);
    if (!tree) return stop('block has no recorded worktree and branch');
    if (entry.status !== 'closed') {
      const deadline = Date.now() + maxMs;
      await autoClaimChanges({ record, runId, id, writeRow: (row) => appendRow(row, { slug: record.project }), print: (line) => writeSafe(json ? stderr : stdout, line), join: false });
      const stamp = async () => {
        const options = { cwd: tree.top, env: gitChildEnv(), timeoutMs: 120000 };
        const head = await exec(['git', 'rev-parse', '--verify', 'HEAD'], options);
        const status = await exec(['git', 'status', '--porcelain', '--untracked-files=all'], options);
        if (head.result !== 'ok' || status.result !== 'ok') throw new Error('cannot stamp block worktree');
        return `${head.stdout}\n${status.stdout}`;
      };
      const before = await stamp();
      const join = async () => {
        const started = Date.now();
        try {
          let joined;
          // Findings are usually final; cap retries so land does not wait the full --max on them.
          for (let attempt = 0; attempt < 3; attempt += 1) {
            const remaining = Math.max(0, deadline - Date.now());
            if (remaining === 0) return 'wait_timeout';
            const duration = remaining > 9_999_999 ? `${Math.floor(remaining / 1000)}s` : `${remaining}ms`;
            joined = await capture((streams) => runReviewFile(['--wait-block', id, '--run', runId, '--max', duration], { ...streams, cwd: tree.workspace }));
            for (const line of joined.error.split('\n')) if (line.startsWith('auto-requeue ')) writeSafe(stderr, `${line}\n`);
            if (joined.code !== 0 || !joined.data?.done || !joined.data.files.some((file) => file.status === 'findings')) break;
          }
          if (joined.code !== 0 || !joined.data?.done) {
            if (joined.data?.status === 'wait_timeout' || joined.data?.reason === 'wait_timeout' || Date.now() >= deadline) return 'wait_timeout';
            return joined.error || joined.data?.message || joined.data?.reason || joined.data?.status || 'reviews did not finish';
          }
          const problems = joined.data.files.filter((/** @type {Record<string, any>} */ file) => file.status !== 'excluded' && file.outcome !== 'approved');
          if (problems.length === 0) return '';
          const details = [];
          for (const file of problems) {
            details.push(`${file.file}: ${file.outcome ?? file.status}`);
            if (file.ticket) {
              const result = await capture((streams) => runReviewFile(['--wait', file.ticket], { ...streams, cwd: tree.workspace }));
              details.push(result.output.trim() || result.error.trim());
            }
          }
          next = landCommand(['code-forge', 'review-file', '--wait', problems[0].ticket]);
          return details.join('\n');
        } finally {
          joinMs = Date.now() - started;
        }
      };
      const gateController = new AbortController();
      const runGates = async () => {
        const started = Date.now();
        try {
          // B363: land merges onto the new tip and its merge gate tests the merged tree, so a stale
        // base gates on as before with the one WARN line (printed below with the gate's stderr)
        return await capture((streams) => runGatesVerb(['run', '--run', runId, '--block', id, '--changed', '--cwd', tree.workspace, '--allow-stale-base'], { ...streams, signal: gateController.signal }));
        } finally {
          gatesMs += Date.now() - started;
        }
      };
      // Observe both rejections immediately, but cancel gates as soon as join refuses.
      const joining = Promise.allSettled([join()]);
      const gating = Promise.allSettled([runGates()]);
      const [joined] = await joining;
      const joinReason = joined.status === 'fulfilled' ? joined.value : joined.reason?.message ?? String(joined.reason);
      if (joinReason) gateController.abort();
      const [initialGates] = await gating;
      let gateResult = initialGates;
      if (!joinReason && before !== await stamp()) [gateResult] = await Promise.allSettled([runGates()]);
      const gates = gateResult.status === 'fulfilled' ? gateResult.value : null;
      const gateReason = gateResult.status === 'rejected' ? gateResult.reason?.message ?? String(gateResult.reason)
        : gates.data?.cancelled ? '' : gates.code !== 0 || gates.data?.allOk !== true ? gates.error + (gates.output || 'gates did not pass') : '';
      if (joinReason) return stop(joinReason + (gateReason ? `\ngates: ${gateReason}` : ''));

      step = 'gates';
      next = landCommand(['code-forge', 'gates', 'run', '--run', runId, '--block', id, '--changed']);
      if (gateReason) return stop(gateReason);
      if (gates.error) writeSafe(stderr, gates.error);

      step = 'close';
      const closeArgs = ['close', id, '--run', runId, '--report', report, '--transcript', transcript];
      next = landCommand(['code-forge', 'block', ...closeArgs]);
      const closed = await capture((streams) => runBlock([...closeArgs, '--json'], { ...deps, ...streams }));
      if (closed.code !== 0 || closed.data?.status !== 'closed') {
        if (typeof closed.data?.next_command === 'string') next = closed.data.next_command;
        return stop(closed.error || closed.data?.reason || closed.data?.status || 'close refused');
      }
      if (closed.error) writeSafe(stderr, closed.error);
    }

    step = 'merge';
    const main = await repoTop(record.workspace);
    if (typeof entry.base_branch !== 'string' || !entry.base_branch) return stop('block has no recorded base branch');
    const mergeArgs = ['git', '-C', main, 'merge', '--no-ff', '--no-edit', '--', tree.branch];
    const command = landCommand(mergeArgs);
    next = command;
    if (entry.status === 'closed') {
      // A retry skips join, gates and close: merge only the branch head close checked (its done file's commit).
      let recorded = null;
      try {
        const done = JSON.parse(await readFile(doneFilePath(runId, id), 'utf8'));
        if (done?.status === 'closed' && typeof done.commit === 'string') recorded = done.commit;
      } catch { /* no readable record: refused below */ }
      if (!recorded || !/^[0-9a-f]{40,64}$/.test(recorded)) {
        next = landCommand(['code-forge', 'status', '--run', runId]);
        return stop('closed block has no recorded close commit; refusing to merge a branch head that close did not check');
      }
      const head = await exec(['git', 'rev-parse', '--verify', '--end-of-options', `refs/heads/${tree.branch}^{commit}`], { cwd: main, env: gitChildEnv(), timeoutMs: 120000 });
      if (head.result !== 'ok') return stop(`cannot resolve block branch ${tree.branch}`);
      if (head.stdout.trim() !== recorded) {
        next = landCommand(['code-forge', 'status', '--run', runId]);
        return stop(`branch ${tree.branch} moved after close (closed at ${recorded.slice(0, 12)}, now ${head.stdout.trim().slice(0, 12)}); commits added after close were not reviewed or gated`);
      }
    }
    if (!merge) {
      writeSafe(json ? stderr : stdout, 'hint: the merge gate runs only with --merge\n');
      if (json) emit({ block: id, event: 'closed', merge: command });
      else writeSafe(stdout, `block ${id} closed; merge: ${command}\n`);
      return 0;
    }
    const gitOptions = { cwd: main, env: gitChildEnv(), timeoutMs: 120000 };
    const branch = await exec(['git', 'symbolic-ref', '-q', 'HEAD'], gitOptions);
    if (branch.result !== 'ok' || branch.stdout.trim() !== `refs/heads/${entry.base_branch}`) {
      next = `${landCommand(['git', '-C', main, 'checkout', entry.base_branch])} && ${command}`;
      return stop(`main checkout must be on recorded base branch ${entry.base_branch}`);
    }
    const status = await exec(['git', 'status', '--porcelain', '--untracked-files=all'], gitOptions);
    if (status.result !== 'ok') return stop(status.stderr || 'cannot check main checkout');
    if (status.stdout) {
      next = landCommand(['git', '-C', main, 'status', '--short']);
      return stop('main checkout is dirty; commit or stash its changes before merging');
    }
    // Close measures uncommitted content too; never call that content landed if git cannot merge it.
    const blockStatus = await exec(['git', 'status', '--porcelain', '--untracked-files=all'], { ...gitOptions, cwd: tree.top });
    if (blockStatus.result !== 'ok' || blockStatus.stdout) {
      next = landCommand(['git', '-C', tree.top, 'status', '--short']);
      return stop('block worktree is dirty or unreadable; commit and review its changes before merging');
    }
    if (noMergeGate || configInForce(record)?.gates?.merge_gate === false) {
      writeSafe(stderr, 'warning: merge gate skipped; merged full suite was not checked\n');
    } else {
      step = 'merge-gate';
      next = landCommand(['code-forge', 'gates', 'run', '--cwd', tree.workspace]);
      const mainHead = await exec(['git', 'rev-parse', '--verify', 'HEAD'], gitOptions);
      const blockHead = await exec(['git', 'rev-parse', '--verify', `refs/heads/${tree.branch}`], gitOptions);
      if (mainHead.result !== 'ok' || blockHead.result !== 'ok') return stop('cannot resolve merge parents');
      const workspacePrefix = await exec(['git', 'rev-parse', '--show-prefix'], { ...gitOptions, cwd: record.workspace });
      if (workspacePrefix.result !== 'ok') return stop('cannot resolve merge-gate workspace prefix');
      const prefix = workspacePrefix.stdout.replace(/\n$/, '');
      if (prefix.startsWith('..') || path.isAbsolute(prefix)) return stop('merge-gate workspace prefix is outside the repository');
      const started = Date.now();
      // A sibling of this run's block worktree, never the process temp directory.
      const temporary = await mkdtemp(path.join(path.dirname(tree.path), '.merge-gate-'));
      let added = false;
      let failure = '';
      try {
        // mkdtemp gives an empty directory; git worktree add accepts an existing empty path.
        const created = await exec(['git', 'worktree', 'add', '--detach', temporary, mainHead.stdout.trim()], gitOptions);
        added = created.result === 'ok';
        if (!added) throw new Error(created.stderr || 'cannot create merge-gate worktree');
        const merged = await exec(['git', 'merge', '--no-ff', '--no-edit', '--', blockHead.stdout.trim()], { ...gitOptions, cwd: temporary });
        if (merged.result !== 'ok') {
          const conflicts = await exec(['git', 'diff', '--name-only', '--diff-filter=U'], { ...gitOptions, cwd: temporary });
          failure = conflicts.stdout.trim() ? `merge conflict:\n${conflicts.stdout.trim()}` : merged.stderr || merged.stdout || 'trial merge failed';
        } else {
          const workspace = path.join(temporary, prefix);
          await prepareMergeGate(main, temporary, workspace, stderr, gitChildEnv());
          // No block scope: the shared gate runner drops scope tokens and runs the full suite.
          const suite = await capture((streams) => runGatesVerb(['run', '--cwd', workspace], streams));
          if (suite.code !== 0 || suite.data?.allOk !== true) {
            const results = suite.data?.results ?? [];
            const output = results.filter((r) => !r.ok).map((r) => `${r.stdout}\n${r.stderr}`).join('\n');
            const failed = [...parseTapPassed(output).failed].slice(0, 10);
            failure = failed.length ? `full suite failed:\n${failed.join('\n')}`
              : `full suite failed:\n${(suite.error || output || suite.output).trim().split('\n').slice(-20).join('\n')}`;
          } else if (!suite.data.results.some((r) => r.gate === 'test' && !r.skipped)) {
            failure = 'full suite unavailable: configure gates.test before merging';
          }
        }
      } finally {
        mergeGateMs = Date.now() - started;
        const removed = await exec(['git', 'worktree', 'remove', '--force', temporary], gitOptions);
        if (added && removed.result !== 'ok') throw new Error(`cannot remove merge-gate worktree ${temporary}: ${removed.stderr}`);
        await rm(temporary, { recursive: true, force: true });
      }
      if (failure) return stop(`${failure}\n${mergeGateHint}`);
      // Refuse a moving target: only merge the same parents whose combined suite passed.
      const currentMain = await exec(['git', 'rev-parse', '--verify', 'HEAD'], gitOptions);
      const currentBlock = await exec(['git', 'rev-parse', '--verify', `refs/heads/${tree.branch}`], gitOptions);
      const currentBranch = await exec(['git', 'symbolic-ref', '-q', 'HEAD'], gitOptions);
      const currentStatus = await exec(['git', 'status', '--porcelain', '--untracked-files=all'], gitOptions);
      if (currentMain.result !== 'ok' || currentBlock.result !== 'ok' || currentBranch.result !== 'ok' || currentStatus.result !== 'ok'
        || currentMain.stdout !== mainHead.stdout || currentBlock.stdout !== blockHead.stdout || currentBranch.stdout !== branch.stdout || currentStatus.stdout) {
        return stop('merge parents or main checkout changed during the full suite; check the checkout and rerun the merge gate');
      }
      writeSafe(json ? stderr : stdout, `merge gate: full suite passed (${((Date.now() - started) / 1000).toFixed(1)}s)\n`);
    }
    step = 'merge';
    next = command;
    const merged = await exec(mergeArgs, gitOptions);
    if (merged.result !== 'ok') {
      next = landCommand(['git', '-C', main, 'status']);
      return stop(merged.stderr || merged.stdout || 'git merge failed; resolve the merge before continuing');
    }
    if (json) emit({ block: id, event: 'landed', merge: command });
    else writeSafe(stdout, `block ${id} landed\n`);
    return 0;
  } catch (thrown) {
    return stop(thrown?.message ?? String(thrown));
  } finally {
    await appendRow(signRow({ event: 'land.timing', run: runId, block: id, join_ms: joinMs, gates_ms: gatesMs, merge_gate_ms: mergeGateMs, parallel: true }, await loadKey(runId)), { slug: record.project });
  }
}

