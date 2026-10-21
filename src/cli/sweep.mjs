import { parseFlags } from '../state/cli-args.mjs';
import { sweepProcesses } from '../util/reaper.mjs';
import { writeSafe } from '../util/redact.mjs';

const HELP = 'usage: code-forge sweep [--apply] [--json]\nDry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.\n--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.\nZombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.\n';

/** @param {string[]} args @param {{sweep?:typeof sweepProcesses}} [deps] */
export default async function runSweep(args, deps = {}) {
  let flags;
  try {
    const parsed = parseFlags(args, { booleans: ['apply', 'json', 'help'] });
    if (parsed.positionals.length) throw new Error('unexpected positional argument');
    flags = parsed.flags;
  } catch {
    writeSafe(process.stderr, HELP);
    return 2;
  }
  if (flags.help) { writeSafe(process.stdout, HELP); return 0; }
  const report = await (deps.sweep ?? sweepProcesses)({ apply: flags.apply === true });
  if (flags.json) writeSafe(process.stdout, `${JSON.stringify(report)}\n`);
  else {
    for (const item of report.items) writeSafe(process.stdout,
      `${report.apply ? item.action ?? 'stopped' : 'would stop'} ${item.kind} pid ${item.pid} ${item.rssMb.toFixed(1)} MB ${item.command}\n`);
    for (const group of report.zombies) writeSafe(process.stdout, `${group.count} zombies (parent ${group.ppid} ${group.command}) — report only\n`);
    writeSafe(process.stdout, `${report.apply ? 'sweep' : 'dry run'}: ${report.items.length} processes, total ${report.totalRssMb.toFixed(1)} MB; ${report.registryRoots.length} dead-run roots\n`);
  }
  return 0;
}
