import assert from 'node:assert/strict';
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';
import { runProof } from '../../src/cli/proof.mjs';
import { blockTier, proofRequirements, tierFor } from '../../src/proof/tiers.mjs';
import { CONFIG_YAML, captureStream, tempDir } from './helpers.mjs';

const HIGH_PATHS = ['app/Billing/**', 'config/auth.php'];

/** §7.1: high ⇐ risk ≥ 2 | high path | security_sensitive; else light (risk 1 stays light). */
const TABLE = [
  { input: { file: 'src/a.mjs', risk: 0 }, expected: { tier: 'light', reason: 'default' } },
  { input: { file: 'src/a.mjs', risk: 1 }, expected: { tier: 'light', reason: 'default' } },
  { input: { file: 'src/a.mjs', risk: 2 }, expected: { tier: 'high', reason: 'risk' } },
  { input: { file: 'src/a.mjs', risk: 3 }, expected: { tier: 'high', reason: 'risk' } },
  { input: { file: 'app/Billing/Invoice.php', risk: 0 }, expected: { tier: 'high', reason: 'path' } },
  { input: { file: 'src/a.mjs', risk: 0, securitySensitive: true }, expected: { tier: 'high', reason: 'security' } },
];

test('tier table: 6 cases over exactly two tiers', () => {
  assert.equal(TABLE.length, 6);
  const got = TABLE.map(({ input }) => tierFor({ ...input, highPaths: HIGH_PATHS }));
  assert.deepEqual(got, TABLE.map((row) => row.expected));
  assert.deepEqual([...new Set(got.map((g) => g.tier))].sort(), ['high', 'light']);
});

test('boundaries: risk 1.99 and a near-miss path stay light; risk outside 0–3 is refused', () => {
  assert.deepEqual(tierFor({ file: 'src/a.mjs', risk: 1.99, highPaths: HIGH_PATHS }), { tier: 'light', reason: 'default' });
  assert.deepEqual(tierFor({ file: 'app/BillingX/Invoice.php', risk: 0, highPaths: HIGH_PATHS }), { tier: 'light', reason: 'default' });
  assert.throws(() => tierFor({ file: 'src/a.mjs', risk: 4 }), { name: 'TypeError', message: 'tierFor: risk must be a number from 0 to 3' });
});

test('block tier is the union of file tiers; high mutants are held (Q16)', () => {
  assert.equal(blockTier([{ tier: 'light' }, { tier: 'high' }]), 'high');
  assert.equal(blockTier([{ tier: 'light' }, { tier: 'light' }]), 'light');
  assert.deepEqual(proofRequirements('high'), { red_green: true, mutants: 'held' });
  assert.deepEqual(proofRequirements('light'), { red_green: true, mutants: 'none' });
});

test('proof tier refuses (exit 1) a .code-forge.yml that exists but is invalid, naming the key path only', async () => {
  const dir = tempDir('bad-config');
  writeFileSync(path.join(dir, '.code-forge.yml'), `${CONFIG_YAML}  isolation: sometimes-FAKE\n`);
  const stdout = captureStream();
  const stderr = captureStream();
  const code = await runProof(['tier', '--file', 'src/a.mjs', '--risk', '0', '--cwd', dir], { stdout, stderr });

  assert.deepEqual([code, stdout.text], [1, '']);
  assert.equal(stderr.text, 'proof tier: .code-forge.yml is invalid (1 error(s) at /proof/isolation) — run code-forge validate\n');
  assert.equal(stderr.text.includes('sometimes-FAKE'), false);
});

test('proof tier with no .code-forge.yml uses the defaults (exit 0)', async () => {
  const dir = tempDir('no-config');
  const stdout = captureStream();
  const code = await runProof(['tier', '--file', 'src/a.mjs', '--risk', '0', '--cwd', dir], { stdout, stderr: captureStream() });
  assert.deepEqual([code, JSON.parse(stdout.text)], [0, { file: 'src/a.mjs', tier: 'light', reason: 'default' }]);
});
