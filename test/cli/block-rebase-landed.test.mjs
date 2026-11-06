import { cfgFor, freshDir, git, writeFile } from '../review/helpers.mjs';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { mock, test } from 'node:test';
import { startGrant } from '../../src/autopilot/grant.mjs';
import { appendRow, readAllRows } from '../../src/ledger/write.mjs';
import { readRun, saveRun, startRun, writeSigned } from '../../src/state/run.mjs';
import { loadKey, verifyRow } from '../../src/state/signer.mjs';
import { exec } from '../../src/util/exec.mjs';
import * as native from '../../src/util/native.mjs';
import { registerSecret } from '../../src/util/redact.mjs';
import { blockTickets, readTicket, resolveTicket } from '../../src/worker/queue.mjs';
import { contentHash } from '../../src/worker/ticket.mjs';

// Never inherit the coder's run/ownership context into fixture children. Build
// the current source privately, independent of installed release binaries.
for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete process.env[key];
const testBinary = path.join(freshDir('native'), 'code-forge-native');
const built = spawnSync('go', ['build', '-o', testBinary, './cmd/code-forge-native'], {
  cwd: fileURLToPath(new URL('../../native/', import.meta.url)), encoding: 'utf8',
  env: { ...process.env, CGO_ENABLED: '0', GOTOOLCHAIN: 'local', GOPROXY: 'off' },
});
assert.equal(built.status, 0, `private native build: ${built.error?.message ?? built.stderr}`);
mock.module('../../src/util/native.mjs', { namedExports: { ...native,
  runNative: async (/** @type {string[]} */ args) => testBinary && process.env.CODE_FORGE_NATIVE !== '0'
    ? exec([testBinary, 'internal', ...args], { timeoutMs: 30_000 }) : null,
} });
const { runBlock } = await import('../../src/cli/block.mjs');
const available = { skip: testBinary ? false : 'needs a locally built native binary' };
const probe = async (/** @type {number} */ pid) => pid === 4242 ? 'fixture-worker' : null;

/** @param {string[]} args @param {Record<string, any>} [deps] */
async function cli(args, deps = {}) {
  let out = ''; let err = '';
  const code = await runBlock(args, { probe, isTTY: false, stdout: { write: (s) => { out += s; } }, stderr: { write: (s) => { err += s; } }, ...deps });
  return { code, out, err };
}

/** @param {Awaited<ReturnType<typeof fixture>>} f @param {boolean} [landed] */
function nodeCLI(f, landed = true) {
  const nativeURL = new URL('../../src/util/native.mjs', import.meta.url).href;
  const blockURL = new URL('../../src/cli/block.mjs', import.meta.url).href;
  const script = `
    import { mock } from 'node:test';
    import { spawnSync } from 'node:child_process';
    const native = await import(${JSON.stringify(nativeURL)});
    mock.module(${JSON.stringify(nativeURL)}, { namedExports: { ...native,
      runNative: async (args) => {
        const result = spawnSync(${JSON.stringify(testBinary)}, ['internal', ...args], { env: process.env, encoding: 'utf8' });
        return { result: result.status === 0 ? 'ok' : 'failed', code: result.status, stdout: result.stdout, stderr: result.stderr };
      },
    } });
    const { runBlock } = await import(${JSON.stringify(blockURL)});
    process.exitCode = await runBlock(process.argv.slice(1));
  `;
  return spawnSync(process.execPath, ['--disable-warning=ExperimentalWarning', '--disable-warning=DeprecationWarning', '--experimental-test-module-mocks', '--input-type=module', '-e', script,
    'rebase', 'B205b', '--run', f.runId, ...(landed ? ['--accept-landed'] : []), '--json'], {
    cwd: f.repo, encoding: 'utf8', env: process.env, timeout: 30_000,
  });
}

/** @param {string} runId */
const lockExists = (runId) => existsSync(path.join(process.env.HOME ?? '', '.code-forge', 'runs', `${runId}.lock`));

