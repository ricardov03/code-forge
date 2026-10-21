/** Best-effort local import relationships; I/O is supplied by the packet reader. */
import path from 'node:path';

const EXTENSIONS = ['.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx', '.mts', '.cts', '.css', '.scss', '.html', '.htm', '.astro', '.vue'];
const STYLES = new Set(['.css', '.scss']);

/** @param {string} file */
function safePath(file) {
  return typeof file === 'string' && !path.posix.isAbsolute(file) &&
    !/[\\\x00-\x1f\x7f]/.test(file) &&
    !file.split('/').some((part) => ['', '.', '..', '.git', '.code-forge'].includes(part));
}

/** @param {string} text @param {string} file @returns {string[]} */
function specifiers(text, file) {
  const ext = path.posix.extname(file).toLowerCase();
  const found = [];
  if (!STYLES.has(ext)) {
    // Keep strings as single tokens, discard comments/templates, and never interpret a string's
    // contents as code. This also handles imports in Astro frontmatter and Vue script blocks.
    const tokens = [...text.matchAll(/\/\/[^\r\n]*|\/\*[\s\S]*?\*\/|`(?:\\[\s\S]|[^`\\])*`|'(?:\\[\s\S]|[^'\\])*'|"(?:\\[\s\S]|[^"\\])*"|[\w$]+|[^\s]/g)]
      .map((match) => match[0]).filter((token) => !token.startsWith('//') && !token.startsWith('/*') && !token.startsWith('`'));
    const quoted = (token) => token?.startsWith('"') || token?.startsWith("'");
    for (let i = 0; i < tokens.length; i += 1) {
      const token = tokens[i];
      if (token !== 'import' && token !== 'require') continue;
      if (tokens[i - 1] === '.') continue;
      if (tokens[i + 1] === '(' && quoted(tokens[i + 2]) && tokens[i + 3] === ')') found.push(tokens[i + 2].slice(1, -1));
      else if (token === 'import') {
        if (quoted(tokens[i + 1])) found.push(tokens[i + 1].slice(1, -1));
        else {
          for (let j = i + 1; j < tokens.length && ![';', 'import', 'require', '('].includes(tokens[j]); j += 1) {
            if (tokens[j] === 'from' && quoted(tokens[j + 1])) { found.push(tokens[j + 1].slice(1, -1)); break; }
          }
        }
      }
    }
  }
  if (STYLES.has(ext) || ['.html', '.htm', '.astro', '.vue'].includes(ext)) {
    const markup = text.replace(/\/\*[\s\S]*?\*\/|<!--[\s\S]*?-->/g, '');
    for (const match of markup.matchAll(/@import\s+(?:url\(\s*(?:'([^']+)'|"([^"]+)"|([^\s)]+))\s*\)|'([^']+)'|"([^"]+)")/gi)) {
      found.push(match.slice(1).find((value) => value !== undefined));
    }
    if (!STYLES.has(ext)) {
      for (const tag of markup.matchAll(/<(link|script)\b[^>]*>/gi)) {
        const attr = tag[1].toLowerCase() === 'link' ? 'href' : 'src';
        const match = tag[0].match(new RegExp(`\\s${attr}\\s*=\\s*(?:"([^"]+)"|'([^']+)'|([^\\s>]+))`, 'i'));
        if (match) found.push(match[1] ?? match[2] ?? match[3]);
      }
    }
  }
  return found;
}

/**
 * Names only: outgoing imports, tracked importers (at most 2000 candidates), and a stylesheet's
 * cascade siblings. A missing/unreadable file is not a relationship. No source text is returned.
 * `listFiles` lists tracked repo-relative paths; `readFile` reads an absolute path or returns null.
 * @param {{repoRoot: string, file: string,
 *   listFiles: (repoRoot: string) => string[] | Promise<string[]>,
 *   readFile: (file: string) => string | null | Promise<string | null>}} opts
 * @returns {Promise<string[]>}
 */
export async function relatedFiles({ repoRoot, file, listFiles, readFile }) {
  if (!safePath(file) || !EXTENSIONS.includes(path.posix.extname(file).toLowerCase())) return [];
  const cache = new Map();
  const read = async (rel) => {
    if (!safePath(rel)) return null;
    if (!cache.has(rel)) {
      try { cache.set(rel, await readFile(path.resolve(repoRoot, rel))); }
      catch { cache.set(rel, null); }
    }
    return cache.get(rel);
  };
  const imports = async (rel) => {
    const text = await read(rel);
    const resolved = new Set();
    if (text === null) return resolved;
    for (const specifier of specifiers(text, rel)) {
      if (!/^\.\.?\//.test(specifier)) continue;
      const target = path.posix.normalize(path.posix.join(path.posix.dirname(rel), specifier.split(/[?#]/)[0]));
      if (!safePath(target)) continue;
      const candidates = [target];
      if (!path.posix.extname(target)) candidates.push(...EXTENSIONS.map((ext) => target + ext), ...EXTENSIONS.map((ext) => `${target}/index${ext}`));
      for (const candidate of candidates) {
        if (await read(candidate) !== null) { resolved.add(candidate); break; }
      }
    }
    return resolved;
  };
  const related = await imports(file);
  let tracked = [];
  try { tracked = [...new Set(await listFiles(repoRoot))].filter((rel) => safePath(rel) && EXTENSIONS.includes(path.posix.extname(rel).toLowerCase())); }
  catch { /* Outgoing imports still help when listing is unavailable. */ }
  if (tracked.length <= 2000) {
    for (const importer of tracked) {
      if (importer === file) continue;
      const targets = await imports(importer);
      if (!targets.has(file)) continue;
      related.add(importer);
      if (STYLES.has(path.posix.extname(file).toLowerCase())) {
        for (const sibling of targets) if (STYLES.has(path.posix.extname(sibling).toLowerCase())) related.add(sibling);
      }
    }
  }
  related.delete(file);
  return [...related].sort().slice(0, 12);
}
