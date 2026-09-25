/**
 * Eval 16 `unbackable-clause-flagged` (plan §3.8, §9.3; C16). `test/fixtures/plans/good.plan.md`
 * cites a NOT-FOUND fact (`--max-turns`, F3 in the facts sheet embedded at its §0) and lists it
 * under "Acceptance clauses the facts sheet cannot back" with a tolerance naming the block (B2).
 * `plan check` passes as shipped; with the tolerance row removed the same clause is refused by
 * name. No real project named (R13); no CLI is spawned (`plan check` is pure text analysis).
 */
import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, copyFileSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, '..', '..');
const GOOD = path.join(REPO, 'test', 'fixtures', 'plans', 'good.plan.md');
const TOLERANCE_ROW = '1. `--max-turns` is NOT-FOUND — B2 cites it only to prove the reviewer never passes it; tolerance: B2 asserts against the help fixture, never a live CLI.';

export async function run() {
  const { runPlanVerb } = await import(path.join(REPO, 'src', 'cli', 'plan.mjs'));
  const goodText = readFileSync(GOOD, 'utf8');
  assert.equal(goodText.split(TOLERANCE_ROW).length, 2, 'fixture must carry the tolerance row exactly once');
  const parent = mkdtempSync(path.join(os.tmpdir(), 'cf-eval16-'));
  try {
    // 1. The plan as shipped (with its tolerance row) is accepted.
    {
      const dir = path.join(parent, 'ok');
      mkdirSync(dir, { recursive: true });
      const stdout = { text: '', write(s) { this.text += String(s); } };
      const stderr = { text: '', write(s) { this.text += String(s); } };
      const code = await runPlanVerb(['check', GOOD], { stdout, stderr, cwd: dir });
      assert.equal(code, 0, stderr.text);
      assert.equal(stdout.text, 'plan check: ok (3 blocks)\n');
    }
    // 2. The same plan with the tolerance row stripped: the NOT-FOUND clause is refused by name.
    {
      const dir = path.join(parent, 'bad');
      mkdirSync(path.join(dir, 'plans'), { recursive: true });
      mkdirSync(path.join(dir, 'briefs'), { recursive: true });
      copyFileSync(path.join(REPO, 'test', 'fixtures', 'briefs', 'tool-brief.md'), path.join(dir, 'briefs', 'tool-brief.md'));
      writeFileSync(path.join(dir, 'plans', 'x.plan.md'), goodText.replace(TOLERANCE_ROW, 'none'));
      const stdout = { text: '', write(s) { this.text += String(s); } };
      const stderr = { text: '', write(s) { this.text += String(s); } };
      const code = await runPlanVerb(['check', 'plans/x.plan.md'], { stdout, stderr, cwd: dir });
      assert.equal(code, 1);
      assert.deepEqual(stderr.text.split('\n').filter(Boolean), ['unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)']);
    }
    return { pass: true, detail: 'plan check: 0 with the tolerance row, 1 naming the unbackable clause+block without it' };
  } finally {
    rmSync(parent, { recursive: true, force: true });
  }
}
