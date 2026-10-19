import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mergeForbidden, scanTranscript } from '../../src/util/forbidden.mjs';

const started = { type: 'thread.started', thread_id: 'thread-119' };
const event = (item, type = 'item.completed') => ({ type, item });
const jsonl = (...events) => events.map((e) => JSON.stringify(e)).join('\n');
const commands = (...texts) => jsonl(started, ...texts.map((command) => event({ type: 'command_execution', command })));
const coderRules = mergeForbidden(['production-marker']);
const ruleText = 'forge block accept-hit --no-require-reviews; gh pr ready; git push --force; production-marker';
const ruleIds = ['gh-pr-ready', 'git-push-force', 'code-forge-no-require-reviews', 'code-forge-block-accept-hit-from-coder', 'production-marker-extra-0'];
const ruleHits = (line) => ruleIds.map((id) => ({ id, line }));
const doubleQuote = (text) => `"${text.replace(/["\\$`]/g, '\\$&')}"`;
const wrapped = (script) => `/bin/zsh -lc ${doubleQuote(script)}`;

// Verbatim command strings from B137b transcript lines 71 and 72 (started/completed).
const b137bCommands = [
  "/bin/zsh -lc \"node --input-type=module <<'NODE'\nimport { readFileSync, writeFileSync } from 'node:fs';\nconst home = 'test/engines/codex-home.test.mjs';\nlet text = readFileSync(home, 'utf8');\ntext = text.replace(\\\"['code-forge-block-ack-waivers-from-coder', 4]]);\\\", \\\"['code-forge-block-ack-waivers-from-coder', 4], ['code-forge-block-human-pass-from-coder', 4]]); // B137: append four human-pass patterns\\\");\nwriteFileSync(home, text);\nconst snapshots = 'test/engines/builders/argv-snapshots.test.mjs';\ntext = readFileSync(snapshots, 'utf8');\ntext = text.replace('assert.equal(CODER_ONLY_FORBIDDEN.length, 8); // B127: + human transcript-hit acceptance', 'assert.equal(CODER_ONLY_FORBIDDEN.length, 9); // B127: + human transcript-hit acceptance; B137: + human-pass');\ntext = text.replace('assert.equal(renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count, 24); // B127: + four acceptance patterns', 'assert.equal(renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count, 28); // B127: + four acceptance patterns; B137: + four human-pass patterns');\nwriteFileSync(snapshots, text);\nconst rendering = 'test/engines/builders/forbidden-rendering.test.mjs';\ntext = readFileSync(rendering, 'utf8');\ntext = text.replaceAll('FORBIDDEN.length + 8); // B45: + the autopilot entry; B62a: + --remove-worktree; B72: + block reopen; B127: + block accept-hit', 'FORBIDDEN.length + 9); // B45: + the autopilot entry; B62a: + --remove-worktree; B72: + block reopen; B127: + block accept-hit; B137: + block human-pass');\ntext = text.replace('forbiddenOnly + 24); // B4.2:', 'forbiddenOnly + 28); // B137: + the 4 \"'`block human-pass` spellings; B4.2:'\"');\ntext = text.replace('19 of 24 ids (B127: + block accept-hit).', '22 of 27 ids (B137: + block human-pass).');\ntext = text.replace('assert.equal(ids.size, 21); // B127: + code-forge-block-accept-hit-from-coder', 'assert.equal(ids.size, 22); // B127: + code-forge-block-accept-hit-from-coder; B137: + code-forge-block-human-pass-from-coder');\nwriteFileSync(rendering, text);\nNODE\n git diff --check; git diff --stat; git diff -- src/util/forbidden.mjs test/engines/codex-home.test.mjs test/engines/builders/argv-snapshots.test.mjs test/engines/builders/forbidden-rendering.test.mjs\"",
  "/bin/zsh -lc \"node --input-type=module <<'NODE'\nimport { readFileSync, writeFileSync } from 'node:fs';\nconst home = 'test/engines/codex-home.test.mjs';\nlet text = readFileSync(home, 'utf8');\ntext = text.replace(\\\"['code-forge-block-ack-waivers-from-coder', 4]]);\\\", \\\"['code-forge-block-ack-waivers-from-coder', 4], ['code-forge-block-human-pass-from-coder', 4]]); // B137: append four human-pass patterns\\\");\nwriteFileSync(home, text);\nconst snapshots = 'test/engines/builders/argv-snapshots.test.mjs';\ntext = readFileSync(snapshots, 'utf8');\ntext = text.replace('assert.equal(CODER_ONLY_FORBIDDEN.length, 8); // B127: + human transcript-hit acceptance', 'assert.equal(CODER_ONLY_FORBIDDEN.length, 9); // B127: + human transcript-hit acceptance; B137: + human-pass');\ntext = text.replace('assert.equal(renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count, 24); // B127: + four acceptance patterns', 'assert.equal(renderCodexRules(renderForCodex(CODER_ONLY_FORBIDDEN)).count, 28); // B127: + four acceptance patterns; B137: + four human-pass patterns');\nwriteFileSync(snapshots, text);\nconst rendering = 'test/engines/builders/forbidden-rendering.test.mjs';\ntext = readFileSync(rendering, 'utf8');\ntext = text.replaceAll('FORBIDDEN.length + 8); // B45: + the autopilot entry; B62a: + --remove-worktree; B72: + block reopen; B127: + block accept-hit', 'FORBIDDEN.length + 9); // B45: + the autopilot entry; B62a: + --remove-worktree; B72: + block reopen; B127: + block accept-hit; B137: + block human-pass');\ntext = text.replace('forbiddenOnly + 24); // B4.2:', 'forbiddenOnly + 28); // B137: + the 4 \"'`block human-pass` spellings; B4.2:'\"');\ntext = text.replace('19 of 24 ids (B127: + block accept-hit).', '22 of 27 ids (B137: + block human-pass).');\ntext = text.replace('assert.equal(ids.size, 21); // B127: + code-forge-block-accept-hit-from-coder', 'assert.equal(ids.size, 22); // B127: + code-forge-block-accept-hit-from-coder; B137: + code-forge-block-human-pass-from-coder');\nwriteFileSync(rendering, text);\nNODE\n git diff --check; git diff --stat; git diff -- src/util/forbidden.mjs test/engines/codex-home.test.mjs test/engines/builders/argv-snapshots.test.mjs test/engines/builders/forbidden-rendering.test.mjs\"",
];

