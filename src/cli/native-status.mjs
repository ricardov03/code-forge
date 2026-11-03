// One companion per native daemon: reuse status's config and review-gate rules
// over the daemon's cached record/rows, without rereading the ledger or running ps.
import { createInterface } from 'node:readline';
import { realpathSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { statusOf } from './status.mjs';

async function serve() {
  for await (const line of createInterface({ input: process.stdin, crlfDelay: Infinity })) {
    try {
      const { record, rows } = JSON.parse(line);
      process.stdout.write(`${JSON.stringify({ runs: [await statusOf(record, rows, { gateDetails: false })] })}\n`);
    } catch {
      process.stdout.write(`${JSON.stringify({ ok: false, error: 'status-unreadable' })}\n`);
    }
  }
}

// Preserve the router's historical result after this internal companion consumes stdin.
export default async function nativeStatus() {
  await serve();
  process.stderr.write('code-forge: verb "native-status" has no default export function\n');
  return 1;
}

if (process.argv[1] && pathToFileURL(realpathSync(process.argv[1])).href === import.meta.url) {
  await serve();
}

export const USAGE = "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n";

export const SUMMARY = "Convert cached daemon records and rows from standard input into run status JSON.";
