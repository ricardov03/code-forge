// Makes plain_golden.json: the bytes the yaml package's
// stringify(value, {lineWidth: 0}) writes (serializeConfig in
// src/install/wizard/project-file.mjs) for each value, B527.
//
//   YAML_JS=<path to node_modules/yaml/dist/index.js> node gen_plain.mjs > plain_golden.json
//
// Each case: {name, value, out}. SEED and COUNT change the random part.
import { pathToFileURL } from 'node:url';

const yamlPath = process.env.YAML_JS;
if (!yamlPath) throw new Error('set YAML_JS to node_modules/yaml/dist/index.js');
const { stringify } = await import(pathToFileURL(yamlPath).href);

let seed = Number(process.env.SEED ?? 527);
const rand = (n) => {
  seed = (seed * 1103515245 + 12345) % 2147483648;
  return seed % n;
};
const pieces = ['a', 'Z', '0', '1', '9', ' ', '-', ':', '#', "'", '"', '\\', ',', '[', ']', '{', '}', '&', '*', '!',
  '|', '>', '%', '@', '`', '?', '.', '/', '_', 'true', 'null', '~', 'no', 'yes', '0x1F', '0o7', '1e3', '.5', '-1',
  '+2', '.inf', '.NaN', 'NULL', 'False', ' #', ': ', 'x'.repeat(90), 'npm', 'run', 'test', '=', '$', '(', ')', ';'];
const word = () => {
  let s = '';
  for (let i = rand(5); i >= 0; i--) s += pieces[rand(pieces.length)];
  return s;
};
const keys = ['a', 'b_c', 'L0', 'model', 'x-y', 'gates', 'K9'];
const scalar = () => {
  switch (rand(6)) {
    case 0: return rand(2) === 0;
    case 1: return null;
    case 2: return [0, 1, 2, 42, -3, 1.5, 0.25, 1e21, 123456789, -0.5][rand(10)];
    default: return word();
  }
};
const value = (depth) => {
  const r = rand(depth > 2 ? 2 : 4);
  if (r === 2) {
    const list = [];
    for (let i = rand(4); i > 0; i--) list.push(scalar());
    return list;
  }
  if (r === 3) {
    const obj = {};
    for (let i = rand(4); i > 0; i--) obj[keys[rand(keys.length)]] = value(depth + 1);
    return obj;
  }
  return scalar();
};

const cases = [
  { name: 'empty root', value: {} },
  { name: 'config shape', value: {
    version: 1, project: { name: 'demo', slug: 'demo' }, provider: 'anthropic',
    levels: { L0: { model: 'claude-haiku-4-5-20251001' }, L3: { model: 'gpt-6-astra', effort: 'high', provider: 'openai' } },
    caps: { coders: 1 }, review: { multimodel: false }, engine: 'auto', harnesses: [],
    system1: { fallback: 'rules' }, gates: { test: ['npm', 'test'], lint: null, types: ['npx', 'tsc', '--noEmit'], format: null },
    proof: { tiers: { high: { paths: [] } }, isolation: 'export', export: { link_dirs: ['node_modules'], copy_untracked: ['.env'] } },
    autonomy: { coder_may_push: false, coder_may_open_draft_pr: false } } },
  { name: 'gate words', value: { gates: { test: ['sh', '-c', 'npm test && echo "ok"', '', "it's", 'a:b', 'a: b', '#x', 'x #y', '- x', '-x'] } } },
  { name: 'long string', value: { a: 'word '.repeat(40).trim(), b: ['x'.repeat(200)] } },
  { name: 'nested empty', value: { a: { b: {} }, c: [], d: { e: [] } } },
];
const count = Number(process.env.COUNT ?? 600);
for (let i = 0; i < count; i++) {
  const v = {};
  for (let j = rand(5) + 1; j > 0; j--) v[keys[rand(keys.length)]] = value(0);
  cases.push({ name: `random ${i}`, value: v });
}
for (const c of cases) c.out = stringify(c.value, { lineWidth: 0 });
process.stdout.write(`${JSON.stringify(cases, null, 1)}\n`);