/** Closed sibling branches model B228/B231 landing while B205b remains open. */
async function fixture(label, prefix = '') {
  const slug = `landed-${label}`;
  const repo = freshDir(slug);
  const workspace = path.join(repo, prefix);
  const files = Array.from({ length: 5 }, (_, i) => `${prefix ? `${prefix}/` : ''}src/shared${i}.mjs`);
  await git(['init', '-qb', 'main'], repo);
  await git(['config', 'user.useConfigOnly', 'true'], repo);
  const config = { version: 1, project: { slug }, ...cfgFor(), gates: { test: ['node', '--test'], lint: null, types: null, format: null }, proof: { isolation: 'worktree', worktree: { dir: freshDir('trees') } } };
  writeFile(workspace, '.code-forge.yml', JSON.stringify(config));
  writeFile(repo, '.gitignore', '.code-forge/\n');
  for (const file of files) writeFile(repo, file, '// base\n');
  writeFile(repo, 'upstream.md', 'base\n');
  await git(['add', '-A'], repo);
  await git(['commit', '-qm', 'base'], repo);
  const runId = `r-${slug}`;
  const writeRow = (row) => appendRow(row, { slug });
  await startRun({ workspace, project: slug, runId, workerPid: 4242, probe, config, writeRow });
  const inputs = freshDir('inputs');
  writeFile(inputs, 'acceptance.json', JSON.stringify([{ clause: 'landed rebase', tests: ['block-rebase-landed'] }]));
  writeFile(inputs, 'coder.log', '$ node --test\n');
  assert.equal((await cli(['open', 'B205b', '--run', runId, '--level', 'L2', '--owned', 'src', '--acceptance', path.join(inputs, 'acceptance.json')])).code, 0);
  const entry = (await readRun(runId)).blocks.B205b;
  const tree = entry.worktree;
  for (const [i, name] of ['First', 'Second'].entries()) {
    writeFile(tree.path, `${prefix ? `${prefix}/` : ''}src/local.mjs`, `// local ${i}\n`);
    await git(['add', '-A'], tree.path);
    await git(['-c', `user.name=${name}`, '-c', `user.email=${name}@example.invalid`, 'commit', '-qm', `local ${i}`], tree.path);
  }
  const head = async () => (await git(['rev-parse', 'HEAD'], tree.path)).trim();
  const sign = (row) => writeSigned(runId, writeRow, { block: 'B205b', ...row });
  return { repo, files, runId, tree, base: entry.base_sha, head, sign,
    rows: () => readAllRows(slug), entry: async () => (await readRun(runId)).blocks.B205b,
    rebase: () => cli(['rebase', 'B205b', '--run', runId, '--accept-landed', '--json']),
    close: () => cli(['close', 'B205b', '--run', runId, '--worker-pid', '4242', '--transcript', path.join(inputs, 'coder.log')]),
    move: async (kind = 'landed') => {
      for (const [index, id] of ['B228', 'B231'].entries()) {
        const branch = index === 0 ? `forge/${runId}/${id}` : 'custom-landed';
        const base = (await git(['rev-parse', 'HEAD'], repo)).trim();
        if (kind === 'landed') await git(['checkout', '-qb', branch], repo);
        const changed = files.slice(index === 0 ? 0 : 3, index === 0 ? 3 : 5);
        for (const file of changed) writeFile(repo, file, `// ${id}\n`);
        writeFile(repo, 'upstream.md', `${id}\n`);
        await git(['add', '-A'], repo);
        await git(['commit', '-qm', id], repo);
        if (kind === 'landed') {
          // One default branch and one recorded custom branch exercise both origin forms.
          await git(['checkout', '-q', 'main'], repo);
          await git(['merge', '--ff-only', branch], repo);
          const record = await readRun(runId);
          record.blocks[id] = { status: 'closed', base_sha: base, owned_files: changed, worktree: { branch, base_sha: base } };
          await saveRun(record);
        }
      }
      return (await git(['rev-parse', 'HEAD'], repo)).trim();
    },
  };
}