test('Codex real B137b lines 71 and 72 ignore literal replacement text', () => {
  const transcript = jsonl(started,
    event({ type: 'command_execution', command: b137bCommands[0] }, 'item.started'),
    event({ type: 'command_execution', command: b137bCommands[1] }));
  assert.deepEqual(scanTranscript(transcript, coderRules), []);
});

for (const form of ['bare', 'wrapped']) {
  const commandForm = form === 'wrapped' ? wrapped : (script) => script;
test(`Codex ${form} inert interpreter heredocs and quoted eval arguments ignore command and contains rules`, () => {
  const scripts = [
    `node <<'NODE'\nconst text = '// --no-require-reviews';\ntext.replaceAll('--no-require-reviews', 'review-required');\nNODE`,
    ...['node', 'python', 'python3', 'ruby', 'perl', 'env node', 'env MODE=test /usr/bin/python3.12', '/usr/bin/ruby', 'node22'].flatMap((interpreter) => [
      `${interpreter} <<END\nprint('${ruleText}')\nEND`,
      `${interpreter} <<"END"\nprint('${ruleText}')\nEND`,
      `${interpreter} <<-'END'\n\tprint('${ruleText}')\n\tEND`,
    ]),
    ...['-e', '-c', '--eval', '-p', '--print'].flatMap((flag) => [
      `node ${flag} 'print("${ruleText}")'`,
      `python3 ${flag} "print('${ruleText}')"`,
    ]),
  ];
  assert.deepEqual(scripts.map((script) => scanTranscript(commands(commandForm(script)), coderRules)), scripts.map(() => []),
    'every supported inert script: exactly zero command or contains hits, including the comment replacement');
});

test(`Codex ${form} interpreter bodies with process-spawn words still match every command and contains rule`, () => {
  const spawnWords = ['exec', 'execSync', 'execFile', 'execFileSync', 'spawn', 'spawnSync', 'fork', 'system', 'popen',
    'subprocess', 'child_process', 'os.system', 'Popen', 'Process.spawn', 'IO.popen'];
  const scripts = [
    ...spawnWords.map((word) => `node <<'NODE'\n${word}('${ruleText}');\nNODE`),
    `python3 -c "import subprocess; subprocess.run(['${ruleText}'])"`,
  ];
  const inert = `python3 -c "print('${ruleText}')"`;
  assert.deepEqual(scanTranscript(commands(...[inert, ...scripts].map(commandForm)), coderRules), scripts.flatMap((_, i) => ruleHits(i + 3)),
    'inert print: zero hits; each spawn body: exactly the five rule ids at its JSONL line');
});

test(`Codex ${form} spawn symbols depend on interpreter and shell quoting`, () => {
  for (const language of ['node', 'python3', 'ruby', 'perl', 'env /usr/bin/perl5.40']) {
    const nativeSymbols = language === 'ruby' || language.includes('perl');
    for (const symbol of ['`echo text`', '$(echo text)', '%x(echo text)', 'qx(echo text)']) {
      const nativeSpawn = symbol.startsWith('qx') ? language.includes('perl') : nativeSymbols;
      const shellSpawn = symbol.startsWith('`') || symbol.startsWith('$(');
      const body = `print("${ruleText}"); ${symbol}`;
      const scripts = [
        ...["'END'", '"END"', '\\END', "E'ND'"].map((delimiter) => [`${language} <<${delimiter}\n${body}\nEND`, nativeSpawn]),
        ...['END', '-END'].map((delimiter) => [`${language} <<${delimiter}\n${body}\nEND`, nativeSpawn || shellSpawn]),
        [`${language} -e '${body}'`, nativeSpawn],
        [`${language} -c "${body.replaceAll('"', '\\"')}"`, nativeSpawn || shellSpawn],
      ];
      for (const [script, hits] of scripts) {
        assert.deepEqual(scanTranscript(commands(commandForm(script)), coderRules), hits ? ruleHits(2) : [],
          `${language}, ${symbol}, ${script}: only executable spawn symbols keep command rules active`);
      }
    }
  }
});

test(`Codex ${form} node template literals are inert only with a quoted heredoc delimiter`, () => {
  const body = 'const text = `--remove-worktree`;';
  assert.deepEqual(scanTranscript(commands(...[
    `node <<'NODE'\n${body}\nNODE`,
    `node <<NODE\n${body}\nNODE`,
    `ruby <<'RUBY'\n${body}\nRUBY`,
    "perl <<'PERL'\nqx(--remove-worktree);\nPERL",
  ].map(commandForm)), coderRules), [
    { id: 'code-forge-remove-worktree', line: 3 },
    { id: 'code-forge-remove-worktree', line: 4 },
    { id: 'code-forge-remove-worktree', line: 5 },
  ], 'quoted node template: no hit; shell expansion, Ruby backticks and Perl qx: one hit each');
});

test(`Codex ${form} shell and unterminated heredocs and guarded paths fail closed; plain text stays unchanged`, () => {
  const unsafe = [
    ...['bash', 'sh', 'zsh', 'dash', 'fish', 'ksh', 'eval', 'xargs', 'unknown'].map((shell) => `${shell} <<'END'\n${ruleText}\nEND`),
    `node <<'END'\nprint('${ruleText}')`,
  ];
  const pathScript = `node <<'NODE'\nrequire('fs').writeFileSync('~/.code-forge/runs/test.key', 'data');\nNODE`;
  const inert = `node <<'NODE'\nprint('${ruleText}')\nNODE`;
  assert.deepEqual({
    unsafe: scanTranscript(commands(...unsafe.map(commandForm)), coderRules),
    path: scanTranscript(commands(commandForm(pathScript)), coderRules),
    plain: scanTranscript(inert, coderRules),
    inert: scanTranscript(commands(commandForm(inert)), coderRules),
  }, {
    unsafe: unsafe.flatMap((_, i) => ruleHits(i + 2)),
    path: [{ id: 'code-forge-runs-access', line: 2 }],
    plain: ruleHits(2),
    inert: [],
  }, 'each unsafe body and plain-text body: exactly five hits; guarded write: one path hit; Codex inert body: none');
});

test(`Codex ${form} body boundaries preserve shell text and do not share spawn words between scripts`, () => {
  const inert = `node -e 'print("${ruleText}")'`;
  const scripts = [
    `${inert}; echo '${ruleText}'`,
    `echo node -e 'print("${ruleText}")'`,
    `node file.js -e 'print("${ruleText}")'`,
    `node -e 'print("${ruleText}")`,
    `bash <<'OUTER'\n${inert}\nOUTER`,
  ];
  assert.deepEqual(scanTranscript(commands(...[...scripts, `${inert}; python3 -c 'import subprocess'`,
    `node <<'ONE'; python3 <<'TWO'\nprint('${ruleText}')\nONE\nimport subprocess\nTWO`,
    `node <<'END'\nprint('execution spawning ecosystem')\nprint('${ruleText}')\nEND`].map(commandForm)), coderRules),
  scripts.flatMap((_, i) => ruleHits(i + 2)),
  'shell text, non-command interpreter names, script-file arguments and malformed quotes: five hits each; separate bodies and non-spawn substrings: none');
});

test(`Codex ${form} B137b module heredoc ignores replacement text and preserves process and shell hits`, () => {
  const body = `import { readFileSync, writeFileSync } from 'node:fs';
const home = 'test/engines/codex-home.test.mjs';
let text = readFileSync(home, 'utf8');
text = text.replace("// code-forge block close B137b --remove-worktree", "// close the block");
text = text.replaceAll("--remove-worktree", "remove the worktree");
writeFileSync(home, text);`;
  const scripts = [
    `node --input-type=module <<'NODE'\n${body}\nNODE`,
    `node --input-type=module <<'NODE'\n${body}\nexecSync('code-forge block close B137b --remove-worktree');\nNODE`,
    `bash <<'SH'\ncode-forge block close B137b --remove-worktree\nSH`,
  ];
  assert.deepEqual(scripts.map((script) => scanTranscript(commands(commandForm(script)), coderRules)), [
    [], [{ id: 'code-forge-remove-worktree', line: 2 }], [{ id: 'code-forge-remove-worktree', line: 2 }],
  ], 'replacement only: zero hits; process call and shell heredoc: exactly one removal-rule hit each');
});
}

