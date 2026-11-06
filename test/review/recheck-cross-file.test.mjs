import { answerFor, cfgFor, commitAll, freshDir, git, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

// Never let fixture children inherit the live coder's run identity.
for (const key of ['CODE_FORGE_RUN_ID', 'CODE_FORGE_OWNER_PID', 'CODE_FORGE_OWNER_START_TIME', 'CODE_FORGE_RUN', 'CODE_FORGE_BLOCK']) delete process.env[key];
const { newFileState, runRound, buildRecheckPacket, fixHunkDiff } = await import('../../src/review/fixloop.mjs');
const { readFileDiff } = await import('../../src/review/packet.mjs');
const { waiveAsAgent } = await import('../../src/review/agent-waiver.mjs');
const { startRun, readRun, writeSigned } = await import('../../src/state/run.mjs');
const { openBlock } = await import('../../src/state/block.mjs');
const { appendRow } = await import('../../src/ledger/write.mjs');

const A = 'src/a.mjs';
const B = 'src/b.mjs';
const FIX = 'export function handover() { return "LAND_HANDOVER_FIXED"; }';
let sequence = 0;
/** @returns {import('../../src/review/triage.mjs').Finding & {answer: string}} */
const finding = (claim = 'The land hand-over is missing.', answer = 'fixed in b.mjs') => ({
  id: 'F1', file: A, line_start: 1, line_end: 1, severity: 'warning', category: 'correctness',
  claim, evidence: 'The transfer was never made.', fix: 'Complete the hand-over.', answer,
});

/** @param {number | string[]} [count] */
async function fixture(count = 1) {
  const repoRoot = await makeRepo();
  const peers = Array.isArray(count) ? count : count === 1 ? [B] : Array.from({ length: count }, (_, i) => `src/peer${i}.mjs`);
  writeFile(repoRoot, A, 'export const land = 0;\n');
  for (const file of peers) writeFile(repoRoot, file, 'export function handover() { return "missing"; }\n');
  await commitAll(repoRoot);
  const base = (await git(['rev-parse', 'HEAD'], repoRoot)).trim();
  for (const file of peers) writeFile(repoRoot, file, `${FIX}\n`);
  const cfg = cfgFor();
  const rows = [];
  const packets = [];
  const deps = {
    repoRoot, base, peers, changedFiles: [A, ...peers], ownedFiles: [A, ...peers], cfg,
    workDir: freshDir('cross-file'), review: async () => { throw new Error('round one must not run'); },
    writeRow: async (row) => { rows.push(row); },
    jev: async () => ({ ok: true, answers: { resolved: { noul: 0.5 } } }),
    spawn: async (opts) => {
      const text = readFileSync(opts.promptPath, 'utf8');
      packets.push(text);
      const hunks = text.split('\nhunks:\n')[1].split('\n').filter((line) => line.startsWith('- @@')).map((line) => line.slice(2));
      return { status: 'ok', exit: 0, usage: { tokens_out: 100 }, answer: answerFor(hunks, {
        resolved: [{ id: 'F1', resolved: text.includes(`+${FIX}`), why: 'Decided from supplied change.', current_lines: 'export const land = 0;' }],
      }) };
    },
  };
  async function recheck(open = finding(), changedA = false) {
    const state = newFileState({ file: A, level: 'L2' });
    Object.assign(state, { round: 1, rounds_at_level: 1, reviewed_content: 'export const land = 0;\n', open: [open] });
    if (changedA) writeFile(repoRoot, A, 'export const land = 1;\n');
    // A new ticket gets fresh peer diffs; readers remain memoised within one ticket.
    await runRound(state, { ...deps });
    return state;
  }
  return { repoRoot, base, peers, cfg, rows, packets, deps, recheck };
}

test('round two sees the other file named by the coder answer, including when A has no fix hunks', async () => {
  const f = await fixture();
  const state = await f.recheck();
  assert.equal(f.packets.length, 1, 'one recheck must run for the fix in B');
  assert.match(f.packets[0], /src\/b\.mjs[\s\S]*@@[\s\S]*\+export function handover\(\) \{ return "LAND_HANDOVER_FIXED"; \}/);
  assert.equal(state.status, 'complete', 'stub resolves F1 only from B fix hunk');
  assert.deepEqual(state.open, [], 'exactly F1 closes');
  assert.deepEqual(f.rows.find((r) => r.event === 'review.round').extra_files, [B]);
  assert.equal(f.rows.filter((r) => r.event === 'review.approved').length, 1);
  // Naming a file supplies evidence, never a verdict.
  writeFile(f.repoRoot, B, 'export function handover() { return "still missing"; }\n');
  const spawn = f.deps.spawn;
  f.deps.spawn = async (opts) => {
    const result = await spawn(opts);
    result.answer.resolved[0].current_lines = 'export function handover() { return "still missing"; }';
    return result;
  };
  const unresolved = await f.recheck();
  assert.deepEqual(unresolved.open.map((item) => item.id), ['F1'], 'unfixed named peer keeps F1 open');
});

test('agent waiver judge receives the named changed peer hunk and accepts its line citation', async () => {
  const f = await fixture();
  const slug = `cross-file-${process.pid}-${++sequence}`;
  const runId = `r-${slug}`;
  const rows = [];
  const writeRow = async (row) => { rows.push(row); await appendRow(row, { slug }); };
  await startRun({ workspace: f.repoRoot, project: slug, runId, config: f.cfg, writeRow });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [A, B], acceptance: [{ clause: 'handover', tests: ['cross-file'] }], writeRow });
  await writeSigned(runId, writeRow, { event: 'review.result', block: 'B1', file: A, status: 'reviewed', approved: false,
    open: [finding('The hand-over in b.mjs is missing.')] });
  let packet;
  await waiveAsAgent({ runId, id: 'B1', file: A, finding: 'F1', reason: `fixed in ${B}:1`, record: await readRun(runId), writeRow,
    judge: async (opts) => {
      packet = JSON.parse(readFileSync(opts.promptPath, 'utf8'));
      return { status: 'ok', answer: { verdict: packet.extra_files?.files?.includes(B) && packet.extra_files.text.includes(`+${FIX}`) ? 'agree' : 'disagree', reason: 'Need B fix hunk.' } };
    },
  }).catch((err) => { assert.fail(`waiver should accept B:1 with B change evidence: ${err.message}`); });
  assert.deepEqual(packet.evidence.map((item) => item.path), [B], 'exactly one line citation');
  assert.deepEqual(packet.extra_files.files, [B], 'exactly B attached to judge');
  assert.deepEqual(rows.filter((r) => r.event === 'review.waived').map((r) => r.extra_files), [[B]], 'one recorded waiver names B');
});

