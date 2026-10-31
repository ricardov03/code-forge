/** Pure projections of the existing project ledger. No cache or persistent state. */
import { createHash } from 'node:crypto';

/** @typedef {{sessions: number, median_seconds: number | null, p90_seconds: number | null, availability: number, findings_per_session: number, waived_share: number, criticals: number}} ModelStats */

/**
 * Attempt rows own timing/availability; result summaries own findings. Older summaries without
 * attempt rows still count, but unknown timing cannot win a performance comparison. Failed
 * attempts remain failures even when a retry or a different model eventually approves the file.
 * @param {Array<Record<string, any>>} rows
 * @param {{now?: number, window_days?: number}} [opts]
 * @returns {Record<string, ModelStats>}
 */
export function modelStats(rows, { now = Date.now(), window_days = 14 } = {}) {
  const since = now - window_days * 86400000;
  const recent = rows.filter((r) => {
    const at = Date.parse(r?.ts);
    return at >= since && at <= now;
  });
  /** @type {Map<string, {attempts: Array<Record<string, any>>, findings: Map<string, Record<string, any>>}>} */
  const models = new Map();
  const of = (/** @type {string} */ model) => {
    if (!models.has(model)) models.set(model, { attempts: [], findings: new Map() });
    return models.get(model);
  };
  const group = (/** @type {Record<string, any>} */ r) => JSON.stringify([r.run, r.block, r.model, r.lens]);
  const findingKey = (/** @type {Record<string, any>} */ r, /** @type {string} */ id) => JSON.stringify([r.run, r.block, r.file, id]);
  const rawGroups = new Set();
  for (const r of recent) {
    if (r.event !== 'session' || r.role !== 'reviewer' || typeof r.model !== 'string') continue;
    of(r.model).attempts.push({ ...r });
    rawGroups.add(group(r));
  }
  // A syntactically valid session can still fail the review schema (e.g. passed: null).
  // Its engine diagnostic corrects the most recent matching attempt, never adds a session.
  const diagnosed = new Set();
  for (const r of recent) {
    if (r.event !== 'review.schema_invalid' || r.role !== 'reviewer') continue;
    const attempts = models.get(r.model)?.attempts ?? [];
    const attempt = [...attempts].reverse().find((s) => !diagnosed.has(s)
      && group(s) === group(r) && Date.parse(s.ts) <= Date.parse(r.ts));
    if (attempt) {
      attempt.status = 'invalid-output';
      diagnosed.add(attempt);
    }
  }
  for (const r of recent) {
    if (r.event !== 'review.result' || !Array.isArray(r.sessions)) continue;
    const reviewers = r.sessions.filter((s) => s?.role === 'reviewer' && typeof s.model === 'string');
    for (const s of reviewers) {
      const data = of(s.model);
      if (!rawGroups.has(group({ ...r, ...s }))) data.attempts.push({ ...s });
      // Recheck result.findings can contain old, still-open findings. Only session findings
      // identify what this model raised; a judge's merged answer is not each reviewer's output.
      const findings = s.findings ?? [];
      for (const f of findings) {
        if (typeof f?.id !== 'string') continue;
        const id = s.section === undefined ? f.id : `S${s.section}.${f.id}`;
        data.findings.set(findingKey(r, id), f);
      }
    }
  }
  const waived = new Set();
  for (const r of recent) {
    if (r.event === 'review.waived') waived.add(findingKey(r, r.finding));
    if (r.event === 'review.unwaived') waived.delete(findingKey(r, r.finding));
  }
  /** @type {Record<string, ModelStats>} */
  const out = {};
  for (const [model, data] of models) {
    const seconds = data.attempts.map((s) => s.duration_ms)
      .filter((n) => typeof n === 'number' && Number.isFinite(n) && n >= 0).map((n) => n / 1000).sort((a, b) => a - b);
    const n = data.attempts.length;
    const mid = Math.floor(seconds.length / 2);
    out[model] = {
      sessions: n,
      median_seconds: seconds.length ? (seconds[mid] + seconds[Math.ceil(seconds.length / 2) - 1]) / 2 : null,
      p90_seconds: seconds.length ? seconds[Math.ceil(seconds.length * 0.9) - 1] : null,
      availability: n ? data.attempts.filter((s) => s.status === 'ok').length / n : 0,
      findings_per_session: n ? data.findings.size / n : 0,
      waived_share: data.findings.size ? [...data.findings.keys()].filter((id) => waived.has(id)).length / data.findings.size : 0,
      criticals: [...data.findings.values()].filter((f) => f.severity === 'critical').length,
    };
  }
  return out;
}

/**
 * Under-measured candidates keep configured order until exploration collects enough evidence.
 * Score uses median seconds; ties retain that same order. Hashing the ticket makes exploration
 * reproducible across worker restarts without a random seed or any mutable state.
 * @param {Array<Record<string, any>>} candidates
 * @param {Record<string, ModelStats>} stats
 * @param {{ticketId?: string, min_sessions?: number, explore?: number, enabled?: boolean}} [opts]
 * @returns {{candidate: Record<string, any>, reason: string}}
 */
export function pickReviewer(candidates, stats, { ticketId = '', min_sessions = 10, explore = 0.1, enabled = true } = {}) {
  if (candidates.length === 0) throw new TypeError('reviewer candidates must not be empty');
  const first = candidates[0];
  if (!enabled) return { candidate: first, reason: 'disabled' };
  const count = (/** @type {Record<string, any>} */ c) => stats[c.model]?.sessions ?? 0;
  const measured = candidates.filter((c) => count(c) >= min_sessions && stats[c.model]?.median_seconds !== null);
  if (measured.length === 0) return { candidate: first, reason: 'configured-order' };
  const least = Math.min(...candidates.map(count));
  const less = candidates.filter((c) => count(c) === least && count(c) < Math.max(...candidates.map(count)));
  const hash = createHash('sha256').update(ticketId).digest();
  if (less.length > 0 && hash.readUInt32BE(0) / 0x100000000 < explore) {
    return { candidate: less[hash.readUInt32BE(4) % less.length], reason: 'explore' };
  }
  if (!measured.includes(first)) return { candidate: first, reason: 'configured-order' };
  const score = (/** @type {Record<string, any>} */ c) => {
    const s = stats[c.model];
    return s.availability * (1 - s.waived_share) / Math.max(s.median_seconds, 0.001);
  };
  const best = measured.reduce((a, b) => score(b) > score(a) ? b : a);
  return { candidate: best, reason: 'performance' };
}

/** @param {Record<string, ModelStats>} stats @returns {string[]} human-only projection. */
export function reviewerLines(stats) {
  return Object.keys(stats).filter((model) => stats[model].median_seconds !== null).sort().map((model) => {
    const s = stats[model];
    return `reviewer ${model}: ${s.sessions} sessions, median ${s.median_seconds ?? '?'} s, ok ${Math.round(s.availability * 100)}%, waived ${Math.round(s.waived_share * 100)}%`;
  });
}
