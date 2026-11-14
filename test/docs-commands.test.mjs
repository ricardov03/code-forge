
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { goBinary } from './helpers/go-binary.mjs';

// B648: docs/reference/commands.md comes from the Go help table
// (`code-forge help --commands-doc`, run by scripts/gen-commands-doc.sh).
const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const DOC = path.join(REPO, 'docs/reference/commands.md');
const TMP = mkdtempSync(path.join(os.tmpdir(), 'docs-commands-'));
mkdirSync(path.join(TMP, 'home'));
mkdirSync(path.join(TMP, 'tmp'));
const env = {
  PATH: process.env.PATH, HOME: path.join(TMP, 'home'), TMPDIR: path.join(TMP, 'tmp'),
  CODE_FORGE_NODE: '/nonexistent-docs-commands-node', CODE_FORGE_HANDOVER_LOG: '0',
  CODE_FORGE_NO_ERROR_LOG: '1', NO_COLOR: '1', LANG: 'C', LC_ALL: 'C', TZ: 'UTC',
};
after(() => rmSync(TMP, { recursive: true, force: true }));

const GO = goBinary();
function cli(args) {
  return spawnSync(GO, args, { cwd: TMP, env, encoding: 'utf8', timeout: 30000 });
}

/** The Usage block of a Go help page, without its two-space indent. */
function pageUsage(page) {
  const block = page.split('\nUsage:\n')[1]?.split('\n\n')[0] ?? '';
  return block.split('\n').map((line) => line.replace(/^ {2}/, '')).join('\n');
}

test('committed command reference checks clean against Go help and holds every Go command and its exact usage', () => {
  const checked = cli(['help', '--commands-doc', '--check', '--root', REPO]);
  assert.equal(checked.status, 0, `committed reference check: ${checked.stderr}`);
  assert.equal(checked.stdout, 'gen-commands-doc --check: command reference is up to date\n');
  const list = cli(['help', '--json']);
  assert.equal(list.status, 0, `help --json: ${list.stderr}`);
  const rows = JSON.parse(list.stdout);
  const doc = existsSync(DOC) ? readFileSync(DOC, 'utf8') : '';
  const sections = [...doc.matchAll(/^## ([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]);
  assert.deepEqual(sections, [...sections].sort(), 'verb sections in name order');
  for (const row of rows.filter((r) => !r.name.includes(' '))) {
    assert.ok(sections.includes(row.name), `${row.name}: verb section`);
    assert.ok(doc.includes(`- [${row.name}](#${row.name})\n`), `${row.name}: one table of contents link`);
    const page = cli(['help', row.name]);
    assert.equal(page.status, 0, `help ${row.name}: ${page.stderr}`);
    const section = doc.split(`\n## ${row.name}\n`)[1].split('\n## ')[0];
    assert.ok(section.startsWith(`\n${row.summary}\n\n\x60\x60\x60text\n${pageUsage(page.stdout)}\n\x60\x60\x60\n`),
      `${row.name}: summary and fenced usage match Go help`);
  }
  const open = cli(['help', 'block', 'open']);
  assert.equal(open.status, 0, `help block open: ${open.stderr}`);
  assert.ok(doc.includes(`### block open\n\nStart a block: owned files, acceptance tests, brief\n\n\x60\x60\x60text\n${pageUsage(open.stdout)}\n\x60\x60\x60`),
    'block open must have its summary (B613) and exact subcommand help');
  assert.ok(doc.includes('### plan check\n'), 'plan check is listed under plan');
  assert.doesNotMatch(doc, /\x1b\[|\/Users\/|\/private\/|\/tmp\/|\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/,
    'reference must contain zero terminal escapes, host paths or timestamps');
});

test('a one-byte change to a temporary page makes --check exit 1 and identify the first differing line', () => {
  assert.ok(existsSync(DOC), 'stale check requires the generated command reference');
  const copy = path.join(TMP, 'commands.md');
  const bytes = readFileSync(DOC);
  bytes[0] ^= 1;
  writeFileSync(copy, bytes);
  const result = cli(['help', '--commands-doc', '--check', copy, '--root', REPO]);
  assert.equal(result.status, 1, `one-byte stale copy must exit 1: ${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /first difference at line 1\)/, 'stale check must identify line 1');
  assert.match(result.stderr, /on disk:.*\n {2}generated:/, 'stale check must show both differing lines');
  assert.equal(result.stdout, '', 'stale check must not report success');
});
