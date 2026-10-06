import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { currentRunRoot } from '../../../src/util/tmp.mjs';
import { test } from 'node:test';
import { CODER_ONLY_FORBIDDEN, FORBIDDEN, mergeForbidden, renderForClaude, renderForCodex, renderForGrok } from '../../../src/util/forbidden.mjs';
import { buildClaudeArgv } from '../../../src/engines/builders/claude.mjs';
import { buildCodexArgv, VALID_EFFORTS as CODEX_VALID_EFFORTS } from '../../../src/engines/builders/codex.mjs';
import { CODEX_CLOSED_BOOK_REFUSAL } from '../../../src/config/closed-book.mjs';
import { renderCodexRules } from '../../../src/engines/codex-home.mjs';
import { buildGrokArgv } from '../../../src/engines/builders/grok.mjs';
import { VALID_ROLES } from '../../../src/engines/builders/validate-params.mjs';
import { idsMissingFromArgv, valuesAfterFlag, valuesAfterRepeatedFlag } from './argv-check.mjs';

/**
 * These snapshot tests deliberately do NOT `assert.deepEqual` the whole argv against one giant
 * hand-typed array for the forbidden-rules segment: `renderForClaude`/`renderForGrok` pin the SET
 * of rule strings per entry (B0's own test asserts via `sorted(...)`), not their internal ORDER —
 * a test asserting exact order there would be pinned to an implementation detail B0 itself does
 * not guarantee. Instead: the FIXED, order-guaranteed portions of argv are checked with exact
 * `deepEqual` slices; the forbidden-rules segment is checked against the argv VALUES actually
 * attached to the relevant flag (via `valuesAfterFlag`/`valuesAfterRepeatedFlag`, fix round 1 —
 * never the whole raw argv, and never only the renderer's own output length).
 */

const SAMPLE_SCHEMA = Object.freeze({ type: 'object', properties: { ok: { type: 'boolean' } }, required: ['ok'] });

// ── 1. Claude — coder ───────────────────────────────────────────────────────

test('Claude coder argv: fixed prefix, permission/output/session flags, no --max-turns, "--" then the prompt last', () => {
  const built = buildClaudeArgv({
    role: 'coder',
    model: 'claude-opus-5-5',
    effort: 'high',
    promptPath: '/tmp/brief.md',
    cwd: '/work/project',
    fallback: [
      { provider: 'anthropic', model: 'claude-sonnet-5' },
      { provider: 'anthropic', model: 'claude-haiku-4-5-20251001' }, // a SECOND same-provider entry: only the first is used
      { provider: 'openai', model: 'gpt-6-astra' }, // different provider: must be excluded
    ],
    maxBudgetUsd: 5,
  });
  assert.equal(built.cli, 'claude');
  assert.equal(built.role, 'coder');
  assert.deepEqual(
    built.argv.slice(0, 9),
    ['claude', '-p', '--model', 'claude-opus-5-5', '--effort', 'high', '--permission-mode', 'bypassPermissions', '--output-format'],
  );
  assert.deepEqual(built.argv.slice(9, 13), ['json', '--no-session-persistence', '--max-budget-usd', '5']);
  assert.equal(built.argv.includes('--max-turns'), false);
  // --fallback-model takes exactly ONE model (fix round 1, MAJOR): the FIRST same-provider entry
  // only — the second same-provider entry and the other-provider entry are both excluded.
  const fallbackIndex = built.argv.indexOf('--fallback-model');
  assert.ok(fallbackIndex >= 0, '--fallback-model must be present');
  assert.equal(built.argv[fallbackIndex + 1], 'claude-sonnet-5');
  assert.equal(built.argv.includes('claude-haiku-4-5-20251001'), false);
  assert.equal(built.argv.includes('gpt-6-astra'), false);
  // BLOCKER fix: "--" then the prompt, always the last two tokens.
  assert.equal(built.argv.at(-2), '--');
  assert.equal(built.argv.at(-1), '/tmp/brief.md');
});

