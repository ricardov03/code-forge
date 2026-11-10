import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mergeForbidden, scanTranscript } from '../../src/util/forbidden.mjs';

const coderRules = mergeForbidden([]);

// Real Claude Code sub-agent JSONL lines (B361) from three block logs that got false rule_break
// hits at close. Each line keeps its real top-level keys and the real flagged text; large
// unflagged strings and subtrees were cut. Keys are the source blocks; the line numbers below
// are the lines' own numbers in those logs.
const REAL = {
  b342: [
    "{\"type\":\"api-request-shape\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"agentId\":\"a3564bd148e45d5c1\",\"shapeHash\":\"4ca0e68215497ad8025e7dc689e1874ead8b8ab23164d635f3dda3424f85f290\",\"timestamp\":\"2026-11-10T09:17:57.795Z\",\"version\":\"2.1.318\",\"shape\":{\"tools\":[{\"name\":\"Bash\",\"input_schema\":{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\",\"properties\":{\"command\":{\"description\":\"The command to execute\",\"type\":\"string\"},\"timeout\":{\"description\":\"Optional timeout in milliseconds (max 600000 for a foreground command)\",\"type\":\"number\"},\"description\":{\"description\":\"- git reset --hard origin/main → \\\"Discard all local changes and match remote main\\\"\",\"type\":\"string\"},\"run_in_background\":{\"description\":\"Set to true to run this command in the background.\",\"type\":\"boolean\"},\"dangerouslyDisableSandbox\":{\"description\":\"Set this to true to dangerously override sandbox mode and run commands without sandboxing.\",\"type\":\"boolean\"}},\"required\":[\"command\"],\"additionalProperties\":false},\"eager_input_streaming\":true},{\"type\":\"advisor_20260301\",\"name\":\"advisor\",\"model\":\"claude-opus-5-5\",\"defer_loading\":true}]}}",
    "{\"parentUuid\":\"374061c0-7d49-4f7a-b178-9896644e200c\",\"isSidechain\":true,\"promptId\":\"045b623b-5912-4313-afa3-34749fc49be0\",\"agentId\":\"a3564bd148e45d5c1\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"toolu_01UMwN1fRuuH4MJP1eQGU6DJ\",\"type\":\"tool_result\",\"content\":\"- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\",\"is_error\":false}]},\"uuid\":\"471771a4-115f-46e0-b455-0dcac4bbb502\",\"timestamp\":\"2026-11-10T09:18:00.938Z\",\"toolUseResult\":{\"stdout\":\"- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\",\"stderr\":\"\",\"interrupted\":false,\"isImage\":false,\"noOutputExpected\":false,\"bashEditDiff\":{\"files\":[],\"moreFiles\":0,\"shared\":true}},\"sandboxed\":false,\"sourceToolAssistantUUID\":\"374061c0-7d49-4f7a-b178-9896644e200c\",\"permissionDecision\":{\"decision\":\"accept\",\"source\":\"config\",\"reasonType\":\"classifier\"},\"userType\":\"external\",\"entrypoint\":\"cli\",\"cwd\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"version\":\"2.1.318\",\"gitBranch\":\"main\",\"slug\":\"modular-zooming-tide\"}",
    "{\"parentUuid\":\"f3fabd19-f11d-4899-8f1b-78d4659f43f8\",\"isSidechain\":true,\"promptId\":\"045b623b-5912-4313-afa3-34749fc49be0\",\"agentId\":\"a3564bd148e45d5c1\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"toolu_014A4Strc3SgdEMDFXLzJcX5\",\"type\":\"tool_result\",\"content\":\"\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"close\\\", id, \\\"--run\\\", run, \\\"--no-require-reviews\\\"}\\n\\t\\t\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"waive\\\", id, p.Finding, \\\"--run\\\", run, \\\"--file\\\", p.File, \\\"--reason\\\", \\\"<reason>\\\"}\\n\\t\\t\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"reopen\\\", id, \\\"--run\\\", run, \\\"--file\\\", p.File, \\\"--reason\\\", \\\"<reason>\\\"}\",\"is_error\":false}]},\"uuid\":\"53e1fd60-e82d-4824-81a4-d2fd6d150012\",\"timestamp\":\"2026-11-10T09:20:21.831Z\",\"toolUseResult\":{\"stdout\":\"\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"close\\\", id, \\\"--run\\\", run, \\\"--no-require-reviews\\\"}\\n\\t\\t\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"waive\\\", id, p.Finding, \\\"--run\\\", run, \\\"--file\\\", p.File, \\\"--reason\\\", \\\"<reason>\\\"}\\n\\t\\t\\t\\t\\targv = []string{\\\"code-forge\\\", \\\"block\\\", \\\"reopen\\\", id, \\\"--run\\\", run, \\\"--file\\\", p.File, \\\"--reason\\\", \\\"<reason>\\\"}\",\"stderr\":\"\",\"interrupted\":false,\"isImage\":false,\"noOutputExpected\":false},\"sandboxed\":false,\"sourceToolAssistantUUID\":\"f3fabd19-f11d-4899-8f1b-78d4659f43f8\",\"permissionDecision\":{\"decision\":\"accept\",\"source\":\"config\",\"reasonType\":\"classifier\"},\"userType\":\"external\",\"entrypoint\":\"cli\",\"cwd\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"version\":\"2.1.318\",\"gitBranch\":\"main\",\"slug\":\"modular-zooming-tide\"}",
  ],
  b348: [
    "{\"type\":\"api-request-shape\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"agentId\":\"a884649ca1076ccae\",\"shapeHash\":\"4ca0e68215497ad8025e7dc689e1874ead8b8ab23164d635f3dda3424f85f290\",\"timestamp\":\"2026-11-10T09:17:57.612Z\",\"version\":\"2.1.318\",\"shape\":{\"tools\":[{\"name\":\"Bash\",\"input_schema\":{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\",\"properties\":{\"command\":{\"description\":\"The command to execute\",\"type\":\"string\"},\"timeout\":{\"description\":\"Optional timeout in milliseconds (max 600000 for a foreground command)\",\"type\":\"number\"},\"description\":{\"description\":\"- git reset --hard origin/main → \\\"Discard all local changes and match remote main\\\"\",\"type\":\"string\"},\"run_in_background\":{\"description\":\"Set to true to run this command in the background.\",\"type\":\"boolean\"},\"dangerouslyDisableSandbox\":{\"description\":\"Set this to true to dangerously override sandbox mode and run commands without sandboxing.\",\"type\":\"boolean\"}},\"required\":[\"command\"],\"additionalProperties\":false},\"eager_input_streaming\":true},{\"type\":\"advisor_20260301\",\"name\":\"advisor\",\"model\":\"claude-opus-5-5\",\"defer_loading\":true}]}}",
    "{\"parentUuid\":\"7e19bf83-a3eb-48ad-978b-8ab9693d38bc\",\"isSidechain\":true,\"promptId\":\"045b623b-5912-4313-afa3-34749fc49be0\",\"agentId\":\"a884649ca1076ccae\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"toolu_01LQgV9qB92Rnfo8tJQ6nSpY\",\"type\":\"tool_result\",\"content\":\"- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\",\"is_error\":false}]},\"uuid\":\"ac526304-df74-4730-8d50-74a15587d473\",\"timestamp\":\"2026-11-10T09:18:00.899Z\",\"toolUseResult\":{\"stdout\":\"- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\",\"stderr\":\"\",\"interrupted\":false,\"isImage\":false,\"noOutputExpected\":false,\"bashEditDiff\":{\"files\":[],\"moreFiles\":0,\"shared\":true}},\"sandboxed\":false,\"sourceToolAssistantUUID\":\"7e19bf83-a3eb-48ad-978b-8ab9693d38bc\",\"permissionDecision\":{\"decision\":\"accept\",\"source\":\"config\",\"reasonType\":\"classifier\"},\"userType\":\"external\",\"entrypoint\":\"cli\",\"cwd\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"version\":\"2.1.318\",\"gitBranch\":\"main\",\"slug\":\"modular-zooming-tide\"}",
    "{\"parentUuid\":\"72748e77-c144-4a62-a562-2dd46a009930\",\"isSidechain\":true,\"agentId\":\"a884649ca1076ccae\",\"message\":{\"model\":\"claude-opus-5-5\",\"id\":\"msg_011Cgtzxb9myunj6cYTt5JES\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"toolu_01FkXzeZPYAPfR9J2ntvtQkK\",\"name\":\"Bash\",\"input\":{\"command\":\"cd /Users/ricardov/Code/Herd/SEISIGMA/coder-forge/.claude/worktrees/cf/B348 && gofmt -l native/docsync native/cmd/code-forge-native; git stash push -u -q -- native/docsync native/cmd/code-forge-native/docs_cmd.go native/cmd/code-forge-native/handover_log.go && git status --short && cd native && go test -count=1 -run \\\"Docs\\\" ./cmd/code-forge-native/ 2>&1 | tail -30\",\"description\":\"Red run on base without implementation\",\"timeout\":600000},\"caller\":{\"type\":\"direct\"}}],\"container\":null,\"stop_reason\":\"tool_use\",\"stop_sequence\":null,\"stop_details\":null,\"input_transformations\":[],\"diagnostics\":null,\"context_management\":null},\"wireToolInputs\":{\"toolu_01FkXzeZPYAPfR9J2ntvtQkK\":{\"command\":\"cd /Users/ricardov/Code/Herd/SEISIGMA/coder-forge/.claude/worktrees/cf/B348 && gofmt -l native/docsync native/cmd/code-forge-native; git stash push -u -q -- native/docsync native/cmd/code-forge-native/docs_cmd.go native/cmd/code-forge-native/handover_log.go && git status --short && cd native && go test -count=1 -run \\\"Docs\\\" ./cmd/code-forge-native/ 2>&1 | tail -30\",\"description\":\"Red run on base without implementation\",\"timeout\":600000}},\"serverClassifierRequest\":\"7a8fa87b-0efb-47da-b2a2-4102f7fd9c9a\",\"apiBlockIndex\":1,\"requestId\":\"req_011Cgtzxasv1e37La3B8tMBZ\",\"requestRef\":\"cc9e6978-880a-43fe-b63c-20921ac92ddd\",\"requestedModel\":\"claude-opus-5-5\",\"promptCacheRequested\":\"5m\",\"attributionAgent\":\"general-purpose\",\"type\":\"assistant\",\"uuid\":\"58edd869-8501-4403-b607-47e8a5afaaff\",\"timestamp\":\"2026-11-10T09:20:17.096Z\",\"advisorModel\":\"claude-opus-5-5\",\"effort\":\"medium\",\"perTurnEffort\":\"medium\",\"userType\":\"external\",\"entrypoint\":\"cli\",\"cwd\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"version\":\"2.1.318\",\"gitBranch\":\"main\",\"slug\":\"modular-zooming-tide\"}",
  ],
  b358: [
    "{\"type\":\"api-request-shape\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"agentId\":\"ae7f55244a0ee283e\",\"shapeHash\":\"4b272ffc46736c6bf37a67a5a44eaf05633802bc25d454023dfa779d12779f8d\",\"timestamp\":\"2026-11-10T14:14:53.727Z\",\"version\":\"2.1.322\",\"shape\":{\"tools\":[{\"name\":\"Bash\",\"input_schema\":{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\",\"properties\":{\"command\":{\"description\":\"The command to execute\",\"type\":\"string\"},\"timeout\":{\"description\":\"Optional timeout in milliseconds (max 600000 for a foreground command)\",\"type\":\"number\"},\"description\":{\"description\":\"- git reset --hard origin/main → \\\"Discard all local changes and match remote main\\\"\",\"type\":\"string\"},\"run_in_background\":{\"description\":\"Set to true to run this command in the background.\",\"type\":\"boolean\"},\"dangerouslyDisableSandbox\":{\"description\":\"Set this to true to dangerously override sandbox mode and run commands without sandboxing.\",\"type\":\"boolean\"}},\"required\":[\"command\"],\"additionalProperties\":false},\"eager_input_streaming\":true},{\"type\":\"advisor_20260301\",\"name\":\"advisor\",\"model\":\"claude-opus-5-5\",\"defer_loading\":true}]}}",
    "{\"parentUuid\":\"1f9f0f26-86b8-4069-8f29-ab428b788441\",\"isSidechain\":true,\"promptId\":\"4ce32c46-4b54-4e6f-b661-52e05f6ace89\",\"agentId\":\"ae7f55244a0ee283e\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"tool_use_id\":\"toolu_0119DnSRRwFgdboKDjJwqATU\",\"type\":\"tool_result\",\"content\":\"50\\t- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\"}]},\"uuid\":\"5ee2ab53-85b2-4193-a03a-67e1c1151ebf\",\"timestamp\":\"2026-11-10T14:14:55.992Z\",\"toolUseResult\":{\"type\":\"text\",\"file\":{\"filePath\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge/plans/tools/forge/b358/prompt.md\",\"content\":\"- EDIT FILES ONLY WITH apply_patch. Never write file text through shell, python, node, sed or heredocs. The transcript grep reads every shell command's text; a command that merely contains \\\"forge block waive\\\", \\\"block reopen\\\", \\\"block accept-hit\\\", \\\"block unwaive\\\", \\\"block ack-waivers\\\", \\\"autopilot\\\" or \\\"--no-require-reviews\\\" counts as running it and the close refuses the block. Never type those phrases in shell commands.\",\"numLines\":72,\"startLine\":1,\"totalLines\":72}},\"sourceToolAssistantUUID\":\"1f9f0f26-86b8-4069-8f29-ab428b788441\",\"permissionDecision\":{\"decision\":\"accept\",\"source\":\"config\",\"reasonType\":\"mode\"},\"userType\":\"external\",\"entrypoint\":\"cli\",\"cwd\":\"/Users/ricardov/Code/Herd/SEISIGMA/coder-forge\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"version\":\"2.1.322\",\"gitBranch\":\"main\",\"slug\":\"modular-zooming-tide\"}",
    "{\"type\":\"api-request-shape\",\"sessionId\":\"6bd8f68e-18be-4180-8409-3cc234c47873\",\"agentId\":\"ae7f55244a0ee283e\",\"shapeHash\":\"f5a05f94cb7d306a780d86678e78bb761af6262c0cd18ef67db752292b263e77\",\"timestamp\":\"2026-11-10T14:29:04.328Z\",\"version\":\"2.1.322\",\"shape\":{\"tools\":[{\"name\":\"Bash\",\"input_schema\":{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\",\"properties\":{\"command\":{\"description\":\"The command to execute\",\"type\":\"string\"},\"timeout\":{\"description\":\"Optional timeout in milliseconds (max 600000 for a foreground command)\",\"type\":\"number\"},\"description\":{\"description\":\"- git reset --hard origin/main → \\\"Discard all local changes and match remote main\\\"\",\"type\":\"string\"},\"run_in_background\":{\"description\":\"Set to true to run this command in the background.\",\"type\":\"boolean\"},\"dangerouslyDisableSandbox\":{\"description\":\"Set this to true to dangerously override sandbox mode and run commands without sandboxing.\",\"type\":\"boolean\"}},\"required\":[\"command\"],\"additionalProperties\":false},\"eager_input_streaming\":true},{\"type\":\"advisor_20260301\",\"name\":\"advisor\",\"model\":\"claude-opus-5-5\",\"defer_loading\":true}]}}",
  ],
};
const REAL_LINES = { b342: [12, 17, 111], b348: [12, 17, 83], b358: [13, 19, 291] };

