import assert from 'node:assert/strict';
import { mock, test } from 'node:test';
import * as realRun from '../../src/state/run.mjs';

let stateReads = 0;
mock.module('../../src/state/run.mjs', { namedExports: {
  ...realRun,
  readRun: async () => { stateReads += 1; throw new Error('help must not access run state'); },
} });
const { runBlock } = await import('../../src/cli/block.mjs');

const VERBS = ['open', 'attempt', 'rebase', 'claim', 'close', 'stop', 'waive', 'reopen', 'accept-hit', 'unwaive', 'ack-waivers', 'human-pass', 'amend-acceptance'];

async function help(args) {
  let out = ''; let err = ''; let effects = 0;
  const code = await runBlock(args, {
    stdout: { write: (s) => { out += s; } },
    stderr: { write: (s) => { err += s; } },
    isTTY: true,
    ui: { confirm: async () => { effects += 1; return true; }, isCancel: () => false },
    removeBlockTree: async () => { effects += 1; throw new Error('help must not remove a worktree'); },
  });
  return { code, out, err, effects };
}

function result(verb, flag, response) {
  return {
    verb, flag, code: response.code, err: response.err, effects: response.effects,
    usage: response.out.split('\n')[0].startsWith(`usage: code-forge block ${verb} `),
    usageCount: (response.out.match(/^usage:/gm) ?? []).length,
    helpFlags: response.out.includes('--help') && response.out.includes('-h'),
    jsonFlag: response.out.includes('--json'),
  };
}

test('every block verb accepts both help flags, prints its own usage and accesses no state', async () => {
  stateReads = 0;
  const actual = []; const expected = [];
  for (const verb of VERBS) {
    for (const flag of ['--help', '-h']) {
      actual.push(result(verb, flag, await help([verb, flag])));
      expected.push({ verb, flag, code: 0, err: '', effects: 0, usage: true, usageCount: 1, helpFlags: true, jsonFlag: true });
    }
  }
  assert.deepEqual(actual, expected, 'all 26 verb/flag pairs must print exactly one matching usage and exit 0');
  assert.equal(stateReads, 0, 'all 26 help requests must perform zero run reads, before any writes');
});

test('help takes precedence over supplied operation arguments, JSON output and missing flag values', async () => {
  stateReads = 0;
  const actual = []; const expected = [];
  for (const verb of VERBS) {
    for (const flag of ['--help', '-h']) {
      const response = await help([verb, 'B1', '--run', 'r-help', '--json', flag, '--reason']);
      actual.push(result(verb, flag, response));
      expected.push({ verb, flag, code: 0, err: '', effects: 0, usage: true, usageCount: 1, helpFlags: true, jsonFlag: true });
    }
  }
  assert.deepEqual(actual, expected, 'all 26 argument-bearing requests must bypass validation, prompts and operations');
  assert.equal(stateReads, 0, 'argument-bearing help must perform zero run reads, before any writes');
});
