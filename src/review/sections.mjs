/**
 * Markdown section packets (block B54, issue #2 field note).
 *
 * A Markdown file (`.md`, `.markdown`) whose diff ALONE is over the packet budget
 * (`split_required`) is reviewed section by section instead of refused:
 *  1. the headings of the CURRENT file are found (`#`..`######`, ATX; a heading inside a fenced
 *     code block — ``` or ~~~ — is not one);
 *  2. every hunk is cut at each heading line it carries on its new side (a context or `+` line), so
 *     a new or rewritten document — ONE hunk — splits too. Each piece is a valid unified-diff hunk
 *     with its own header (`@@ -a,b +c,d @@`, real line numbers on both sides); a hunk that holds no
 *     heading keeps its own header. A piece with no `+`/`-` line joins its neighbour;
 *  3. the pieces are grouped by the nearest preceding heading (lines before the first heading are
 *     one group), and consecutive groups are packed, in file order, into section packets whose
 *     diff-only packet stays within the budget — splits happen only at headings.
 * One group that alone is still over the budget ⇒ the file stays `split_required` (with that
 * group's `tokensIn` and its heading): nothing is ever truncated. A diff with no hunk, or whose
 * hunks do not parse one to one, never yields a section: it is `split_required` too — and so,
 * fail-safe, is a deleted file or one with no heading whose diff is over the budget (one group). Any other file keeps
 * `split_required`. Pure functions: no I/O.
 */

import { parseDiff, splitLines } from './context.mjs';

/** @typedef {import('./packet.mjs').FileDiff} FileDiff */

/** @param {string} file @returns {boolean} whether `file` is reviewed by section when over budget. */
export const isMarkdownFile = (file) => /\.(md|markdown)$/i.test(file);