test('Codex shell wrappers decode one quoted script with shell escape rules', () => {
  const script = `node <<END\nprint("${ruleText}")\nEND`;
  const wrappers = ['sh -c', '/bin/bash -lc', 'zsh -ic', '/usr/local/bin/dash -c', 'ksh -l -c', '/bin/zsh -lc'];
  const scripts = wrappers.flatMap((prefix) => [`${prefix} ${doubleQuote(script)}`, `${prefix} '${script}'`]);
  // Backslash before an ordinary letter remains in double quotes: the delimiter must agree.
  scripts.push(`/bin/zsh -lc "node <<'E\\ND'\nprint('${ruleText}')\nE\\ND"`);
  scripts.push(`/bin/zsh -lc "node <<'END'\nprint('${ruleText}')\nEN\\\nD"`);
  assert.deepEqual(scripts.map((command) => scanTranscript(commands(command), coderRules)), scripts.map(() => []),
    'all shell names, paths, flag clusters, quotes, literal backslashes and escaped newlines: zero hits');
});

test('Codex malformed, unsupported and nested shell wrappers scan whole', () => {
  const script = `node <<END\nprint("${ruleText}")\nEND`;
  const scripts = [
    wrapped(wrapped(script)),
    `/bin/zsh -lc ${doubleQuote(script)} extra`,
    `/bin/zsh -lc ${doubleQuote(script).slice(0, -1)}`,
    `/bin/zsh -lc ${doubleQuote(script)}'suffix'`,
    `/bin/zsh -lc "node <<'E\\ND'\nprint('${ruleText}')\nEND"`,
    `/bin/fish -c ${doubleQuote(script)}`,
    `/bin/zsh -xc ${doubleQuote(script)}`,
  ];
  assert.deepEqual(scripts.map((command) => scanTranscript(commands(command), coderRules)), scripts.map(() => ruleHits(2)),
    'every malformed, unsupported or nested wrapper retains exactly the five rule hits');
});

