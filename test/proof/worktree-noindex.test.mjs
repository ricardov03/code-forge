import assert from 'node:assert/strict';
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import path from 'node:path';
import { mock, test } from 'node:test';

let writes = 0;
const writeFileSync = fs.writeFileSync;
mock.method(fs, 'writeFileSync', (file, data, options) => {
  writes++;
  return writeFileSync(file, data, options);
});
syncBuiltinESMExports();
let top;
let entries = [];
let removeFails = false;
mock.module('../../src/util/exec.mjs', { namedExports: { exec: async (argv) => {
  const ok = (stdout = '') => ({ result: 'ok', code: 0, stdout, stderr: '' });
  if (argv[1] === 'rev-parse') return ok(top);
  if (argv[1] === 'check-ref-format') return ok(argv[3]);
  if (argv[1] === 'show-ref') return { ...ok(), result: 'nonzero', code: 1 };
  if (argv[1] === 'worktree' && argv[2] === 'list') return ok(entries.map((e) => `worktree ${e.path}\0branch refs/heads/${e.branch}\0\0`).join(''));
  if (argv[1] === 'worktree' && argv[2] === 'add') {
    fs.mkdirSync(argv[3], { recursive: true });
    return ok();
  }
  if (argv[1] === 'worktree' && argv[2] === 'remove' && removeFails) return { ...ok(), result: 'nonzero', code: 128, stderr: 'fatal: Directory not empty\n' };
  throw new Error(`unexpected git call: ${argv.join(' ')}`);
} } });
const { prepareWorktree, removeWorktree } = await import('../../src/proof/worktree.mjs');

/** @returns {Parameters<typeof prepareWorktree>[0]} */
function fixture() {
  const root = fs.mkdtempSync(path.join(process.env.CODE_FORGE_TEST_ROOT, 'noindex-'));
  top = path.join(root, 'repo');
  fs.mkdirSync(top);
  entries = [];
  removeFails = false;
  writes = 0;
  return { cwd: top, settings: { dir: null, branch: 'forge/{run}/{block}', base: null }, runId: 'r-old', id: 'B2', baseSha: 'a'.repeat(40), previous: null, platform: 'darwin' };
}

test('only the darwin default creates a marker; linux and explicit directories stay unchanged', async () => {
  for (const [platform, override, suffix, marked] of /** @type {const} */ ([
    ['darwin', null, '-worktrees.noindex', true],
    ['linux', null, '-worktrees', false],
    ['darwin', '../custom', null, false],
    ['darwin', '../repo-worktrees.noindex', null, false],
  ])) {
    const opts = fixture();
    opts.platform = platform;
    opts.settings.dir = override;
    const planned = await prepareWorktree(opts);
    const dir = suffix ? `${top}${suffix}` : path.resolve(top, override);
    assert.equal(planned.path, path.join(dir, 'B2'), `${platform}: exact worktree path`);
    await planned.make();
    assert.deepEqual(fs.readdirSync(dir).sort(), marked ? ['.metadata_never_index', 'B2'] : ['B2']);
    const marker = path.join(dir, '.metadata_never_index');
    assert.equal(fs.existsSync(marker), marked);
    if (marked) assert.equal(fs.readFileSync(marker, 'utf8'), '');
    assert.equal(writes, marked ? 1 : 0, `${platform} ${override}: marker writes`);
  }
});

test('a run stays in its old directory, a new run uses noindex, and an open block keeps its recorded path', async () => {
  const opts = fixture();
  const previous = { path: `${top}-worktrees/B1`, branch: 'forge/r-old/B1', base_sha: opts.baseSha };
  fs.mkdirSync(previous.path, { recursive: true });
  entries = [previous];
  const planned = await prepareWorktree(opts);
  assert.equal(planned.path, `${top}-worktrees/B2`);
  await planned.make();
  const reopened = await prepareWorktree({ ...opts, id: 'B1', previous });
  assert.equal(reopened.path, previous.path);
  assert.deepEqual(await reopened.make(), { worktree: previous, created: false, rollback: null });
  assert.equal(fs.existsSync(`${top}-worktrees/.metadata_never_index`), false, 'old directories remain unmarked on create and reuse');
  assert.equal(writes, 0);
  assert.equal(fs.existsSync(previous.path), true, 'the live tree remains at its recorded path');
  assert.equal(fs.existsSync(`${top}-worktrees.noindex`), false, 'reuse does not create a new directory');
  assert.equal((await prepareWorktree({ ...opts, runId: 'r-new' })).path, `${top}-worktrees.noindex/B2`);
  assert.equal((await prepareWorktree({ ...opts, settings: { ...opts.settings, dir: '../custom' } })).path, path.resolve(top, '../custom/B2'));
});

test('an unregistered partial delete reports the path and command once, keeps leftovers, and a registered tree keeps the normal error', async () => {
  fixture();
  const worktree = { path: path.join(top, 'left over'), branch: 'forge/r-old/B1', base_sha: 'a'.repeat(40) };
  fs.mkdirSync(worktree.path);
  fs.writeFileSync(path.join(worktree.path, '.DS_Store'), 'finder');
  removeFails = true;
  assert.equal(await removeWorktree({ cwd: top, worktree }), `worktree ${worktree.path} was unregistered but files are left; remove them with: rm -rf '${worktree.path}'`);
  assert.equal(fs.readFileSync(path.join(worktree.path, '.DS_Store'), 'utf8'), 'finder');
  entries = [worktree];
  assert.equal(await removeWorktree({ cwd: top, worktree }), 'Directory not empty');
});

test('recreating and reusing recorded trees marks only the darwin default directory', async () => {
  for (const [platform, override, suffix, marked] of /** @type {const} */ ([
    ['darwin', null, '-worktrees.noindex', true],
    ['darwin', null, '-worktrees', false],
    ['darwin', '../repo-worktrees.noindex', '-worktrees.noindex', false],
    ['linux', null, '-worktrees', false],
  ])) {
    const opts = fixture();
    opts.platform = platform;
    opts.settings.dir = override;
    const previous = { path: `${top}${suffix}/B1`, branch: 'forge/r-old/B1', base_sha: opts.baseSha };
    const marker = path.join(path.dirname(previous.path), '.metadata_never_index');
    const recreated = await prepareWorktree({ ...opts, id: 'B1', previous });
    assert.equal(recreated.path, previous.path);
    assert.equal((await recreated.make()).created, true);
    assert.equal(fs.existsSync(marker), marked);
    assert.equal(writes, marked ? 1 : 0);
    if (marked) fs.unlinkSync(marker);
    entries = [previous];
    writes = 0;
    const reused = await prepareWorktree({ ...opts, id: 'B1', previous });
    assert.equal((await reused.make()).created, false);
    assert.equal(fs.existsSync(marker), marked);
    assert.equal(writes, marked ? 1 : 0);
    if (marked) assert.equal(fs.readFileSync(marker, 'utf8'), '');
  }
});

test('creating a worktree adds one file write, and first use never overwrites an existing marker', async () => {
  const opts = fixture();
  const planned = await prepareWorktree(opts);
  assert.equal(writes, 0, 'planning is read-only');
  await planned.make();
  assert.equal(writes, 1, 'one marker write per new directory');
  const marker = path.join(path.dirname(planned.path), '.metadata_never_index');
  fs.writeFileSync(marker, 'keep me');
  writes = 0;
  await (await prepareWorktree({ ...opts, id: 'B3' })).make();
  assert.equal(writes, 0, 'an existing marker needs no write');
  assert.equal(fs.readFileSync(marker, 'utf8'), 'keep me');
});