test('Node landed rebase releases refused locks and replays only X after the credited pre-land Y tip', available, async () => {
  const f = await fixture('chained');
  const own = (await git(['rev-list', '--reverse', `${f.base}..HEAD`], f.tree.path)).trim().split('\n');
  const oldBranch = 'pre-land-Y';
  const branch = `forge/${f.runId}/B228`;
  await git(['checkout', '-qb', oldBranch], f.repo);
  for (const stage of ['intermediate', 'final']) {
    writeFile(f.repo, f.files[0], `// Y ${stage}\n`);
    await git(['add', '-A'], f.repo);
    await git(['commit', '-qm', `Y ${stage}`], f.repo);
  }
  const oldTip = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
  const inherited = (await git(['rev-list', '--reverse', `${f.base}..HEAD`], f.repo)).trim().split('\n');
  await git(['reset', '--hard', oldTip], f.tree.path);
  await git(['cherry-pick', ...own], f.tree.path);
  await git(['checkout', '-qb', branch, 'main'], f.repo);
  // Landing rebases Y first: new commit IDs, identical content, then --no-ff.
  for (const commit of inherited) await git(['-c', 'user.name=Landing', 'cherry-pick', commit], f.repo);
  assert.notEqual((await git(['rev-parse', 'HEAD'], f.repo)).trim(), oldTip, 'Y landing has new commit IDs');
  assert.equal(await git(['diff', oldTip, 'HEAD'], f.repo), '', 'Y landing preserves exactly the old tip content');
  await git(['checkout', '-q', 'main'], f.repo);
  await git(['merge', '--no-ff', '-m', 'land Y', branch], f.repo);
  const merged = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
  const record = await readRun(f.runId);
  record.blocks.B228 = { status: 'closed', base_sha: f.base, owned_files: [f.files[0]], worktree: { branch: oldBranch, base_sha: f.base } };
  await saveRun(record);

  const plain = nodeCLI(f, false);
  assert.equal(plain.status, 1, plain.stderr);
  assert.match(plain.stderr, /commits since its base changed owned files: src\/shared0\.mjs/);
  assert.equal(plain.stdout, '', 'plain refusal has zero stdout bytes');
  assert.equal(lockExists(f.runId), false, 'plain refusal leaves zero run lock directories');

  const result = nodeCLI(f);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stderr, '', 'successful Node rebase has zero stderr bytes');
  assert.deepEqual(JSON.parse(result.stdout), { block: 'B205b', event: 'rebased', old_base: f.base, new_base: merged, accepted_landed: [{ file: f.files[0], blocks: ['B228'] }] });
  assert.equal((await git(['rev-parse', 'HEAD~2'], f.tree.path)).trim(), merged, 'exactly two X commits sit on main');
  assert.deepEqual((await git(['log', '--reverse', '--format=%s', `${merged}..HEAD`], f.tree.path)).trim().split('\n'), ['local 0', 'local 1'], 'zero pre-land Y commits are replayed');
  assert.equal(lockExists(f.runId), false, 'success leaves zero run lock directories');
});

test('native refusal reports one redacted reason line, releases its lock, and preserves exact state', available, async () => {
  const f = await fixture('refusal-line');
  const secret = 'fixture-private-diagnostic';
  registerSecret(secret);
  const file = `src/${secret}\nsecond-line.mjs`;
  writeFile(f.repo, file, '// foreign\n');
  await git(['add', '-A'], f.repo);
  await git(['commit', '-qm', 'foreign owned file'], f.repo);
  const foreign = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
  const before = [await f.head(), await f.entry(), await f.rows()];
  const result = await f.rebase();
  assert.equal(result.code, 1, 'native refusal preserves exit 1');
  assert.equal(result.out, '', 'native refusal has zero stdout bytes');
  assert.match(result.err, /owned-conflict/);
  assert.ok(result.err.includes(foreign), 'reason identifies the refused commit');
  assert.equal(result.err.includes(secret), false, 'reason has zero registered secret occurrences');
  assert.equal(result.err.trimEnd().split('\n').length, 1, 'native reason occupies exactly one stderr line');
  assert.equal(lockExists(f.runId), false, 'native refusal leaves zero run lock directories');
  assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'refusal changes zero HEADs, bases or ledger rows');
});

