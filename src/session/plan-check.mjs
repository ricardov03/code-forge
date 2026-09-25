/**
 * `forge plan check` — the deterministic harden exit (plan §3.2, §3.8 rule 4, §10.1; block B9b).
 *
 * Reads a plan's block tables (any markdown table whose header has `id`, `owned_files` and
 * `acceptance` columns; the wave is the nearest heading that says `Wave <n>`) and checks:
 *  - a facts sheet is present (pasted in the plan's §0 or given as `--facts`) and was built from
 *    the brief as it is now; a separate sheet's `brief_sha256` line must also appear in the plan;
 *  - every acceptance clause (the cell split on `;`) whose claim tokens the sheet does not mark
 *    VERIFIED is listed in the "… the facts sheet cannot back" section by a line that names the
 *    token, the block, and a tolerance — else `unbackable clause without tolerance: <block> <clause>`;
 *  - a caller map section exists and names every block (C9);
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
 * @typedef {object} CheckOpts
 * @property {string} [planPath] - where the plan lives (resolves the sheet's brief reference).
 * @property {string} [factsText] @property {string} [factsPath] - a separate sheet (else the plan's own §0).
 * @property {Record<string, any>} [cfg] - `caps.coders`, `budget.block_cases`, `budget.block_lines`.
 */

/**
 * @param {string} text - the plan.
 * @param {CheckOpts} [opts]
 * @returns {{ok: boolean, errors: string[], blocks: number}}
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
  if (blocks.length === 0) return { ok: false, errors: ['no block table found (a table with id, owned_files and acceptance columns)'], blocks: 0 };

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

  // unbackable clauses
  const unbackable = sections.find((s) => /cannot back/i.test(s.title));
  if (!unbackable) errors.push('unbackable-clauses section missing (write "none" when there is none)');
  const tolerances = unbackable ? listItems(unbackable.body).filter((l) => /\btolerance\b/i.test(l)) : [];
  for (const b of blocks) {
    for (const clause of b.acceptance.split(';').map((c) => plain(c)).filter((c) => c.length > 0)) {
      const unbacked = extractClaims(clause).filter((c) => !verified.has(c.token));
      if (unbacked.every((c) => tolerances.some((t) => t.includes(c.token) && idRe(b.id).test(t)))) continue;
      errors.push(`unbackable clause without tolerance: ${b.id} ${clause}`);
    }
  }

  // caller map
  const callerMap = sections.find((s) => /caller map/i.test(s.title));
  if (!callerMap) errors.push('caller map missing');
  else for (const b of blocks) if (!idRe(b.id).test(callerMap.body)) errors.push(`caller map: block ${b.id} is absent`);

  // lane, owned files, forecasts
  for (const b of blocks) {
    if (b.level === null) errors.push(`block ${b.id}: no lane (level L0–L3)`);
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

  return { ok: errors.length === 0, errors, blocks: blocks.length };
}
