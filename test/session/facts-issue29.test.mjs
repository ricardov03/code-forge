import { cfgWith, fakeDeps, freshDir, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { buildFacts, extractClaims, validateFacts } = await import('../../src/session/facts.mjs');

const claims = /** @type {const} */ ([{ token: './a', kind: 'path' }, { token: './b', kind: 'path' }]);
/** @returns {{facts: import('../../src/session/facts.mjs').Fact[]}} */
const answer = () => ({ facts: claims.map((claim, i) => ({
  fact_id: `F${i + 1}`, claim: claim.token, kind: claim.kind,
  command: `ls ${claim.token}`, output_excerpt: claim.token, tag: 'VERIFIED', why: null,
})) });

test('issue29: a 350-character excerpt is capped with an ellipsis and keeps its tag and other rows', () => {
  const input = answer();
  input.facts[0].output_excerpt = 'x'.repeat(350);
  const before = structuredClone(input);
  let result;
  assert.doesNotThrow(() => { result = validateFacts(input, claims); }, 'one long excerpt must not refuse the sheet');
  assert.equal(result.facts.length, 2);
  assert.deepEqual(result.facts[0], { ...before.facts[0], output_excerpt: `${'x'.repeat(199)}…` });
  assert.equal(result.facts[0].output_excerpt.length, 200);
  assert.deepEqual(result.facts[1], before.facts[1]);
  assert.deepEqual(input, before, 'the caller answer stays unchanged');
});

test('issue29: an excerpt at the cap is unchanged while a long excerpt on a null-command row is normalized', () => {
  const input = answer();
  input.facts[0].output_excerpt = 'b'.repeat(200);
  input.facts[1].command = null;
  input.facts[1].output_excerpt = 'c'.repeat(201);
  let result;
  assert.doesNotThrow(() => { result = validateFacts(input, claims); });
  assert.equal(result.facts.length, 2);
  assert.deepEqual(result.facts[0], input.facts[0]);
  assert.deepEqual(result.facts[1], {
    ...input.facts[1], command: '', output_excerpt: `${'c'.repeat(199)}…`,
    tag: 'UNVERIFIABLE', why: 'delegate returned null command',
  });
  assert.deepEqual(result.downgraded, [{ fact_id: 'F2', reason: 'delegate returned null command' }]);
});

test('issue29: repo-relative files and absolute paths are path claims, while spaced section numbers are not versions', () => {
  assert.deepEqual(extractClaims('Edit app/Services/Sigma/SigmaClient.php and routes/api.php'), [
    { token: 'app/Services/Sigma/SigmaClient.php', kind: 'path' },
    { token: 'routes/api.php', kind: 'path' },
  ]);
  assert.deepEqual(extractClaims('/Users/x/other-repo/app/Models/User.php'), [
    { token: '/Users/x/other-repo/app/Models/User.php', kind: 'path' },
  ]);
  assert.deepEqual(extractClaims('see § 12.1 and § 4.3'), []);
  assert.deepEqual(extractClaims('§12.1; version 12.1; src/a.mjs; ./a; ../b; ~/c'), [
    { token: '12.1', kind: 'version' }, { token: 'src/a.mjs', kind: 'path' },
    { token: './a', kind: 'path' }, { token: '../b', kind: 'path' }, { token: '~/c', kind: 'path' },
  ]);
});

test('issue29: only known top-level folders admit extensionless relative paths; packages, URLs and slash words stay masked', () => {
  const projectDir = freshDir('issue29-folders');
  mkdirSync(path.join(projectDir, 'app'));
  writeIn(projectDir, 'file', 'not a folder');
  const text = '@scope/pkg https://a.b/c.php and/or a/b app/Services file/child';
  const masked = [
    { token: '@scope/pkg', kind: 'package' }, { token: 'https://a.b/c.php', kind: 'endpoint' },
  ];
  assert.deepEqual(extractClaims(text), masked, 'without a root no extensionless path is inferred');
  assert.deepEqual(extractClaims(text, { projectDir }), [
    ...masked, { token: 'app/Services', kind: 'path' },
  ], 'a top-level file, unknown folder and slash word are not paths');
});

/** @param {number} lines @param {number} count */
async function buildBrief(lines, count) {
  const root = freshDir('issue29-brief');
  const briefPath = writeIn(root, 'brief.md', [
    Array.from({ length: count }, (_, i) => `./item${i}`).join(' '),
    ...Array.from({ length: lines - 1 }, () => 'plain prose'),
  ].join('\n'));
  const { deps, stderr } = fakeDeps({ FAKE_ANSWER: JSON.stringify({ facts: [] }) });
  const result = await buildFacts({
    cfg: cfgWith({ provider: 'anthropic', model: 'fake-L0' }),
    briefPath, outPath: path.join(root, 'sheet.md'), runRoot: root,
  }, deps);
  return { result, stderr: stderr.text() };
}

test('issue29: a 50-line brief with two claims prints exactly one warning and still succeeds', async () => {
  const { result, stderr } = await buildBrief(50, 2);
  assert.equal(result.status, 'ok');
  assert.equal(result.claims.length, 2);
  assert.equal(result.facts.length, 2);
  assert.deepEqual(stderr.split('\n').filter((line) => line.startsWith('forge facts:')), [
    'forge facts: the brief has 50 lines but only 2 claims; the sheet checks almost nothing',
  ]);
});

test('issue29: warning thresholds are strictly over 40 lines and strictly under five claims', async () => {
  for (const [lines, count, warned] of [[41, 4, true], [40, 2, false], [50, 5, false]]) {
    const { result, stderr } = await buildBrief(Number(lines), Number(count));
    assert.equal(result.status, 'ok');
    assert.equal(result.claims.length, count);
    assert.deepEqual(stderr.split('\n').filter((line) => line.startsWith('forge facts:')), warned
      ? [`forge facts: the brief has ${lines} lines but only ${count} claims; the sheet checks almost nothing`]
      : [], `${lines} lines and ${count} claims`);
  }
});
