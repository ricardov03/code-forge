import { commitAll, freshDir, lines, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { describe, test } from 'node:test';

const { assemblePacket, attachFiles, buildPacket, PacketError, readFileDiff } = await import('../../src/review/packet.mjs');

test('B97 reproduction: needs_file attaches a new owned changed test file', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'test/widget.test.mjs', "test('handles empty input', () => {});\n");
  const section = await attachFiles({
    repoRoot: repo, paths: ['test/widget.test.mjs'], budgetTokens: 1000,
    ownedFiles: ['test/widget.test.mjs'], changedFiles: ['test/widget.test.mjs'],
  });
  assert.equal(section, "## attached files (read-only, requested in needs_file)\n### test/widget.test.mjs\n1| test('handles empty input', () => {});\n");
});

/** @type {import('../../src/review/packet.mjs').FileDiff} */
const fixtureDiff = {
  file: 'src/widget.mjs', kind: 'new', content: 'export const widget = 1;\n',
  diffText: '@@ -0,0 +1 @@\n+export const widget = 1;\n',
  hunks: [{ header: '@@ -0,0 +1 @@', oldStart: 0, oldLines: 0, newStart: 1, newLines: 1 }],
  plusCount: 1, minusCount: 0,
};
const fixtureOpts = { diff: fixtureDiff, lens: 'quick', rulesDigest: 'rule one\nrule two', factsExcerpt: 'F1: fixture', moved: '## moved code\n(none moved)' };

test('B97 legacy: no paired input is byte-equal to the HEAD packet', () => {
  const packet = assemblePacket(fixtureOpts);
  assert.equal(packet.status, 'ok');
  if (packet.status !== 'ok') return;
  // Captured from HEAD 79bbe61 before packet.mjs changed; hashes output, never src text.
  assert.equal(createHash('sha256').update(packet.text).digest('hex'), 'a187a50dcf5624afb3b6a5bf862f401ee5b6095bf4a546b667857beba75447fa');
  assert.deepEqual([packet.contextMode, packet.contextLines, packet.digestLines, packet.tokensIn, packet.budget, packet.overBudget], ['whole', 1, 2, 547, 6000, false]);
  assert.deepEqual(assemblePacket({ ...fixtureOpts, paired: '' }), packet);
  assert.deepEqual(assemblePacket({ ...fixtureOpts, paired: '  \n' }), packet);
});

test('B97 paired section: after context, before moved and diff, capped at full_in / 16', () => {
  const packet = assemblePacket({ ...fixtureOpts, paired: 'test/widget.test.mjs\n- handles empty input' });
  assert.equal(packet.status, 'ok');
  if (packet.status !== 'ok') return;
  assert.deepEqual(packet.text.split('\n').filter((line) => ['## context', '## paired', '## moved code', '## diff'].includes(line)), ['## context', '## paired', '## moved code', '## diff']);
  assert.equal(packet.text.split('## paired\n')[1].split('## moved code')[0], 'test/widget.test.mjs\n- handles empty input\n');
  const capped = assemblePacket({ ...fixtureOpts, paired: 'é'.repeat(300), cfg: { review: { budgets: { full_in: 1600 } } } });
  assert.equal(capped.status, 'ok');
  if (capped.status !== 'ok') return;
  const section = capped.text.split('## paired\n')[1].split('## moved code')[0];
  assert.equal(section, 'é'.repeat(194) + '\n');
  assert.equal(Buffer.byteLength('## paired\n' + section), 399);
});

test('B97 paired budget: dropped first with whole context, digest and diff preserved', () => {
  const plain = assemblePacket(fixtureOpts);
  assert.equal(plain.status, 'ok');
  if (plain.status !== 'ok') return;
  const packet = assemblePacket({ ...fixtureOpts, budget: plain.tokensIn, paired: 'test/widget.test.mjs\n' + 'extra context '.repeat(5000) });
  assert.deepEqual(packet, { ...plain, budget: plain.tokensIn });
  if (packet.status !== 'ok') return;
  assert.equal(packet.text.split('## paired').length - 1, 0);
  assert.equal(packet.text.split('## diff\n')[1], 'file: src/widget.mjs\nhunks:\n- @@ -0,0 +1 @@\n\n@@ -0,0 +1 @@\n+export const widget = 1;\n');
});

