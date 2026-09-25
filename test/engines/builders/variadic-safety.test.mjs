import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { buildClaudeArgv } from '../../../src/engines/builders/claude.mjs';

/**
 * BLOCKER regression (fix round 1): `--disallowedTools <tools...>` is VARIADIC in Claude's pinned
 * `--help`. A bare positional prompt pushed directly after it — which happened whenever there was
 * no same-provider `--fallback-model` to act as a terminator — was silently swallowed as one more
 * "disallowed tool" instead of being read as the prompt. The fix: every Claude argv this module
 * builds now ends with a literal `--` end-of-options marker immediately before the prompt. This
 * file proves it with an INDEPENDENT simulation of how a commander-style CLI parses argv — built
 * directly from the pinned fixture text, not from any assumption baked into the builder itself.
 */

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const CLAUDE_FIXTURE = path.join(__dirname, '..', '..', 'fixtures', 'help', 'claude-2.1.282.txt');

/**
 * Every declared option flag's ARITY, read directly off the pinned fixture text: a line like
 * `  --disallowedTools, --disallowed-tools <tools...>` yields `'--disallowedTools' -> 'variadic'`
 * and `'--disallowed-tools' -> 'variadic'`; `  --model <model>` yields `'--model' -> 1` (exactly
 * one value); `  -p, --print` (no `<...>` on the flag's own line at all) yields `'-p' -> 0` and
 * `'--print' -> 0` (boolean, no value). Independent of `probe.mjs` and of anything in
 * `src/engines/**` — built straight from the fixture's own text.
 *
 * A REAL flag-declaration line in this fixture has EXACTLY 2 leading spaces; a wrapped
 * continuation/description line is indented to the description column (40 spaces here). Anchoring
 * on exactly 2 spaces (`^ {2}(?=\S)`) is what keeps a cross-reference mention buried in another
 * flag's description — e.g. "...(only works with --print and --output-format=stream-json)"
 * wrapping onto its own 40-space-indented line — from being misread as a flag declaration.
 *
 * Fix round 3 (MINOR): a flag declared with an OPTIONAL value (`-r, --resume [value]`,
 * `--cloud [description|session_id|url]`) yields `'optional'` — a commander-style parser takes the
 * next token as its value when that token does not start with `-`, so it can swallow a prompt too.
 * @param {string} helpText
 * @returns {Map<string, 0 | 1 | "variadic" | "optional">}
 */
function extractFlagArities(helpText) {
  /** @type {Map<string, 0 | 1 | "variadic" | "optional">} */
  const arities = new Map();
  const lineRe = /^ {2}(?=\S)((?:-\S,\s*)?--[\w-]+(?:,\s*--[\w-]+)?)(?:\s+(<[\w-]+(\.\.\.)?>)|\s+(\[[\w|-]+(?:\.\.\.)?\]))?/gm;
  let match;
  while ((match = lineRe.exec(helpText))) {
    /** @type {0 | 1 | "variadic" | "optional"} */
    let arity = 0;
    if (match[4] !== undefined) arity = 'optional';
    else if (match[2] !== undefined) arity = match[3] === undefined ? 1 : 'variadic';
    for (const name of match[1].split(',').map((s) => s.trim())) {
      arities.set(name, arity);
    }
  }
  return arities;
}

/**
 * A minimal, INDEPENDENT simulation of commander-style argv parsing (never imports anything from
 * `src/engines/**`): after the binary name, anything starting with `-` begins option parsing
 * (unless a literal `--` has already been seen, which switches everything remaining to positional,
 * unconditionally — the standard end-of-options marker). A flag's arity (from
 * {@link extractFlagArities}, read off the real fixture) decides how many following tokens it
 * consumes: `0` none, `1` exactly one, `'variadic'` every following token up to (not including) the
 * next `-`-prefixed token or `--`; `'optional'` the next token only when it is not `-`-prefixed.
 * A flag NOT in the arity table is REFUSED (fix round 3, MINOR: guessing an arity would let a
 * typo'd or unparsed flag pass whenever the guess happened to fit). Anything reached outside a
 * flag's consumption is positional.
 * @param {ReadonlyArray<string>} argv
 * @param {Map<string, 0 | 1 | "variadic" | "optional">} arities
 * @returns {string[]}
 */
function simulatePositionals(argv, arities) {
  const positionals = [];
  let endOfOptions = false;
  let i = 1; // argv[0] is the binary name, never parsed as an option
  while (i < argv.length) {
    const token = argv[i];
    if (!endOfOptions && token === '--') {
      endOfOptions = true;
      i += 1;
      continue;
    }
    if (!endOfOptions && token.startsWith('-')) {
      assert.ok(arities.has(token), `flag ${token} not declared in fixture`);
      const arity = arities.get(token);
      if (arity === 'variadic') {
        i += 1;
        while (i < argv.length && argv[i] !== '--' && !argv[i].startsWith('-')) i += 1;
      } else if (arity === 'optional') {
        i += 1;
        if (i < argv.length && !argv[i].startsWith('-')) i += 1;
      } else if (arity === 1) {
        i += 2;
      } else {
        i += 1;
      }
      continue;
    }
    positionals.push(token);
    i += 1;
  }
  return positionals;
}

