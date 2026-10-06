/**
 * `forge plan check` — the deterministic harden exit (plan §3.2, §3.8 rule 4, §10.1; block B9b).
 *
 * Reads a plan's block tables (any markdown table whose header has `id`, `owned_files` and
 * `acceptance` columns; the wave is the nearest heading that says `Wave <n>`) and checks:
 *  - a facts sheet is present (pasted in the plan's §0 or given as `--facts`) and was built from
 *    the brief as it is now; a separate sheet's `brief_sha256` line must also appear in the plan;
 *  - every acceptance clause (the cell split on `;` and on ` · (n) ` clause markers, B50) whose
 *    claim tokens the sheet does not mark VERIFIED is listed in the "… the facts sheet cannot
 *    back" section by a line that names the token, the block, and a tolerance (a list item saying
 *    `tolerance`, or a row of an older-template table with a non-empty tolerance column) — else
 *    `unbackable clause without tolerance: <block> <clause>`;
 *  - a caller map section exists and names every block (C9);
 *  - the required sections are found by their exact headings (`plan-sections.mjs`); a near miss at
 *    the expected position is accepted with a WARN naming the exact heading (B36);
 *  - every block's level is the lane recorded for it in the ledger by `jev ask lane --block <id>`
 *    (Jev, or `--rules`, the deterministic fallback) — none, or a different one, is refused (B36);
 *  - every block has a lane, owned files that pass B8's `assertOwned` (no `[ ] ( ) ! + @` in a
 *    glob, V6) and are disjoint from the other blocks of its wave (O26);
 *  - `depends_on` is acyclic and every path a block imports (`imports` column) that another block
 *    owns belongs to one of its (transitive) dependencies — a batch-mate arrives as a parameter,
 *    never as an import (C17);
 *  - every block has a `cases` and a `lines` forecast within `budget.block_cases` /
 *    `budget.block_lines` (V1);
 *  - no step of a wave's "Dispatch order" runs more than `caps.coders` blocks concurrently (`∥`, R10).
 * Every failing row is reported by name; the verb exits 1 when there is any.
 */

import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { assertOwned, findOverlap, ownsFile } from '../state/registry.mjs';
import { extractClaims, parseSheet, sha256 } from './facts.mjs';
import { findSection, nearMissWarning, REQUIRED_SECTIONS } from './plan-sections.mjs';

/** Limits used when the config does not set them (plan v1.3 §10.7, R10). */
export const DEFAULT_LIMITS = Object.freeze({ coders: 2, blockCases: 80, blockLines: 2000 });

/**
 * @typedef {object} PlanBlock
 * @property {string} id @property {string} wave @property {string | null} level
 * @property {string[]} dependsOn @property {string[]} owned @property {string[]} imports
 * @property {string} acceptance @property {number | null} cases @property {number | null} lines
 */

/** @param {string} s */
const plain = (s) => s.replace(/\*\*/g, '').trim();

/** @param {string} s */
const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** @param {string} id @returns {RegExp} `id` as a whole word (`B1` does not match inside `B1.1` or `B10`). */
const idRe = (id) => new RegExp(`(?<![\\w.])${escapeRe(id)}(?!\\w|\\.\\w)`);

/** @param {string} line @returns {string[]} the cells of a markdown table row (`\|` stays inside a cell). */
function cells(line) {
  const parts = line.trim().split(/(?<!\\)\|/);
  return parts.slice(1, parts.length - 1).map((c) => c.trim());
}

