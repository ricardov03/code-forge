import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { probeHelpText, REQUIRED_FLAGS } from '../../src/engines/probe.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FIXTURES_DIR = path.join(__dirname, '..', 'fixtures', 'help');

const FIXTURES = Object.freeze({
  claude: 'claude-2.1.282.txt',
  codex: 'codex-0.155.1.txt',
  grok: 'grok-1.0.34.txt',
});

test('probeHelpText PASSES on all 3 pinned fixtures (every required flag is present, 0 missing)', async () => {
  for (const [provider, fileName] of Object.entries(FIXTURES)) {
    const text = await readFile(path.join(FIXTURES_DIR, fileName), 'utf8');
    const result = probeHelpText(/** @type {"claude"|"codex"|"grok"} */ (provider), text);
    assert.deepEqual(result.missing, [], `${provider}: unexpected missing flags`);
    assert.equal(result.ok, true, `${provider}: probe should pass`);
  }
});

// Fix round 1 (MAJOR): replaced the old `length >= 10` check — which stayed green even if a
// builder-critical flag like `--safe-mode` were deleted from REQUIRED_FLAGS entirely — with an
// EXACT pinned list per provider.
test('REQUIRED_FLAGS is EXACTLY the pinned per-provider list (deepEqual, not a length threshold)', () => {
  assert.deepEqual(
    [...REQUIRED_FLAGS.claude],
    [
      '-p, --print',
      '--model <model>',
      '--effort <level>',
      '--permission-mode <mode>',
      '--output-format <format>',
      '--no-session-persistence',
      '--max-budget-usd <amount>',
      '--disallowedTools, --disallowed-tools <tools...>',
      '--fallback-model <model>',
      '--safe-mode',
      '--tools <tools...>',
      '--strict-mcp-config',
      '--json-schema <schema>',
      '--system-prompt <prompt>',
      '--restricted',
    ],
  );
  assert.deepEqual(
    [...REQUIRED_FLAGS.codex],
    [
      '-m, --model <MODEL>',
      '-c, --config <key=value>',
      '-s, --sandbox <SANDBOX_MODE>',
      '--approve-for-me',
      '-C, --cd <DIR>',
      '--json',
      '-o, --output-last-message <FILE>',
      '--ephemeral',
      '--ignore-rules',
      '--ignore-user-config',
      '--skip-git-repo-check',
      '--output-schema <FILE>',
    ],
  );
  assert.deepEqual(
    [...REQUIRED_FLAGS.grok],
    [
      '--prompt-file <PATH>',
      '-m, --model <MODEL>',
      '--reasoning-effort <EFFORT>',
      '--permission-mode <MODE>',
      '--cwd <CWD>',
      '--deny <RULE>',
      '--json-schema <SCHEMA>',
      '--disallowed-tools <TOOLS>',
      '--no-plan',
      '--no-subagents',
      '--max-turns <N>',
      '--system-prompt-override <PROMPT>',
    ],
  );
});

test('every argv token each builder actually emits that looks like a flag name is covered by that provider\'s REQUIRED_FLAGS (no builder-critical flag can silently go unprobed)', async () => {
  const { buildClaudeArgv } = await import('../../src/engines/builders/claude.mjs');
  const { buildCodexArgv } = await import('../../src/engines/builders/codex.mjs');
  const { buildGrokArgv } = await import('../../src/engines/builders/grok.mjs');
  const { VALID_ROLES } = await import('../../src/engines/builders/validate-params.mjs');
  // Fix round 3 (MINOR): every role × every optional param filled, the UNION of flag tokens per
  // provider — a flag emitted only by one role or one optional param can no longer go unprobed.
  // Excluded: '--' (end-of-options marker) and '-' (Codex "prompt from stdin"): argv-parsing
  // conventions, not flags a --help documents. Flags are matched as whole tokens that look like a
  // flag name; values such as rule strings never start with '-'.
  const isFlag = (t) => t !== '--' && t !== '-' && /^--?[A-Za-z][\w-]*$/.test(t);
  const optional = {
    claude: { model: 'claude-opus-5-5', effort: 'high', fallback: [{ provider: 'anthropic', model: 'claude-sonnet-5' }], maxBudgetUsd: 3, schema: { type: 'object' }, systemPromptText: 'lens' },
    codex: { model: 'gpt-6-astra', effort: 'high', outPath: '/tmp/o.json', schemaPath: '/tmp/s.json' },
    grok: { model: 'grok-4.7', effort: 'high', schema: { type: 'object' }, systemPromptText: 'lens' },
  };
  const builders = { claude: buildClaudeArgv, codex: buildCodexArgv, grok: buildGrokArgv };
  const coveredByAny = (flag, provider) => REQUIRED_FLAGS[provider].some((entry) => entry.split(',').map((s) => s.trim().split(' ')[0]).includes(flag));
  /** @type {Record<string, string[]>} */
  const unions = {};
  for (const provider of /** @type {const} */ (['claude', 'codex', 'grok'])) {
    const union = new Set();
    for (const role of VALID_ROLES) {
      const built = builders[provider]({ role, promptPath: '/p', cwd: '/c', ...optional[provider] });
      for (const token of built.argv.slice(1)) if (isFlag(token)) union.add(token);
    }
    unions[provider] = [...union].sort();
    for (const flag of union) assert.equal(coveredByAny(flag, provider), true, `${provider} flag ${flag} not covered by REQUIRED_FLAGS.${provider}`);
  }
  // The unions are pinned so a builder that STOPS emitting a flag (or starts emitting a new one) is
  // noticed here too — a literal list, hand-derived from the §5.2 flag table.
  assert.deepEqual(unions.claude, [
    '--disallowedTools', '--effort', '--fallback-model', '--json-schema', '--max-budget-usd', '--model', '--no-session-persistence',
    '--output-format', '--permission-mode', '--restricted', '--safe-mode', '--strict-mcp-config', '--system-prompt', '--tools', '-p',
  ]);
  assert.deepEqual(unions.codex, [
    '--approve-for-me', '--ephemeral', '--ignore-rules', '--ignore-user-config', '--json', '--output-schema', '--skip-git-repo-check', '-C', '-c', '-m', '-o', '-s',
  ]);
  assert.deepEqual(unions.grok, [
    '--cwd', '--deny', '--disallowed-tools', '--json-schema', '--max-turns', '--no-plan', '--no-subagents', '--permission-mode', '--prompt-file',
    '--reasoning-effort', '--system-prompt-override', '-m',
  ]);
});

