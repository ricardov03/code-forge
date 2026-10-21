import { BIN, cli, makeRepo } from '../worker/helpers.mjs';
import assert from 'node:assert/strict';
import { appendFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { assemblePack, ContextError } = await import('../../src/context/pack.mjs');
const version = JSON.parse(readFileSync(new URL('../../package.json', import.meta.url), 'utf8')).version;

test('context pack names exactly the running node, absolute repo bin and installed version before commands', async () => {
  const { repo, runId } = await makeRepo();
  for (const context of ['', 'context: {}\n']) {
    appendFileSync(path.join(repo, '.code-forge.yml'), context);
    const result = await cli(['context', 'pack', '--run', runId, '--block', 'B11', '--json'], repo);
    assert.equal(result.code, 0, result.stderr);
    const lines = readFileSync(result.json.path, 'utf8').trimEnd().split('\n');
    const sections = lines.flatMap((line, index) => line === '## code-forge' ? [index] : []);
    assert.equal(sections.length, 1, 'exactly one code-forge section');
    const index = sections[0];
    const quote = (/** @type {string} */ token) => /^[A-Za-z0-9_@%+=:,./-]+$/.test(token) ? token : `'${token.replaceAll("'", "'\\''")}'`;
    assert.deepEqual(lines.slice(index, index + 4), [
      '## code-forge',
      `run code-forge verbs with exactly: ${quote(process.execPath)} ${quote(BIN)}`,
      `version: ${version}`,
      'do not use another code-forge from PATH',
    ]);
    assert.equal(path.isAbsolute(BIN), true, 'bin path is absolute');
    assert.deepEqual(lines.slice(index - 2, index), ['owned files:', '- src/**'], 'section follows the block header');
    assert.equal(lines[index + 4], context ? '## commands (never run by code-forge)' : '## proof rule (code-forge)');
  }
});

test('assemblePack renders only supplied install values, shell-quotes spaces, and omits the section for legacy callers', () => {
  const options = { settings: null, sources: { rules: [], lessons: null }, block: { run: 'example', block: 'B1', owned: [] } };
  const legacy = assemblePack(options);
  assert.equal(legacy.text.split('\n').filter((line) => line === '## code-forge').length, 0);
  const codeForge = { node: '/node install/bin/node', bin: "/forge's install/bin/code-forge.mjs", version: '9.8.7' };
  const supplied = assemblePack({ ...options, block: { ...options.block, codeForge } });
  const section = [
    '## code-forge',
    "run code-forge verbs with exactly: '/node install/bin/node' '/forge'\\''s install/bin/code-forge.mjs'",
    'version: 9.8.7',
    'do not use another code-forge from PATH',
    '',
  ].join('\n');
  assert.equal(supplied.text.split(section).length - 1, 1, 'exactly one section with the supplied values');
  assert.equal(supplied.text.replace(section, ''), legacy.text, 'legacy header and proof remain identical');
  assert.deepEqual([supplied.lines - legacy.lines, supplied.cut], [4, 0]);
});

test('assemblePack refuses control characters in each code-forge install value', () => {
  for (const field of ['node', 'bin', 'version']) {
    for (const control of ['\n', '\u007f', '\u0085']) {
      const codeForge = { node: '/node', bin: '/bin/code-forge.mjs', version: '9.8.7', [field]: `bad${control}value` };
      assert.throws(() => assemblePack({
        settings: null,
        sources: { rules: [], lessons: null },
        block: { run: 'example', block: 'B1', codeForge },
      }), (error) => error instanceof ContextError && error.code === 'control-character'
        && error.message === `block B1: code-forge ${field} ${JSON.stringify(codeForge[field])} contains a control character`, `${field}: ${JSON.stringify(control)} must be refused`);
    }
  }
});
