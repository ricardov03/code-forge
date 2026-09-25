import assert from 'node:assert/strict';
import { test } from 'node:test';
import { HARNESSES, getHarness, globalSkillPath, projectSkillPath } from '../../src/install/harnesses.mjs';

// ── Acceptance: "path table has 6 rows (Gemini row UNVERIFIED)" ──────────────

test('the harness table has exactly 6 rows', () => {
  assert.equal(HARNESSES.length, 6);
});

test('the table lists exactly these 6 ids, in this order', () => {
  assert.deepEqual(
    HARNESSES.map((h) => h.id),
    ['claude', 'codex', 'grok', 'gemini', 'cursor', 'copilot'],
  );
});

test('exactly ONE row is unverified, and it is the Gemini row', () => {
  const unverified = HARNESSES.filter((h) => h.verified === false);
  assert.equal(unverified.length, 1);
  assert.equal(unverified[0].id, 'gemini');
});

test('every row other than Gemini is verified: true', () => {
  const verifiedIds = HARNESSES.filter((h) => h.verified === true).map((h) => h.id);
  assert.deepEqual(verifiedIds.sort(), ['claude', 'codex', 'copilot', 'cursor', 'grok']);
});

test('every row has a non-empty projectSkillsDir and homeMarker', () => {
  for (const h of HARNESSES) {
    assert.ok(typeof h.projectSkillsDir === 'string' && h.projectSkillsDir.length > 0, `${h.id} has no projectSkillsDir`);
    assert.ok(typeof h.homeMarker === 'string' && h.homeMarker.startsWith('.'), `${h.id}'s homeMarker must start with "."`);
  }
});

test('Claude Code and Codex/Grok/Gemini/Cursor/Copilot split project paths correctly: Claude gets its own dir, the rest share .agents/skills', () => {
  const byId = Object.fromEntries(HARNESSES.map((h) => [h.id, h.projectSkillsDir]));
  assert.equal(byId.claude, '.claude/skills');
  for (const id of ['codex', 'grok', 'gemini', 'cursor', 'copilot']) {
    assert.equal(byId[id], '.agents/skills', `${id} should share .agents/skills`);
  }
});

test('cursor and copilot have no CLI (command: null); every other row has a command name', () => {
  const byId = Object.fromEntries(HARNESSES.map((h) => [h.id, h.command]));
  assert.equal(byId.cursor, null);
  assert.equal(byId.copilot, null);
  for (const id of ['claude', 'codex', 'grok', 'gemini']) {
    assert.equal(typeof byId[id], 'string', `${id} should have a command`);
    assert.ok(byId[id].length > 0);
  }
});

test('HARNESSES and each row are frozen (cannot be mutated in place)', () => {
  assert.equal(Object.isFrozen(HARNESSES), true);
  assert.equal(Object.isFrozen(HARNESSES[0]), true);
});

// ── getHarness ────────────────────────────────────────────────────────────────

test('getHarness returns the exact row for a known id', () => {
  assert.equal(getHarness('grok').label, 'Grok CLI');
});

test('getHarness throws RangeError, naming the id, for an unknown id', () => {
  assert.throws(() => getHarness('not-a-real-harness'), (err) => err instanceof RangeError && /not-a-real-harness/.test(err.message));
});

// ── Path builders ────────────────────────────────────────────────────────────

test('projectSkillPath joins projectRoot + projectSkillsDir + skillName', () => {
  const claude = getHarness('claude');
  assert.equal(projectSkillPath(claude, '/repo'), '/repo/.claude/skills/code-forge');
  assert.equal(projectSkillPath(claude, '/repo', 'other-skill'), '/repo/.claude/skills/other-skill');

  const codex = getHarness('codex');
  assert.equal(projectSkillPath(codex, '/repo'), '/repo/.agents/skills/code-forge');
});

test('globalSkillPath joins home + homeMarker + "skills" + skillName', () => {
  const grok = getHarness('grok');
  assert.equal(globalSkillPath(grok, '/home/ricardo'), '/home/ricardo/.grok/skills/code-forge');
});
