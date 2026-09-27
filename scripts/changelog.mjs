#!/usr/bin/env node
/**
 * Record a change in `CHANGELOG.md` as you go (B23), and the CHANGELOG parser the release tool
 * (`scripts/release.mjs`) shares.
 *
 *   npm run changelog -- <added|changed|fixed|removed|deprecated|security> "<text>"
 *   npm run changelog -- list
 *
 * Adding puts one `- <text>` bullet at the end of the `### <Type>` subsection of
 * `## [Unreleased]`, creating the subsection (in Keep-a-Changelog order: Added, Changed,
 * Deprecated, Removed, Fixed, Security) or the `## [Unreleased]` section when missing. Long text is
 * wrapped at the column the file already uses (100 by default), continuation lines indented two
 * spaces. Every other line of the file stays byte-identical. Refuses empty text, an unknown type
 * and an exact duplicate bullet in the same subsection.
 *
 * Works on `CHANGELOG.md` in the current directory. Exit 0 on success, 1 on a refusal, 2 on usage.
 */

import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/** The Keep-a-Changelog subsections, in order. */
export const TYPES = Object.freeze(['Added', 'Changed', 'Deprecated', 'Removed', 'Fixed', 'Security']);

const USAGE = `usage: npm run changelog -- <${TYPES.map((t) => t.toLowerCase()).join('|')}> "<text>"\n       npm run changelog -- list`;

export class ChangelogError extends Error {
  /** @param {string} message @param {number} [code] */
  constructor(message, code = 1) {
    super(message);
    this.code = code;
  }
}

const BULLET = /^[-*]\s+\S/;

/**
 * A `## [<name>]` section: its heading line and the line where it ends (the next `## ` heading,
 * the first link definition, or the end of the file). `name` is `Unreleased` or a version.
 * @param {string[]} lines @param {string} name
 * @returns {{ start: number, end: number } | null}
 */
