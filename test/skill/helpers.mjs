/**
 * Shared paths and file walkers for the skill lints. The tests run under the isolate preload
 * (cwd pinned to a temp root), so every path here is derived from `import.meta.url`, never from
 * `process.cwd()`. No temp dirs are created by these helpers.
 */
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const ROOT = path.resolve(__dirname, '..', '..');
export const SKILL_DIR = path.join(ROOT, 'skill');
export const REFERENCES_DIR = path.join(SKILL_DIR, 'references');

/**
 * @param {string} dir
 * @returns {Promise<string[]>} every regular file under `dir`, sorted, absolute.
 */
async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) files.push(...(await walk(full)));
    else if (entry.isFile()) files.push(full);
  }
  return files.sort();
}

/** @returns {Promise<string[]>} every file under `skill/`, absolute, sorted. */
export function listSkillFiles() {
  return walk(SKILL_DIR);
}

/**
 * @param {string} rel - path relative to `skill/`.
 * @returns {Promise<string>}
 */
export function readSkillFile(rel) {
  return readFile(path.join(SKILL_DIR, rel), 'utf8');
}

/**
 * The text of one `## §N …` section of a Markdown file: from that heading to the next `## `
 * heading (or the end of the file).
 * @param {string} text
 * @param {number} n
 * @returns {string}
 */
export function section(text, n) {
  const re = new RegExp(`^## §${n}\\b[^\\n]*\\n([\\s\\S]*?)(?=^## |(?![\\s\\S]))`, 'm');
  const m = re.exec(text);
  if (!m) throw new Error(`section §${n} not found`);
  return m[1];
}

/**
 * Data rows of the first Markdown table whose header row contains `headerCell`.
 * @param {string} text
 * @param {string} headerCell
 * @returns {string[][]} one array of trimmed cells per data row (the header and separator excluded).
 */
export function tableRows(text, headerCell) {
  const lines = text.split('\n');
  const start = lines.findIndex((l) => l.startsWith('|') && l.includes(headerCell));
  if (start < 0) throw new Error(`table with header cell "${headerCell}" not found`);
  const rows = [];
  for (let i = start + 2; i < lines.length && lines[i].startsWith('|'); i += 1) {
    rows.push(
      lines[i]
        .slice(1, lines[i].lastIndexOf('|'))
        .split('|')
        .map((c) => c.trim()),
    );
  }
  return rows;
}