test('landed siblings: five owned files queue again, bases move, row verifies and committers survive without identity', available, async () => {
  const f = await fixture('siblings', 'project');
  for (const file of f.files) await f.sign({ event: 'review.approved', file, content_hash: contentHash(f.tree.path, file), round: 1 });
  const original = await git(['log', '-2', '--format=%s%x00%cn%x00%ce'], f.tree.path);
  const newBase = await f.move();
  const previous = process.env.GIT_CONFIG_GLOBAL;
  process.env.GIT_CONFIG_GLOBAL = '/dev/null';
  let result;
  try { result = await f.rebase(); } finally {
    if (previous === undefined) delete process.env.GIT_CONFIG_GLOBAL; else process.env.GIT_CONFIG_GLOBAL = previous;
  }
  assert.equal(result.code, 0, result.err);
  // B228's commits precede B231's fork and cannot be credited to B231.
  const accepted = f.files.map((file, i) => ({ file, blocks: [i < 3 ? 'B228' : 'B231'] }));
  assert.deepEqual(JSON.parse(result.out), { block: 'B205b', event: 'rebased', old_base: f.base, new_base: newBase, accepted_landed: accepted });
  const entry = await f.entry();
  assert.deepEqual([entry.base_sha, entry.worktree.base_sha, entry.status], [newBase, newBase, 'open']);
  assert.equal(await git(['log', '-2', '--format=%s%x00%cn%x00%ce'], f.tree.path), original);
  assert.equal((await git(['rev-parse', 'HEAD~2'], f.tree.path)).trim(), newBase);
  const tickets = blockTickets({ repoRoot: f.repo, run: f.runId, block: 'B205b', tree: f.tree.path });
  assert.deepEqual(tickets.map((t) => t.file).sort(), f.files, 'exactly the five changed owned files are queued');
  for (const file of f.files) {
    const id = resolveTicket({ repoRoot: f.repo, run: f.runId, block: 'B205b', file, tree: f.tree.path }).ticket;
    assert.equal(readTicket(f.repo, id).content_hash, contentHash(f.tree.path, file), `${file}: Node ticket identity and hash`);
  }
  const rows = (await f.rows()).filter((r) => r.event === 'block.rebase');
  assert.equal(rows.length, 1);
  assert.deepEqual([rows[0].old_base, rows[0].new_base, rows[0].worktree, rows[0].accepted_landed], [f.base, newBase, true, accepted]);
  assert.deepEqual(verifyRow(rows[0], await loadKey(f.runId)), { ok: true });
});

test('no-ff landing credits the closed block exactly; an extra owned-file merge edit refuses without mutation', available, async () => {
  for (const evil of [true, false]) {
    const f = await fixture(`merge-${evil}`, 'project');
    const branch = `forge/${f.runId}/B228`;
    await git(['checkout', '-qb', branch], f.repo);
    writeFile(f.repo, f.files[0], '// B228\n');
    await git(['add', '-A'], f.repo);
    await git(['commit', '-qm', 'closed block change'], f.repo);
    const tip = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
    await git(['checkout', '-q', 'main'], f.repo);
    // Diverge main so the merge's diff against its second parent also has files
    // that the sibling did not change. Eligibility uses the first parent only.
    writeFile(f.repo, 'upstream.md', 'unowned main change\n');
    await git(['add', '-A'], f.repo);
    await git(['commit', '-qm', 'advance main'], f.repo);
    await git(['merge', '--no-ff', '--no-commit', branch], f.repo);
    if (evil) writeFile(f.repo, f.files[1], '// extra merge edit\n');
    await git(['add', '-A'], f.repo);
    await git(['commit', '-qm', 'land closed block'], f.repo);
    const merged = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
    assert.equal((await git(['rev-parse', 'HEAD^2'], f.repo)).trim(), tip, 'second parent is the closed block tip');
    const record = await readRun(f.runId);
    record.blocks.B228 = { status: 'closed', base_sha: f.base, owned_files: [f.files[0]], worktree: { branch, base_sha: f.base } };
    await saveRun(record);
    const before = [await f.head(), await f.entry(), await f.rows()];
    const result = await f.rebase();
    if (evil) {
      assert.equal(result.code, 1, result.err || result.out);
      assert.equal(result.out, '');
      assert.match(result.err, /owned-conflict/);
      assert.ok(result.err.includes(merged), 'refusal names the evil merge commit');
      assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'evil merge changes no HEAD, bases or rows');
    } else {
      assert.equal(result.code, 0, result.err);
      const accepted = [{ file: f.files[0], blocks: ['B228'] }];
      assert.deepEqual(JSON.parse(result.out), { block: 'B205b', event: 'rebased', old_base: f.base, new_base: merged, accepted_landed: accepted });
      const rows = (await f.rows()).filter((row) => row.event === 'block.rebase');
      assert.equal(rows.length, 1, 'exactly one rebase row');
      assert.deepEqual(rows[0].accepted_landed, accepted, 'row credits exactly the closed block and its one changed owned file');
      assert.equal((await git(['rev-parse', 'HEAD~2'], f.tree.path)).trim(), merged, 'local commits replay onto the landing merge');
    }
  }
});

