// Makes golden.json: the bytes Node's editDoc (src/autopilot/limits.mjs)
// writes for each fixture, computed with the yaml package itself.
//
//   YAML_JS=<path to node_modules/yaml/dist/index.js> node gen.mjs > golden.json
//
// SEED and COUNT change the fuzz-like part (defaults 494 and 1500); a wider
// run is checked with YAMLEDIT_GOLDEN=<file> go test ./yamledit/.
//
// Each case: {name, text, segs, op: "set"|"remove", value, prune} and either
// out (the written text), error "throw" (editDoc throws: a parent is not a
// mapping), error "parse" (parseDocument reports errors or the root is not
// a mapping: Node never edits it) or error "stringify" (String(doc) throws).
import { pathToFileURL } from 'node:url';

const yamlPath = process.env.YAML_JS;
if (!yamlPath) throw new Error('set YAML_JS to node_modules/yaml/dist/index.js');
const { parseDocument } = await import(pathToFileURL(yamlPath).href);

/** editDoc, as limits.mjs does it. */
function edit(text, segs, change) {
  const doc = parseDocument(text, { prettyErrors: false });
  const js = doc.errors.length === 0 ? doc.toJS() : null;
  if (js === null || typeof js !== 'object' || Array.isArray(js)) return { error: 'parse' };
  try {
    if ('set' in change) doc.setIn(segs, doc.createNode(change.set));
    else {
      doc.deleteIn(segs);
      for (let i = 0; i < (change.prune ?? 0); i++) {
        const parent = segs.slice(0, segs.length - 1 - i);
        if (parent.length === 0) break;
        const node = doc.getIn(parent, true);
        if (!node || !Array.isArray(node.items) || node.items.length > 0) break;
        doc.deleteIn(parent);
      }
    }
  } catch {
    return { error: 'throw' };
  }
  try {
    return { out: String(doc) };
  } catch {
    return { error: 'stringify' };
  }
}

const cases = [];
function add(name, text, key, op, arg) {
  const segs = key.split('.');
  const c = { name, text, segs };
  if (op === 'set') Object.assign(c, { op, value: arg }, edit(text, segs, { set: arg }));
  else Object.assign(c, { op, prune: arg ?? 0 }, edit(text, segs, { remove: true, prune: arg ?? 0 }));
  cases.push(c);
}

// ---------------------------------------------------------------- hand-written

const REAL = `version: 1
project:
  name: code-forge
  slug: code-forge
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
caps:
  coders: 2
review:
  multimodel: false
engine: auto
harnesses:
  - claude
gates:
  timeout_s: 3600
  test:
    - sh
    - test/helpers/run-suite.sh
    - "{test_files}"
  lint: null
  # B427: hubs measured on the B362b+B402 batch (most tests reach a change
  # through them); their own tests still run when the closure reaches them.
  import_graph:
    stop:
      - src/session/spawn.mjs
      - src/config/validate.mjs
proof:
  tiers:
    high:
      paths: []
  isolation: export
`;
add('real set caps.coders', REAL, 'caps.coders', 'set', 4);
add('real set new review key', REAL, 'review.max_rounds_per_file', 'set', 3);
add('real set new top key', REAL, 'autopilot.max_cost_usd', 'set', 12.5);
add('real remove new key', REAL, 'review.multimodel', 'remove', 1);
add('real remove nested prune', REAL, 'proof.tiers.high.paths', 'remove', 3);
add('real set list', REAL, 'harnesses', 'set', ['claude', 'codex']);
add('real set object', REAL, 'caps', 'set', { coders: 3, reviewers: 1 });
add('real set gates.timeout_s', REAL, 'gates.timeout_s', 'set', 7200);
add('real set under comment', REAL, 'gates.import_graph.stop', 'set', ['a.mjs']);
add('real crlf', REAL.replace(/\n/g, '\r\n'), 'caps.coders', 'set', 4);
add('real no final newline', REAL.slice(0, -1), 'caps.coders', 'set', 4);
add('real doc markers', `---\n${REAL}...\n`, 'caps.coders', 'set', 4);
add('real scalar parent', REAL, 'engine.mode', 'set', 1);
add('real seq parent', REAL, 'harnesses.x', 'set', 1);