test('extractFlagArities: --disallowedTools/--tools are variadic, --model/--permission-mode/--fallback-model take exactly one value, -p/--safe-mode/--no-session-persistence/--restricted/--strict-mcp-config are boolean', async () => {
  const text = await readFile(CLAUDE_FIXTURE, 'utf8');
  const arities = extractFlagArities(text);
  assert.equal(arities.get('--disallowedTools'), 'variadic');
  assert.equal(arities.get('--disallowed-tools'), 'variadic');
  assert.equal(arities.get('--tools'), 'variadic');
  assert.equal(arities.get('--model'), 1);
  assert.equal(arities.get('--permission-mode'), 1);
  assert.equal(arities.get('--fallback-model'), 1);
  assert.equal(arities.get('-p'), 0);
  assert.equal(arities.get('--print'), 0);
  assert.equal(arities.get('--safe-mode'), 0);
  assert.equal(arities.get('--no-session-persistence'), 0);
  assert.equal(arities.get('--restricted'), 0);
  assert.equal(arities.get('--strict-mcp-config'), 0);
  // Fix round 3 (MINOR): optional-value flags are recognised, not mistaken for booleans.
  assert.equal(arities.get('--resume'), 'optional');
  assert.equal(arities.get('-r'), 'optional');
  assert.equal(arities.get('--cloud'), 'optional');
  assert.equal(arities.get('--debug'), 'optional');
});

test('simulatePositionals: an optional-value flag swallows a following non-dash token (so a builder can never place one before a prompt unnoticed)', async () => {
  const arities = extractFlagArities(await readFile(CLAUDE_FIXTURE, 'utf8'));
  assert.deepEqual(simulatePositionals(['claude', '-p', '--resume', '/p'], arities), []);
  assert.deepEqual(simulatePositionals(['claude', '-p', '--resume', '--', '/p'], arities), ['/p']);
});

test('simulatePositionals refuses a flag that the fixture does not declare (no arity guessing)', async () => {
  const arities = extractFlagArities(await readFile(CLAUDE_FIXTURE, 'utf8'));
  assert.throws(() => simulatePositionals(['claude', '-p', '--disalowedTools', 'x', '--', '/p'], arities), /flag --disalowedTools not declared in fixture/);
});

test('Claude coder argv WITHOUT a same-provider fallback (the exact BLOCKER scenario: nothing else follows --disallowedTools) — a variadic-aware parser recovers EXACTLY [promptPath]', async () => {
  const text = await readFile(CLAUDE_FIXTURE, 'utf8');
  const arities = extractFlagArities(text);
  const built = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/tmp/brief.md', cwd: '/work/project' });
  assert.deepEqual(simulatePositionals(built.argv, arities), ['/tmp/brief.md']);
});

test('Claude coder argv WITH a same-provider fallback — the prompt is still recovered exactly, not swallowed by --fallback-model either', async () => {
  const text = await readFile(CLAUDE_FIXTURE, 'utf8');
  const arities = extractFlagArities(text);
  const built = buildClaudeArgv({
    role: 'coder',
    model: 'claude-opus-5-5',
    promptPath: '/tmp/brief.md',
    cwd: '/work/project',
    fallback: [{ provider: 'anthropic', model: 'claude-sonnet-5' }],
  });
  assert.deepEqual(simulatePositionals(built.argv, arities), ['/tmp/brief.md']);
});

// Fix round 3 (root ruling): closed-book Claude reads the packet CONTENT from stdin, so its argv
// must carry ZERO positionals — any positional would become the prompt instead of stdin.
test('Claude closed-book and facts argv (all optional params filled): a variadic-aware parser recovers ZERO positionals', async () => {
  const text = await readFile(CLAUDE_FIXTURE, 'utf8');
  const arities = extractFlagArities(text);
  for (const role of /** @type {const} */ (['reviewer', 'judge', 's2', 'author', 'facts'])) {
    const built = buildClaudeArgv({
      role,
      model: 'claude-opus-5-5',
      effort: 'high',
      promptPath: '/tmp/packet.json',
      cwd: '/tmp/iso',
      schema: { type: 'object' },
      systemPromptText: 'lens preamble',
      maxBudgetUsd: 2,
    });
    assert.deepEqual(simulatePositionals(built.argv, arities), [], `role ${role}`);
    assert.equal(built.stdinFile, '/tmp/packet.json', `role ${role}`);
  }
});

test('the Claude coder argv literally ends with "--" then the brief pointer; closed-book argv contains no "--" at all', () => {
  const coder = buildClaudeArgv({ role: 'coder', model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
  assert.deepEqual(coder.argv.slice(-2), ['--', '/p']);
  assert.equal(coder.argv.filter((t) => t === '--').length, 1);
  for (const role of /** @type {const} */ (['reviewer', 'facts'])) {
    const built = buildClaudeArgv({ role, model: 'claude-opus-5-5', promptPath: '/p', cwd: '/c' });
    assert.equal(built.argv.filter((t) => t === '--').length, 0, `role ${role}`);
    assert.equal(built.argv.filter((t) => t === '/p').length, 0, `role ${role}`);
  }
});
