/**
 * "8 fixture probes replay to the documented decisions" (plan §10.3, B3 acceptance). The fixture
 * (`test/fixtures/jev/probes.json`) is a real Jev response captured on this Mac, 2026-09-24
 * (`research/jev/jev_test.py` / `jev_results.json`) — every expected value below was hand-computed
 * from those REAL probabilities against O8's formulas and the documented default bands
 * (act 0.90 / check 0.60 / close_margin 0.15), not derived by calling the code under test.
 */

import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { mustRouteNextThroughEscalation } from '../../src/decide/escalation.mjs';
import { bandFinding, decide } from '../../src/decide/thresholds.mjs';

const FIXTURE_PATH = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures', 'jev', 'probes.json');

/** @param {number} actual @param {number} expected @param {string} msg */
function closeTo(actual, expected, msg, eps = 1e-9) {
  assert.ok(Math.abs(actual - expected) < eps, `${msg}: expected ${expected}, got ${actual}`);
}

/** @returns {Promise<{name: string, answers: Record<string, any>}[]>} */
async function loadProbes() {
  const raw = JSON.parse(await readFile(FIXTURE_PATH, 'utf8'));
  return raw.probes;
}

test('probes.json holds exactly the 8 captured scenarios, in the captured order', async () => {
  const probes = await loadProbes();
  assert.deepEqual(
    probes.map((p) => p.name),
    ['classify:easy', 'classify:medium', 'classify:hard', 'next:green', 'next:flaky_fail', 'next:repeated_fail', 'finding:real', 'finding:nit'],
  );
});

test('probe 1/8 classify:easy — lane and risk both decided (S1 acts)', async () => {
  const [probe] = await loadProbes();
  const lane = decide('lane', probe.answers.lane, {});
  assert.equal(lane.decided, true);
  assert.equal(lane.value, 'L0');
  closeTo(lane.confidence, 1.0, 'lane confidence');
  closeTo(lane.margin, 1.0, 'lane margin');

  const risk = decide('risk', probe.answers.risk, {});
  assert.equal(risk.decided, true);
  closeTo(Number(risk.value), 0.06, 'risk value (the score field)');
  closeTo(risk.confidence, 0.94, 'risk confidence');
  closeTo(risk.margin, 0.89, 'risk margin');
});

test('probe 2/8 classify:medium — lane decided, risk genuinely uncertain (S2 decides alone)', async () => {
  const probes = await loadProbes();
  const probe = probes[1];
  const lane = decide('lane', probe.answers.lane, {});
  assert.equal(lane.decided, true);
  assert.equal(lane.value, 'L1');

  const risk = decide('risk', probe.answers.risk, {});
  assert.equal(risk.decided, false);
  assert.equal(risk.stage, 'decide'); // confidence 0.42 < check (0.60): S2 decides from state alone
  closeTo(risk.confidence, 0.42, 'risk confidence');
  closeTo(risk.margin, 0.09, 'risk margin');
  closeTo(Number(risk.value), 2.07, 'risk value');
});

test('probe 3/8 classify:hard — lane and risk both decided, risk at the top of the scale', async () => {
  const probes = await loadProbes();
  const probe = probes[2];
  const lane = decide('lane', probe.answers.lane, {});
  assert.equal(lane.decided, true);
  assert.equal(lane.value, 'L2');

  const risk = decide('risk', probe.answers.risk, {});
  assert.equal(risk.decided, true);
  closeTo(Number(risk.value), 2.99, 'risk value');
  closeTo(risk.confidence, 0.99, 'risk confidence');
  closeTo(risk.margin, 0.98, 'risk margin');
});

test('probe 4/8 next:green — decided complete', async () => {
  const probes = await loadProbes();
  const next = decide('next', probes[3].answers.next, {});
  assert.equal(next.decided, true);
  assert.equal(next.value, 'complete');
});

test('probe 5/8 next:flaky_fail — decided retry', async () => {
  const probes = await loadProbes();
  const next = decide('next', probes[4].answers.next, {});
  assert.equal(next.decided, true);
  assert.equal(next.value, 'retry');
  assert.equal(mustRouteNextThroughEscalation(next.value), false);
});

test('probe 6/8 next:repeated_fail — S1 is confident, but "escalate" is NEVER acted on directly (rule 4)', async () => {
  const probes = await loadProbes();
  const next = decide('next', probes[5].answers.next, {});
  assert.equal(next.decided, true); // the threshold math alone says "act" ...
  assert.equal(next.value, 'escalate');
  closeTo(next.confidence, 0.93, 'next confidence');
  closeTo(next.margin, 0.86, 'next margin');
  // ... but escalation rule 4 overrides it regardless of confidence.
  assert.equal(mustRouteNextThroughEscalation(next.value), true);
});

test('probe 7/8 finding:real — noul 0.89: general band is "check" (not act), finding band is "judge"', async () => {
  const probes = await loadProbes();
  const answer = probes[6].answers.defect;
  const general = decide('defect', answer, {});
  assert.equal(general.decided, false);
  assert.equal(general.stage, 'check');
  assert.equal(general.value, true);
  closeTo(general.confidence, 0.89, 'defect confidence');
  closeTo(general.margin, 0.78, 'defect margin');

  assert.equal(bandFinding(answer.noul, {}), 'judge');
});

test('probe 8/8 finding:nit — noul 0.03 ⇒ decided=true, answer=false (the plan §3.3 worked example); finding band is "nit"', async () => {
  const probes = await loadProbes();
  const answer = probes[7].answers.defect;
  assert.equal(answer.noul, 0.03);

  const general = decide('defect', answer, {});
  assert.equal(general.decided, true);
  assert.equal(general.value, false);
  closeTo(general.confidence, 0.97, 'defect confidence');
  closeTo(general.margin, 0.94, 'defect margin');

  assert.equal(bandFinding(answer.noul, {}), 'nit');
});

// A mutant that reads `p` (0.03) as the confidence instead of `max(p, 1-p)` (0.97) must turn this
// red: 0.03 < check (0.60) would make `decided` false, contradicting the fixture-documented answer.
test('mutation guard: the finding:nit noul math is NOT the identity function on p', async () => {
  const probes = await loadProbes();
  const answer = probes[7].answers.defect;
  const { confidence } = decide('defect', answer, {});
  assert.notEqual(confidence, answer.noul);
  closeTo(confidence, 1 - answer.noul, 'confidence must equal max(p, 1-p) = 1-p for p < 0.5');
});