/** Put each real line at its own line number; the gaps are empty lines. */
function realLog(block) {
  const out = [];
  REAL_LINES[block].forEach((n, i) => {
    while (out.length < n - 1) out.push('');
    out.push(REAL[block][i]);
  });
  return out.join('\n');
}

const base = { parentUuid: null, isSidechain: true, sessionId: 'session-b361' };
const assistant = (...content) => JSON.stringify({ ...base, type: 'assistant', message: { role: 'assistant', content } });
const toolResult = (text) => JSON.stringify({
  ...base, type: 'user', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: text }] },
});
const tool = (name, input) => ({ type: 'tool_use', id: 'toolu_1', name, input });
const bash = (command) => tool('Bash', { command, description: 'run' });

test('Claude JSONL: help text, tool_result text and the prompt in real logs give no rule_break', () => {
  assert.deepEqual(scanTranscript(realLog('b342'), coderRules), []);
  assert.deepEqual(scanTranscript(realLog('b358'), coderRules), []);
  // b348 line 83 is the coder's own Bash tool_use running git stash: that one stays a hit.
  assert.deepEqual(scanTranscript(realLog('b348'), coderRules), [{ id: 'git-stash', line: 83 }]);
});

test('Claude JSONL: forbidden tool_use commands and write paths still hit, on their own lines', () => {
  const log = [
    toolResult('Forbidden: git reset --hard, git stash, forge block close --no-require-reviews'),
    assistant({ type: 'text', text: 'I will not run git stash.' }, bash('git reset --hard HEAD')),
    assistant(bash('node bin/code-forge.mjs block waive B1 x')),
    assistant(tool('Write', { file_path: '~/.code-forge/runs/r.key', content: 'git stash' })),
    assistant(tool('Edit', { file_path: '.code-forge/reviews/x.json', old_string: 'a', new_string: 'git stash' })),
    assistant(tool('Read', { file_path: '~/.code-forge/runs/r.key' })),
    assistant({ type: 'thinking', thinking: 'git reset --hard would lose work' }, tool('Edit', { file_path: 'src/a.mjs', old_string: 'git stash', new_string: 'x' })),
  ].join('\n');
  assert.deepEqual(scanTranscript(log, coderRules), [
    { id: 'git-reset-hard', line: 2 },
    { id: 'code-forge-block-waive-from-coder', line: 3 },
    { id: 'code-forge-runs-access', line: 4 },
    { id: 'code-forge-reviews-write', line: 5 },
    { id: 'code-forge-runs-access', line: 6 },
  ]);
});

