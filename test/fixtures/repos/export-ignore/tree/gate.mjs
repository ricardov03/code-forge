// Fixture gate: reads every file named on the command line (relative to the cwd) and exits 0.
import { readFileSync } from 'node:fs';

for (const file of process.argv.slice(2)) readFileSync(file, 'utf8');
process.stdout.write('gate ok\n');
