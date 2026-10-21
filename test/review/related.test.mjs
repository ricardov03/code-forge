import { commitAll, makeRepo, writeFile } from './helpers.mjs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const { assembleLightPacket, assemblePacket, buildPacket, readFileDiff } = await import('../../src/review/packet.mjs');

const guidance = 'A finding that claims something is missing or wrong across the project must say in `evidence` which related file it could not see, e.g. "assumes no rule in src/styles/base.css".';

function section(text, heading) {
  return text.split(`## ${heading}\n`)[1]?.split('\n## ')[0].trimEnd() ?? null;
}

async function packet(repoRoot, file, lens = 'full', cfg = {}) {
  const result = await buildPacket({ repoRoot, file, lens, cfg });
  assert.equal(result.status, 'ok', `${file}: assembled packet`);
  assert.ok('text' in result);
  return result;
}

test('related packet names imports, importers and cascade siblings once, sorted, without their content', async () => {
  const repo = await makeRepo();
  const files = {
    'src/a.css': '@import "base.css";\n@import url(home.css);\n',
    'src/base.css': '* { animation: none !important; } /* UNSEEN_BASE */\n',
    'src/home.css': '@import url("./theme.scss");\n.home { color: red; }\n',
    'src/theme.scss': '/* UNSEEN_THEME */\n',
    'src/layout.astro': '---\nimport "./base.css";\nimport "./home.css";\n---\n',
    'src/page.html': '<link href="home.css" rel="stylesheet"><link href="base.css">\n',
    'src/widget.js': 'export const widget = 1;\n',
    'src/one.ts': 'import { widget } from "./widget.js";\n',
    'src/two.vue': '<script>const w = import("./widget.js");</script>\n',
    'src/three.cjs': 'const w = require("./widget.js");\n',
    'src/script.html': '<script src="widget.js"></script>\n',
  };
  for (const [file, text] of Object.entries(files)) writeFile(repo, file, text);
  await commitAll(repo);
  writeFile(repo, 'src/home.css', files['src/home.css'] + '.home { color: blue; }\n');
  writeFile(repo, 'src/widget.js', 'export const widget = 2;\n');
  for (const lens of ['quick', 'full']) {
    const css = await packet(repo, 'src/home.css', lens);
    assert.equal(section(css.text, 'related files (not shown)'), 'src/a.css\nsrc/base.css\nsrc/layout.astro\nsrc/page.html\nsrc/theme.scss', `${lens}: exactly five related names`);
    assert.equal((css.text.match(/UNSEEN_/g) ?? []).length, 0, `${lens}: no related content`);
    const js = await packet(repo, 'src/widget.js', lens);
    assert.equal(section(js.text, 'related files (not shown)'), 'src/one.ts\nsrc/script.html\nsrc/three.cjs\nsrc/two.vue', `${lens}: exactly four importers`);
  }
  const diffs = await Promise.all(['src/home.css', 'src/widget.js'].map((file) => readFileDiff({ repoRoot: repo, file })));
  const light = assembleLightPacket({ diffs });
  assert.equal(section(light.text, 'related files (not shown)'), 'src/a.css\nsrc/base.css\nsrc/layout.astro\nsrc/one.ts\nsrc/page.html\nsrc/script.html\nsrc/theme.scss\nsrc/three.cjs\nsrc/two.vue', 'light full packet includes exactly nine unseen related files');
  const lightWithout = light.text.replace(`## related files (not shown)\n${section(light.text, 'related files (not shown)')}\n`, '');
  const lightTight = assembleLightPacket({ diffs, cfg: { review: { budgets: { full_in: Math.ceil(Buffer.byteLength(lightWithout) / 4) } } } });
  assert.equal(lightTight.text, lightWithout, 'light full packet drops only related names before shrinking context');
});