test('Claude JSONL: a broken line or a line of another shape makes the whole log a plain grep', () => {
  const tail = assistant(bash('ls'));
  const expected = [
    { id: 'code-forge-block-waive-from-coder', line: 1 },
    { id: 'code-forge-no-require-reviews', line: 1 },
  ];
  assert.deepEqual(scanTranscript([REAL.b342[1], '{"type":"assistant",', tail].join('\n'), coderRules), expected);
  const other = JSON.stringify({ type: 'item.completed', item: { type: 'command_execution', command: 'ls' } });
  assert.deepEqual(scanTranscript([REAL.b342[1], other, tail].join('\n'), coderRules), expected);
});

test('Claude JSONL perf: three logs the size of the real ones scan in under 300 ms', () => {
  const sizes = { b342: 1_469_776, b348: 927_304, b358: 1_583_447 };
  const logs = Object.entries(sizes).map(([block, size]) => {
    const lines = [];
    let length = 0;
    while (length < size) {
      for (const line of [...REAL[block], toolResult('ok'), assistant(bash('npm test'))]) {
        lines.push(line);
        length += line.length + 1;
      }
    }
    return lines.join('\n');
  });
  const start = performance.now();
  const hits = logs.map((log) => scanTranscript(log, coderRules));
  const ms = performance.now() - start;
  assert.deepEqual(hits.map((h) => h.length), [0, hits[1].length, 0]);
  assert.ok(hits[1].length > 0 && hits[1].every((h) => h.id === 'git-stash'), 'only the real git stash tool_use hits');
  assert.ok(ms < 300, `scan took ${Math.round(ms)} ms`);
});