test('Claude coder argv WITHOUT any same-provider fallback: no --fallback-model flag at all', () => {
  const built = buildClaudeArgv({
    role: 'coder',
    model: 'claude-opus-5-5',
    promptPath: '/p',
    cwd: '/c',
    fallback: [{ provider: 'openai', model: 'gpt-6-astra' }],
  });
  assert.equal(built.argv.includes('--fallback-model'), false);
  // Fix round 3 (MINOR): the BLOCKER scenario itself — nothing but `--` separates the variadic
  // --disallowedTools list from the prompt, so the token before `--` is the LAST rule string.
  assert.equal(built.argv.at(-2), '--');
  assert.equal(built.argv.at(-1), '/p');
  const lastRule = renderForClaude(mergeForbidden()).flatMap((e) => e.rules).at(-1); // B4.2: the coder renders the merged list
  assert.equal(built.argv.at(-3), lastRule);
  assert.equal(built.argv.filter((t) => t === '/p').length, 1);
});

test('Claude coder skips a same-provider fallback whose model EQUALS the main --model (Claude Code refuses --fallback-model == --model)', () => {
  const built = buildClaudeArgv({
    role: 'coder',
    model: 'claude-opus-5-5',
    promptPath: '/p',
    cwd: '/c',
    fallback: [
      { provider: 'anthropic', model: 'claude-opus-5-5' },
      { provider: 'anthropic', model: 'claude-sonnet-5' },
    ],
  });
  const fallbackIndex = built.argv.indexOf('--fallback-model');
  assert.ok(fallbackIndex >= 0, '--fallback-model must be present');
  assert.equal(built.argv[fallbackIndex + 1], 'claude-sonnet-5');
  assert.equal(built.argv.filter((t) => t === 'claude-opus-5-5').length, 1); // only the --model value
  const onlySame = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', fallback: [{ provider: 'anthropic', model: 'claude-opus-5-5' }] });
  assert.equal(onlySame.argv.includes('--fallback-model'), false);
});

test('Claude coder refuses a malformed fallback list with a clear TypeError naming the index (null entry, non-string model, empty provider, non-array)', () => {
  const cases = [
    [[null], 'buildClaudeArgv: fallback[0] must be an object with non-empty string provider and model'],
    [[{ provider: 'anthropic', model: 'claude-sonnet-5' }, { provider: 'anthropic', model: 42 }], 'buildClaudeArgv: fallback[1] must be an object with non-empty string provider and model'],
    [[{ provider: '', model: 'claude-sonnet-5' }], 'buildClaudeArgv: fallback[0] must be an object with non-empty string provider and model'],
    ['claude-sonnet-5', 'buildClaudeArgv: fallback must be an array when given'],
  ];
  for (const [fallback, message] of cases) {
    assert.throws(
      () => buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', fallback: /** @type {any} */ (fallback) }),
      { name: 'TypeError', message },
    );
  }
});

test('Claude coder: EVERY forbidden entry is represented in --disallowedTools, exact rule-string count and set (not just "renderer length")', () => {
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  // B4.2: the coder's list is mergeForbidden() — FORBIDDEN + the 3 coder-only entries (B45: + autopilot).
  const fresh = renderForClaude(mergeForbidden());
  assert.equal(fresh.length, FORBIDDEN.length + CODER_ONLY_FORBIDDEN.length); // "count = list length"
  assert.equal(CODER_ONLY_FORBIDDEN.length, 3);
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  const expectedRuleStrings = fresh.flatMap((e) => e.rules);
  assert.equal(attached.length, expectedRuleStrings.length); // exact count IN ARGV, not just the renderer's own output
  assert.deepEqual([...attached].sort(), [...expectedRuleStrings].sort());
  assert.deepEqual(idsMissingFromArgv(fresh, attached), []);
});

