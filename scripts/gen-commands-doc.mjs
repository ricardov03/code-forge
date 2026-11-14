#!/usr/bin/env node
/**
 * B648: a thin wrapper kept for callers that still run `node scripts/gen-commands-doc.mjs`
 * (B620 removes it with the rest of Node). The page now comes from the Go help table:
 * this runs scripts/gen-commands-doc.sh with the same arguments and exit code.
 */
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const script = path.join(path.dirname(fileURLToPath(import.meta.url)), 'gen-commands-doc.sh');
const result = spawnSync('sh', [script, ...process.argv.slice(2)], { stdio: 'inherit' });
process.exitCode = result.status ?? 1;
