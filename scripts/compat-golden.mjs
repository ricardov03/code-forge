// Regenerate with: node scripts/compat-golden.mjs [output-directory]
// Every expected value comes from the production Node implementation.
import { appendFile, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { canonicalJSON, macFor, signRow, UNSIGNED_FIELDS, verifyRow } from '../src/state/signer.mjs';
import { ticketId } from '../src/worker/ticket.mjs';
import { resolveHashTicket } from '../src/worker/queue.mjs';
import { statePath } from '../src/worker/review-state.mjs';
import { runRecordPath } from '../src/state/paths.mjs';
import { ledgerDir, ledgerPath } from '../src/ledger/paths.mjs';
import { appendRow, readAllRows, readRows } from '../src/ledger/write.mjs';

const stamp = '2026-01-02T03:04:05.006Z';
const key = Buffer.from('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'hex');

/** Build the kit in a disposable HOME; never consult an existing run or queue. */
export async function buildGoldens() {
  const home = await mkdtemp(path.join(os.tmpdir(), 'compat-home-'));
  const previousHome = process.env.HOME;
  process.env.HOME = home;
  try {
    const values = [
      ['unicode', { text: 'é 😀 𝄞 \u2028\u2029 <>&', escaped: '\u0000\b\t\n\f\r\u001f"\\' }],
      ['utf16-order', { '\ue000': 1, '😀': 2, 'é': 3, a: 4, '𝄞': 5 }],
      ['numeric-keys', { 2: 'two', 10: 'ten', 1: 'one', '01': 'leading' }],
      ['nested', [{ z: [null, true, false, { b: 2, a: 1 }], a: {} }, []]],
      ['zero', 0], ['negative-zero', -0], ['exponent-large', 1e21],
      ['exponent-small', 1e-7], ['decimal-threshold', 1e-6], ['large-decimal', 1e20],
      ['sum', 0.1 + 0.2], ['safe-int', Number.MAX_SAFE_INTEGER],
      ['rounded-int', 9007199254740993], ['rounded-large-int', 1000000000000000128],
      ['subnormal', 5e-324], ['max-finite', Number.MAX_VALUE],
      ['negative-exponent', -1.2345e-7], ['true', true], ['false', false], ['null', null],
    ];
    const canon = values.map(([name, value]) => ({
      name, input: Object.is(value, -0) ? '-0' : JSON.stringify(value), expected: canonicalJSON(value),
    }));

    const inputs = [
      { event: 'review.done', ts: stamp, file: 'src/é😀.mjs', result: { z: '<>&', a: [0.1 + 0.2, 5e-324] } },
      { event: 'session', ts: stamp, tokens_in: 12, cost_usd: 0.03, mac: 'discard me', tokens_source: 'reported', cost_source: 'estimated' },
      { event: 'nested', ts: stamp, state: { mac: 'covered', tokens_source: 'covered', cost_source: 'covered' } },
    ];
    const signed = inputs.map((row) => ({ row, mac: macFor(row, key), signed: signRow(row, key) }));
    const good = signed[0].signed;
    const verificationRows = [
      ['valid', good],
      ['unsigned-edits', { ...good, tokens_source: 'changed', cost_source: 99 }],
      ['covered-edit', { ...good, file: 'src/changed.mjs' }],
      ['timestamp-edit', { ...good, ts: '2026-01-02T03:04:05.007Z' }],
      ['missing-mac', inputs[0]], ['empty-mac', { ...good, mac: '' }],
      ['uppercase-mac', { ...good, mac: good.mac.toUpperCase() }],
      ['short-mac', { ...good, mac: '00' }],
    ];
    const sign = {
      key_hex: key.toString('hex'), unsigned_fields: UNSIGNED_FIELDS, cases: signed,
      verify: verificationRows.map(([name, row]) => ({ name, row, expected: verifyRow(row, key) })),
    };

    const base = { run: 'compat-run', block: 'B1', file: 'src/é😀.mjs', content_hash: '0123456789abcdef'.repeat(4) };
    const ids = [
      { name: 'base', parts: base },
      { name: 'null-tree', parts: { ...base, tree: null } },
      { name: 'tree', parts: { ...base, tree: '/compat/tree' } },
      { name: 'empty-tree', parts: { ...base, tree: '' } },
    ].map(({ name, parts }) => ({ name, parts, expected: ticketId(parts) }));
    const review = [];
    for (const revision of [undefined, '', 'acceptance-v2']) {
      const record = runRecordPath(base.run);
      await mkdir(path.dirname(record), { recursive: true });
      await writeFile(record, JSON.stringify({ blocks: { B1: { acceptance_revision: revision } } }));
      for (const reopened of [undefined, '2026-02-03T04:05:06.007Z']) {
        const parts = { ...base, acceptance_revision: revision, reopened_at: reopened };
        const result = resolveHashTicket({ ...base, repoRoot: home, after: reopened });
        review.push({ parts, expected: result.ticket });
      }
    }
    const ticket = { ids, review, states: ['src/é😀.mjs', 'a.mjs', 'nested/quote".mjs'].map((file) => ({
      file, expected: path.basename(statePath(base.run, base.block, file)),
    })) };

    const ledger = [];
    async function capture(name, slug) {
      const files = {};
      for (const file of (await readdir(ledgerDir())).filter((f) => f.startsWith(slug)).sort()) {
        files[file] = await readFile(path.join(ledgerDir(), file), 'utf8');
      }
      let current, all, error = false;
      try { current = await readRows(slug); all = await readAllRows(slug); }
      catch (err) { if (!(err instanceof SyntaxError)) throw err; error = true; }
      ledger.push({ name, slug, files, current, all, error });
    }
    const slug = 'compat';
    await appendRow(signRow({ event: 'run.start', ts: stamp, label: 'é😀' }, key), { slug });
    await appendRow(signRow({ event: 'session', ts: stamp, tokens_in: 7, cost_usd: 0.03 }, key), { slug, rotateAtBytes: 1 });
    await appendRow(signRow({ event: 'run.end', ts: stamp }, key), { slug });
    await appendFile(ledgerPath(slug), '{"event":"torn é😀');
    await capture('rotation-and-torn-tail', slug);
    for (let i = 0; i < 11; i++) {
      await appendRow({ event: 'row', ts: stamp, i }, { slug: 'ordered', rotateAtBytes: 1 });
    }
    await writeFile(path.join(ledgerDir(), 'ordered-other.99.jsonl'), 'unrelated corrupt data\n');
    await writeFile(path.join(ledgerDir(), 'ordered.3.jsonl.bak'), 'unrelated corrupt data\n');
    await capture('numeric-rotation-order-and-exact-names', 'ordered');
    const texts = [
      ['complete-no-newline', '{"event":"complete"}'],
      ['only-torn', '{"event":'],
      ['blank-lines', '\n{"event":"good"}\n\n'],
      ['invalid-final-with-newline', '{"event":"good"}\nbroken\n'],
      ['interior-corruption', 'broken\n{"event":"good"}\n'],
    ];
    for (const [name, text] of texts) {
      await writeFile(ledgerPath(name), text);
      await capture(name, name);
    }
    await capture('missing', 'missing');
    return { 'canon.json': canon, 'sign.json': sign, 'ticket.json': ticket, 'ledger.json': ledger };
  } finally {
    if (previousHome === undefined) delete process.env.HOME;
    else process.env.HOME = previousHome;
    await rm(home, { recursive: true, force: true });
  }
}

/** @param {string} out */
export async function generateGoldens(out) {
  const kit = await buildGoldens();
  await mkdir(out, { recursive: true });
  for (const [name, value] of Object.entries(kit)) {
    await writeFile(path.join(out, name), `${JSON.stringify(value, null, 2)}\n`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await generateGoldens(process.argv[2] ?? fileURLToPath(new URL('../test/compat/golden/', import.meta.url)));
}