test('B4.2 Claude coder: --disallowedTools carries the 4 `block waive` deny rules; the `contains` --no-require-reviews entry renders none; facts (closed-book) is unchanged', () => {
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  const waive = attached.filter((r) => r.includes('block waive'));
  assert.deepEqual(waive, [
    'Bash(code-forge block waive:*)',
    'Bash(npx code-forge block waive:*)',
    'Bash(npx @codedology/code-forge block waive:*)',
    'Bash(forge block waive:*)',
  ]);
  assert.equal(attached.filter((r) => r.includes('--no-require-reviews')).length, 0);
  const facts = buildClaudeArgv({ role: 'facts', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  assert.equal(facts.argv.filter((t) => t.includes('block waive')).length, 0);
});

test('Claude coder, using the REAL landed FORBIDDEN (no override): the ★ path entries render as Read(…)/Edit(…)/Write(…) (3 asserts + the exact real set)', () => {
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  assert.equal(attached.some((r) => r.startsWith('Read(')), true, 'expected at least one Read(...) rule');
  assert.equal(attached.some((r) => r.startsWith('Edit(')), true, 'expected at least one Edit(...) rule');
  assert.equal(attached.some((r) => r.startsWith('Write(')), true, 'expected at least one Write(...) rule');
  // Hand-derived independently from FORBIDDEN's two `path`-kind entries (§8.4, B8's landed code):
  // `code-forge-runs-access` (readwrite, ~/.code-forge/runs) and `code-forge-reviews-write`
  // (write-only, .code-forge/reviews + .code-forge/queue/*.done).
  for (const rule of [
    'Read(~/.code-forge/runs/**)',
    'Edit(~/.code-forge/runs/**)',
    'Write(~/.code-forge/runs/**)',
    'Edit(.code-forge/reviews/**)',
    'Write(.code-forge/reviews/**)',
    'Edit(.code-forge/queue/*.done)',
    'Write(.code-forge/queue/*.done)',
  ]) {
    assert.equal(attached.includes(rule), true, `missing real ★ path rule: ${rule}`);
  }
});

test('Claude coder throws on an EMPTY forbidden render (never a dangling --disallowedTools flag)', () => {
  assert.throws(
    () => buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', renderedForbidden: [] }),
    /requires a non-empty forbidden-list render/,
  );
});

test('Claude coder rejects an invalid maxBudgetUsd (NaN, negative, zero, non-number)', () => {
  for (const bad of [NaN, -1, 0, '5', null]) {
    assert.throws(
      () => buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', maxBudgetUsd: /** @type {any} */ (bad) }),
      TypeError,
      `maxBudgetUsd ${JSON.stringify(bad)} should have been rejected`,
    );
  }
});

// ── 2. Claude — closed-book (reviewer/judge/s2/author) ──────────────────────

test('Claude closed-book argv: --safe-mode, --tools "", --strict-mcp-config, --no-session-persistence (4 asserts), no --max-turns', () => {
  const built = buildClaudeArgv({
    role: 'reviewer',
    model: 'claude-opus-5-5',
    effort: 'medium',
    promptPath: '/tmp/packet.json',
    cwd: '/tmp/iso-1',
    schema: SAMPLE_SCHEMA,
    systemPromptText: 'You are the full lens.',
    maxBudgetUsd: 2,
  });
  assert.equal(built.role, 'reviewer');
  assert.equal(built.argv.includes('--safe-mode'), true);
  const toolsIndex = built.argv.indexOf('--tools');
  assert.ok(toolsIndex >= 0 && built.argv[toolsIndex + 1] === '', '--tools "" must appear as an adjacent flag/empty-value pair');
  assert.equal(built.argv.includes('--strict-mcp-config'), true);
  assert.equal(built.argv.includes('--no-session-persistence'), true);
  assert.equal(built.argv.includes('--max-turns'), false);
  const schemaIndex = built.argv.indexOf('--json-schema');
  assert.ok(schemaIndex >= 0);
  assert.deepEqual(JSON.parse(built.argv[schemaIndex + 1]), SAMPLE_SCHEMA);
  assert.equal(built.argv.includes('dontAsk'), true);
  assert.equal(built.argv.includes('--disallowedTools'), false);
  assert.equal(built.argv.includes('--restricted'), false);
  // Fix round 3 (root ruling): closed-book gets the packet CONTENT on stdin, never a pointer it
  // cannot open with `--tools ""` — no prompt positional, no `--`, `stdinFile` names the packet.
  assert.equal(built.stdinFile, '/tmp/packet.json');
  assert.equal(built.argv.includes('/tmp/packet.json'), false);
  assert.equal(built.argv.includes('--'), false);
  assert.deepEqual(built.argv.slice(-2), ['--permission-mode', 'dontAsk']);
});

// ── 3. Claude — facts ────────────────────────────────────────────────────────

test('Claude facts argv: contains --restricted and --tools "Bash" (facts-only overrides), forbiddenRendered has real content', () => {
  const built = buildClaudeArgv({ role: 'facts', model: 'claude-opus-5-5', promptPath: '/tmp/facts-packet.json', cwd: '/tmp/iso-2' });
  assert.equal(built.role, 'facts');
  assert.equal(built.argv.includes('--restricted'), true);
  const toolsIndex = built.argv.indexOf('--tools');
  assert.ok(toolsIndex >= 0 && built.argv[toolsIndex + 1] === 'Bash', '--tools "Bash" must appear for facts');
  assert.equal(built.argv.includes('--max-turns'), false);
  // `forbiddenRendered` content, not just its presence (fix round 1, MINOR): deep-equal to the
  // fresh render this call actually used (the default, since no override was given).
  assert.deepEqual(built.forbiddenRendered, renderForClaude(FORBIDDEN));
  const fresh = renderForClaude(FORBIDDEN);
  const attached = valuesAfterFlag(built.argv, '--disallowedTools');
  assert.deepEqual(idsMissingFromArgv(fresh, attached), []);
  assert.equal(built.stdinFile, '/tmp/facts-packet.json');
  assert.equal(built.argv.includes('/tmp/facts-packet.json'), false);
  assert.equal(built.argv.at(-1), '--restricted');
});

test('Claude facts throws on an EMPTY forbidden render (a facts delegate with Bash must carry the list)', () => {
  assert.throws(
    () => buildClaudeArgv({ role: 'facts', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c', renderedForbidden: [] }),
    { name: 'Error', message: 'buildClaudeArgv: facts role requires a non-empty forbidden-list render (got 0 usable rules)' },
  );
});

// ── 4. Codex — coder ─────────────────────────────────────────────────────────

test('Codex coder argv: exec + workspace-write + approval_policy="never" (no --approve-for-me, B4.2), -o output NEVER inside cwd, rules file EXACTLY matches renderForCodex(mergeForbidden())', () => {
  const built = buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', effort: 'high', promptPath: '/tmp/brief.md', cwd: '/work/project' });
  assert.equal(built.cli, 'codex');
  assert.deepEqual(built.argv.slice(0, 6), ['codex', 'exec', '-m', 'gpt-6-astra', '-c', 'model_reasoning_effort=high']);
  assert.deepEqual(built.argv.slice(6, 15), [
    '-s', 'workspace-write',
    '-c', 'sandbox_workspace_write.exclude_tmpdir_env_var=true',
    '-c', 'sandbox_workspace_write.exclude_slash_tmp=true',
    '-c', 'approval_policy="never"',
    '-C',
  ]);
  assert.equal(built.argv[15], '/work/project');
  assert.equal(built.argv.includes('--approve-for-me'), false); // B4.2: Codex 0.155.1 refuses it next to -s
  assert.equal(built.argv.length, 20);
  assert.deepEqual([built.argv[16], built.argv[17]], ['--json', '-o']);
  assert.equal(built.argv.includes('--json'), true);
  assert.equal(built.argv.at(-1), '/tmp/brief.md');
  assert.equal(built.argv.includes('read-only'), false);
  // fix round 1, MINOR: the default -o path must never land inside cwd.
  const outIndex = built.argv.indexOf('-o');
  assert.ok(outIndex >= 0);
  assert.equal(built.argv[outIndex + 1].startsWith('/work/project'), false, `-o path must not be inside cwd: ${built.argv[outIndex + 1]}`);
  // Fix round 3 (MAJOR): the effective default -o path is RETURNED, so the caller can read it.
  assert.equal(built.outPath, built.argv[outIndex + 1]);
  assert.equal(built.argv.filter((t) => t === '-o').length, 1);
  assert.equal('stdinFile' in built, false); // coder keeps the pointer
  // B4.1: the rules reach Codex through env CODEX_HOME=<per-session home>/rules/code-forge.rules.
  const coder = /** @type {{env: {CODEX_HOME: string}, rulesFile: {path: string, content: string, count: number}}} */ (built);
  assert.deepEqual(Object.keys(coder.env), ['CODEX_HOME']);
  // fix round 1: the home is under the run temp root, never the real ~/.codex, fresh per build.
  assert.equal(path.relative(currentRunRoot(), coder.env.CODEX_HOME).startsWith('..'), false, coder.env.CODEX_HOME);
  const realCodex = path.join(os.homedir(), '.codex');
  assert.notEqual(coder.env.CODEX_HOME, realCodex);
  assert.equal(path.relative(realCodex, coder.env.CODEX_HOME).startsWith('..'), true);
  const again = /** @type {any} */ (buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/tmp/brief.md', cwd: '/work/project' }));
  assert.notEqual(again.env.CODEX_HOME, coder.env.CODEX_HOME);
  assert.equal(path.dirname(again.env.CODEX_HOME), path.dirname(coder.env.CODEX_HOME));
  assert.equal(coder.rulesFile.path, path.join(coder.env.CODEX_HOME, 'rules', 'code-forge.rules'));
  assert.equal(readFileSync(coder.rulesFile.path, 'utf8'), coder.rulesFile.content);
  const expected = renderForCodex(mergeForbidden()).flatMap((e) => e.patterns.map((p) => `prefix_rule(pattern=${JSON.stringify(p).replaceAll(',', ', ')}, decision="forbidden", justification="code-forge: ${e.id}")`));
  const ruleLines = coder.rulesFile.content.split('\n').filter((l) => l.startsWith('prefix_rule('));
  // B4.2: FORBIDDEN's rules + the 4 `block waive` and (B45) 4 `autopilot` patterns (the `contains` entry renders none).
  const expectedCount = renderCodexRules(renderForCodex(FORBIDDEN)).count + renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count;
  assert.equal(renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count, 8);
  assert.equal(ruleLines.length, expectedCount);
  assert.equal(coder.rulesFile.count, expectedCount);
  assert.deepEqual(ruleLines, expected);
  assert.equal(ruleLines.filter((l) => l.startsWith('prefix_rule(pattern=["code-forge", "block", "waive"], decision="forbidden"')).length, 1);
});

test('Codex coder throws on an EMPTY forbidden render', () => {
  assert.throws(
    () => buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', renderedForbidden: [] }),
    /requires a non-empty forbidden-list render/,
  );
});

test('Codex rejects an effort outside VALID_EFFORTS', () => {
  assert.deepEqual([...CODEX_VALID_EFFORTS], ['minimal', 'low', 'medium', 'high']);
  assert.throws(
    () => buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', effort: 'xhigh' }),
    /effort must be one of/,
  );
  // A valid one must NOT throw.
  assert.doesNotThrow(() => buildCodexArgv({ role: 'coder', model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', effort: 'high' }));
});

// ── 5. Codex — no-tools roles are refused (B32, issue #2) ──────────────────────
// Codex 0.155.1 has no mode without a shell (`-s read-only` still reads any absolute path), so
// reviewer/judge/s2/author are never built; facts (read-only tools by design) keeps its argv.

const CODEX_REFUSAL = 'codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author';

test('B32 Codex refuses each of the 4 no-tools roles with the exact refusal (code closed-book); facts and coder still build', () => {
  assert.equal(CODEX_CLOSED_BOOK_REFUSAL, CODEX_REFUSAL);
  const refused = [];
  for (const role of VALID_ROLES) {
    try {
      buildCodexArgv({ role, model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', outPath: '/tmp/o.json' });
    } catch (err) {
      assert.equal(/** @type {any} */ (err).message, CODEX_REFUSAL, role);
      assert.equal(/** @type {any} */ (err).code, 'closed-book', role);
      refused.push(role);
    }
  }
  assert.deepEqual(refused, ['reviewer', 'judge', 's2', 'author']);
});

test('Codex facts argv: the exact closed-book token list (read-only, ephemeral, ignore-rules, ignore-user-config, schema, -o, stdin)', () => {
  const built = buildCodexArgv({ role: 'facts', model: 'gpt-6-astra', effort: 'high', promptPath: '/tmp/packet.json', cwd: '/tmp/iso-3', outPath: '/tmp/out-3.json', schemaPath: '/tmp/schema-3.json' });
  assert.equal(built.role, 'facts');
  assert.deepEqual(built.argv, [
    'codex', 'exec', '-m', 'gpt-6-astra', '-c', 'model_reasoning_effort=high',
    '-s', 'read-only', '--ephemeral', '--ignore-rules', '--ignore-user-config',
    '-C', '/tmp/iso-3', '--skip-git-repo-check', '--output-schema', '/tmp/schema-3.json',
    '-o', '/tmp/out-3.json', '--json', '-',
  ]);
  assert.equal(built.stdinFile, '/tmp/packet.json');
  assert.equal(built.outPath, '/tmp/out-3.json');
});

test('B32 opt-in: allowOpenBook: true builds each of the 4 no-tools roles with the exact read-only argv (the facts shape); false still refuses', () => {
  for (const role of /** @type {const} */ (['reviewer', 'judge', 's2', 'author'])) {
    const built = buildCodexArgv({ role, model: 'gpt-6-sol', promptPath: '/tmp/packet.json', cwd: '/tmp/iso-5', outPath: '/tmp/out-5.json', allowOpenBook: true });
    assert.equal(built.role, role);
    assert.deepEqual(built.argv, [
      'codex', 'exec', '-m', 'gpt-6-sol', '-s', 'read-only', '--ephemeral', '--ignore-rules', '--ignore-user-config',
      '-C', '/tmp/iso-5', '--skip-git-repo-check', '-o', '/tmp/out-5.json', '--json', '-',
    ], role);
    assert.equal(built.stdinFile, '/tmp/packet.json', role);
    assert.throws(() => buildCodexArgv({ role, model: 'gpt-6-sol', promptPath: '/p', cwd: '/c', allowOpenBook: false }), { message: CODEX_REFUSAL });
  }
});

test('Codex facts: the default -o lands outside cwd; a caller-given outPath is used verbatim (coder too); an empty/non-string one is refused', () => {
  const dflt = buildCodexArgv({ role: 'facts', model: 'gpt-6-astra', promptPath: '/p', cwd: '/tmp/iso-4' });
  assert.equal(dflt.argv[dflt.argv.indexOf('-o') + 1].startsWith('/tmp/iso-4'), false);
  assert.equal(dflt.outPath, dflt.argv[dflt.argv.indexOf('-o') + 1]);
  for (const role of /** @type {const} */ (['coder', 'facts'])) {
    const built = buildCodexArgv({ role, model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', outPath: '/tmp/caller-out.json' });
    assert.equal(built.outPath, '/tmp/caller-out.json', role);
    assert.equal(built.argv[built.argv.indexOf('-o') + 1], '/tmp/caller-out.json', role);
    for (const bad of ['', 42, null]) {
      assert.throws(
        () => buildCodexArgv({ role, model: 'gpt-6-astra', promptPath: '/p', cwd: '/c', outPath: /** @type {any} */ (bad) }),
        { name: 'TypeError', message: 'buildCodexArgv: outPath must be a non-empty string when given' },
      );
    }
  }
});

// ── 7. Grok — coder ───────────────────────────────────────────────────────────

test('Grok coder argv: --prompt-file, no -p, every --deny pair matches a real rendered rule, exact count', () => {
  const built = buildGrokArgv({ role: 'coder', model: 'grok-4.7', effort: 'high', promptPath: '/tmp/brief.md', cwd: '/work/project' });
  assert.equal(built.cli, 'grok');
  assert.deepEqual(built.argv.slice(0, 7), ['grok', '--prompt-file', '/tmp/brief.md', '-m', 'grok-4.7', '--reasoning-effort', 'high']);
  assert.deepEqual(built.argv.slice(7, 11), ['--permission-mode', 'bypassPermissions', '--cwd', '/work/project']);
  assert.equal(built.argv.includes('-p'), false);
  const fresh = renderForGrok(mergeForbidden()); // B4.2: the coder renders the merged list
  const attached = valuesAfterRepeatedFlag(built.argv, '--deny');
  assert.equal(attached.includes('Bash(code-forge block waive:*)'), true);
  const expectedRuleStrings = fresh.flatMap((e) => e.rules);
  assert.equal(attached.length, expectedRuleStrings.length); // exact count, not just "> 0"
  assert.deepEqual([...attached].sort(), [...expectedRuleStrings].sort());
  assert.deepEqual(idsMissingFromArgv(fresh, attached), []);
  // number of --deny FLAG occurrences itself equals the number of rendered rule strings (1:1 pairing)
  const denyFlagCount = built.argv.filter((t) => t === '--deny').length;
  assert.equal(denyFlagCount, expectedRuleStrings.length);
});

test('Grok coder throws on an EMPTY forbidden render', () => {
  assert.throws(
    () => buildGrokArgv({ role: 'coder', model: 'grok-4.7', promptPath: '/p', cwd: '/c', renderedForbidden: [] }),
    /requires a non-empty forbidden-list render/,
  );
});

test('Grok rejects a non-string / empty effort', () => {
  for (const bad of [42, '', null]) {
    assert.throws(
      () => buildGrokArgv({ role: 'coder', model: 'grok-4.7', promptPath: '/p', cwd: '/c', effort: /** @type {any} */ (bad) }),
      TypeError,
    );
  }
});

// ── 8. Grok — closed-book ─────────────────────────────────────────────────────

test('Grok closed-book argv: --deny "*", --no-plan, --no-subagents, --max-turns 1, --permission-mode dontAsk', () => {
  const built = buildGrokArgv({
    role: 's2',
    model: 'grok-4.7',
    promptPath: '/tmp/packet.json',
    cwd: '/tmp/iso-4',
    schema: SAMPLE_SCHEMA,
    systemPromptText: 'lens preamble',
  });
  assert.equal(built.role, 's2');
  const denyIndex = built.argv.indexOf('--deny');
  assert.equal(built.argv[denyIndex + 1], '*');
  assert.equal(built.argv.filter((t) => t === '--deny').length, 1); // exactly one wildcard deny, not repeated
  assert.deepEqual(built.argv.slice(built.argv.indexOf('--no-plan'), built.argv.indexOf('--no-plan') + 5), [
    '--no-plan',
    '--no-subagents',
    '--max-turns',
    '1',
    '--permission-mode',
  ]);
  assert.equal(built.argv[built.argv.indexOf('--permission-mode') + 1], 'dontAsk');
  const schemaIndex = built.argv.indexOf('--json-schema');
  assert.deepEqual(JSON.parse(built.argv[schemaIndex + 1]), SAMPLE_SCHEMA);
  const overrideIndex = built.argv.indexOf('--system-prompt-override');
  assert.equal(built.argv[overrideIndex + 1], 'lens preamble');
});

// ── 9. Grok — facts (same shape as closed-book, pinned to closed-book markers) ────────────

test('Grok facts argv carries the CLOSED-BOOK markers (--deny "*", --max-turns 1, no coder-shape --deny pairs)', () => {
  const built = buildGrokArgv({ role: 'facts', model: 'grok-4.7', promptPath: '/p', cwd: '/c' });
  assert.equal(built.role, 'facts');
  assert.equal(built.argv.filter((t) => t === '--deny').length, 1);
  assert.equal(built.argv[built.argv.indexOf('--deny') + 1], '*');
  assert.equal(built.argv.includes('--max-turns'), true);
  assert.equal(built.argv.includes('--prompt-file'), true);
});

test('Grok facts argv is the same closed-book shape as reviewer/judge/s2/author byte-for-byte', () => {
  const author = buildGrokArgv({ role: 'author', model: 'grok-4.7', promptPath: '/p', cwd: '/c' });
  const facts = buildGrokArgv({ role: 'facts', model: 'grok-4.7', promptPath: '/p', cwd: '/c' });
  assert.deepEqual(facts.argv, author.argv);
});

// ── Cross-cutting: required-argument and role guards (fix round 1, MAJOR ×3) ─────────────

test('every builder refuses a missing role, model, promptPath or cwd', () => {
  for (const build of [buildClaudeArgv, buildCodexArgv, buildGrokArgv]) {
    assert.throws(() => build(/** @type {any} */ ({ model: 'x', promptPath: '/p', cwd: '/c' })), TypeError);
    assert.throws(() => build(/** @type {any} */ ({ role: 'coder', promptPath: '/p', cwd: '/c' })), TypeError);
    assert.throws(() => build(/** @type {any} */ ({ role: 'coder', model: 'x', cwd: '/c' })), TypeError);
    assert.throws(() => build(/** @type {any} */ ({ role: 'coder', model: 'x', promptPath: '/p' })), TypeError);
    assert.throws(() => build(/** @type {any} */ ({ role: 'coder', model: 'x', promptPath: '/p', cwd: '' })), TypeError);
  }
});

test('every builder throws (never silently falls back to closed-book) for an UNKNOWN role — a typo, a level name, a number', () => {
  assert.deepEqual([...VALID_ROLES], ['coder', 'reviewer', 'judge', 's2', 'author', 'facts']);
  for (const build of [buildClaudeArgv, buildCodexArgv, buildGrokArgv]) {
    for (const badRole of ['reviwer', 'L2', 'Coder', '', 42, null, undefined]) {
      assert.throws(
        () => build(/** @type {any} */ ({ role: badRole, model: 'x', promptPath: '/p', cwd: '/c' })),
        TypeError,
        `role ${JSON.stringify(badRole)} should have been refused by ${build.name}`,
      );
    }
  }
});

test('every valid role is actually accepted (the guard is not accidentally over-strict)', () => {
  for (const role of VALID_ROLES) {
    assert.doesNotThrow(() => buildClaudeArgv({ role, model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' }), `claude role ${role}`);
    // B32: Codex accepts coder and facts; the 4 no-tools roles are refused (see section 5).
    if (role === 'coder' || role === 'facts') assert.doesNotThrow(() => buildCodexArgv({ role, model: 'gpt-6-astra', promptPath: '/p', cwd: '/c' }), `codex role ${role}`);
    assert.doesNotThrow(() => buildGrokArgv({ role, model: 'grok-4.7', promptPath: '/p', cwd: '/c' }), `grok role ${role}`);
  }
});

// ── Prompt delivery per role (fix round 3, root ruling on MAJOR claude.mjs:184) ─────────────
// Closed-book roles cannot open a path, so they receive the packet CONTENT on stdin; coders keep
// a pointer. Expected values are a hand-written literal table, one row per provider × role.

/** @type {ReadonlyArray<[string, string, {tail: string[], stdinFile: string | undefined, promptPathInArgv: number}]>} */
const DELIVERY_TABLE = [
  ['claude', 'coder', { tail: ['--', '/packet.md'], stdinFile: undefined, promptPathInArgv: 1 }],
  ['claude', 'reviewer', { tail: ['--permission-mode', 'dontAsk'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['claude', 'judge', { tail: ['--permission-mode', 'dontAsk'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['claude', 's2', { tail: ['--permission-mode', 'dontAsk'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['claude', 'author', { tail: ['--permission-mode', 'dontAsk'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['claude', 'facts', { tail: ['Bash(cp*)', '--restricted'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['codex', 'coder', { tail: ['/tmp/o.json', '/packet.md'], stdinFile: undefined, promptPathInArgv: 1 }],
  ['codex', 'facts', { tail: ['--json', '-'], stdinFile: '/packet.md', promptPathInArgv: 0 }],
  ['grok', 'coder', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
  ['grok', 'reviewer', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
  ['grok', 'judge', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
  ['grok', 's2', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
  ['grok', 'author', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
  ['grok', 'facts', { tail: [], stdinFile: undefined, promptPathInArgv: 1 }],
];

test('prompt delivery table: 14 rows (3 providers × 6 roles, minus the 4 Codex no-tools roles B32 refuses) — argv tail, stdinFile, and how many times the packet path appears in argv', () => {
  assert.equal(DELIVERY_TABLE.length, 14);
  const builders = { claude: buildClaudeArgv, codex: buildCodexArgv, grok: buildGrokArgv };
  const models = { claude: 'claude-opus-5-5', codex: 'gpt-6-astra', grok: 'grok-4.7' };
  for (const [cli, role, expected] of DELIVERY_TABLE) {
    const built = builders[cli]({ role, model: models[cli], promptPath: '/packet.md', cwd: '/iso', outPath: '/tmp/o.json' });
    const label = `${cli}/${role}`;
    if (expected.tail.length > 0) assert.deepEqual(built.argv.slice(-expected.tail.length), expected.tail, label);
    assert.equal(built.stdinFile, expected.stdinFile, label);
    assert.equal(Object.hasOwn(built, 'stdinFile'), expected.stdinFile !== undefined, label);
    assert.equal(built.argv.filter((t) => t === '/packet.md').length, expected.promptPathInArgv, label);
    if (cli === 'grok') {
      // Grok reads the file itself: `--prompt-file <path>` right after the binary, never stdin.
      assert.deepEqual(built.argv.slice(1, 3), ['--prompt-file', '/packet.md'], label);
    }
    assert.equal(built.outPath, cli === 'codex' ? '/tmp/o.json' : undefined, label);
  }
});

test('Claude closed-book argv: a top-level $schema meta pointer is dropped from --json-schema', () => {
  const schema = { $schema: 'https://json-schema.org/draft/2020-12/schema', ...SAMPLE_SCHEMA };
  const built = buildClaudeArgv({
    role: 'reviewer',
    model: 'claude-opus-5-5',
    promptPath: '/tmp/packet.json',
    cwd: '/tmp/iso-1',
    schema,
    systemPromptText: 'You are the full lens.',
  });
  const schemaIndex = built.argv.indexOf('--json-schema');
  assert.ok(schemaIndex >= 0);
  assert.deepEqual(JSON.parse(built.argv[schemaIndex + 1]), SAMPLE_SCHEMA);
  assert.equal(schema.$schema, 'https://json-schema.org/draft/2020-12/schema');
});