test('Codex JSON commands give one hit at the JSONL line; output and messages give none', () => {
  const mention = 'cat ~/.code-forge/runs/x.key';
  const command = event({ type: 'command_execution', command: '/bin/zsh -lc "cat a.txt"', aggregated_output: mention, exit_code: 0 });
  const message = event({ type: 'agent_message', text: mention });
  assert.deepEqual(scanTranscript(jsonl(started, command, message)), [], 'output and agent text: zero hits');
  command.item.command = `/bin/zsh -lc '${mention}'`;
  assert.deepEqual(scanTranscript(jsonl(started, { type: 'turn.started' }, command, message)), [
    { id: 'code-forge-runs-access', line: 3 },
  ], 'one forbidden command: exactly one hit on JSONL line 3');
});

test('Codex file changes scan paths as writes and unknown mcp_tool_call items scan whole', () => {
  assert.deepEqual(scanTranscript(jsonl(started, event({ type: 'file_change', changes: [
    { path: '.code-forge/reviews/x.json', kind: 'add' },
  ] }))), [{ id: 'code-forge-reviews-write', line: 2 }], 'file_change: exactly one forbidden write');
  assert.deepEqual(scanTranscript(jsonl(started, event({ type: 'mcp_tool_call', server: 'files', tool: 'read', arguments: {
    nested: { path: '~/.code-forge/runs/x.key' },
  } }))), [{ id: 'code-forge-runs-access', line: 2 }], 'unknown mcp_tool_call: exactly one nested-path hit');
  assert.deepEqual(scanTranscript(jsonl(started, event({ type: 'command_execution', command: ['cat', '~/.code-forge/runs/x.key'], aggregated_output: '' }))),
    [{ id: 'code-forge-runs-access', line: 2 }], 'a command that is not a string scans the item whole: exactly one hit');
});

