/** Best-effort JavaScript/TypeScript definition context, with caller-supplied I/O. */
import nodePath from 'node:path';
import { isSecretLike } from './packet.mjs';

const KEYWORDS = new Set(['if', 'for', 'while', 'switch', 'catch', 'function', 'return', 'typeof', 'new']);
const EXTENSIONS = ['.mjs', '.js', '.ts', '.cjs', '.tsx'];
const IDENTIFIER = /^[A-Za-z_$][\w$]*$/;

/** @typedef {{path: string, name: string, start: number, end: number, lines: string[]}} Definition */

/** Comments and literals stay opaque; positions still refer to the original source.
 * @param {string} text
 */
function tokensOf(text) {
  const tokens = [];
  let line = 1;
  let offset = 0;
  for (const match of text.matchAll(/\/\/[^\r\n]*|\/\*[\s\S]*?\*\/|`(?:\\[\s\S]|[^`\\])*`|'(?:\\[\s\S]|[^'\\])*'|"(?:\\[\s\S]|[^"\\])*"|\/(?:\\.|\[(?:\\.|[^\]\\])*\]|[^/\n\\])+\/[dgimsuvy]*|[A-Za-z_$][\w$]*|=>|[^\s]/g)) {
    line += text.slice(offset, match.index).split('\n').length - 1;
    offset = match.index;
    if (!/^\/[/\*]/.test(match[0])) tokens.push({ value: match[0], line });
  }
  return tokens;
}

/** @param {string} rel */
function safePath(rel) {
  return !nodePath.posix.isAbsolute(rel) && !/[\\\x00-\x1f\x7f]/.test(rel) &&
    !rel.split('/').some((part) => ['', '.', '..', '.git', '.code-forge'].includes(part)) && !isSecretLike(rel);
}

/** @param {ReturnType<typeof tokensOf>} tokens @param {number} start */
function closing(tokens, start) {
  const open = tokens[start]?.value;
  const close = { '(': ')', '{': '}', '[': ']' }[open];
  let depth = 0;
  for (let i = start; i < tokens.length; i++) {
    if (tokens[i].value === open) depth++;
    if (tokens[i].value === close && --depth === 0) return i;
  }
  return -1;
}

/** @param {string} text @param {string} file @param {string} name @param {boolean} exported
 * @returns {Definition | null}
 */
function declaration(text, file, name, exported) {
  const tokens = tokensOf(text);
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].value !== name) continue;
    const prev = tokens[i - 1]?.value;
    const kind = ['function', 'const', 'class'].includes(prev) ? prev : null;
    let start = i - 1;
    while (start > 0 && ['async', 'default', 'export', 'declare'].includes(tokens[start - 1].value)) start--;
    if (exported && !tokens.slice(start, i).some((token) => token.value === 'export')) continue;
    if (kind === null && (exported || tokens[i + 1]?.value !== '(' || !['{', '}', ';', 'async', 'static', 'get', 'set'].includes(prev))) continue;
    if (kind === null && tokens[closing(tokens, i + 1) + 1]?.value !== '{') continue;
    if (kind === null) start = ['async', 'static', 'get', 'set'].includes(prev) ? i - 1 : i;
    if (kind === 'const' && tokens[i + 1]?.value !== '=') continue;
    let body = i + 1;
    let end = -1;
    for (; body < tokens.length; body++) {
      const value = tokens[body].value;
      if (value === '(' || value === '[') {
        const last = closing(tokens, body);
        if (last < 0) break;
        body = last;
      } else if (value === '{') {
        end = closing(tokens, body);
        break;
      } else if (value === ';' || (kind === 'const' && tokens[body].line > tokens[i].line && tokens[body - 1]?.value !== '=')) {
        end = value === ';' ? body : body - 1;
        break;
      }
    }
    if (end < 0) continue; // Malformed or incomplete declaration: keep looking.
    const firstLine = tokens[start].line;
    const lastLine = tokens[end].line;
    const lastShown = Math.min(lastLine, firstLine + 39);
    return { path: file, name, start: firstLine, end: lastShown,
      lines: [...lines.slice(firstLine - 1, lastShown), ...(lastLine > lastShown ? ['…'] : [])] };
  }
  return null;
}

/** Parse local bindings, preserving aliases and namespace owners.
 * @param {string} header
 */
function importsOf(header) {
  const tokens = tokensOf(header);
  const imports = new Map();
  for (let i = 0; i < tokens.length; i++) {
    if (tokens[i].value !== 'import' || ['(', '.'].includes(tokens[i + 1]?.value)) continue;
    let end = i + 1;
    while (end < tokens.length && !['from', ';', 'import'].includes(tokens[end].value)) end++;
    if (tokens[end]?.value !== 'from') continue;
    const source = tokens[end + 1]?.value;
    if (!/^['"]\.\.?\//.test(source ?? '')) continue;
    const specifier = source.slice(1, -1);
    for (let j = i + 1; j < end; j++) {
      const name = tokens[j].value;
      if (!IDENTIFIER.test(name) || ['as', 'type'].includes(name)) continue;
      if (tokens[j - 1]?.value === 'as') continue;
      const alias = tokens[j + 1]?.value === 'as' ? tokens[j + 2]?.value : name;
      imports.set(alias, { name, specifier, namespace: tokens[j - 1]?.value === '*' });
    }
    // `* as owner` has no ordinary imported name.
    if (tokens[i + 1]?.value === '*' && tokens[i + 2]?.value === 'as') {
      imports.set(tokens[i + 3]?.value, { name: '*', specifier, namespace: true });
    }
    i = end + 1;
  }
  return imports;
}

/**
 * Calls on added unified-diff lines only. Missing or malformed source never aborts a packet.
 * `readFile` takes an absolute path, as in relatedFiles; windows are current-file line ranges.
 * @param {{path: string, diffLines: string[], header: string, io: {
 *   repoRoot: string, readFile: (file: string) => string | null | Promise<string | null>,
 *   windows?: Array<{start: number, end: number}>
 * }}} opts
 * @returns {Promise<Definition[]>}
 */
export async function findDefinitions({ path, diffLines, header, io }) {
  const found = [];
  if (!safePath(path) || !EXTENSIONS.includes(nodePath.posix.extname(path).toLowerCase())) return found;
  try {
    const cache = new Map();
    const read = async (rel) => {
      if (!safePath(rel)) return null;
      if (!cache.has(rel)) {
        try { cache.set(rel, await io.readFile(nodePath.resolve(io.repoRoot, rel))); }
        catch { cache.set(rel, null); }
      }
      return cache.get(rel);
    };
    const text = await read(path);
    if (typeof text !== 'string') return found;
    const added = tokensOf(diffLines.filter((line) => line.startsWith('+') && !line.startsWith('+++')).map((line) => line.slice(1)).join('\n'));
    const defined = new Set(added.filter((token, i) => ['function', 'const', 'class', 'let', 'var'].includes(added[i - 1]?.value) ||
      (added[i + 1]?.value === '(' && added[closing(added, i + 1) + 1]?.value === '{')).map((token) => token.value));
    const imports = importsOf(header);
    const seen = new Set();
    for (let i = 0; i < added.length; i++) {
      const name = added[i].value;
      if (!IDENTIFIER.test(name) || added[i + 1]?.value !== '(' || KEYWORDS.has(name) || defined.has(name)) continue;
      const receiver = added[i - 1]?.value === '.' ? added[i - 2]?.value : null;
      const binding = imports.get(receiver ?? name);
      let definition = declaration(text, path, name, false);
      if (definition && io.windows?.some((w) => w.start <= definition.start && definition.end <= w.end)) definition = null;
      if (!definition && binding && (!receiver || binding.namespace)) {
        const target = nodePath.posix.normalize(nodePath.posix.join(nodePath.posix.dirname(path), binding.specifier));
        if (!safePath(target)) continue;
        const candidates = nodePath.posix.extname(target) ? [target] : [target, ...EXTENSIONS.map((ext) => target + ext), ...EXTENSIONS.map((ext) => `${target}/index${ext}`)];
        for (const candidate of candidates) {
          const imported = await read(candidate);
          if (typeof imported !== 'string') continue;
          definition = declaration(imported, candidate, binding.namespace ? name : binding.name, true);
          break;
        }
      }
      if (!definition) continue;
      const key = `${definition.path}:${definition.start}`;
      if (!seen.has(key)) { seen.add(key); found.push(definition); }
    }
  } catch { /* Partial context is preferable to aborting review on unparseable source. */ }
  return found;
}
