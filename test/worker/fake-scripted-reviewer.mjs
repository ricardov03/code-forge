#!/usr/bin/env node
// Scripted fake `claude -p --output-format json` reviewer (B12c). It answers the packet it is
// given, so the stub guard's `reviewed_hunks` check passes on every round: it echoes the packet's
// `hunks:` list. What it finds comes from the JSON file named by FAKE_SCRIPT, re-read per session
// (a test rewrites it between rounds; the worker's env is fixed at start):
//   {"full": {"findings": [...]}, "recheck": {"resolve": true | false, "findings": [...]}, "fail": true?}
// `fail: true` answers a JSON object that fails the finding schema (the stub guard's `schema`).
// A packet with an `## open findings` section is a recheck: every listed id is answered in
// `resolved[]` with `resolve`. Each packet is copied to FAKE_RECORD as `packet-<hrtime>.md`.
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { readStdin } from '../fixtures/bin/fake-common.mjs';

const text = (await readStdin()).bytes.toString('utf8');
if (process.env.FAKE_RECORD) writeFileSync(path.join(process.env.FAKE_RECORD, `packet-${process.hrtime.bigint()}.md`), text);
const script = process.env.FAKE_SCRIPT ? JSON.parse(readFileSync(process.env.FAKE_SCRIPT, 'utf8')) : {};
const listed = text.slice(text.indexOf('\nhunks:\n') + 8).split('\n');
const hunks = listed.slice(0, listed.findIndex((l) => !l.startsWith('- @@'))).map((l) => l.slice(2));
const at = text.indexOf('\n## open findings\n');
const recheck = at >= 0;
const openIds = recheck ? [...text.slice(at).matchAll(/^- (\S+) \(/gm)].map((m) => m[1]) : [];
const step = (recheck ? script.recheck : script.full) ?? {};
const findings = step.findings ?? [];
const value = {
  passed: findings.length === 0,
  summary: recheck ? 'fake recheck' : 'fake review',
  reviewed_hunks: hunks,
  findings,
  resolved: openIds.map((id) => ({ id, resolved: step.resolve === true, why: 'fake' })),
  needs_file: [],
};
const out = {
  type: 'result',
  subtype: 'success',
  is_error: false,
  api_error_status: null,
  result: JSON.stringify(script.fail === true ? { broken: true } : value),
  usage: { input_tokens: 10, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 42 },
};
process.stdout.write(`${JSON.stringify(out)}\n`);
