/** Stem pairs and bounded names for closed-book review packets (B97). No file reads. */
import path from 'node:path';
import { findSecret } from '../util/gh.mjs';
import { redact } from '../util/redact.mjs';

/** @param {string} file */
function identity(file) {
  if (typeof file !== 'string' || file.startsWith('/') || /[\\\x00-\x1f]/.test(file) || file.split('/').some((s) => s === '.' || s === '..' || s === '')) return null;
  const ext = path.posix.extname(file);
  let stem = path.posix.basename(file, ext);
  let test = /(^|\/)(?:test|tests)\//.test(file);
  if (ext === '.mjs' || ext === '.ts') {
    if (/\.(test|spec)$/.test(stem)) { test = true; stem = stem.replace(/\.(test|spec)$/, ''); }
  } else if (ext === '.php') {
    if (stem.endsWith('Test')) { test = true; stem = stem.slice(0, -4); }
  } else if (ext === '.go') {
    if (stem.endsWith('_test')) { test = true; stem = stem.slice(0, -5); }
  } else if (ext === '.py') {
    if (stem.startsWith('test_')) { test = true; stem = stem.slice(5); }
  } else if (ext === '.swift') {
    if (stem.endsWith('Tests')) { test = true; stem = stem.slice(0, -5); }
  } else return null;
  return { ext, stem, test };
}

/** @param {string} file @param {ReadonlyArray<string>} candidates @returns {string[]} */
export function pairsFor(file, candidates) {
  const source = identity(file);
  if (!source) return [];
  return [...new Set(candidates)].filter((candidate) => {
    const other = identity(candidate);
    return candidate !== file && other?.ext === source.ext && other.stem === source.stem && other.test !== source.test;
  });
}

/**
 * A best-effort list, not a parser or proof of coverage. Names only, never function bodies.
 * Redact before truncating so a token crossing the 120-character boundary cannot leak.
 * @param {string} text @param {string} file @returns {string[]}
 */
export function summarize(text, file) {
  const id = identity(file);
  if (!id) return [];
  /** @type {Set<string>} */
  const names = new Set();
  for (const line of text.split(/\r?\n/)) {
    /** @type {RegExpMatchArray | null} */
    let match = null;
    if (id.test) {
      if (id.ext === '.mjs' || id.ext === '.ts' || id.ext === '.php') {
        match = line.match(/^\s*(?:await\s+)?(?:test|it)(?:\.(?:only|skip|todo))?\(\s*(['"`])([^\r\n]+?)\1/);
        if (match) { names.add(match[2]); match = null; }
        else if (id.ext === '.php') match = line.match(/^\s*(?:(?:public|protected)\s+)?function\s+(test\w+)\s*\(/);
        else continue;
      } else if (id.ext === '.go') match = line.match(/^\s*func\s+(Test\w+)\s*\(/);
      else if (id.ext === '.py') match = line.match(/^\s*(?:async\s+)?def\s+(test_\w+)\s*\(/);
      else if (id.ext === '.swift') match = line.match(/^\s*func\s+(test\w+)\s*\(/);
    } else {
      if (id.ext === '.mjs' || id.ext === '.ts') {
        match = line.match(/^\s*export\s+(?:default\s+)?(?:async\s+)?(?:function\*?|class|const|let|var|interface|type|enum)\s+(\w+)/);
        const list = line.match(/^\s*export\s*\{([^}]+)\}/);
        if (list) {
          for (const item of list[1].split(',')) {
            const symbol = item.trim().match(/^(?:type\s+)?(\w+)(?:\s+as\s+(\w+))?$/);
            if (symbol) names.add(symbol[2] ?? symbol[1]);
          }
        }
      } else if (id.ext === '.php') match = line.match(/^\s*(?:(?:public|protected|private|static|final|abstract)\s+)*(?:function|class|interface|trait)\s+(\w+)/);
      else if (id.ext === '.go') match = line.match(/^\s*(?:func\s+(?:\([^)]*\)\s+)?|(?:type|var|const)\s+)([A-Z]\w*)/);
      else if (id.ext === '.py') match = line.match(/^\s*(?:async\s+)?(?:def|class)\s+([a-zA-Z]\w*)/);
      else if (id.ext === '.swift') match = line.match(/^\s*(?:public|open)\s+(?:final\s+)?(?:func|class|struct|enum|protocol|let|var)\s+(\w+)/);
    }
    if (match) names.add(match[1]);
    if (names.size >= 40) break;
  }
  return [...names].slice(0, 40).map((name) => {
    const safe = redact(name);
    return Array.from(findSecret(safe) ? '[REDACTED]' : safe).slice(0, 120).join('');
  });
}
