import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { Script } from 'node:vm';

/** @typedef {{value: string, string?: boolean, start: number, end: number}} Token */

/** Tokenize without treating comments, quoted text or regular expressions as imports.
 * @param {string} source @returns {Token[]}
 */
function tokens(source) {
  const out = /** @type {Token[]} */ ([]);
  const templates = /** @type {{expression: boolean, depth: number}[]} */ ([]);
  let i = 0;
  while (i < source.length) {
    const start = i, char = source[i];
    const template = templates.at(-1);
    if (template && !template.expression) {
      if (char === '\\') { i += 2; continue; }
      if (char === '`') { templates.pop(); i++; continue; }
      if (source.startsWith('${', i)) {
        template.expression = true;
        i += 2;
        out.push({ value: '{', start, end: i });
      } else i++;
      continue;
    }
    if (char === '`') { templates.push({ expression: false, depth: 0 }); i++; continue; }
    if (template && char === '{') template.depth++;
    if (template && char === '}') {
      if (template.depth === 0) template.expression = false;
      else template.depth--;
    }
    if (/\s/.test(char)) { i++; continue; }
    if (source.startsWith('//', i) || (i === 0 && source.startsWith('#!'))) {
      i = source.indexOf('\n', i);
      if (i < 0) break;
      continue;
    }
    if (source.startsWith('/*', i)) {
      i = source.indexOf('*/', i + 2);
      if (i < 0) throw new SyntaxError('unclosed comment');
      i += 2;
      continue;
    }
    if (char === '"' || char === "'") {
      i++;
      while (i < source.length && source[i] !== char) {
        if (source[i] === '\\') i++;
        i++;
      }
      if (i === source.length) throw new SyntaxError('unclosed string');
      out.push({ value: source.slice(start + 1, i), string: true, start, end: ++i });
      continue;
    }
    // A slash after an expression-start token is a regexp, not a division operator.
    if (char === '/' && (!out.length || /^(?:[=(:,;!&|?{}\[]|return|throw|case|=>)$/.test(out.at(-1).value))) {
      let bracket = false;
      i++;
      while (i < source.length) {
        if (source[i] === '\\') { i += 2; continue; }
        if (source[i] === '[') bracket = true;
        if (source[i] === ']') bracket = false;
        if (source[i++] === '/' && !bracket) break;
      }
      while (/[a-z]/i.test(source[i] ?? '') && i < source.length) i++;
      out.push({ value: 'regexp', start, end: i });
      continue;
    }
    const word = /^[\w$]+/.exec(source.slice(i));
    i += word ? word[0].length : 1;
    out.push({ value: source.slice(start, i), start, end: i });
  }
  if (templates.length) throw new SyntaxError('unclosed template');
  return out;
}

/** Extract module edges and syntax-check a script equivalent without executing it.
 * Module declarations are removed only for the syntax check; dynamic imports stay intact.
 * @param {string} source @returns {string[]}
 */
function specifiers(source) {
  const list = tokens(source);
  const specs = [];
  const edits = /** @type {{start: number, end: number, text: string}[]} */ ([]);
  let depth = 0;
  for (let i = 0; i < list.length; i++) {
    const token = list[i], next = list[i + 1];
    if (token.string) continue;
    if (['{', '[', '('].includes(token.value)) depth++;
    if (['}', ']', ')'].includes(token.value)) depth--;
    if (!next || list[i - 1]?.value === '.') continue;
    if (token.value === 'import' && next.value === '(') {
      if (list[i + 2]?.string && [')', ','].includes(list[i + 3]?.value)) specs.push(list[i + 2].value);
      continue;
    }
    if (token.value === 'import' && next.value === '.') {
      edits.push({ start: token.start, end: list[i + 2].end, text: '({})' });
      i += 2;
      continue;
    }
    if (depth !== 0 || (token.value !== 'import' && token.value !== 'export')) continue;
    if (token.value === 'export' && !['{', '*'].includes(next.value)) {
      const isDefault = next.value === 'default';
      edits.push({ start: token.start, end: isDefault ? next.end : token.end, text: isDefault ? 'const __default_export__ =' : '' });
      if (isDefault) i++;
      continue;
    }
    let end = i + 1;
    if (!next.string) {
      if (next.value === '{') {
        while (end < list.length && list[end].value !== '}') end++;
        if (list[end + 1]?.value === 'from') end += 2;
      } else {
        while (end < list.length && !(list[end].value === 'from' && list[end + 1]?.string)) end++;
        end++;
      }
    }
    if (!list[end]) throw new SyntaxError('incomplete module declaration');
    if (list[end].string) specs.push(list[end].value);
    // JSON import attributes are part of the declaration being removed.
    if (['with', 'assert'].includes(list[end + 1]?.value) && list[end + 2]?.value === '{') {
      end += 3;
      while (end < list.length && list[end].value !== '}') end++;
      if (!list[end]) throw new SyntaxError('incomplete import attributes');
    }
    edits.push({ start: token.start, end: list[end].end, text: '' });
    i = end;
  }
  // Keep source positions stable when stripping a hashbang.
  let script = source.replace(/^#![^\n]*/, (line) => ' '.repeat(line.length));
  for (const edit of edits.reverse()) script = script.slice(0, edit.start) + edit.text + script.slice(edit.end);
  new Script(`(async function () { "use strict";\n${script}\n})`);
  return specs.filter((specifier) => specifier.startsWith('./') || specifier.startsWith('../'));
}

/** @param {string} file */
const testPath = (file) => /(?:^|\/)(?:test|tests|__tests__|spec)\//.test(file) || /\.(?:test|spec)\.[^/]+$/.test(file);

/** Build a reverse transitive graph, shared by the async selector and the synchronous scope.
 * Every module is read and parsed at most once. Missing/deleted targets remain graph edges.
 * @param {{cwd: string, testFiles: string[]}} opts
 * @returns {Promise<Record<string, string[]>>}
 */
export async function testDependents({ cwd, testFiles }) {
  const root = path.resolve(cwd);
  const reads = new Map();
  const stats = new Map();
  const graph = /** @type {Record<string, string[]>} */ (Object.create(null));
  /** @param {string} file @returns {Promise<boolean>} */
  function exists(file) {
    if (!stats.has(file)) stats.set(file, stat(path.join(root, file)).then((s) => s.isFile()).catch(() => false));
    return stats.get(file);
  }
  /** @param {string} file @returns {Promise<string[] | null>} */
  function imports(file) {
    if (!reads.has(file)) reads.set(file, (async () => {
      try {
        const specs = specifiers(await readFile(path.join(root, file), 'utf8'));
        const edges = [];
        for (const spec of specs) {
          const relative = path.relative(root, path.resolve(root, path.dirname(file), spec)).split(path.sep).join('/');
          if (relative === '..' || relative.startsWith('../') || path.isAbsolute(relative)) continue;
          const choices = path.extname(relative) ? [relative] : [relative + '.mjs', relative + '.js', relative + '/index.mjs'];
          let resolved = choices[0];
          for (const choice of choices) if (await exists(choice)) { resolved = choice; break; }
          edges.push(resolved);
        }
        return edges;
      } catch { return null; }
    })());
    return reads.get(file);
  }
  for (const test of [...new Set(testFiles)].sort()) {
    if (!(await imports(test))) continue;
    const seen = new Set([test]);
    const pending = [test];
    while (pending.length) {
      for (const dependency of (await imports(pending.pop())) ?? []) {
        if (seen.has(dependency)) continue;
        seen.add(dependency);
        (graph[dependency] ??= []).push(test);
        pending.push(dependency);
      }
    }
    if (/^test\/(?:config|doctor)\//.test(test)) (graph['schema/code-forge.schema.json'] ??= []).push(test);
  }
  for (const file of Object.keys(graph)) graph[file] = [...new Set(graph[file])].sort();
  return graph;
}

/** Tests importing any changed non-test source, directly or through repository modules.
 * @param {{cwd: string, changed: string[], testFiles: string[]}} opts
 * @returns {Promise<string[]>}
 */
export async function dependentTests({ cwd, changed, testFiles }) {
  const sources = changed.filter((file) => !testPath(file) && !testFiles.includes(file));
  if (!sources.length) return [];
  const graph = await testDependents({ cwd, testFiles });
  return [...new Set(sources.flatMap((file) => graph[file] ?? []))].sort();
}
