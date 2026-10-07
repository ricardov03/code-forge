import './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { triageFindings } = await import('../../src/review/triage.mjs');

/** @param {string} id @param {'critical' | 'warning' | 'nit'} severity */
const finding = (id, severity) => ({ id, file: 'src/a.mjs', line_start: 3, line_end: 4, severity, category: 'correctness', claim: `claim ${id}`, evidence: 'e', fix: 'f' });

/** A mocked Jev: `defect` answers from `table` by finding id; every call is recorded. */
function mockJev(/** @type {Record<string, number>} */ table) {
  /** @type {Array<Record<string, any>>} */
  const calls = [];
  const jev = async (/** @type {{state: Record<string, any>, questions: Record<string, any>}} */ req) => {
    calls.push(req);
    return { ok: true, answers: { defect: { type: 'noul', noul: table[req.state.finding.id] } } };
  };
  return { jev, calls };
}

test('judge findings are final: S1 defect runs as shadow and never changes the verdict', async () => {
  const { jev, calls } = mockJev({ J1: 0.05, J2: 0.99 });
  /** @type {Array<Record<string, any>>} */
  const rows = [];
  const out = await triageFindings({ file: 'src/a.mjs', findings: [finding('J1', 'critical'), finding('J2', 'nit')], fromJudge: true, jev, writeRow: async (r) => void rows.push(r) });
  assert.deepEqual(out.fix_now.map((f) => f.id), ['J1']);
  assert.deepEqual(out.nit.map((f) => f.id), ['J2']);
  assert.equal(out.rulings, 0);
  assert.equal(calls.length, 2);
  assert.deepEqual(rows.map((r) => [r.finding, r.source, r.band, r.verdict]), [
    ['J1', 'shadow', 'nit', 'fix_now'],
    ['J2', 'shadow', 'fix_now', 'nit'],
  ]);
});

test('single-session findings are banded by S1; the mid band goes to ONE L3 ruling per file; Jev down ⇒ all residue', async () => {
  const { jev } = mockJev({ S1: 0.95, S2: 0.1, S3: 0.6, S4: 0.5 });
  /** @type {Array<Record<string, any>>} */
  const ruled = [];
  const rule = async (/** @type {{file: string, findings: Array<{id: string}>}} */ req) => {
    ruled.push(req);
    return { S3: /** @type {'nit'} */ ('nit'), S4: /** @type {'fix_now'} */ ('fix_now') };
  };
  const findings = [finding('S1', 'nit'), finding('S2', 'critical'), finding('S3', 'critical'), finding('S4', 'warning')];
  const out = await triageFindings({ file: 'src/a.mjs', findings, fromJudge: false, jev, rule });
  // B52: S2 (critical, S1 says nit) and S3 (critical, ruled nit) stay fix_now — a critical is never demoted
  assert.deepEqual(out.fix_now.map((f) => f.id), ['S1', 'S2', 'S3', 'S4']);
  assert.deepEqual(out.nit.map((f) => f.id), []);
  assert.equal(ruled.length, 1);
  assert.deepEqual(ruled[0].findings.map((f) => f.id), ['S3', 'S4']);

  // Jev unavailable and no ruling: fail closed on severity (critical/warning ⇒ fix_now)
  const down = async () => ({ ok: false, kind: 'unavailable' });
  const fallback = await triageFindings({ file: 'src/a.mjs', findings, fromJudge: false, jev: down });
  assert.deepEqual(fallback.fix_now.map((f) => f.id), ['S2', 'S3', 'S4']);
  assert.deepEqual(fallback.nit.map((f) => f.id), ['S1']);
});