test('a sync merge cannot credit an evil landing edit to a file changed only on main', available, async () => {
  const f = await fixture('sync-merge', 'project');
  const branch = `forge/${f.runId}/B228`;
  await git(['checkout', '-qb', branch], f.repo);
  writeFile(f.repo, f.files[0], '// B228\n');
  await git(['add', '-A'], f.repo);
  await git(['commit', '-qm', 'block change'], f.repo);
  await git(['checkout', '-q', 'main'], f.repo);
  writeFile(f.repo, f.files[1], '// main sibling change\n');
  await git(['add', '-A'], f.repo);
  await git(['commit', '-qm', 'main sibling change'], f.repo);
  const syncedBase = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
  const mainBranch = `forge/${f.runId}/B231`;
  await git(['branch', mainBranch], f.repo);
  await git(['checkout', '-q', branch], f.repo);
  await git(['merge', '--no-ff', '-m', 'sync main', 'main'], f.repo);
  await git(['checkout', '-q', 'main'], f.repo);
  await git(['merge', '--no-ff', '--no-commit', branch], f.repo);
  writeFile(f.repo, f.files[1], '// evil landing edit\n');
  await git(['add', '-A'], f.repo);
  await git(['commit', '-qm', 'evil landing'], f.repo);
  const merged = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
  const record = await readRun(f.runId);
  // The recorded synced base excludes main's commit from B228's origins, but
  // diffing the sync merge against every parent still exposes main's file.
  record.blocks.B228 = { status: 'closed', base_sha: f.base, owned_files: [f.files[0]], worktree: { branch, base_sha: syncedBase } };
  record.blocks.B231 = { status: 'closed', base_sha: f.base, owned_files: [f.files[1]], worktree: { branch: mainBranch, base_sha: f.base } };
  await saveRun(record);
  const before = [await f.head(), await f.entry(), await f.rows()];
  const result = await f.rebase();
  assert.equal(result.code, 1, result.err || result.out);
  assert.equal(result.out, '');
  assert.match(result.err, /owned-conflict/);
  assert.ok(result.err.includes(merged), 'refusal names the evil landing merge, not the credited main commit');
  assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'HEAD, bases and ledger stay unchanged');
});

test('foreign owned commits before a sibling fork or recorded worktree base refuse without mutation', available, async (t) => {
  for (const base of ['block', 'worktree']) {
    await t.test(base, async () => {
      const f = await fixture(`foreign-before-${base}`);
      const file = f.files[0];
      writeFile(f.repo, file, '// foreign before sibling fork\n');
      await git(['add', '-A'], f.repo);
      await git(['commit', '-qm', 'foreign before sibling fork'], f.repo);
      const foreign = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
      // The foreign commit is strictly before the recorded base, not just equal to it.
      writeFile(f.repo, 'upstream.md', 'advance before fork\n');
      await git(['add', '-A'], f.repo);
      await git(['commit', '-qm', 'advance before fork'], f.repo);
      await f.move();
      if (base === 'worktree') {
        const record = await readRun(f.runId);
        for (const id of ['B228', 'B231']) record.blocks[id].base_sha = f.base;
        await saveRun(record);
      }
      const before = [await f.head(), await f.entry(), await f.rows()];
      const result = await f.rebase();
      assert.equal(result.code, 1, result.err || result.out);
      assert.equal(result.out, '');
      assert.match(result.err, /owned-conflict/);
      assert.ok(result.err.includes(file) && result.err.includes(foreign), 'refusal names the foreign commit and owned file');
      assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'HEAD, both bases and ledger stay unchanged');
    });
  }
});

test('errors after replay restore HEAD and leave bases and ledger unchanged', available, async (t) => {
  for (const failure of ['queue', 'signer']) {
    await t.test(failure, async () => {
      const f = await fixture(`post-replay-${failure}`);
      await f.move();
      const before = [await f.head(), await f.entry(), await f.rows()];
      const queue = path.join(f.repo, '.code-forge', 'queue');
      const key = path.join(process.env.HOME ?? '', '.code-forge', 'runs', `${f.runId}.key`);
      const keyBytes = readFileSync(key);
      if (failure === 'queue') {
        rmSync(queue, { recursive: true, force: true });
        writeFileSync(queue, 'queue cannot be read as a directory\n');
      } else rmSync(key);
      let result;
      try { result = await f.rebase(); } finally {
        if (failure === 'queue') rmSync(queue);
        else writeFileSync(key, keyBytes, { mode: 0o600 });
      }
      assert.equal(result.code, 1);
      assert.equal(result.out, '');
      assert.match(result.err, failure === 'queue' ? /queue-unreadable/ : /no signer key/);
      assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'post-replay failure restores HEAD and publishes no rebase');
      assert.equal(await git(['status', '--porcelain'], f.tree.path), '');
      assert.equal((await git(['symbolic-ref', '--short', 'HEAD'], f.tree.path)).trim(), f.tree.branch);
      const retry = await f.rebase();
      assert.equal(retry.code, 0, retry.err);
      assert.equal((await f.rows()).filter((row) => row.event === 'block.rebase').length, 1);
    });
  }
});

