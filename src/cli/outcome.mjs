import { checkOutcome, sealOutcome } from '../outcome/outcome.mjs';

const USAGE = 'usage: code-forge outcome seal|check <outcome.json> [--root <dir>] [--json (check only)]\n';

/** @param {string[]} args @returns {Promise<number>} */
export default async function outcome(args) {
  const [command, ...rest] = args;
  let file;
  let root;
  let json = false;
  const usage = () => { process.stderr.write(USAGE); return 2; };
  if (command !== 'seal' && command !== 'check') return usage();
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (arg === '--root') {
      if (root !== undefined || !rest[i + 1] || rest[i + 1].startsWith('-')) return usage();
      root = rest[++i];
    } else if (arg === '--json' && command === 'check' && !json) {
      json = true;
    } else if (arg.startsWith('-') || file !== undefined) {
      return usage();
    } else {
      file = arg;
    }
  }
  if (!file) return usage();
  const result = command === 'seal' ? sealOutcome(file, { root }) : checkOutcome(file, { root });
  if (json) {
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } else if (!result.ok) {
    for (const problem of result.problems) process.stderr.write(`${problem}\n`);
  } else if (command === 'check') {
    process.stdout.write('ok\n');
  }
  return result.ok ? 0 : 1;
}