const HAND = [
  ['comments', '# head\n\n# second\na: 1 # line\n# before b\nb:\n  c: x\n  # end of b\n\n# tail\n'],
  ['quoted', "a: 'one'\nb: \"two\"\nc: 'it''s'\nd: \"x: y\"\ne: '#no'\n"],
  ['flow', 'a: []\nb: {}\nc:\n  - 1\n  - two\n  - "3"\n'],
  ['blank lines', 'a: 1\n\n\nb:\n\n  c: 2\n\n  d: 3\n\ne: 4\n'],
  ['numbers', 'a: 1.50\nb: -0\nc: 007\nd: +3\ne: 0.0\nf: 10.\ng: .5\n'],
  ['nulls bools', 'a: ~\nb: null\nc:\nd: True\ne: FALSE\nf: Null\n'],
  ['doc start comment', '# pre\n---\n# post\n\na: 1\n'],
  ['doc end comment', 'a: 1\n... # done\n'],
  ['seq comments', 'a:\n  # first\n  - x # x\n\n  - y\n  # after\nb: 2\n'],
  ['seq zero indent', 'a:\n- x\n- y\nb: 1\n'],
  ['crlf comments', '# h\r\n\r\na: 1\r\n# e\r\n\r\n# f\r\n'],
  ['empty comment', '#\na: 1 #\n#\n'],
  ['deep', 'a:\n  b:\n    c:\n      d: 1\n    e: 2\n'],
  ['tabs', 'a:\t1\n'],
  ['flow full', 'a: [1, 2]\n'],
  ['flow mixed', "a: [ x, 'y', \"z\" ] # c\nb: {k: 1, l: [a, {m: n}]}\nc: {}\n"],
  ['flow long', `a: [${Array.from({ length: 14 }, (_, i) => `item${i}`).join(', ')}]\n`],
  ['flow odd', 'a: [a, ]\nb: {k}\n'],
  ['doc end before', 'a: 1\n# c\n... # done\n'],
  ['doc end after', 'a: 1\n...\n# more\n\n# x\n'],
  ['doc end blank', 'a: 1\n# c\n\n...\n'],
  ['multi doc', 'a: 1\n---\nb: 2\n'],
  ['anchor', 'a: &x 1\nb: *x\n'],
  ['block scalar', 'a: |\n  text\n'],
  ['long string', `a: ${'word '.repeat(30).trim()}\n`],
  ['unicode', 'a: café\n'],
];
const OPS = [
  ['a', 'set', 5], ['a', 'remove', 0], ['b', 'set', 'text'], ['b.c', 'set', true], ['b.c', 'remove', 1],
  ['z', 'set', null], ['z.y', 'set', { k: [1, 'x'], e: [], o: {} }], ['a.b.c', 'remove', 2], ['a.b.c.d', 'remove', 3],
  ['c', 'set', []], ['c', 'set', {}], ['a', 'set', 'yes: no'], ['a', 'set', '12'], ['a', 'set', "it's \"q\""],
  ['a', 'set', '#tag'], ['a', 'set', ''], ['a', 'set', -0.5], ['a', 'set', 1e21], ['a', 'set', 'null'],
  ['e', 'remove', 0], ['b', 'remove', 0], ['a', 'set', ['x', 2, false, null]],
  ['b.k', 'set', 'p, q'], ['b.new', 'set', 'r]'], ['b.k', 'remove', 1], ['a', 'set', 1.5e-7], ['b.l', 'set', 2],
];
for (const [name, text] of HAND) {
  for (const [key, op, arg] of OPS) {
    add(`${name} ${op} ${key} ${JSON.stringify(arg)}`, text, key, op, arg);
    add(`${name} crlf ${op} ${key} ${JSON.stringify(arg)}`, text.replace(/\n/g, '\r\n'), key, op, arg);
    add(`${name} nofinal ${op} ${key} ${JSON.stringify(arg)}`, text.replace(/\r?\n$/, ''), key, op, arg);
  }
}

