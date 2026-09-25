#!/usr/bin/env node
// Fake `claude -p --output-format json` reviewer for the worker tests. Unlike the shared fakes in
// test/fixtures/bin (reused here for stdin/sleep/answer), it also records its environment — NAMES
// only (`env_keys`), plus the names of variables whose value carries a fake secret marker
// (`fake_valued_keys`), never a value. That is what the "0 key variables" clause is about.
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { answer, maybeSleep, readStdin } from '../fixtures/bin/fake-common.mjs';

const stdin = await readStdin();
if (process.env.FAKE_RECORD) {
  const entry = { pid: process.pid, argv: process.argv.slice(2), env_keys: Object.keys(process.env).sort(),
    fake_valued_keys: Object.keys(process.env).filter((k) => String(process.env[k]).includes('FAKE-')),
    stdin_bytes: stdin.bytes.length, started_at: Date.now() };
  writeFileSync(path.join(process.env.FAKE_RECORD, `reviewer-${process.pid}.json`), JSON.stringify(entry));
}
await maybeSleep();
const value = answer();
const out = {
  type: 'result',
  subtype: 'success',
  is_error: false,
  api_error_status: null,
  result: JSON.stringify(value),
  usage: { input_tokens: 10, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 42 },
};
process.stdout.write(`${JSON.stringify(out)}\n`);