test('probeHelpText FAILS on a fixture missing a flag, naming exactly that flag (replaceAll: every occurrence stripped)', async () => {
  const real = await readFile(path.join(FIXTURES_DIR, FIXTURES.claude), 'utf8');
  const removedFlag = '--safe-mode';
  assert.ok(REQUIRED_FLAGS.claude.includes(removedFlag), 'precondition: the flag must be one probeHelpText actually checks');
  assert.ok(real.includes(removedFlag), 'precondition: the real fixture must contain the flag we are about to strip');
  const mutilated = real.replaceAll(removedFlag, 'XXXXXXXXXX');
  assert.equal(mutilated.includes(removedFlag), false, 'precondition: every occurrence must be gone after the mutation');
  const result = probeHelpText('claude', mutilated);
  assert.equal(result.ok, false);
  assert.deepEqual(result.missing, [removedFlag]);
});

test('probeHelpText FAILS and names MULTIPLE flags when several are stripped', async () => {
  const real = await readFile(path.join(FIXTURES_DIR, FIXTURES.codex), 'utf8');
  const toStrip = ['--ephemeral', '--ignore-rules'];
  for (const flag of toStrip) {
    assert.ok(REQUIRED_FLAGS.codex.includes(flag), `precondition: ${flag} must be checked`);
    assert.ok(real.includes(flag), `precondition: the real fixture must contain ${flag}`);
  }
  let mutilated = real;
  for (const flag of toStrip) mutilated = mutilated.replaceAll(flag, 'X'.repeat(flag.length));
  const result = probeHelpText('codex', mutilated);
  assert.equal(result.ok, false);
  assert.deepEqual(result.missing.sort(), [...toStrip].sort());
});

// Fix round 3 (MINOR): renamed from an overstated "--json missing" title (REQUIRED_FLAGS.claude
// has no '--json'); now asserts the EXACT missing list for a two-flag help text.
test('probeHelpText does not over-reject exact matches: a help text with only two Claude flags reports exactly the other 13 missing', () => {
  const fakeHelpText = ['  --json-schema <schema>   JSON Schema for structured output', '  --restricted             Restricted mode'].join('\n');
  const result = probeHelpText('claude', fakeHelpText);
  assert.equal(result.ok, false);
  assert.deepEqual(result.missing, [
    '-p, --print',
    '--model <model>',
    '--effort <level>',
    '--permission-mode <mode>',
    '--output-format <format>',
    '--no-session-persistence',
    '--max-budget-usd <amount>',
    '--disallowedTools, --disallowed-tools <tools...>',
    '--fallback-model <model>',
    '--safe-mode',
    '--tools <tools...>',
    '--strict-mcp-config',
    '--system-prompt <prompt>',
  ]);
});

test('probeHelpText word-boundary check on a Codex-shaped case: --json alone (not --json-schema) must be satisfied only by a literal standalone --json', () => {
  // No standalone "--json" substring appears anywhere else in this text (unlike an earlier draft
  // of this test, whose own description prose accidentally contained one) — the ONLY occurrence
  // is inside the longer "--json-something", which the boundary check must correctly reject.
  const withOnlyLongerFlag = '  --json-something <X>   an unrelated flag';
  const result = probeHelpText('codex', withOnlyLongerFlag);
  assert.equal(result.missing.includes('--json'), true, '--json must be reported missing when only a LONGER flag starting with it is present');

  const withRealFlag = '  --json   Print events to stdout as JSONL';
  const result2 = probeHelpText('codex', withRealFlag);
  assert.equal(result2.missing.includes('--json'), false, '--json must be found when the real, standalone flag is present');
});

// Fix round 1 (MINOR): `REQUIRED_FLAGS[provider]` also resolved prototype-chain members
// ('constructor', 'toString', '__proto__', …) instead of hitting the documented "unknown
// provider" error.
test('probeHelpText throws the documented "unknown provider" error for a prototype-chain name, never a prototype member crash', () => {
  for (const trap of ['constructor', 'toString', '__proto__', 'hasOwnProperty', 'valueOf']) {
    assert.throws(() => probeHelpText(/** @type {any} */ (trap), 'text'), /unknown provider/, `provider ${trap} should be refused`);
  }
});

test('probeHelpText throws for an ordinary unknown provider', () => {
  assert.throws(() => probeHelpText(/** @type {any} */ ('unknown-provider'), 'text'), /unknown provider/);
});

test('probeHelpText throws a TypeError for a non-string helpText', () => {
  assert.throws(() => probeHelpText('claude', /** @type {any} */ (null)), TypeError);
});
