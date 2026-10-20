import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { Ajv2020 } from 'ajv/dist/2020.js';
import { parse as parseYAML } from 'yaml';
import { checkOutcome, loadOutcome, validateOutcome } from '../outcome/outcome.mjs';

const schema = JSON.parse(readFileSync(new URL('../../schema/pipeline.schema.json', import.meta.url), 'utf8'));
const validate = new Ajv2020({ allErrors: true, useDefaults: true }).compile(schema);

/** @typedef {{id: string, skill: string, needs: string[], outcome: string, approval: 'human'|'none'}} PipelineStep */
/** @typedef {{root: string, steps: PipelineStep[]}} Pipeline */
/** @typedef {{exists: false} | {exists: true, ok: boolean, problems: string[], pipeline: Pipeline|null}} PipelineResult */

/** Load and validate the pipeline in the project folder. Never writes to disk.
 * @param {string} root @returns {PipelineResult}
 */
export function loadPipeline(root) {
  let raw;
  try {
    raw = readFileSync(path.join(root, 'code-forge.pipeline.yml'), 'utf8');
  } catch (err) {
    if (err.code === 'ENOENT') return { exists: false };
    return { exists: true, ok: false, problems: [`Cannot read pipeline (${err.code ?? 'error'})`], pipeline: null };
  }
  let pipeline;
  try {
    pipeline = parseYAML(raw, { prettyErrors: false });
  } catch (err) {
    return { exists: true, ok: false, problems: [`Invalid pipeline YAML: ${err.message}`.replace(/[\r\n]/g, ' ')], pipeline: null };
  }
  if (!validate(pipeline)) {
    const problems = (validate.errors ?? []).map((err) => {
      const extra = err.keyword === 'additionalProperties' ? ` (${JSON.stringify(err.params.additionalProperty)})` : '';
      return `${err.instancePath || '/'} ${err.message}${extra}`.replace(/[\r\n]/g, ' ');
    });
    return { exists: true, ok: false, problems, pipeline: null };
  }

  const checked = /** @type {Pipeline} */ (pipeline);
  const problems = [];
  const byId = new Map();
  for (const step of checked.steps) {
    if (byId.has(step.id)) problems.push(`duplicate step id ${JSON.stringify(step.id)}`);
    byId.set(step.id, step);
  }
  for (const step of checked.steps) {
    for (const need of step.needs) {
      if (!byId.has(need)) problems.push(`step ${JSON.stringify(step.id)}: unknown need ${JSON.stringify(need)}`);
    }
  }
  const visiting = new Set();
  const visited = new Set();
  /** @param {string} id */
  function visit(id) {
    if (visiting.has(id)) {
      problems.push(`cycle at step ${JSON.stringify(id)}`);
      return;
    }
    if (visited.has(id)) return;
    visiting.add(id);
    for (const need of byId.get(id).needs) visit(need);
    visiting.delete(id);
    visited.add(id);
  }
  if (problems.length === 0) for (const step of checked.steps) visit(step.id);
  return { exists: true, ok: problems.length === 0, problems, pipeline: checked };
}

/** @typedef {{id: string, skill: string, state: 'waiting'|'ready'|'draft'|'blocked'|'failed'|'approved'|'invalid', problem?: string}} PipelineRow */

/** Evaluate dependencies before their consumers, then return rows in file order.
 * Missing pipelines have no rows. Invalid pipelines throw their validation problems.
 * @param {string} root @returns {PipelineRow[]}
 */
export function pipelineStatus(root) {
  const loaded = loadPipeline(root);
  if (!loaded.exists) return [];
  if (!loaded.ok) throw new Error(loaded.problems.join('\n'));
  const byId = new Map(loaded.pipeline.steps.map((step) => [step.id, step]));
  /** @type {Map<string, PipelineRow>} */
  const rows = new Map();
  /** @param {PipelineStep} step @returns {PipelineRow} */
  function evaluate(step) {
    if (rows.has(step.id)) return rows.get(step.id);
    /** @type {PipelineRow} */
    const row = { id: step.id, skill: step.skill, state: 'waiting' };
    if (step.needs.every((id) => evaluate(byId.get(id)).state === 'approved')) {
      const file = path.resolve(root, step.outcome);
      if (!existsSync(file)) {
        row.state = 'ready';
      } else {
        try {
          const outcome = loadOutcome(file);
          let result = validateOutcome(outcome);
          if (result.ok && outcome.status === 'approved') result = checkOutcome(file, { root });
          if (result.ok) {
            row.state = outcome.status;
          } else {
            row.state = 'invalid';
            row.problem = result.problems[0];
          }
        } catch (err) {
          row.state = 'invalid';
          row.problem = err.message;
        }
      }
    }
    rows.set(step.id, row);
    return row;
  }
  return loaded.pipeline.steps.map(evaluate);
}
