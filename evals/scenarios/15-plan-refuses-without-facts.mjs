/**
 * Eval 15 `plan-refuses-without-facts` (plan §3.8, §9.3; C16). A direct `author --job plan` call
 * without `--facts` is refused (exit 2, no session spawned); the same call with the facts sheet
 * that `forge facts` would have produced for this brief succeeds (exit 0) — the pair the plan
 * names ("`forge facts` before `forge author`"). Reuses B9b's fixture brief/facts sheet
 * (`test/fixtures/briefs/tool-brief.*`), which name no real project (R13). Fake CLI only.
 */
import assert from 'node:assert/strict';
import { copyFileSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const BRIEFS = path.join(REPO, 'test', 'fixtures', 'briefs');
const FAKE_CLAUDE = path.join(REPO, 'test', 'fixtures', 'bin', 'fake-claude');

/** A minimal answer with no follow-up questions (the loop stops after round 1). */
const NO_QUESTIONS = JSON.stringify({ draft: '# Draft\n', questions: [] });

export async function run() {
  const { runAuthorVerb } = await import(path.join(REPO, 'src', 'cli', 'author.mjs'));
  const parent = mkdtempSync(path.join(os.tmpdir(), 'cf-eval15-'));
  // `runAuthor` writes a `session` ledger row from THIS process (the orchestrator side, not the
  // fake child), and the ledger path is `~/.code-forge/ledger/<slug>.jsonl` off `os.homedir()` —
  // deps.env only reaches the spawned fake, so the real HOME must be pinned here too and restored
  // after, exactly like `test/*/helpers.mjs` do at import time (§9.6 hygiene).
  const originalHome = process.env.HOME;
  const home = path.join(parent, 'home');
  process.env.HOME = home;
  try {
    mkdirSync(home, { recursive: true });
    const ws = path.join(parent, 'ws');
    mkdirSync(ws, { recursive: true });
    copyFileSync(path.join(REPO, 'test', 'fixtures', 'config', 'minimal-anthropic.yml'), path.join(ws, '.code-forge.yml'));
    copyFileSync(path.join(BRIEFS, 'tool-brief.md'), path.join(ws, 'tool-brief.md'));
    copyFileSync(path.join(BRIEFS, 'tool-brief.facts.md'), path.join(ws, 'tool-brief.facts.md'));
    const records = path.join(parent, 'records');
    mkdirSync(records, { recursive: true });
    const stderrChunks = [];
    const stderr = { write: (s) => stderrChunks.push(String(s)) };
    const env = { PATH: process.env.PATH ?? '', HOME: home, TMPDIR: parent, FAKE_RECORD: records, FAKE_ANSWER: NO_QUESTIONS };
    const bins = { claude: FAKE_CLAUDE };

    const withoutFacts = await runAuthorVerb(['--job', 'plan', '--brief', 'tool-brief.md'], { bins, env, stderr, stdout: { write: () => {} }, cwd: ws });
    assert.equal(withoutFacts, 2, 'author --job plan without --facts must exit 2');
    assert.equal(
      stderrChunks.join('').trim(),
      'author: facts sheet required: run forge facts first',
    );
    assert.equal(readdirSync(records).length, 0, 'no session may spawn when facts are missing');

    const stdoutChunks = [];
    const stdout = { write: (s) => stdoutChunks.push(String(s)) };
    const withFacts = await runAuthorVerb(['--job', 'plan', '--brief', 'tool-brief.md', '--facts', 'tool-brief.facts.md'], { bins, env, stderr, stdout, cwd: ws });
    assert.equal(withFacts, 0, 'author --job plan with --facts must exit 0');
    const printed = JSON.parse(stdoutChunks.join(''));
    assert.equal(readFileSync(printed.draft, 'utf8'), '# Draft\n');
    assert.equal(readdirSync(records).length, 1, 'exactly one author session ran once facts were present');

    return { pass: true, detail: 'author --job plan: 2 without --facts (0 spawns), 0 with it (1 spawn)' };
  } finally {
    process.env.HOME = originalHome;
    rmSync(parent, { recursive: true, force: true });
  }
}
