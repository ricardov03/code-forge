// One companion per native daemon: reuse status's config and review-gate rules
// over the daemon's cached record/rows, without rereading the ledger or running ps.
import { createInterface } from 'node:readline';
import { statusOf } from '../cli/status.mjs';

for await (const line of createInterface({ input: process.stdin, crlfDelay: Infinity })) {
  try {
    const { record, rows } = JSON.parse(line);
    process.stdout.write(`${JSON.stringify({ runs: [await statusOf(record, rows)] })}\n`);
  } catch {
    process.stdout.write(`${JSON.stringify({ ok: false, error: 'status-unreadable' })}\n`);
  }
}