test('B97 attachment refusals: unowned, unchanged, ignored, secret and missing eligibility', async () => {
  const repo = await makeRepo();
  writeFile(repo, '.gitignore', 'test/ignored.test.mjs\n');
  for (const file of ['test/unowned.test.mjs', 'test/unchanged.test.mjs', 'test/ignored.test.mjs', '.env', 'test/eligible.test.mjs']) writeFile(repo, file, 'FAKE-refused-body\n');
  const section = await attachFiles({
    repoRoot: repo, paths: ['test/unowned.test.mjs', 'test/unchanged.test.mjs', 'test/ignored.test.mjs', '.env'], budgetTokens: 1000,
    ownedFiles: ['test/unchanged.test.mjs', 'test/ignored.test.mjs', '.env'],
    changedFiles: ['test/unowned.test.mjs', 'test/ignored.test.mjs', '.env'],
  });
  assert.equal(section, '## attached files (read-only, requested in needs_file)\n### test/unowned.test.mjs (refused: untracked, not owned by this block)\n### test/unchanged.test.mjs (refused: untracked, not changed in this block)\n### test/ignored.test.mjs (refused: gitignored)\n### .env (refused: secret-like path)\n');
  assert.equal(section.split('FAKE-refused-body').length - 1, 0);
  assert.equal(await attachFiles({ repoRoot: repo, paths: ['test/eligible.test.mjs'], budgetTokens: 1000 }), '## attached files (read-only, requested in needs_file)\n### test/eligible.test.mjs (refused: untracked, not owned by this block)\n');
});

