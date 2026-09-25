#!/usr/bin/env node
/**
 * The `evals/` runner (plan §9.3, §10.4 B15). Two jobs:
 *
 *  1. **Load** every `evals/<case>/case.yaml` in the layout `claude plugin eval` itself reads
 *     (`<eval dir>/**\/case.yaml`, [A12]) and check its required shape (`schema_version`,
 *     `execution.prompt`, `graders[].type`). This is what `--dry-run` (the default) does: it never
 *     spawns anything and never touches a real CLI.
 *  2. **Run** the handful of cases that carry an `x_code_forge.scenario` pointer — a JS module
 *     under `evals/scenarios/` that exercises the real code-forge CLI against fake `claude`s only
 *     (never a real model call; `claude plugin validate` is the only real CLI this package ever
 *     runs, and only read-only). The other cases (the ones `claude plugin eval` runs for real, on
 *     tag) only need to load here — this runner is not a re-implementation of the live-agent grader.
 *
 * No `node:test` import: this file must also work stand-alone (`node evals/run.mjs`), on tag.
 */
import { readFileSync, readdirSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parse as parseYAML } from 'yaml';

const HERE = path.dirname(fileURLToPath(import.meta.url));
export const EVALS_DIR = HERE;
const SCENARIOS_DIR = path.join(HERE, 'scenarios');

const GRADER_TYPES = new Set(['regex', 'tool_order', 'tool_used', 'file_exists', 'llm', 'baseline']);

/** @typedef {{id: string, dir: string, file: string, meta: any, scenario: string | null}} CaseInfo */

/**
 * Every `evals/<case>/case.yaml`, sorted by case id. Throws with every structural problem found
 * (not just the first) when a case fails to parse or is missing a required field.
 * @returns {CaseInfo[]}
 */
export function listCases() {
  const ids = readdirSync(EVALS_DIR, { withFileTypes: true })
    .filter((e) => e.isDirectory() && e.name !== 'lib' && e.name !== 'scenarios')
    .map((e) => e.name)
    .sort();
  /** @type {CaseInfo[]} */
  const cases = [];
  /** @type {string[]} */
  const problems = [];
  for (const id of ids) {
    const file = path.join(EVALS_DIR, id, 'case.yaml');
    let text;
    try {
      text = readFileSync(file, 'utf8');
    } catch {
      problems.push(`${id}: no case.yaml`);
      continue;
    }
    /** @type {any} */
    let meta;
    try {
      meta = parseYAML(text);
    } catch (err) {
      problems.push(`${id}: case.yaml YAML parse failed: ${/** @type {Error} */ (err).message}`);
      continue;
    }
    if (meta === null || typeof meta !== 'object' || Array.isArray(meta)) {
      problems.push(`${id}: case.yaml must be a YAML object`);
      continue;
    }
    if (typeof meta.schema_version !== 'string' || meta.schema_version.length === 0) {
      problems.push(`${id}: missing required field schema_version`);
      continue;
    }
    if (typeof meta.execution?.prompt !== 'string' || meta.execution.prompt.length === 0) {
      problems.push(`${id}: execution.prompt is required`);
      continue;
    }
    if (!Array.isArray(meta.graders) || meta.graders.length === 0) {
      problems.push(`${id}: graders must be a non-empty array`);
      continue;
    }
    for (const g of meta.graders) {
      if (!GRADER_TYPES.has(g?.type)) problems.push(`${id}: grader type "${g?.type}" is not one of ${[...GRADER_TYPES].join('|')}`);
    }
    const scenario = typeof meta.x_code_forge?.scenario === 'string' ? meta.x_code_forge.scenario : null;
    cases.push({ id, dir: path.join(EVALS_DIR, id), file, meta, scenario });
  }
  if (problems.length > 0) throw new Error(`evals/run.mjs: ${problems.length} case(s) failed to load:\n${problems.map((p) => `  - ${p}`).join('\n')}`);
  return cases;
}

/**
 * Run one case's `x_code_forge.scenario` module (never for a case with no scenario — those are
 * dry-run only here; `claude plugin eval` runs them for real, on tag).
 * @param {CaseInfo} c
 * @returns {Promise<{id: string, pass: boolean, detail: string}>}
 */
export async function runCase(c) {
  if (c.scenario === null) return { id: c.id, pass: true, detail: 'dry-run only (no x_code_forge.scenario; run for real via `claude plugin eval` on tag)' };
  const modulePath = path.join(SCENARIOS_DIR, c.scenario);
  try {
    const mod = await import(modulePath);
    const result = await mod.run();
    return { id: c.id, pass: result.pass === true, detail: result.detail ?? '' };
  } catch (err) {
    return { id: c.id, pass: false, detail: /** @type {Error} */ (err).stack ?? String(err) };
  }
}

/**
 * @param {{ids?: string[]}} [opts] - `ids`: only run the scenario cases with one of these ids
 *   (others still load, for the count); omitted ⇒ every scenario case.
 * @returns {Promise<{cases: CaseInfo[], results: Array<{id: string, pass: boolean, detail: string}>}>}
 */
export async function runLive(opts = {}) {
  const cases = listCases();
  const wanted = opts.ids ? new Set(opts.ids) : null;
  const results = [];
  for (const c of cases) {
    if (c.scenario === null) continue;
    if (wanted && !wanted.has(c.id)) continue;
    results.push(await runCase(c));
  }
  return { cases, results };
}

/** CLI entry: `node evals/run.mjs [--live] [--case <id>...]`. */
async function main() {
  const args = process.argv.slice(2);
  const live = args.includes('--live');
  const ids = args.flatMap((a, i) => (a === '--case' ? [args[i + 1]] : [])).filter(Boolean);
  const cases = listCases();
  console.log(`evals: loaded ${cases.length} case(s) from ${EVALS_DIR}`);
  if (!live) {
    for (const c of cases) console.log(`  - ${c.id}${c.scenario ? ` (runner: ${c.scenario})` : ''}`);
    return 0;
  }
  const runnableIds = cases.filter((c) => c.scenario !== null).map((c) => c.id);
  const unknown = ids.filter((id) => !runnableIds.includes(id));
  if (unknown.length > 0) {
    console.error(`evals: --case ${unknown.join(', ')}: unknown, or has no x_code_forge.scenario (never run live here); runnable ids: ${runnableIds.join(', ')}`);
    return 1;
  }
  const { results } = await runLive(ids.length > 0 ? { ids } : {});
  if (results.length === 0) {
    console.error('evals: --live produced 0 results (no case carries an x_code_forge.scenario) — nothing was actually checked');
    return 1;
  }
  let failed = 0;
  for (const r of results) {
    console.log(`  ${r.pass ? 'PASS' : 'FAIL'} ${r.id} — ${r.detail}`);
    if (!r.pass) failed += 1;
  }
  return failed > 0 ? 1 : 0;
}

/**
 * True when this module was invoked directly (`node evals/run.mjs`), never on a plain `import()`
 * (as `test/evals-smoke.test.mjs` does). Compares realpaths through `pathToFileURL`, not a
 * string-built `file://` URL, so a symlinked invocation (an npm bin shim) and any character that
 * needs percent-encoding both resolve correctly; `process.argv[1]` is absent for some embedders
 * (the SEA/snapshot cases Node's own docs call out), so this is guarded rather than assumed.
 */
function isMainModule() {
  if (typeof process.argv[1] !== 'string' || process.argv[1].length === 0) return false;
  try {
    return import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href;
  } catch {
    return false;
  }
}

if (isMainModule()) {
  main().then((code) => {
    process.exitCode = code;
  });
}