// ---------------------------------------------------------------- fuzz-like

function mulberry32(seed) {
  return () => {
    seed |= 0;
    seed = (seed + 0x6d2b79f5) | 0;
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
const rand = mulberry32(Number(process.env.SEED ?? 494));
const pick = (xs) => xs[Math.floor(rand() * xs.length)];
const KEYS = ['a', 'b', 'c', 'caps', 'coders', 'max_rounds', 'L1', 'x-y'];
const SCALARS = ['[a, b]', '{k: v}', '[ "x" ]', '1', '0', '-3', '2.50', '3.0', '1.25', 'text', 'two words', "'quoted'", '"double"', "'it''s'", 'null', '~', '', 'true', 'False', '[]', '{}', 'a/b.mjs', '"{test_files}"', "''", '""'];
const COMMENTS = ['# c', '#', '# two  spaces', '#x'];

function trivia(indent) {
  const out = [];
  const n = rand() < 0.6 ? 0 : 1 + Math.floor(rand() * 3);
  for (let i = 0; i < n; i++) out.push(rand() < 0.4 ? '' : ' '.repeat(indent) + pick(COMMENTS));
  return out;
}
function mapping(indent, depth) {
  const lines = [];
  const used = new Set();
  const n = 1 + Math.floor(rand() * 3);
  for (let i = 0; i < n; i++) {
    const key = pick(KEYS);
    if (used.has(key)) continue;
    used.add(key);
    lines.push(...trivia(indent));
    const pad = ' '.repeat(indent);
    const r = rand();
    if (depth < 3 && r < 0.3) {
      lines.push(`${pad}${key}:`);
      lines.push(...mapping(indent + 2, depth + 1));
    } else if (r < 0.45) {
      lines.push(`${pad}${key}:`);
      const seqIndent = rand() < 0.3 ? indent : indent + 2;
      const m = 1 + Math.floor(rand() * 3);
      for (let j = 0; j < m; j++) {
        lines.push(...trivia(seqIndent));
        lines.push(`${' '.repeat(seqIndent)}- ${pick(SCALARS.filter((s) => s !== '' && s !== '[]' && s !== '{}'))}${rand() < 0.2 ? ' ' + pick(COMMENTS) : ''}`);
      }
    } else {
      const v = pick(SCALARS);
      lines.push(`${pad}${key}:${v === '' ? '' : ' ' + v}${v !== '' && rand() < 0.2 ? ' ' + pick(COMMENTS) : ''}`);
    }
  }
  if (rand() < 0.2) lines.push(...trivia(indent));
  return lines;
}
const VALUES = [4, 0, -2, 1.5, true, false, null, 'text', 'two words', '', 'a: b', '1', 'null', ['x', 'y'], [], {}, { coders: 3 }, { a: { b: 1 } }, ['1', 2]];
for (let i = 0; i < Number(process.env.COUNT ?? 1500); i++) {
  let lines = [...(rand() < 0.2 ? trivia(0) : []), ...(rand() < 0.15 ? ['---'] : []), ...mapping(0, 0)];
  if (rand() < 0.1) lines.push('...');
  let text = lines.join('\n') + '\n';
  if (rand() < 0.2) text = text.replace(/\n/g, '\r\n');
  if (rand() < 0.15) text = text.replace(/\r?\n$/, '');
  const depth = 1 + Math.floor(rand() * 3);
  const key = Array.from({ length: depth }, () => pick(KEYS)).join('.');
  if (rand() < 0.6) add(`fuzz ${i} set`, text, key, 'set', pick(VALUES));
  else add(`fuzz ${i} remove`, text, key, 'remove', Math.floor(rand() * 3));
}

process.stdout.write(JSON.stringify(cases, null, 1) + '\n');