test('related boundaries ignore bare/missing/self/untracked references, cap at 12, skip scans above 2000 and drop before paired/context/rules', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'src/empty.js', 'import "package"; import("./missing.js"); require("./empty.js");\n');
  writeFile(repo, 'src/untracked.ts', 'import "./empty.js";\n');
  const empty = await packet(repo, 'src/empty.js');
  assert.equal(section(empty.text, 'related files (not shown)'), null, 'zero related paths omit the section');
  const names = Array.from({ length: 14 }, (_, i) => `src/dep${String(i).padStart(2, '0')}.js`);
  for (const name of names) writeFile(repo, name, '/* UNSEEN_DEP */\n');
  writeFile(repo, 'src/many.js', names.map((name) => `import "./${name.slice(4)}";`).join('\n') + '\nimport "./dep00.js";\n');
  const many = await packet(repo, 'src/many.js');
  assert.deepEqual(section(many.text, 'related files (not shown)')?.split('\n'), names.slice(0, 12), 'exactly the first 12 sorted unique paths');

  const diff = await readFileDiff({ repoRoot: repo, file: 'src/many.js' });
  const opts = { diff, lens: 'full', paired: 'test/many.test.js\n- imports', rulesDigest: 'keep this rule', factsExcerpt: 'keep this fact' };
  const roomy = assemblePacket(opts);
  assert.equal(roomy.status, 'ok');
  assert.ok('text' in roomy);
  const related = section(roomy.text, 'related files (not shown)');
  assert.equal(related?.split('\n').length, 12, 'roomy assembled packet includes 12 paths');
  const without = roomy.text.replace(`## related files (not shown)\n${related}\n`, '');
  const tight = assemblePacket({ ...opts, budget: Math.ceil(Buffer.byteLength(without) / 4) });
  assert.equal(tight.status, 'ok');
  assert.ok('text' in tight);
  assert.equal(tight.text, without, 'only related section dropped: paired, whole context, rules, facts and diff retained');

  const { relatedFiles } = await import('../../src/review/related.mjs');
  const tracked = Array.from({ length: 2000 }, (_, i) => `src/importer${i}.js`);
  const reads = [];
  const readFile = (file) => { reads.push(file); return file.endsWith('/target.js') ? 'import "./dep.js";' : file.endsWith('/dep.js') ? '' : 'import "./target.js";'; };
  const atLimit = await relatedFiles({ repoRoot: repo, file: 'src/target.js', listFiles: () => tracked, readFile });
  assert.equal(atLimit.length, 12, '2000 tracked candidates are scanned and capped');
  reads.length = 0;
  assert.deepEqual(await relatedFiles({ repoRoot: repo, file: 'src/target.js', listFiles: () => [...tracked, 'src/extra.ts'], readFile }), ['src/dep.js'], '2001 candidates skip reverse scan but retain outgoing import');
  assert.equal(reads.filter((file) => /importer|extra/.test(file)).length, 0, 'no reverse candidate read above scan cap');
});

test('quick and full assembled lenses require one evidence assumption naming the unseen related file', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'home.css', '.home { animation: pulse 1s; }\n');
  for (const lens of ['quick', 'full']) {
    const result = await packet(repo, 'home.css', lens);
    assert.equal(result.text.split(guidance).length - 1, 1, `${lens}: exactly one project-wide evidence instruction`);
  }
});

test('quick and full evidence instruction survives when the unseen names are dropped over budget', async () => {
  const repo = await makeRepo();
  writeFile(repo, 'home.css', '@import "./base.css";\n.home { animation: pulse 1s; }\n');
  writeFile(repo, 'base.css', '* { animation: none !important; }\n');
  const diff = await readFileDiff({ repoRoot: repo, file: 'home.css' });
  for (const lens of ['quick', 'full']) {
    const roomy = assemblePacket({ diff, lens });
    assert.equal(roomy.status, 'ok');
    assert.ok('text' in roomy);
    const without = roomy.text.replace('## related files (not shown)\nbase.css\n', '');
    const tight = assemblePacket({ diff, lens, budget: Math.ceil(Buffer.byteLength(without) / 4) });
    assert.equal(tight.status, 'ok');
    assert.ok('text' in tight);
    assert.equal(tight.text.split(guidance).length - 1, 1, `${lens}: exactly one evidence instruction at budget boundary`);
    assert.equal(section(tight.text, 'related files (not shown)'), null, `${lens}: related section dropped`);
  }
});