test('foreign owned commits, including a reverted change after a landed commit, refuse with exact state unchanged', available, async () => {
  for (const reverted of [false, true]) {
    const f = await fixture(`foreign-${reverted}`, 'project');
    if (reverted) await f.move();
    const file = f.files[0];
    writeFile(f.repo, file, '// foreign\n');
    await git(['add', '-A'], f.repo);
    await git(['commit', '-qm', 'foreign'], f.repo);
    const foreign = (await git(['rev-parse', 'HEAD'], f.repo)).trim();
    if (reverted) {
      writeFile(f.repo, file, '// B228\n');
      await git(['add', '-A'], f.repo);
      await git(['commit', '-qm', 'revert foreign'], f.repo);
    }
    const before = [await f.head(), await f.entry(), await f.rows()];
    const result = await f.rebase();
    assert.equal(result.code, 1);
    assert.equal(result.out, '');
    assert.match(result.err, /owned-conflict/);
    assert.ok(result.err.includes(file) && result.err.includes(foreign), 'refusal identifies the file and foreign commit');
    assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'head, both bases and every row stay unchanged');
  }
  const f = await fixture('replay-conflict');
  writeFile(f.tree.path, 'upstream.md', 'local conflict\n');
  await git(['add', '-A'], f.tree.path);
  await git(['commit', '-qm', 'conflict after earlier replayed commits'], f.tree.path);
  await f.move();
  const before = [await f.head(), await f.entry(), await f.rows()];
  const result = await f.rebase();
  assert.equal(result.code, 1);
  assert.equal(result.out, '');
  assert.match(result.err, /rebase-conflict/);
  assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before, 'conflict after replay restores exact head and both bases, with zero rows');
  assert.equal(await git(['status', '--porcelain'], f.tree.path), '');
  assert.equal((await git(['symbolic-ref', '--short', 'HEAD'], f.tree.path)).trim(), f.tree.branch);
  assert.doesNotMatch(await git(['status'], f.tree.path), /cherry-pick|rebase in progress/);
});

test('under an active grant landed rebase needs no TTY, human acceptance stays refused, and close prints no unowned_change', available, async () => {
  const f = await fixture('grant');
  const now = new Date();
  await startGrant({ runId: f.runId, input: { until: new Date(now.getTime() + 3600000).toISOString(), delegate: 'L2', allow: 'waive:warning' }, now, writeRow: (row) => appendRow(row, { slug: 'landed-grant' }) });
  await f.move();
  const before = [await f.head(), await f.entry(), await f.rows()];
  const humanArgs = ['rebase', 'B205b', '--run', f.runId, '--accept-changed', f.files[0], '--reason', 'owner acceptance'];
  const human = await cli(humanArgs, { isTTY: true, confirm: async () => true });
  assert.equal(human.code, 1);
  assert.match(human.err, /refused while an autopilot grant is active/);
  assert.equal((await cli(humanArgs)).code, 2, 'human acceptance still requires a terminal');
  assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before);
  const result = await f.rebase();
  assert.equal(result.code, 0, result.err);
  const closed = await f.close();
  assert.equal(closed.code, 1, 'close exposes remaining review and gate blockers');
  assert.match(closed.out, /not_approved|no_green_gates/);
  assert.doesNotMatch(closed.out + closed.err, /unowned_change/, 'all printed close blockers exclude upstream files');
});

test('disabled native binary requires the native binary and changes nothing', async () => {
  const f = await fixture('disabled');
  await f.move();
  const before = [await f.head(), await f.entry(), await f.rows()];
  const previous = process.env.CODE_FORGE_NATIVE;
  process.env.CODE_FORGE_NATIVE = '0';
  let result;
  try { result = await f.rebase(); } finally {
    if (previous === undefined) delete process.env.CODE_FORGE_NATIVE; else process.env.CODE_FORGE_NATIVE = previous;
  }
  assert.deepEqual(result, { code: 1, out: '', err: 'block rebase --accept-landed needs the native binary (native/bin/<platform>-<arch>)\n' });
  assert.deepEqual([await f.head(), await f.entry(), await f.rows()], before);
});