test('B97 attachment git safety: inherited GIT_* stripped and git failure refuses', async () => {
  const repo = await makeRepo();
  const other = await makeRepo();
  writeFile(repo, 'test/widget.test.mjs', 'FAKE-allowed-body\n');
  const saved = { GIT_DIR: process.env.GIT_DIR, GIT_WORK_TREE: process.env.GIT_WORK_TREE, GIT_INDEX_FILE: process.env.GIT_INDEX_FILE, GIT_CONFIG_COUNT: process.env.GIT_CONFIG_COUNT };
  Object.assign(process.env, { GIT_DIR: path.join(other, '.git'), GIT_WORK_TREE: other, GIT_INDEX_FILE: path.join(other, 'missing-index'), GIT_CONFIG_COUNT: 'invalid' });
  try {
    assert.equal(await attachFiles({ repoRoot: repo, paths: ['test/widget.test.mjs'], budgetTokens: 1000, ownedFiles: ['test/widget.test.mjs'], changedFiles: ['test/widget.test.mjs'] }), '## attached files (read-only, requested in needs_file)\n### test/widget.test.mjs\n1| FAKE-allowed-body\n');
  } finally {
    for (const [key, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
  }
  const nonRepo = freshDir('nongit');
  writeFile(nonRepo, 'widget.mjs', 'FAKE-refused-git-body\n');
  assert.equal(await attachFiles({ repoRoot: nonRepo, paths: ['widget.mjs'], budgetTokens: 1000, ownedFiles: ['widget.mjs'], changedFiles: ['widget.mjs'] }), '## attached files (read-only, requested in needs_file)\n### widget.mjs (refused: git failed)\n');
});

/** @param {string} text @returns {string[]} the diff body lines that add a line. */
function plusLines(text) {
  const body = text.slice(text.indexOf('## diff'));
  const rows = body.split('\n');
  return rows.slice(rows.findIndex((l) => l.startsWith('@@'))).filter((l) => l.startsWith('+'));
}

describe('packet builder (§4.1)', () => {
  test('a new 40-line file yields a packet with 40 `+` lines', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/new.mjs', lines(40));
    const packet = /** @type {any} */ (await buildPacket({ repoRoot: repo, file: 'src/new.mjs', base: null, lens: 'full', cfg: {} }));
    assert.equal(packet.status, 'ok');
    assert.equal(packet.diff.kind, 'new');
    assert.equal(packet.diff.plusCount, 40);
    assert.equal(plusLines(packet.text).length, 40);
    assert.deepEqual(packet.hunkHeaders, ['@@ -0,0 +1,40 @@']);
  });

  test('control: the same file tracked and unchanged yields no packet', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/new.mjs', lines(40));
    await commitAll(repo);
    const res = await buildPacket({ repoRoot: repo, file: 'src/new.mjs', base: null, lens: 'full', cfg: {} });
    assert.deepEqual(res, { status: 'no_change', file: 'src/new.mjs' });
  });

  test('the section order and the lens/rules/facts prefix are identical across two files', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/a.mjs', lines(5));
    writeFile(repo, 'lib/b.mjs', lines(12, (i) => `const b${i} = ${i};`));
    const common = { repoRoot: repo, base: null, lens: 'full', rulesDigest: 'rule 1: argv arrays only\nrule 2: no secrets', factsExcerpt: 'F1 VERIFIED node >= 22', cfg: {} };
    const a = /** @type {any} */ (await buildPacket({ ...common, file: 'src/a.mjs' }));
    const b = /** @type {any} */ (await buildPacket({ ...common, file: 'lib/b.mjs' }));
    const heads = (/** @type {string} */ t) => t.split('\n').filter((l) => /^#{1,2} /.test(l));
    const order = ['# code-forge review packet', '## lens', '## project rules', '## facts', '## context', '## diff'];
    assert.deepEqual([heads(a.text).filter((h) => order.includes(h)), heads(b.text).filter((h) => order.includes(h))], [order, order]);
    const prefix = (/** @type {string} */ t) => t.slice(0, t.indexOf('## context'));
    assert.equal(prefix(a.text), prefix(b.text));
    assert.notEqual(a.text, b.text);
  });

  test('a symlinked `file` is refused with bad-path (V4); the target bytes reach 0 packets', async () => {
    const repo = await makeRepo();
    const marker = 'FAKE-secret-b12a-symlink-target-9c1e';
    const outside = path.join(freshDir('outside'), 'secret.txt');
    writeFileSync(outside, `${marker}\n`);
    writeFile(repo, 'src/real.mjs', lines(3));
    symlinkSync(outside, path.join(repo, 'src', 'link-out.mjs')); // → outside the repo
    symlinkSync(path.join(repo, 'src', 'real.mjs'), path.join(repo, 'src', 'link-in.mjs')); // → inside the repo
    const common = { repoRoot: repo, base: null, lens: 'full', cfg: {} };
    /** @type {string[]} */
    const codes = [];
    for (const file of ['src/link-out.mjs', 'src/link-in.mjs']) {
      await assert.rejects(buildPacket({ ...common, file }), (err) => {
        assert.ok(err instanceof PacketError);
        codes.push(/** @type {any} */ (err).code);
        return true;
      });
    }
    assert.deepEqual(codes, ['bad-path', 'bad-path']);
    // the only packet this repo yields is the real file's, and the target's bytes are not in it
    const control = /** @type {any} */ (await buildPacket({ ...common, file: 'src/real.mjs' }));
    assert.deepEqual([control.status, control.diff.plusCount], ['ok', 3]);
    assert.equal(control.text.includes(marker), false);
  });

  test('B11.1: the diff content of a regular new file (non-ASCII, CRLF) is byte-identical to the file on disk', async () => {
    const repo = await makeRepo();
    const text = 'export const s = "é ✓";\r\nexport const t = 2;\n';
    writeFile(repo, 'src/u.mjs', text);
    const diff = await readFileDiff({ repoRoot: repo, file: 'src/u.mjs', base: null });
    assert.deepEqual([diff.kind, Buffer.compare(Buffer.from(/** @type {string} */ (diff.content)), Buffer.from(text))], ['new', 0]);
  });

  test('pathspec magic: `src/*` and a `:`-prefixed value are refused with bad-path; no other file is diffed', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/one.mjs', lines(2));
    writeFile(repo, 'src/two.mjs', lines(4, (i) => `const two${i} = ${i};`));
    const common = { repoRoot: repo, base: null, lens: 'full', cfg: {} };
    /** @type {string[]} */
    const codes = [];
    for (const file of ['src/*', 'src/one?mjs', 'src/[ot]*.mjs', ':/src/one.mjs', ':(glob)src/**']) {
      await assert.rejects(buildPacket({ ...common, file }), (err) => {
        assert.ok(err instanceof PacketError, `${file}: ${err}`);
        codes.push(/** @type {any} */ (err).code);
        return true;
      });
    }
    assert.deepEqual(codes, ['bad-path', 'bad-path', 'bad-path', 'bad-path', 'bad-path']);
    const one = /** @type {any} */ (await buildPacket({ ...common, file: 'src/one.mjs' })); // a literal name diffs exactly itself
    assert.deepEqual([one.status, one.diff.plusCount, one.text.includes('two1')], ['ok', 2, false]);
  });

  test('an untracked `.env` (FAKE secret) and an untracked gitignored file are refused with bad-path; the secret reaches 0 packets', async () => {
    const repo = await makeRepo();
    const marker = 'FAKE-untracked-secret-b12a-4b7d';
    writeFile(repo, '.env', `API_TOKEN=${marker}\n`);
    writeFile(repo, '.gitignore', 'local/\n');
    writeFile(repo, 'local/build-secret.txt', `${marker}-ignored\n`);
    writeFile(repo, 'src/ok.mjs', lines(2));
    const common = { repoRoot: repo, base: null, lens: 'full', cfg: {} };
    /** @type {string[]} */
    const codes = [];
    for (const file of ['.env', 'local/build-secret.txt']) {
      await assert.rejects(buildPacket({ ...common, file }), (err) => {
        assert.ok(err instanceof PacketError, `${file}: ${err}`);
        codes.push(/** @type {any} */ (err).code);
        return true;
      });
    }
    assert.deepEqual(codes, ['bad-path', 'bad-path']);
    const ok = /** @type {any} */ (await buildPacket({ ...common, file: 'src/ok.mjs' }));
    assert.deepEqual([ok.status, ok.diff.kind, ok.diff.plusCount], ['ok', 'new', 2]);
    assert.equal(ok.text.includes(marker), false);
  });

  test('a tracked directory (`src`) is refused with bad-path, never a multi-file diff or an EISDIR', async () => {
    const repo = await makeRepo();
    writeFile(repo, 'src/a.mjs', lines(2));
    writeFile(repo, 'src/b.mjs', lines(3));
    await commitAll(repo);
    writeFile(repo, 'src/a.mjs', lines(5)); // a change git would show under `src`
    await assert.rejects(buildPacket({ repoRoot: repo, file: 'src', base: null, lens: 'full', cfg: {} }), (err) => {
      assert.ok(err instanceof PacketError, String(err));
      assert.deepEqual([/** @type {any} */ (err).code, err.message], ['bad-path', 'not a regular file']);
      return true;
    });
    const file = /** @type {any} */ (await buildPacket({ repoRoot: repo, file: 'src/a.mjs', base: null, lens: 'full', cfg: {} })); // the file itself still builds
    assert.deepEqual([file.status, file.diff.kind, file.diff.plusCount], ['ok', 'tracked', 3]);
  });

  test('a `file` value with `../` is refused with bad-path before git runs', async () => {
    const repo = await makeRepo();
    await assert.rejects(buildPacket({ repoRoot: repo, file: '../outside.mjs', base: null, lens: 'full', cfg: {} }), (err) => {
      assert.ok(err instanceof PacketError);
      assert.equal(/** @type {any} */ (err).code, 'bad-path');
      return true;
    });
  });
});