export function findSection(lines, name) {
  const want = `[${name.toLowerCase()}]`;
  const start = lines.findIndex((l) => l.startsWith('## ') && l.slice(3).trim().toLowerCase().split(/\s/)[0] === want);
  if (start < 0) return null;
  const end = lines.findIndex((l, i) => i > start && (/^## /.test(l) || /^\[[^\]]+\]:\s/.test(l)));
  return { start, end: end < 0 ? lines.length : end };
}

/**
 * The `## [Unreleased]` section (see `findSection`).
 * @param {string[]} lines
 */
export const findUnreleased = (lines) => findSection(lines, 'Unreleased');

/**
 * The subsections of lines `[from, to)` and their bullets. Entries above any `### ` heading land
 * in a subsection whose `type` is `''`.
 * @param {string[]} lines @param {number} from @param {number} to
 * @returns {Array<{ type: string, heading: number, end: number, bullets: Array<{ text: string, first: number, last: number }> }>}
 */
export function parseSubsections(lines, from, to) {
  const subs = [{ type: '', heading: from - 1, end: to, bullets: [] }];
  let bullet = null;
  for (let i = from; i < to; i += 1) {
    const line = lines[i];
    const h = /^###\s+(.+?)\s*$/.exec(line);
    if (h) {
      subs.at(-1).end = i;
      subs.push({ type: h[1], heading: i, end: to, bullets: [] });
      bullet = null;
    } else if (BULLET.test(line)) {
      bullet = { text: line.replace(/^[-*]\s+/, '').trim(), first: i, last: i };
      subs.at(-1).bullets.push(bullet);
    } else if (bullet && line.trim() && /^\s/.test(line)) {
      bullet.text = `${bullet.text} ${line.trim()}`;
      bullet.last = i;
    } else bullet = null;
  }
  return subs;
}

/**
 * The entries of `## [Unreleased]`, grouped by subsection (empty groups dropped).
 * @param {string} text
 * @returns {Array<{ type: string, entries: string[] }>}
 */
export function unreleasedEntries(text) {
  const lines = text.split('\n');
  const sec = findUnreleased(lines);
  if (!sec) return [];
  return parseSubsections(lines, sec.start + 1, sec.end)
    .filter((s) => s.bullets.length)
    .map((s) => ({ type: s.type, entries: s.bullets.map((b) => b.text) }));
}

/**
 * The wrap column the file already uses: the longest bullet or continuation line, clamped to
 * 72..120; 100 when the file has none.
 * @param {string[]} lines
 */
export function wrapColumn(lines) {
  const widths = lines.filter((l) => BULLET.test(l) || /^ {2}\S/.test(l)).map((l) => l.length);
  return widths.length ? Math.min(120, Math.max(72, ...widths)) : 100;
}

/**
 * `- text` wrapped at `width`, continuation lines indented two spaces. A word longer than the
 * width stays whole on its own line.
 * @param {string} text @param {number} width
 * @returns {string[]}
 */
export function wrapBullet(text, width) {
  const out = [];
  let line = '';
  for (const word of text.split(/\s+/).filter(Boolean)) {
    if (!line) line = `- ${word}`;
    else if (line.length + 1 + word.length <= width) line = `${line} ${word}`;
    else {
      out.push(line);
      line = `  ${word}`;
    }
  }
  out.push(line);
  return out;
}

/**
 * Add one bullet under `## [Unreleased]` → `### <Type>`.
 * @param {string} text - the whole CHANGELOG
 * @param {string} type - added, changed, deprecated, removed, fixed or security (any case)
 * @param {string} entry
 * @returns {string}
 */
export function addEntry(text, type, entry) {
  const heading = TYPES.find((t) => t.toLowerCase() === String(type).toLowerCase());
  if (!heading) throw new ChangelogError(`unknown type "${type}": use one of ${TYPES.map((t) => t.toLowerCase()).join(', ')}`, 2);
  const clean = String(entry ?? '').replace(/\s+/g, ' ').trim();
  if (!clean) throw new ChangelogError('the entry text is empty', 2);

  const lines = text.split('\n');
  const bullet = wrapBullet(clean, wrapColumn(lines));
  let sec = findUnreleased(lines);
  if (!sec) {
    const firstRelease = lines.findIndex((l) => /^## /.test(l));
    if (firstRelease >= 0) {
      lines.splice(firstRelease, 0, '## [Unreleased]', '', `### ${heading}`, '', ...bullet, '');
    } else {
      while (lines.length && lines.at(-1) === '') lines.pop();
      lines.push('', '## [Unreleased]', '', `### ${heading}`, '', ...bullet, '');
    }
    return lines.join('\n');
  }

  const subs = parseSubsections(lines, sec.start + 1, sec.end);
  const lastNonBlank = (from, to) => {
    for (let i = to - 1; i > from; i -= 1) if (lines[i].trim()) return i;
    return from;
  };
  const existing = subs.find((s) => s.type.toLowerCase() === heading.toLowerCase());
  if (existing) {
    if (existing.bullets.some((b) => b.text === clean)) {
      throw new ChangelogError(`"${clean}" is already under ${heading} in [Unreleased]`);
    }
    const last = lastNonBlank(existing.heading, existing.end);
    if (last === existing.heading) lines.splice(last + 1, 0, '', ...bullet);
    else lines.splice(last + 1, 0, ...bullet);
    return lines.join('\n');
  }

  /** @param {string} t the position of a known subsection type, -1 for any other heading */
  const rank = (t) => TYPES.findIndex((k) => k.toLowerCase() === t.toLowerCase());
  const after = subs.find((s) => s.type && rank(s.type) > rank(heading));
  if (after) lines.splice(after.heading, 0, `### ${heading}`, '', ...bullet, '');
  else lines.splice(lastNonBlank(sec.start, sec.end) + 1, 0, '', `### ${heading}`, '', ...bullet);
  return lines.join('\n');
}

/**
 * The `list` output: each subsection with its count, then its entries on one line each.
 * @param {string} text
 */
export function formatList(text) {
  const groups = unreleasedEntries(text);
  if (!groups.length) return '[Unreleased] has no entries\n';
  return groups
    .map((g) => `${g.type || '(no subsection)'} (${g.entries.length})\n${g.entries.map((e) => `  - ${e}\n`).join('')}`)
    .join('');
}

/** @param {string[]} argv @param {string} [root] @returns {number} */
export function main(argv, root = process.cwd()) {
  const file = path.join(root, 'CHANGELOG.md');
  try {
    if (argv.length === 1 && argv[0] === 'list') {
      if (!existsSync(file)) throw new ChangelogError('CHANGELOG.md not found');
      process.stdout.write(formatList(readFileSync(file, 'utf8')));
      return 0;
    }
    if (argv.length !== 2) throw new ChangelogError(USAGE, 2);
    if (!existsSync(file)) throw new ChangelogError('CHANGELOG.md not found');
    const before = readFileSync(file, 'utf8');
    const after = addEntry(before, argv[0], argv[1]);
    writeFileSync(file, after);
    process.stdout.write(`added to [Unreleased] → ${TYPES.find((t) => t.toLowerCase() === argv[0].toLowerCase())}\n`);
    return 0;
  } catch (e) {
    if (!(e instanceof ChangelogError)) throw e;
    process.stderr.write(`REFUSED: ${e.message}\n${e.code === 2 && e.message !== USAGE ? `${USAGE}\n` : ''}`);
    return e.code;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv.slice(2));
}