/** @param {string} cell @returns {string[]} code spans, else comma-separated words; `—`/`none` ⇒ []. */
function listCell(cell) {
  const spans = [...cell.matchAll(/`([^`]+)`/g)].map((m) => m[1].trim());
  if (spans.length > 0) return spans;
  return plain(cell)
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0 && !['—', '-', 'none'].includes(s.toLowerCase()));
}

/** @param {string | undefined} raw @returns {number | null} */
function forecastNumber(raw) {
  if (raw === undefined) return null;
  const text = plain(raw).replace(/\(.*?\)/g, '').replace(/[\s   ]/g, '');
  return /^\d+$/.test(text) ? Number(text) : null;
}

/**
 * @param {string} text
 * @returns {{blocks: PlanBlock[], sections: Array<{title: string, level: number, body: string}>, dispatch: Array<{wave: string, text: string}>}}
 */
export function parsePlan(text) {
  const lines = text.split(/\r?\n/);
  /** @type {PlanBlock[]} */
  const blocks = [];
  /** @type {Array<{title: string, level: number, start: number, end: number}>} */
  const heads = [];
  /** @type {Array<{wave: string, text: string}>} */
  const dispatch = [];
  let wave = 'default';
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    const head = /^(#{1,6})\s+(.*)$/.exec(line);
    if (head) {
      heads.push({ title: plain(head[2]), level: head[1].length, start: i + 1, end: lines.length });
      const w = /\bWave\s+([\w.]+)/i.exec(head[2]);
      if (w) wave = w[1];
      continue;
    }
    if (/dispatch order[^:]*:/i.test(plain(line))) {
      const after = plain(line).replace(/^.*?dispatch order[^:]*:/i, '');
      dispatch.push({ wave, text: after });
      continue;
    }
    if (!line.trim().startsWith('|') || i + 1 >= lines.length || !/^\s*\|[\s|:-]+\|\s*$/.test(lines[i + 1])) continue;
    const header = cells(line).map((h) => plain(h).toLowerCase());
    const col = (/** @type {string} */ name) => header.findIndex((h) => h === name || h.startsWith(`${name} `) || h.startsWith(`${name}(`));
    const idCol = col('id');
    const ownedCol = header.findIndex((h) => h.startsWith('owned_files'));
    const accCol = header.findIndex((h) => h.startsWith('acceptance'));
    if (idCol < 0 || ownedCol < 0 || accCol < 0) continue;
    const forecastCol = header.findIndex((h) => /\bcases\b/.test(h) && h.includes('→'));
    const parts = forecastCol >= 0 ? header[forecastCol].split('→').map((p) => p.trim()) : [];
    let j = i + 2;
    for (; j < lines.length && lines[j].trim().startsWith('|'); j += 1) {
      const row = cells(lines[j]);
      const at = (/** @type {number} */ c) => (c >= 0 ? row[c] ?? '' : '');
      const id = plain(at(idCol));
      if (id.length === 0) continue;
      const forecast = forecastCol >= 0 ? at(forecastCol).split('→') : [];
      const pick = (/** @type {string} */ name) => {
        const own = col(name);
        if (own >= 0 && own !== forecastCol) return forecastNumber(at(own));
        const k = parts.indexOf(name);
        return k >= 0 ? forecastNumber(forecast[k]) : null;
      };
      const level = /\bL[0-3]\b/.exec(plain(at(col('level'))));
      blocks.push({
        id,
        wave,
        level: level ? level[0] : null,
        dependsOn: plain(at(col('depends_on')))
          .split(/[,\s]+/)
          .filter((s) => s.length > 0 && !['—', '-', 'none'].includes(s.toLowerCase())),
        owned: listCell(at(ownedCol)),
        imports: listCell(at(col('imports'))),
        acceptance: at(accCol),
        cases: pick('cases'),
        lines: pick('lines'),
      });
    }
    i = j - 1;
  }
  const sections = heads.map((h, k) => {
    const next = heads.slice(k + 1).find((n) => n.level <= h.level);
    return { title: h.title, level: h.level, body: lines.slice(h.start, next ? next.start - 1 : lines.length).join('\n') };
  });
  return { blocks, sections, dispatch };
}

/**
 * @param {PlanBlock[]} blocks
 * @returns {string[]} one message per cycle found in `depends_on` (among the plan's own blocks).
 */
function dependencyCycles(blocks) {
  const byId = new Map(blocks.map((b) => [b.id, b]));
  const state = new Map();
  /** @type {string[]} */
  const out = [];
  /** @param {string} id @param {string[]} trail */
  const visit = (id, trail) => {
    if (state.get(id) === 'done') return;
    if (state.get(id) === 'open') {
      out.push(`depends_on cycle: ${[...trail.slice(trail.indexOf(id)), id].join(' → ')}`);
      return;
    }
    state.set(id, 'open');
    for (const dep of byId.get(id)?.dependsOn ?? []) if (byId.has(dep)) visit(dep, [...trail, id]);
    state.set(id, 'done');
  };
  for (const b of blocks) visit(b.id, []);
  return out;
}

/** @param {PlanBlock[]} blocks @param {string} id @returns {Set<string>} every (transitive) dependency of `id`. */
function closure(blocks, id) {
  const byId = new Map(blocks.map((b) => [b.id, b]));
  const seen = new Set();
  const stack = [...(byId.get(id)?.dependsOn ?? [])];
  while (stack.length > 0) {
    const dep = /** @type {string} */ (stack.pop());
    if (seen.has(dep)) continue;
    seen.add(dep);
    stack.push(...(byId.get(dep)?.dependsOn ?? []));
  }
  return seen;
}

/** @param {string} body @returns {string[]} the list items of a section. */
const listItems = (body) =>
  body
    .split('\n')
    .filter((l) => /^\s*(?:[-*]|\d+\.)\s+/.test(l))
    .map((l) => l.trim());

/**
 * The tolerance lines of the unbackable-clauses section (B50): every list item that says
 * `tolerance` (the form `templates/plan.md` shows), plus — so plans written from the older table
 * template keep passing — every data row of a table in the section whose HEADER (its first row,
 * never a later one) has a `tolerance` cell, when that row's tolerance cell is filled: empty and
 * the placeholders in {@link EMPTY_CELLS} do not count.
 * @param {string} body @returns {string[]}
 */
export function toleranceLines(body) {
  const out = listItems(body).filter((l) => /\btolerance\b/i.test(l));
  /** @type {number | null} null: not in a table; -1: in a table without a tolerance column. */
  let col = null;
  for (const raw of body.split('\n')) {
    const line = raw.trim();
    if (!line.startsWith('|')) {
      col = null;
      continue;
    }
    const row = cells(line);
    if (col === null) {
      col = row.findIndex((c) => /\btolerance\b/i.test(plain(c)));
      continue;
    }
    if (col < 0 || row.every((c) => /^:?-+:?$/.test(c))) continue; // no tolerance column, or the separator row
    if (!EMPTY_CELLS.has(plain(row[col] ?? '').toLowerCase())) out.push(line);
  }
  return out;
}

/** Tolerance cells that say nothing (compared lower-cased, after trimming). */
const EMPTY_CELLS = new Set(['', '—', '–', '-', '--', '…', '...', 'n/a']);

/**
 * Is a clause's claim backed by a VERIFIED fact? Its token is, or (B50) a command claim written
 * without its CLI (`run reload`) matches the sheet's token that `forge facts` prefixed with the
 * project's CLI (`code-forge run reload`: exactly one more leading word, and that word one of the
 * project's own CLIs — `projectBins`).
 * @param {{token: string, kind: string}} claim @param {ReadonlySet<string>} verified
 * @param {() => ReadonlyArray<string>} projectBins - called only when a command claim needs it.
 * @returns {boolean}
 */
function isVerified(claim, verified, projectBins) {
  if (verified.has(claim.token)) return true;
  if (claim.kind !== 'command') return false;
  return projectBins().some((bin) => verified.has(`${bin} ${claim.token}`));
}

/**
 * The clauses of an acceptance cell (B50): split on `;` and on a ` · (n) ` clause marker (the
 * template numbers clauses `(1) … · (2) …`); a `·` not followed by `(n)` stays inside its clause.
 * @param {string} acceptance @returns {string[]}
 */
export function splitClauses(acceptance) {
  return acceptance
    .split(/;|\s·\s+(?=\(\d+\))/)
    .map((c) => plain(c))
    .filter((c) => c.length > 0);
}

/** Where a lane may come from: Jev's `lane` answer, or the deterministic fallback rule (`--rules`). */
export const LANE_SOURCES = Object.freeze(['jev', 'rules']);

/**
 * @typedef {{lane: string, source: string, decision_id: string | null}} RecordedLane
 */

/**
 * A recorded lane answer, normalised: trimmed, upper-cased, the first `L0`–`L3` token (`" l1 "` ⇒
 * `L1`); an answer with no such token stays as written, trimmed (`split`).
 * @param {string} answer @returns {string}
 */
export function normaliseLane(answer) {
  const token = /\bL[0-3]\b/.exec(answer.trim().toUpperCase());
  return token ? token[0] : answer.trim();
}

/**
 * The lane recorded per block by `forge jev ask lane --block <id>` (or `--rules`): the latest
 * `decision` row for `lane` whose `block` is set and whose `source` is Jev or the fallback rules.
 * A row that names a plan (`--plan`) counts only for the plan with that base name (both sides are
 * compared as `path.basename`); with no plan name given, only untagged rows count.
 * @param {Array<Record<string, any>>} rows - ledger rows, oldest first.
 * @param {string} [planName] - the plan file's base name.
 * @returns {Map<string, RecordedLane>}
 */
export function recordedLanes(rows, planName) {
  /** @type {Map<string, RecordedLane>} */
  const out = new Map();
  for (const row of rows) {
    if (row?.event !== 'decision' || row.question !== 'lane' || typeof row.block !== 'string') continue;
    if (!LANE_SOURCES.includes(row.source) || typeof row.answer !== 'string') continue;
    if (row.plan !== undefined && (planName === undefined || typeof row.plan !== 'string' || path.basename(row.plan) !== path.basename(planName))) continue;
    out.set(row.block, { lane: normaliseLane(row.answer), source: row.source, decision_id: typeof row.decision_id === 'string' ? row.decision_id : null });
  }
  return out;
}

/**
 * @typedef {object} CheckOpts
 * @property {string} [planPath] - where the plan lives (resolves the sheet's brief reference).
 * @property {string} [factsText] @property {string} [factsPath] - a separate sheet (else the plan's own §0).
 * @property {Record<string, any>} [cfg] - `caps.coders`, `budget.block_cases`, `budget.block_lines`.
 * @property {ReadonlyArray<string> | (() => ReadonlyArray<string>)} [projectBins] - B50: the
 *   project's own CLI names (`readProjectBins` of the project root), or a function giving them —
 *   called at most once, and only when a command claim is not VERIFIED as written; a clause's
 *   `run reload` is then backed by a VERIFIED `<bin> run reload`.
 * @property {Array<Record<string, any>>} [decisions] - the project's ledger rows (the recorded lanes);
 *   none given ⇒ no block has a recorded lane.
 */

/**
 * @param {string} text - the plan.
 * @param {CheckOpts} [opts]
 * @returns {{ok: boolean, errors: string[], warnings: string[], blocks: number, lanes: Array<{block: string, level: string | null, lane: string | null, source: string | null}>}}
 */
export function checkPlan(text, opts = {}) {
  const limits = {
    coders: opts.cfg?.caps?.coders ?? DEFAULT_LIMITS.coders,
    blockCases: opts.cfg?.budget?.block_cases ?? DEFAULT_LIMITS.blockCases,
    blockLines: opts.cfg?.budget?.block_lines ?? DEFAULT_LIMITS.blockLines,
  };
  const { blocks, sections, dispatch } = parsePlan(text);
  /** @type {string[]} */
  const errors = [];
  if (blocks.length === 0) return { ok: false, errors: ['no block table found (a table with id, owned_files and acceptance columns)'], warnings: [], blocks: 0, lanes: [] };

  // facts sheet: present, fresh, and (when separate) pasted into the plan
  const sheetText = opts.factsText ?? text;
  const sheet = parseSheet(sheetText);
  if (!sheet) {
    errors.push('facts sheet missing: run forge facts and paste the sheet into the plan §0');
  } else {
    if (opts.factsText !== undefined && !text.includes(`brief_sha256: \`${sheet.briefSha}\``)) errors.push('the plan §0 is not the facts sheet: its brief_sha256 is absent from the plan');
    const base = opts.factsPath ?? opts.planPath;
    if (base !== undefined) {
      const brief = path.resolve(path.dirname(base), sheet.briefRef);
      if (!existsSync(brief)) errors.push(`facts sheet brief not found: ${sheet.briefRef}`);
      else if (sha256(readFileSync(brief)) !== sheet.briefSha) errors.push('facts sheet is stale: brief changed after it was built');
    }
  }
  const verified = new Set((sheet?.facts ?? []).filter((f) => f.tag === 'VERIFIED').map((f) => f.claim));

  // the required sections, by their exact headings (B36: a near miss at the expected position is a WARN)
  /** @type {string[]} */
  const warnings = [];
  /** @param {import('./plan-sections.mjs').RequiredSection} spec */
  const required = (spec) => {
    const hit = findSection(sections, spec);
    if (hit && !hit.exact) warnings.push(nearMissWarning(hit.section.title, spec));
    return hit?.section;
  };

  // unbackable clauses
  const unbackable = required(REQUIRED_SECTIONS.unbackable);
  if (!unbackable) errors.push('unbackable-clauses section missing (write "none" when there is none)');
  const tolerances = unbackable ? toleranceLines(unbackable.body) : [];
  /** @type {ReadonlyArray<string> | undefined} */
  let binsCache;
  const given = opts.projectBins;
  const projectBins = () => (binsCache ??= typeof given === 'function' ? given() : given ?? []);
  for (const b of blocks) {
    for (const clause of splitClauses(b.acceptance)) {
      const unbacked = extractClaims(clause).filter((c) => !isVerified(c, verified, projectBins));
      if (unbacked.every((c) => tolerances.some((t) => t.includes(c.token) && idRe(b.id).test(t)))) continue;
      errors.push(`unbackable clause without tolerance: ${b.id} ${clause}`);
    }
  }

  // caller map
  const callerMap = required(REQUIRED_SECTIONS.callerMap);
  if (!callerMap) errors.push('caller map missing');
  else for (const b of blocks) if (!idRe(b.id).test(callerMap.body)) errors.push(`caller map: block ${b.id} is absent`);

  // lane (B36: the level is the lane recorded for the block), owned files, forecasts
  const lanes = recordedLanes(opts.decisions ?? [], opts.planPath === undefined ? undefined : path.basename(opts.planPath));
  for (const b of blocks) {
    const recorded = lanes.get(b.id);
    if (b.level === null) errors.push(`block ${b.id}: no lane (level L0–L3)`);
    else if (!recorded) errors.push(`block ${b.id}: level ${b.level} has no recorded lane decision — run forge jev ask lane --block ${b.id} --state <file> (or --rules when Jev is unavailable)`);
    else if (recorded.lane !== b.level) errors.push(`block ${b.id}: level ${b.level} differs from the recorded lane ${recorded.lane} (${recorded.source})`);
    try {
      assertOwned(b.owned);
    } catch (err) {
      errors.push(`block ${b.id}: ${/** @type {Error} */ (err).message}`);
    }
    if (b.cases === null) errors.push(`block ${b.id}: no cases forecast`);
    else if (b.cases > limits.blockCases) errors.push(`block ${b.id}: cases forecast ${b.cases} exceeds budget.block_cases ${limits.blockCases} — split it`);
    if (b.lines === null) errors.push(`block ${b.id}: no lines forecast`);
    else if (b.lines > limits.blockLines) errors.push(`block ${b.id}: lines forecast ${b.lines} exceeds budget.block_lines ${limits.blockLines} — split it`);
  }

  // disjoint owned files per wave
  for (let i = 0; i < blocks.length; i += 1) {
    for (let j = i + 1; j < blocks.length; j += 1) {
      const [a, b] = [blocks[i], blocks[j]];
      if (a.wave !== b.wave) continue;
      const hit = findOverlap(a.owned, b.owned);
      if (hit) errors.push(`blocks ${a.id} and ${b.id} (wave ${a.wave}) overlap: ${hit[0]} / ${hit[1]}`);
    }
  }

  // depends_on and the seam rule
  errors.push(...dependencyCycles(blocks));
  for (const b of blocks) {
    const deps = closure(blocks, b.id);
    for (const imported of b.imports) {
      const owner = blocks.find((o) => o.id !== b.id && ownsFile(o.owned, imported));
      if (owner && !deps.has(owner.id)) errors.push(`block ${b.id} imports ${imported} owned by ${owner.id}, which is not in its depends_on`);
    }
  }

  // dispatch slots (R10)
  const ids = blocks.map((b) => b.id);
  for (const { wave, text: order } of dispatch) {
    order.split('→').forEach((step, k) => {
      const running = step
        .split('∥')
        .map((seg) => {
          const hits = ids.map((id) => ({ id, at: seg.search(idRe(id)) })).filter((h) => h.at >= 0);
          return hits.sort((x, y) => x.at - y.at)[0]?.id;
        })
        .filter((id) => id !== undefined);
      if (running.length > limits.coders) errors.push(`dispatch step ${k + 1} (wave ${wave}) runs ${running.length} blocks concurrently (caps.coders ${limits.coders}): ${running.join(' ∥ ')}`);
    });
  }

  return {
    ok: errors.length === 0,
    errors,
    warnings,
    blocks: blocks.length,
    lanes: blocks.map((b) => ({ block: b.id, level: b.level, lane: lanes.get(b.id)?.lane ?? null, source: lanes.get(b.id)?.source ?? null })),
  };
}
