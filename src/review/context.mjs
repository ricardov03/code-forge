/**
 * Packet context (plan §4.3, C10; block B12a).
 *
 * A reviewer sees the current file around the diff, never the project. The rule:
 *  - `whole`: the file at its current content has ≤ `review.context.whole_file_max_lines` (400)
 *    lines ⇒ the whole file, every line numbered;
 *  - `hunks`: otherwise each hunk with ± `review.context.hunk_context_lines` (40) lines of the
 *    current file (overlapping windows merge);
 *  - `minimal`: the budget fallback ⇒ each hunk with ± `review.context.min_context_lines` (10);
 *  - `recheck`: a round ≥ 2 packet (§4.11) ⇒ always the hunk form on the fix hunks, never the
 *    whole file.
 * Lines are numbered with their line number in the CURRENT file, so a finding's `line_start`
 * points at a real line. Pure functions: no I/O.
 */

export const CONTEXT_MODES = Object.freeze(['whole', 'hunks', 'minimal', 'recheck']);

export const DEFAULT_CONTEXT = Object.freeze({ whole_file_max_lines: 400, hunk_context_lines: 40, min_context_lines: 10 });

/** `@@ -a[,b] +c[,d] @@` — the part of a hunk header a reviewer must echo (no function context). */
const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

/**
 * @typedef {{header: string, oldStart: number, oldLines: number, newStart: number, newLines: number}} Hunk
 */

/**
 * Parse a unified diff of ONE file. The file header (`diff --git`, `index`, `---`, `+++`) is
 * skipped by position — everything before the first `@@` — so a content line that starts with
 * `++` or `--` is never mistaken for a header.
 * @param {string} text
 * @returns {{hunks: Hunk[], plusCount: number, minusCount: number}}
 */
export function parseDiff(text) {
  const lines = typeof text === 'string' ? text.split('\n') : [];
  const first = lines.findIndex((line) => line.startsWith('@@'));
  /** @type {Hunk[]} */
  const hunks = [];
  let plusCount = 0;
  let minusCount = 0;
  if (first < 0) return { hunks, plusCount, minusCount };
  for (const line of lines.slice(first)) {
    if (line.startsWith('@@')) {
      const m = HUNK_RE.exec(line);
      if (!m) continue;
      hunks.push({
        header: m[0],
        oldStart: Number(m[1]),
        oldLines: m[2] === undefined ? 1 : Number(m[2]),
        newStart: Number(m[3]),
        newLines: m[4] === undefined ? 1 : Number(m[4]),
      });
    } else if (line.startsWith('+')) {
      plusCount += 1;
    } else if (line.startsWith('-')) {
      minusCount += 1;
    }
  }
  return { hunks, plusCount, minusCount };
}

/**
 * The file's lines (a final newline does not make an extra empty line).
 * @param {string | null} content
 * @returns {string[]}
 */
export function splitLines(content) {
  if (typeof content !== 'string' || content.length === 0) return [];
  const lines = content.split('\n');
  if (lines.at(-1) === '') lines.pop();
  return lines;
}

/**
 * One window per hunk over the CURRENT file: `[newStart − radius, newStart + max(newLines, 1) − 1
 * + radius]`, clipped to the file; overlapping or touching windows merge.
 * @param {ReadonlyArray<Hunk>} hunks @param {number} totalLines @param {number} radius
 * @returns {Array<{start: number, end: number}>} 1-based, inclusive.
 */
export function hunkWindows(hunks, totalLines, radius) {
  if (totalLines === 0) return [];
  const raw = hunks
    .map((h) => {
      const anchor = Math.max(1, h.newStart);
      const last = anchor + Math.max(h.newLines, 1) - 1;
      return { start: Math.max(1, anchor - radius), end: Math.min(totalLines, last + radius) };
    })
    .filter((w) => w.start <= w.end)
    .sort((a, b) => a.start - b.start);
  /** @type {Array<{start: number, end: number}>} */
  const merged = [];
  for (const w of raw) {
    const prev = merged.at(-1);
    if (prev && w.start <= prev.end + 1) prev.end = Math.max(prev.end, w.end);
    else merged.push({ ...w });
  }
  return merged;
}

/** @param {Record<string, any> | undefined} cfg */
export function contextSettings(cfg) {
  const c = cfg?.review?.context ?? {};
  const pick = (/** @type {string} */ key) => (Number.isInteger(c[key]) && c[key] >= 0 ? c[key] : DEFAULT_CONTEXT[/** @type {keyof typeof DEFAULT_CONTEXT} */ (key)]);
  return { whole_file_max_lines: pick('whole_file_max_lines'), hunk_context_lines: pick('hunk_context_lines'), min_context_lines: pick('min_context_lines') };
}

/**
 * @typedef {object} Context
 * @property {'whole' | 'hunks' | 'minimal' | 'recheck'} mode
 * @property {string} text - the rendered context section body.
 * @property {Array<{start: number, end: number}>} windows
 * @property {number} lineCount - how many file lines the context carries.
 */

/**
 * Build the context for one file.
 * @param {{file: string, content: string | null, hunks: ReadonlyArray<Hunk>, cfg?: Record<string, any>, mode?: 'auto' | 'minimal' | 'recheck'}} opts
 *   `mode: 'auto'` (default) applies the whole-file threshold; `minimal` and `recheck` force the
 *   hunk form with their radius.
 * @returns {Context}
 */
export function buildContext({ file, content, hunks, cfg, mode = 'auto' }) {
  const settings = contextSettings(cfg);
  const lines = splitLines(content);
  /** @type {Context['mode']} */
  let chosen;
  if (mode === 'minimal') chosen = 'minimal';
  else if (mode === 'recheck') chosen = 'recheck';
  else chosen = lines.length <= settings.whole_file_max_lines ? 'whole' : 'hunks';

  if (chosen === 'whole') {
    const windows = lines.length > 0 ? [{ start: 1, end: lines.length }] : [];
    const head = `### ${file} (whole file, ${lines.length} lines)`;
    return { mode: chosen, windows, lineCount: lines.length, text: [head, ...numbered(lines, 1, lines.length)].join('\n') };
  }
  const radius = chosen === 'minimal' ? settings.min_context_lines : settings.hunk_context_lines;
  const windows = hunkWindows(hunks, lines.length, radius);
  const parts = [];
  let lineCount = 0;
  for (const w of windows) {
    parts.push(`### ${file} lines ${w.start}-${w.end}`, ...numbered(lines, w.start, w.end));
    lineCount += w.end - w.start + 1;
  }
  if (windows.length === 0) parts.push(`### ${file} (no current content)`);
  return { mode: chosen, windows, lineCount, text: parts.join('\n') };
}

/**
 * @param {string[]} lines @param {number} start @param {number} end - 1-based, inclusive.
 * @returns {string[]}
 */
function numbered(lines, start, end) {
  const out = [];
  for (let n = start; n <= end; n += 1) out.push(`${n}| ${lines[n - 1]}`);
  return out;
}
