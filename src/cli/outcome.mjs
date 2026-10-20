import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { loadProjectConfig, slugFor } from '../config/load.mjs';
import { appendRow, readAllRows } from '../ledger/write.mjs';
import { checkOutcome, sealOutcome } from '../outcome/outcome.mjs';
import { loadPipeline } from '../pipeline/pipeline.mjs';
import { withRunLock, writeSigned } from '../state/run.mjs';

const USAGE = 'usage: code-forge outcome seal|check|record <outcome.json> [--root <dir>] [--step <id> (record only)] [--json (check or record)]\n';

/** @param {string} file @param {string} root @param {string | undefined} explicitStep */
async function recordOutcome(file, root, explicitStep) {
  const bytes = readFileSync(file);
  const obj = JSON.parse(bytes.toString('utf8'));
  const sha256 = createHash('sha256').update(bytes).digest('hex');
  const pipeline = loadPipeline(root);
  const step = explicitStep ?? (pipeline.exists && pipeline.ok
    ? pipeline.pipeline.steps.find((s) => path.resolve(root, s.outcome) === path.resolve(file))?.id
    : undefined) ?? null;
  const loaded = await loadProjectConfig(root);
  if (!loaded.ok && loaded.error !== 'not-found') throw new Error(`project config: ${loaded.message}`);
  const slug = slugFor(loaded.ok ? loaded.config : {}, root);
  const rows = await readAllRows(slug);
  const run = rows.filter((r) => r.event === 'run.start' && typeof r.run === 'string').at(-1)?.run;
  if (!run) throw new Error('no project run to sign the outcome; start a run first');
  return withRunLock(run, async () => {
    const current = await readAllRows(slug);
    if (current.some((r) => r.event === 'outcome.recorded' && r.sha256 === sha256 && r.step === step)) {
      return { ok: true, problems: [], already_recorded: true };
    }
    const row = await writeSigned(run, (signed) => appendRow(signed, { slug }), {
      event: 'outcome.recorded', skill: obj.skill, step, status: obj.status, sha256,
      cost_usd: obj.cost_usd ?? null, minutes: obj.minutes ?? null,
    });
    return { ok: true, problems: [], already_recorded: false, row };
  });
}

/** @param {string[]} args @returns {Promise<number>} */
export default async function outcome(args) {
  const [command, ...rest] = args;
  let file;
  let root;
  let step;
  let json = false;
  const usage = () => { process.stderr.write(USAGE); return 2; };
  if (!['seal', 'check', 'record'].includes(command)) return usage();
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (arg === '--root') {
      if (root !== undefined || !rest[i + 1] || rest[i + 1].startsWith('-')) return usage();
      root = rest[++i];
    } else if (arg === '--step' && command === 'record') {
      if (step !== undefined || !rest[i + 1] || rest[i + 1].startsWith('-')) return usage();
      step = rest[++i];
    } else if (arg === '--json' && command !== 'seal' && !json) {
      json = true;
    } else if (arg.startsWith('-') || file !== undefined) {
      return usage();
    } else {
      file = arg;
    }
  }
  if (!file) return usage();
  let result = command === 'seal' ? sealOutcome(file, { root }) : checkOutcome(file, { root });
  let alreadyRecorded = false;
  if (command === 'record' && result.ok) {
    try {
      const recorded = await recordOutcome(file, path.resolve(root ?? process.cwd()), step);
      alreadyRecorded = recorded.already_recorded;
      result = recorded;
    } catch (err) {
      result = { ok: false, problems: [err.message] };
    }
  }
  if (json) {
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } else if (!result.ok) {
    for (const problem of result.problems) process.stderr.write(`${problem}\n`);
  } else if (command === 'check') {
    process.stdout.write('ok\n');
  } else if (command === 'record') {
    process.stdout.write(alreadyRecorded ? 'already recorded\n' : 'recorded\n');
  }
  return result.ok ? 0 : 1;
}