test('Invalid Codex detection falls back to the unchanged plain-text hits for the entire log', () => {
  const message = event({ type: 'agent_message', text: 'cat ~/.code-forge/runs/x.key' });
  const valid = jsonl(started, message);
  assert.deepEqual(scanTranscript(valid), [], 'valid Codex message: zero hits before fallback');
  for (const bad of ['cat ~/.code-forge/runs/y.key', '{bad json', 'null', '[]', '42', '"text"', '{}', '{"type":7}']) {
    assert.deepEqual(scanTranscript(`${valid}\n${bad}`), [
      { id: 'code-forge-runs-access', line: 2 },
      ...(bad.startsWith('cat ') ? [{ id: 'code-forge-runs-access', line: 3 }] : []),
    ], `invalid line ${bad}: scan all original lines as plain text`);
  }
  assert.deepEqual(scanTranscript(jsonl({ type: 'turn.started' }, message, started)), [
    { id: 'code-forge-runs-access', line: 2 },
  ], 'thread.started after the first event does not enable Codex scanning');
  assert.deepEqual(scanTranscript(jsonl(message)), [{ id: 'code-forge-runs-access', line: 1 }], 'missing thread.started falls back');
});

test('Codex boundary: blank lines preserve numbering; skipped items and non-item events give zero hits', () => {
  const mention = 'cat ~/.code-forge/runs/x.key';
  const text = '\n \t\n' + jsonl(started,
    event({ type: 'reasoning', text: mention }),
    event({ type: 'aggregated_output', text: mention }),
    { type: 'turn.completed', text: mention, item: { type: 'mcp_tool_call', path: '~/.code-forge/runs/x.key' } },
    event({ type: 'command_execution', command: mention, aggregated_output: '' }, 'item.started'),
    event({ type: 'command_execution', command: mention, aggregated_output: mention }),
    event({ type: 'file_change', changes: [{ path: 'src/a.mjs', kind: 'update' }], output: mention }),
  ) + '\n';
  assert.deepEqual(scanTranscript(text), [
    { id: 'code-forge-runs-access', line: 5 },
    { id: 'code-forge-runs-access', line: 7 },
    { id: 'code-forge-runs-access', line: 8 },
  ], 'the unknown aggregated_output item type scans whole; the two command events hit; reasoning and non-item events give none');
});
