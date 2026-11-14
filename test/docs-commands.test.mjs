import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = path.join(REPO, 'scripts/gen-commands-doc.mjs');
const DOC = path.join(REPO, 'docs/reference/commands.md');
const TMP = mkdtempSync(path.join(os.tmpdir(), 'docs-commands-'));
mkdirSync(path.join(TMP, 'home'));
mkdirSync(path.join(TMP, 'tmp'));
const env = {
  PATH: process.env.PATH, HOME: path.join(TMP, 'home'), TMPDIR: path.join(TMP, 'tmp'),
  CODE_FORGE_NATIVE: '0', CODE_FORGE_NO_ERROR_LOG: '1', NO_COLOR: '1', LANG: 'C', LC_ALL: 'C', TZ: 'UTC',
};
after(() => rmSync(TMP, { recursive: true, force: true }));

function cli(args) {
  return spawnSync(process.execPath, [path.join(REPO, 'bin/code-forge.mjs'), ...args], {
    cwd: TMP, env, encoding: 'utf8', timeout: 15000,
  });
}

test('committed command reference checks clean and contains every runtime verb and its exact help', async () => {
  const help = cli(['help']);
  assert.equal(help.status, 0, `help must exit 0: ${help.stderr}`);
  const verbs = [...help.stdout.matchAll(/^ {2}([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]);
  assert.ok(verbs.length > 0, 'help must list at least one verb');
  const doc = existsSync(DOC) ? readFileSync(DOC, 'utf8') : '';
  assert.deepEqual([...doc.matchAll(/^## ([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]), verbs,
    `reference must contain exactly ${verbs.length} verb sections in CLI order`);
  const checked = spawnSync(process.execPath, [SCRIPT, '--check'], { cwd: TMP, env, encoding: 'utf8' });
  assert.equal(checked.status, 0, `committed reference check: ${checked.stderr}`);
  assert.match(checked.stdout, /up to date/, 'clean check must report up to date');
  for (const verb of verbs) {
    const section = doc.split(`\n## ${verb}\n`)[1].split('\n## ')[0];
    assert.ok(doc.includes(`- [${verb}](#${verb})\n`), `${verb}: one table of contents link`);
    const response = cli([verb, '--help']);
    const usage = response.stdout || response.stderr;
    if (response.status === 0 && usage.trim()) {
      assert.ok(section.includes(`\n\x60\x60\x60text\n${usage}${usage.endsWith('\n') ? '' : '\n'}\x60\x60\x60\n`),
        `${verb}: fenced help must preserve all CLI output bytes`);
    } else {
      assert.ok(section.includes('No usage text yet.'), `${verb}: failed help must have a placeholder`);
      assert.ok(checked.stderr.split('\n').includes(`No usage text yet: ${verb}`), `${verb}: failed help must be listed on stderr`);
    }
  }
  const open = cli(['block', 'open', '--help']);
  assert.equal(open.status, 0, `block open help: ${open.stderr}`);
  assert.ok(doc.includes(`### block open\n\nStart a block: owned files, acceptance tests, brief\n\n\x60\x60\x60text\n${open.stdout}\x60\x60\x60`), 'block open must have its summary (B613) and exact subcommand help');
  assert.ok(doc.includes('### plan check\n'), 'plan check must be discovered even when plan --help has no usage');
  const { collectCommandHelp, renderCommandsDoc } = await import('../scripts/gen-commands-doc.mjs');
  assert.equal(renderCommandsDoc(collectCommandHelp().commands), doc, 'pure renderer and CLI must produce identical bytes');
  assert.doesNotMatch(doc, /\x1b\[|\/Users\/|\/private\/|\/tmp\/|\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/,
    'reference must contain zero terminal escapes, host paths or timestamps');
});

test('a one-byte change to a temporary page makes --check exit 1 and identify the first differing line', () => {
  assert.ok(existsSync(DOC), 'stale check requires the generated command reference');
  const copy = path.join(TMP, 'commands.md');
  const bytes = readFileSync(DOC);
  bytes[0] ^= 1;
  writeFileSync(copy, bytes);
  const result = spawnSync(process.execPath, [SCRIPT, '--check', copy], { cwd: TMP, env, encoding: 'utf8' });
  assert.equal(result.status, 1, `one-byte stale copy must exit 1: ${result.stdout}\n${result.stderr}`);
  assert.match(result.stderr, /first difference at line 1\)/, 'stale check must identify line 1');
  assert.match(result.stderr, /on disk:.*\n {2}generated:/, 'stale check must show both differing lines');
  assert.equal(result.stdout, '', 'stale check must not report success');
});