test('F1: a failed optional file-set lookup still runs the single-file waiver judge without extra evidence', async () => {
  const f = await fixture();
  const slug = `cross-file-${process.pid}-${++sequence}`;
  const runId = `r-${slug}`;
  const rows = [];
  const writeRow = async (row) => { rows.push(row); await appendRow(row, { slug }); };
  await startRun({ workspace: f.repoRoot, project: slug, runId, config: f.cfg, writeRow });
  await openBlock({ runId, id: 'B1', level: 'L2', owned: [A], acceptance: [{ clause: 'local value', tests: ['cross-file'] }], writeRow });
  await writeSigned(runId, writeRow, { event: 'review.result', block: 'B1', file: A, status: 'reviewed', approved: false,
    open: [finding('The local value is wrong.', 'fixed locally')] });
  const record = await readRun(runId);
  record.blocks.B1.base_sha = 'missing-waiver-base';
  const packets = [];
  await assert.doesNotReject(() => waiveAsAgent({ runId, id: 'B1', file: A, finding: 'F1', reason: `local value justified by ${A}:1`, record, writeRow,
    judge: async (opts) => {
      packets.push(JSON.parse(readFileSync(opts.promptPath, 'utf8')));
      return { status: 'ok', answer: { verdict: 'agree', reason: 'The local citation justifies this waiver.' } };
    },
  }), 'optional git failure must not escape as a raw error');
  assert.equal(packets.length, 1, 'exactly one judge runs despite the bad base');
  assert.equal(packets[0].extra_files, undefined, 'judge gets no extra evidence after lookup failure');
  assert.deepEqual(packets[0].evidence.map((item) => item.path), [A], 'local citation remains available');
  const waivers = rows.filter((row) => row.event === 'review.waived');
  assert.equal(waivers.length, 1, 'the agreed single-file waiver is recorded once');
  assert.equal(waivers[0].extra_files, undefined, 'waiver records no extra files');
});

