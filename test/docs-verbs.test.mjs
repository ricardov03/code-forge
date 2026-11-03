import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const TMP = mkdtempSync(path.join(os.tmpdir(), 'docs-verbs-'));
mkdirSync(path.join(TMP, 'home'));
mkdirSync(path.join(TMP, 'tmp'));
after(() => rmSync(TMP, { recursive: true, force: true }));
const env = {
  PATH: process.env.PATH, HOME: path.join(TMP, 'home'), TMPDIR: path.join(TMP, 'tmp'),
  CODE_FORGE_NATIVE: '0', CODE_FORGE_NO_ERROR_LOG: '1', NO_COLOR: '1', LANG: 'C', LC_ALL: 'C', TZ: 'UTC',
};

function cli(args) {
  return spawnSync(process.execPath, [path.join(REPO, 'bin/code-forge.mjs'), ...args], {
    cwd: TMP, env, encoding: 'utf8', timeout: 15000,
  });
}

/** Extract code with original offsets, so every failure points to its Markdown line. */
function codeSpans(markdown) {
  const spans = [];
  let offset = 0;
  let fence = null;
  let start = 0;
  let proseStart = 0;
  const inline = (end) => {
    const text = markdown.slice(proseStart, end);
    for (const match of text.matchAll(/(`+)([\s\S]*?)\1(?!`)/g)) {
      spans.push({ text: match[2], offset: proseStart + match.index + match[1].length, fenced: false });
    }
  };
  for (const line of markdown.split(/(?<=\n)/)) {
    const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fence === null && marker) {
      inline(offset);
      fence = marker[1];
      start = offset + line.length;
    } else if (fence !== null && new RegExp(`^ {0,3}${fence[0]}{${fence.length},}[ \\t]*(?:\\r?\\n)?$`).test(line)) {
      spans.push({ text: markdown.slice(start, offset), offset: start, fenced: true });
      fence = null;
      proseStart = offset + line.length;
    }
    offset += line.length;
  }
  if (fence !== null) spans.push({ text: markdown.slice(start), offset: start, fenced: true });
  else inline(markdown.length);
  return spans;
}

function mentions(markdown) {
  const pattern = /(?:^[ \t]*|\$ |&& |\| |; |npx )(?:code-forge|forge)[ \t]+([A-Za-z][\w-]*)(?:[ \t]+([A-Za-z][\w-]*(?:\|[A-Za-z][\w-]*)*))?/;
  return codeSpans(markdown).flatMap((span) => [...span.text.matchAll(
    new RegExp(pattern.source, span.fenced ? 'gm' : 'g'),
  )].filter((match) => span.text.lastIndexOf('<', match.index) <= span.text.lastIndexOf('>', match.index)).map((match) => ({
    verb: match[1], subs: match[1] === 'block' && match[2] ? match[2].split('|') : [],
    line: markdown.slice(0, span.offset + match.index).split('\n').length,
  })));
}

function docFiles(dir) {
  return readdirSync(path.join(REPO, dir), { withFileTypes: true }).flatMap((entry) => {
    const file = `${dir}/${entry.name}`;
    if (file === 'docs/marketing' || file === 'docs/reference/commands.md') return [];
    if (entry.isDirectory()) return docFiles(file);
    return entry.isFile() && entry.name.endsWith('.md') ? [file] : [];
  });
}

test('all command mentions in README and docs code name runtime verbs and valid block subcommands', () => {
  const help = cli(['help']);
  assert.equal(help.status, 0, `help must exit 0: ${help.stderr}`);
  const verbs = new Set([...help.stdout.matchAll(/^ {2}([a-z][a-z0-9-]*)$/gm)].map((m) => m[1]));
  assert.ok(verbs.size > 0, 'help must list at least one real verb');
  const subcodes = new Map();
  function badMentions(file, markdown) {
    const bad = [];
    for (const mention of mentions(markdown)) {
      if (!verbs.has(mention.verb)) bad.push(`${file}:${mention.line}, ${mention.verb}`);
      else for (const sub of mention.subs) {
        if (!subcodes.has(sub)) subcodes.set(sub, cli(['block', sub, '--help']).status);
        if (subcodes.get(sub) !== 0) bad.push(`${file}:${mention.line}, block ${sub}`);
      }
    }
    return bad;
  }
  // Boundary: inspect both executable names and both fence styles; ignore prose,
  // native/package names and placeholders. Report every invalid occurrence.
  const sample = [
    'Prose code-forge nonexistent-command is ignored.',
    '`code-forge help` and ``forge block open`` are valid.',
    '`code-forge-native help` and `@codedology/code-forge help` are packages.',
    '`code-forge <verb>` and `forge block <sub>` are placeholders.',
    '```sh', 'code-forge nonexistent-command', '```',
    '~~~sh', 'forge block nonexistent-subcommand', '~~~',
    '`forge nonexistent-command`',
  ].join('\n');
  assert.deepEqual(badMentions('sample.md', sample), [
    'sample.md:6, nonexistent-command', 'sample.md:9, block nonexistent-subcommand',
    'sample.md:11, nonexistent-command',
  ], 'scanner must report exactly three invalid code mentions with their original lines');
  const positions = [
    '`<code-forge home>` and `<path to code-forge missing>` are placeholders.',
    '`echo code-forge missing` and `grep "code-forge missing" file` are arguments.',
    '`$ code-forge missing-dollar`',
    '`true && forge missing-and`',
    '`true | code-forge missing-pipe`',
    '`true; forge missing-semicolon`',
    '`npx code-forge missing-npx`',
    '```sh',
    '  code-forge missing-indented',
    'echo code-forge missing-argument',
    '```',
    '`echo\ncode-forge missing-inline-continuation`',
  ].join('\n');
  assert.deepEqual(badMentions('positions.md', positions), [
    'positions.md:3, missing-dollar', 'positions.md:4, missing-and',
    'positions.md:5, missing-pipe', 'positions.md:6, missing-semicolon',
    'positions.md:7, missing-npx', 'positions.md:9, missing-indented',
  ], 'only shell command positions are checked, with placeholders ignored');
  const files = ['README.md', ...docFiles('docs')];
  const bad = files.flatMap((file) => badMentions(file, readFileSync(path.join(REPO, file), 'utf8')));
  assert.equal(bad.length, 0, `${bad.length} invalid command mentions across ${files.length} pages:\n${bad.join('\n')}`);
});