/** A fence opener/closer: up to 3 spaces, then 3+ backticks or tildes. */
const FENCE_RE = /^ {0,3}(`{3,}|~{3,})(.*)$/;
/** An ATX heading: up to 3 spaces, 1–6 `#`, then a space/tab or the end of the line. */
const HEADING_RE = /^ {0,3}#{1,6}(?:[ \t]|$)/;
/** A heading is named in results by its text, cut at this many characters. */
const HEADING_MAX = 120;

/**
 * The ATX headings of `content`, outside fenced code blocks and outside a YAML front matter block.
 * @param {string | null} content @returns {Map<number, string>} 1-based line ⇒ the heading line, trimmed.
 */
export function markdownHeadings(content) {
  /** @type {Map<number, string>} */
  const out = new Map();
  /** @type {{char: string, len: number} | null} */
  let fence = null;
  const lines = splitLines(content).map((raw) => (raw.endsWith('\r') ? raw.slice(0, -1) : raw)); // a CRLF file matches like an LF one
  // YAML front matter (`---` on line 1 … a closing `---` or `...`) holds no heading
  const close = lines[0] === '---' ? lines.findIndex((l, i) => i > 0 && (l === '---' || l === '...')) : -1;
  lines.forEach((line, i) => {
    if (i <= close) return;
    const f = FENCE_RE.exec(line);
    if (fence) {
      // a closing fence: the same character, at least as long, nothing after it
      if (f && f[1][0] === fence.char && f[1].length >= fence.len && f[2].trim() === '') fence = null;
      return;
    }
    // a backtick fence whose info string carries a backtick is inline code, not a fence
    if (f && !(f[1][0] === '`' && f[2].includes('`'))) {
      fence = { char: f[1][0], len: f[1].length };
      return;
    }
    if (HEADING_RE.test(line)) out.set(i + 1, line.trim().slice(0, HEADING_MAX));
  });
  return out;
}

/**
 * @typedef {object} Piece - one hunk, or one heading-bounded part of a hunk.
 * @property {number} key - the line of the nearest preceding heading (0 = before the first).
 * @property {string | null} header - the original header line when the piece is the whole hunk.
 * @property {number} oldCursor - the first old-side line the piece covers.
 * @property {number} newCursor - the first new-side line the piece covers.
 * @property {string[]} lines - the body lines (` `, `+`, `-`, `\`).
 */

/** @param {Piece} p @returns {boolean} whether the piece changes anything. */
const changes = (p) => p.lines.some((l) => l.startsWith('+') || l.startsWith('-'));

/** @param {Map<number, string>} headings @param {number} line @returns {number} the last heading at or before `line`, else 0. */
function headingAtOrBefore(headings, line) {
  let best = 0;
  for (const h of headings.keys()) if (h <= line && h > best) best = h;
  return best;
}

/**
 * Cut one hunk at the heading lines on its new side.
 * @param {{header: string, oldStart: number, oldLines: number, newStart: number, newLines: number}} hunk
 * @param {string} headerLine - the hunk's header line as git printed it.
 * @param {string[]} body @param {Map<number, string>} headings
 * @returns {Piece[]}
 */
function cutHunk(hunk, headerLine, body, headings) {
  let oldCursor = hunk.oldLines === 0 ? hunk.oldStart + 1 : hunk.oldStart;
  let newCursor = hunk.newLines === 0 ? hunk.newStart + 1 : hunk.newStart;
  const firstNew = body.length > 0 && (body[0].startsWith(' ') || body[0].startsWith('+'));
  /** @type {Piece[]} */
  const pieces = [{ key: headingAtOrBefore(headings, firstNew ? newCursor : newCursor - 1), header: headerLine, oldCursor, newCursor, lines: [] }];
  for (const line of body) {
    const onNew = line.startsWith(' ') || line.startsWith('+');
    const current = /** @type {Piece} */ (pieces.at(-1));
    if (onNew && headings.has(newCursor) && current.lines.some((l) => !l.startsWith('\\'))) {
      pieces.push({ key: newCursor, header: null, oldCursor, newCursor, lines: [] });
    }
    /** @type {Piece} */ (pieces.at(-1)).lines.push(line);
    if (line.startsWith(' ')) {
      oldCursor += 1;
      newCursor += 1;
    } else if (line.startsWith('+')) {
      newCursor += 1;
    } else if (line.startsWith('-')) {
      oldCursor += 1;
    }
  }
  if (pieces.length === 1) return pieces;
  // a piece that only carries context joins the next piece (or, when last, the previous one)
  for (let i = 0; i < pieces.length && pieces.length > 1; ) {
    const p = pieces[i];
    if (changes(p)) {
      i += 1;
      continue;
    }
    const next = pieces[i + 1];
    if (next) pieces[i + 1] = { ...next, oldCursor: p.oldCursor, newCursor: p.newCursor, lines: [...p.lines, ...next.lines] };
    else pieces[i - 1].lines.push(...p.lines);
    pieces.splice(i, 1);
  }
  for (const p of pieces) p.header = null;
  if (pieces.length === 1) pieces[0].header = headerLine;
  return pieces;
}

/** @param {Piece} p @returns {string} the piece's header line. */
function headerOf(p) {
  if (p.header !== null) return p.header;
  const oldCount = p.lines.filter((l) => l.startsWith(' ') || l.startsWith('-')).length;
  const newCount = p.lines.filter((l) => l.startsWith(' ') || l.startsWith('+')).length;
  // an empty side names the line BEFORE the change, as git does (`-0,0` for a new file)
  return `@@ -${oldCount === 0 ? p.oldCursor - 1 : p.oldCursor},${oldCount} +${newCount === 0 ? p.newCursor - 1 : p.newCursor},${newCount} @@`;
}

/**
 * @typedef {object} SectionGroup
 * @property {number} key - the heading line (0 = before the first heading).
 * @property {string} heading - the heading line, or `(before first heading)`.
 * @property {FileDiff} diff - the group's own diff (the file header, then its pieces).
 */

/**
 * Split a Markdown file's diff into heading groups (steps 1–3 of the module doc, before packing).
 * @param {FileDiff} diff
 * @returns {SectionGroup[] | null} in file order (one group when the file has no heading; none when
 *   the diff has no hunk); null when the `@@` lines of the text do not match `diff.hunks` one to
 *   one — a hunk is never dropped silently.
 */
export function sectionGroups(diff) {
  const headings = markdownHeadings(diff.content);
  const all = diff.diffText.split('\n');
  if (all.at(-1) === '') all.pop();
  const first = all.findIndex((l) => l.startsWith('@@'));
  if (first < 0) return diff.hunks.length === 0 ? [] : null;
  const fileHeader = all.slice(0, first);
  /** @type {Piece[]} */
  const pieces = [];
  let h = 0;
  for (let i = first; i < all.length; ) {
    let end = i + 1;
    while (end < all.length && !all[end].startsWith('@@')) end += 1;
    const hunk = diff.hunks[h];
    if (!hunk || !all[i].startsWith(hunk.header)) return null;
    h += 1;
    pieces.push(...cutHunk(hunk, all[i], all.slice(i + 1, end), headings));
    i = end;
  }
  if (h !== diff.hunks.length) return null;
  /** @type {Array<{key: number, pieces: Piece[]}>} */
  const groups = [];
  for (const p of pieces) {
    const last = groups.at(-1);
    if (last && last.key === p.key) last.pieces.push(p);
    else groups.push({ key: p.key, pieces: [p] });
  }
  return groups.map((g) => ({ key: g.key, heading: headings.get(g.key) ?? '(before first heading)', diff: diffOf(diff, fileHeader, g.pieces) }));
}

/**
 * @param {FileDiff} diff @param {string[]} fileHeader @param {Piece[]} pieces
 * @returns {FileDiff} the pieces as one file diff (the hunk list re-parsed from its own text).
 */
function diffOf(diff, fileHeader, pieces) {
  const diffText = `${[...fileHeader, ...pieces.flatMap((p) => [headerOf(p), ...p.lines])].join('\n')}\n`;
  return { file: diff.file, kind: diff.kind, diffText, content: diff.content, ...parseDiff(diffText) };
}

/**
 * @typedef {object} Section
 * @property {number} index - 1-based, in file order.
 * @property {string[]} headings - the headings the section covers.
 * @property {number[]} keys - the heading lines of its groups (0 = before the first heading).
 * @property {FileDiff} diff
 */

/**
 * Pack the heading groups into section packets within `budget` (step 3 of the module doc). A
 * candidate's size is `measure(its diff, its groups)` (a recheck adds the open findings it lists
 * for those groups). Never `ok` with no section: a diff with no hunk, or one whose hunks do not
 * parse one to one, is `split_required` at `measure(diff, [])` with `section: null`.
 * @param {{diff: FileDiff, budget: number, measure: (diff: FileDiff, groups: SectionGroup[]) => number}} opts -
 *   `measure`: the packet tokens of a candidate (the largest over the session lenses).
 * @returns {{status: 'ok', sections: Section[]} | {status: 'split_required', tokensIn: number, budget: number, section: string | null}}
 */
export function packSections({ diff, budget, measure }) {
  const groups = sectionGroups(diff);
  if (groups === null || groups.length === 0) return { status: 'split_required', tokensIn: measure(diff, []), budget, section: null };
  /** @param {SectionGroup[]} set */
  const size = (set) => measure(merge(diff, set), set);
  /** @type {Section[]} */
  const sections = [];
  /** @type {SectionGroup[]} */
  let open = [];
  const flush = () => {
    if (open.length === 0) return;
    sections.push({ index: sections.length + 1, headings: open.map((g) => g.heading), keys: open.map((g) => g.key), diff: merge(diff, open) });
    open = [];
  };
  for (const g of groups) {
    const alone = size([g]);
    if (alone > budget) return { status: 'split_required', tokensIn: alone, budget, section: g.heading };
    if (open.length > 0 && size([...open, g]) > budget) flush();
    open.push(g);
  }
  flush();
  return { status: 'ok', sections };
}

/**
 * @param {FileDiff} diff @param {SectionGroup[]} groups @returns {FileDiff} the groups as one diff.
 */
function merge(diff, groups) {
  if (groups.length === 1) return groups[0].diff;
  const bodies = groups.map((g) => {
    const text = g.diff.diffText;
    return text.slice(text.startsWith('@@') ? 0 : text.indexOf('\n@@') + 1);
  });
  const head = groups[0].diff.diffText;
  const fileHeader = head.startsWith('@@') ? '' : head.slice(0, head.indexOf('\n@@') + 1);
  const diffText = `${fileHeader}${bodies.join('')}`;
  return { file: diff.file, kind: diff.kind, diffText, content: diff.content, ...parseDiff(diffText) };
}

/**
 * Where an open finding is listed in a sectioned recheck (each open finding in exactly ONE packet).
 * Contract: with at least one section, always a valid position — the section whose hunks span the
 * finding's first line, else the nearest section by line distance (ties go to the earlier one), so
 * a finding outside every section (a late finding kept open by `review.late_findings: block`, a
 * line that moved in the fix) still lands in one; -1 only when `sections` is empty.
 * @param {ReadonlyArray<{diff: FileDiff}>} sections @param {{line_start: number}} finding
 * @returns {number} the position in `sections`, or -1.
 */
export function sectionOf(sections, finding) {
  let best = -1;
  let bestDistance = Number.POSITIVE_INFINITY;
  sections.forEach((s, i) => {
    const start = Math.min(...s.diff.hunks.map((h) => h.newStart));
    const end = Math.max(...s.diff.hunks.map((h) => h.newStart + Math.max(h.newLines, 1) - 1));
    const line = finding.line_start;
    const distance = line < start ? start - line : line > end ? line - end : 0;
    if (distance < bestDistance) {
      best = i;
      bestDistance = distance;
    }
  });
  return best;
}

/**
 * A section's findings with ids unique per file: `S<index>.<id>`.
 * @template {{id: string}} F
 * @param {ReadonlyArray<F>} findings @param {number} index @returns {F[]}
 */
export function sectionFindings(findings, index) {
  return findings.map((f) => ({ ...f, id: `S${index}.${f.id}` }));
}