test('F2: a named z.mjs precedes four linked a-d.mjs peers and the cap cut is recorded', async () => {
  const named = 'src/z.mjs';
  const linked = ['src/linked/a.mjs', 'src/linked/b.mjs', 'src/linked/c.mjs', 'src/linked/d.mjs'];
  const f = await fixture([linked[3], named, linked[1], linked[2], linked[0]]);
  writeFile(f.repoRoot, A, `${[...linked].reverse().map((file, i) => `import { handover as h${i} } from './${file.slice(4)}';`).join('\n')}\nexport const land = 0;\n`);
  await f.recheck(finding('The hand-over is missing.', `fixed in ${named}`));
  assert.equal(f.packets.length, 1, 'one recheck gets the capped evidence pack');
  const row = f.rows.find((item) => item.event === 'review.round');
  assert.deepEqual(row.extra_files, [named, ...linked.slice(0, 3)], 'named peer first, then linked peers sorted by path');
  assert.equal(row.extra_files_omitted, 1, 'exactly one linked peer is cut');
  const section = f.packets[0].split('## cross-file evidence\n')[1]?.split('\n## ')[0];
  assert.deepEqual([...section.matchAll(/^### (.+)$/gm)].map((match) => match[1]), [named, ...linked.slice(0, 3)], 'packet contains exactly the four recorded peers in priority order');
  assert.match(section, /### src\/z\.mjs\n[\s\S]*\+export function handover\(\) \{ return "LAND_HANDOVER_FIXED"; \}/, 'named peer fix hunk reaches the reviewer');
  assert.match(section, /\(1 other files omitted: cap\)/, 'packet records the cut');
});

test('unnamed findings and single-file rechecks stay byte-identical; outgoing imports add evidence', async () => {
  const f = await fixture();
  const open = finding('The local value is wrong.', 'fixed locally');
  await f.recheck(open, true);
  const diff = await fixHunkDiff({ file: A, previous: 'export const land = 0;\n', current: 'export const land = 1;\n', workDir: f.deps.workDir });
  const oldPacket = buildRecheckPacket({ diff, open: [open], cfg: f.cfg });
  assert.equal(oldPacket.status, 'ok');
  assert.equal(f.packets[0], oldPacket.text, 'no named or imported peer: exact legacy packet');
  const single = await fixture();
  single.deps.peers = [];
  single.deps.changedFiles = [A];
  await single.recheck(finding(), true);
  const singlePacket = buildRecheckPacket({ diff, open: [finding()], cfg: single.cfg });
  assert.equal(singlePacket.status, 'ok');
  assert.equal(single.packets[0], singlePacket.text, 'single-file packet stays byte-identical despite peer name');
  writeFile(f.repoRoot, A, `import { handover } from './b.mjs';\nexport const land = handover();\n`);
  await f.recheck(open);
  assert.equal(f.packets.length, 2, 'one more recheck for imported B');
  assert.match(f.packets[1], /\+export function handover\(\) \{ return "LAND_HANDOVER_FIXED"; \}/, 'imported peer contributes its actual change');
  const first = await import('../../src/review/packet.mjs');
  const roundOne = await first.buildPacket({ repoRoot: f.repoRoot, file: A, base: f.base, lens: 'full', cfg: f.cfg });
  assert.equal(roundOne.status, 'ok');
  assert.equal(roundOne.text.includes('## cross-file evidence'), false, 'first round has zero new evidence sections');
});

test('ten named changed peers send only four and record the cut within the byte cap', async () => {
  const f = await fixture(10);
  // Large UTF-8 hunks must shrink, with the byte cap counting headings and cut markers.
  for (const file of f.peers) writeFile(f.repoRoot, file, `${FIX}\n${'// é'.repeat(6000)}\n`);
  const open = finding('The hand-over is missing.', `fixed in ${f.peers.join(', ')}`);
  await f.recheck(open, true);
  assert.equal(f.packets.length, 1, 'one bounded recheck');
  const row = f.rows.find((r) => r.event === 'review.round');
  assert.ok(row, 'one review.round is recorded');
  assert.deepEqual(row.extra_files, f.peers.slice(0, 4), 'exactly first four sorted peer paths sent');
  assert.equal(row.extra_files_omitted, 6, 'exactly six peers omitted');
  const section = f.packets[0].split('## cross-file evidence\n')[1]?.split('\n## ')[0];
  assert.equal(section?.match(/^### src\/peer\d\.mjs$/gm)?.length, 4, 'four diff entries, no full files');
  assert.match(section, /6 other files omitted/);
  assert.match(section, /trimmed: over budget/);
  assert.ok(Buffer.byteLength(`## cross-file evidence\n${section}\n`) <= 16000, 'entire extra section at most 16000 bytes');
  const baseline = buildRecheckPacket({ diff: await readFileDiff({ repoRoot: f.repoRoot, file: A, base: f.base }), open: [open], cfg: f.cfg });
  assert.equal(baseline.status, 'ok');
  assert.ok(Buffer.byteLength(f.packets[0]) - Buffer.byteLength(baseline.text) <= 16000, 'packet growth at most the evidence cap');
});
