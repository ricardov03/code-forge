import path from 'node:path';
import { loadPipeline, pipelineStatus } from '../pipeline/pipeline.mjs';

const USAGE = 'usage: code-forge pipeline status|next [--json] [--root <dir>]\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function pipeline(args) {
  const [command, ...rest] = args;
  let root;
  let json = false;
  const usage = () => { process.stderr.write(USAGE); return 2; };
  if (command !== 'status' && command !== 'next') return usage();
  for (let i = 0; i < rest.length; i += 1) {
    if (rest[i] === '--root') {
      if (root !== undefined || !rest[i + 1] || rest[i + 1].startsWith('-')) return usage();
      root = rest[++i];
    } else if (rest[i] === '--json' && !json) {
      json = true;
    } else {
      return usage();
    }
  }
  root = path.resolve(root ?? process.cwd());
  const loaded = loadPipeline(root);
  if (!loaded.exists) {
    process.stdout.write(json ? '{"exists":false}\n' : `no code-forge.pipeline.yml in ${root}\n`);
    return 0;
  }
  if (!loaded.ok) {
    for (const problem of loaded.problems) process.stderr.write(`${problem}\n`);
    return 1;
  }
  const steps = pipelineStatus(root);
  if (command === 'status') {
    if (json) {
      process.stdout.write(`${JSON.stringify({ exists: true, steps })}\n`);
    } else {
      for (const step of steps) {
        process.stdout.write(`${step.id}  ${step.state}  ${step.skill}${step.problem ? `  — ${step.problem}` : ''}\n`);
      }
    }
    return 0;
  }
  const states = new Map(steps.map((step) => [step.id, step.state]));
  const ready = steps.filter((step) => step.state === 'ready').map((step) => step.id);
  const waiting = loaded.pipeline.steps.filter((step) => states.get(step.id) === 'waiting').map((step) => ({
    id: step.id, needs: step.needs.filter((id) => states.get(id) !== 'approved'),
  }));
  const done = steps.every((step) => step.state === 'approved');
  if (json) {
    process.stdout.write(`${JSON.stringify({ exists: true, ready, waiting, done })}\n`);
  } else if (ready.length > 0) {
    process.stdout.write(`${ready.join('\n')}\n`);
  } else if (done) {
    process.stdout.write('all steps approved\n');
  } else {
    process.stdout.write('nothing ready\n');
    for (const step of waiting) process.stdout.write(`${step.id}  waiting for ${step.needs.join(', ')}\n`);
  }
  return 0;
}
