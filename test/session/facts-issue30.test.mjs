import { cfgWith, fakeDeps, freshDir, readRecords, writeIn } from './helpers.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { test } from 'node:test';

const { buildFacts, extractClaims, renderSheet, validateFacts } = await import('../../src/session/facts.mjs');
const { exec } = await import('../../src/util/exec.mjs');
const cfg = cfgWith({ provider: 'anthropic', model: 'fake' });

function put(dir, name, text = 'fixture\n') {
  mkdirSync(path.dirname(path.join(dir, name)), { recursive: true });
  writeFileSync(path.join(dir, name), text);
}

function git(dir, ...args) {
  return execFileSync('git', ['-c', 'user.name=fixture', '-c', 'user.email=fixture@example.invalid', ...args], { cwd: dir, encoding: 'utf8' }).trim();
}

function repo(files = {}) {
  const dir = freshDir('repo');
  git(dir, 'init', '-q');
  for (const [name, text] of Object.entries({ 'app/base.php': '<?php\n', ...files })) put(dir, name, text);
  git(dir, 'add', '.');
  git(dir, 'commit', '-qm', 'fixture');
  return dir;
}

// Inspect the actual packet and snapshot, then let the fake CLI return the answer.
/** @param {string} projectDir @param {string} text @param {string[]} [sources]
 * @param {(claim: {claim: string, source?: string, check: string, kind: string, cwd: string, id: string}) => object} [answer]
 * @param {(cwd: string) => void} [probe] */
async function sheet(projectDir, text, sources = [], answer = () => ({}), probe = () => {}) {
  const root = freshDir('facts-root');
  const briefPath = writeIn(root, 'brief.md', text);
  const outPath = path.join(root, 'sheet.md');
  const fake = fakeDeps();
  const ignoreCalls = [];
  let packet = '';
  const started = performance.now();
  const result = await buildFacts({ cfg, projectDir, sources, briefPath, outPath, runRoot: root,
    gitRun: async (argv, opts) => {
      if (argv[1] === 'check-ignore') ignoreCalls.push({ argv, cwd: opts.cwd, input: opts.input });
      return exec(argv, opts);
    },
  }, { ...fake.deps, exec: async (argv, opts) => {
    packet = String(opts.input);
    probe(opts.cwd);
    const facts = [...packet.matchAll(/^- (F\d+) (\w+): (.*)$/gm)].map((m) => {
      const body = m[3].replace(/ \[check with (?:find|ls -d) .*\]$/, '');
      const suffix = /^(.*?) \((?:source (.*?), )?check at (.*?)\)$/.exec(body);
      const claim = suffix?.[1] ?? body;
      const source = suffix?.[2];
      const check = suffix?.[3] ?? claim;
      const row = { fact_id: m[1], claim, kind: m[2], command: `ls ${check}`, output_excerpt: 'No such file', tag: 'NOT-FOUND', why: null };
      return { ...row, ...answer({ claim, source, check, kind: m[2], cwd: opts.cwd, id: m[1] }) };
    });
    return exec(argv, { ...opts, env: { ...opts.env, FAKE_ANSWER: JSON.stringify({ facts }) } });
  } });
  const elapsed = performance.now() - started;
  assert.equal(result.status, 'ok');
  const recorded = readRecords(fake.records);
  assert.equal(recorded.length, 1, 'one fake delegate');
  assert.equal(Buffer.from(recorded[0].stdin_b64, 'base64').toString(), packet);
  return { ...result, packet, markdown: readFileSync(outPath, 'utf8'), ignoreCalls, elapsed };
}

test('symbol extraction is opt-in: three whole code spans, no prose Model or partial spans', async () => {
  const text = '`SigmaClient.php` `TenantSubscription` `fiscal_documents` Model `Model` `TenantSubscription extra`';
  assert.deepEqual(extractClaims(text), [], 'plan-check extraction stays unchanged');
  const result = await sheet(repo(), text);
  assert.deepEqual(result.claims.map(({ token, kind }) => [token, kind]), [
    ['SigmaClient.php', 'symbol'], ['TenantSubscription', 'symbol'], ['fiscal_documents', 'symbol'],
  ]);
  assert.deepEqual(extractClaims(text, { symbols: true }), result.claims);
});

test('symbol proofs require quoted source grep or root find, with source hits and whole-folder absence', async () => {
  const dir = repo({ 'app/Models/TenantSubscription.php': 'class TenantSubscription\n', 'app/SigmaClient.php': 'class SigmaClient\n' });
  const proofs = [
    ["grep -rn -- 'class TenantSubscription' app", 'app/Models/TenantSubscription.php:3:class TenantSubscription', 'VERIFIED'],
    ["grep -rn -- 'TenantSubscription' app", 'docs/x.md:3:class TenantSubscription', 'UNVERIFIABLE'],
    ['grep -rn -- TenantSubscription app', 'app/Models/TenantSubscription.php:3:class TenantSubscription', 'UNVERIFIABLE'],
    ["grep -rn -- 'TenantSubscription' app", 'app/x.php:3:class TenantSubscriptionExtra', 'UNVERIFIABLE'],
    ["grep -rn -- 'TenantSubscription' app/Models", '', 'UNVERIFIABLE', 'NOT-FOUND'],
    ["grep -rn -- 'TenantSubscription' app", '', 'NOT-FOUND', 'NOT-FOUND'],
    ['ls app/Models/TenantSubscription.php', 'app/Models/TenantSubscription.php', 'UNVERIFIABLE'],
  ];
  // One fake session covers the valid grep; validation exercises each rejected proof against it.
  const result = await sheet(dir, '`TenantSubscription` `SigmaClient.php`', [], ({ kind, claim }) => kind === 'symbol' && claim === 'TenantSubscription'
    ? { command: proofs[0][0], output_excerpt: proofs[0][1], tag: 'VERIFIED' }
    : { command: 'find . -name SigmaClient.php', output_excerpt: './app/SigmaClient.php', tag: 'VERIFIED' });
  assert.deepEqual(result.facts.map((f) => f.tag), ['VERIFIED', 'VERIFIED']);
  assert.deepEqual(result.downgraded, []);
  const rootFile = validateFacts({ facts: [{ fact_id: 'F1', claim: 'SigmaClient.php', kind: 'symbol', command: 'find . -name SigmaClient.php', output_excerpt: './SigmaClient.php', tag: 'VERIFIED', why: null }] }, [{ token: 'SigmaClient.php', kind: 'symbol' }]);
  assert.equal(rootFile.facts[0].tag, 'VERIFIED', 'find can find a file directly in the root');
  const cwd = freshDir('symbol-validation');
  mkdirSync(path.join(cwd, 'app', 'Models'), { recursive: true });
  for (const [command, output_excerpt, expected, tag = 'VERIFIED'] of proofs) {
    const checked = validateFacts({ facts: [{ fact_id: 'F1', claim: 'TenantSubscription', kind: 'symbol', command, output_excerpt, tag, why: null }] },
      [{ token: 'TenantSubscription', kind: 'symbol' }], { cwd });
    assert.equal(checked.facts[0].tag, expected, command + ': ' + output_excerpt);
    if (expected === 'UNVERIFIABLE') assert.match(checked.facts[0].why, /grep -rn.*find/);
  }
  const badFind = validateFacts({ facts: [{ fact_id: 'F1', claim: 'SigmaClient.php', kind: 'symbol', command: 'find app -name SigmaClient.php', output_excerpt: './app/SigmaClient.php', tag: 'VERIFIED', why: null }] }, [{ token: 'SigmaClient.php', kind: 'symbol' }], { cwd });
  assert.equal(badFind.facts[0].tag, 'UNVERIFIABLE', 'file symbols search the whole root');
});

test('two ignored paths and one untracked path are unavailable before the packet, never NOT-FOUND', async () => {
  const dir = repo({ '.gitignore': 'vendor/\n.claude/plans/\n' });
  const paths = ['vendor/bin/pint', '.claude/plans/rules.md', 'notes/new.md'];
  for (const name of paths) put(dir, name);
  const result = await sheet(dir, paths.map((p) => '`' + p + '`').join(' '));
  assert.equal(result.facts.length, 3);
  assert.deepEqual(result.facts.map((f) => f.tag), Array(3).fill('UNVERIFIABLE'));
  assert.deepEqual(result.facts.map((f) => f.why.split(':')[0]), [
    `git-ignored in ${path.basename(dir)}`, `git-ignored in ${path.basename(dir)}`, `not at HEAD in ${path.basename(dir)}`,
  ]);
  assert.deepEqual(result.packet.match(/^- F\d+ /gm), null, 'all three withheld');
  assert.equal(result.ignoreCalls.length, 1);
  assert.deepEqual(String(result.ignoreCalls[0].input).split('\0').filter(Boolean), paths);
});

test('source directories and files choose check locations and add zero claims', async () => {
  const dir = repo();
  put(dir, 'notes/CLAUDE.md', '--force https://example.com/modules /a /h1 4.3');
  const result = await sheet(dir, '`app/base.php` `2.0.1`', [path.join(dir, 'notes'), path.join(dir, 'notes/CLAUDE.md')]);
  assert.deepEqual(result.claims.map(({ kind, token }) => [kind, token]), [['path', 'app/base.php'], ['version', '2.0.1']]);
  assert.equal(result.facts.length, 2);
});

test('outside git roots map one absolute and two relative claims, snapshot and sheet name the source', async () => {
  const a = repo({ 'app/Shared.php': 'A\n' });
  const b = repo({ 'app/Billing.php': 'B\n', 'app/Shared.php': 'B\n' });
  const label = path.basename(b);
  const result = await sheet(a, '`' + b + '/app/Billing.php` `app/Shared.php`', [a, b], ({ check }) => ({ command: `ls ${check}`, output_excerpt: check, tag: 'VERIFIED' }), (cwd) => {
    assert.equal(existsSync(path.join(cwd, '_sources', label, 'app/Billing.php')), true, 'repo B is in the snapshot');
    assert.equal(readFileSync(path.join(cwd, '_sources', label, 'app/Billing.php'), 'utf8'), 'B\n');
    assert.equal(statSync(path.join(cwd, '_sources', label)).mode & 0o777, 0o555);
  });
  assert.deepEqual(result.facts.map((f) => [f.claim, f.source, f.tag]), [
    [b + '/app/Billing.php', label, 'VERIFIED'], ['app/Shared.php', path.basename(a), 'VERIFIED'], ['app/Shared.php', label, 'VERIFIED'],
  ]);
  assert.match(result.markdown, /\| kind \| source \|/);
  assert.equal(result.ignoreCalls.length, 2);
  assert.deepEqual(result.downgraded, []);
});

test('glob paths accept quoted find and exact ls -d; devices and routes add no claims', async () => {
  const dir = repo({ 'lang/es/auth.php': 'hola\n', 'src/a.mjs': 'export {};\n' });
  for (const command of ["find lang/es -name '*.php'", 'ls -d lang/es/*.php', "ls -d 'lang/es/*.php'"]) {
    const result = await sheet(dir, '`lang/es/*.php` `/dev/null` `/dev/tty` `/api/v1/*` `/portal/*` `/a` `/h1`', [], () => ({ command, output_excerpt: 'lang/es/auth.php', tag: 'VERIFIED' }));
    assert.deepEqual(result.claims.map((c) => c.token), ['lang/es/*.php']);
    assert.deepEqual(result.facts.map((f) => f.tag), ['VERIFIED'], command);
    assert.match(result.packet, /^- F1 path: lang\/es\/\*\.php .*find lang\/es -name '\*\.php'/m);
  }
  for (const [brief, command, excerpt, tag] of [
    ['`src/a.mjs`', 'ls -d src/*.mjs', 'src/a.mjs', 'VERIFIED'],
    ['`lang/es/*.php`', 'ls -d lang/es/*.php', 'lang/en/auth.php', 'VERIFIED'],
    ['`lang/es/*.php`', 'ls -d lang/es/*.php', '', 'NOT-FOUND'],
    ['`lang/es/*.php`', "find lang/es -name '*.php'", '', 'NOT-FOUND'],
  ]) {
    const result = await sheet(dir, brief, [], () => ({ command, output_excerpt: excerpt, tag }));
    assert.deepEqual(result.facts.map((f) => f.tag), ['UNVERIFIABLE'], command);
  }
});

test('single-root rows and rendering stay byte-identical without source metadata', async () => {
  const dir = repo();
  const result = await sheet(dir, '`app/base.php`', [], () => ({ command: 'ls app/base.php', output_excerpt: 'app/base.php', tag: 'VERIFIED' }));
  assert.deepEqual(result.facts, [{ fact_id: 'F1', claim: 'app/base.php', kind: 'path', command: 'ls app/base.php', output_excerpt: 'app/base.php', tag: 'VERIFIED', why: null }]);
  const rendered = renderSheet({ briefRef: 'b', briefSha: 's', briefMtime: 'm', builtAt: 't', facts: result.facts });
  assert.equal(rendered, '# Facts sheet\n\n- brief: `b`\n- brief_sha256: `s`\n- brief_mtime: `m`\n- built_at: `t`\n- claims: 1 (VERIFIED 1 · NOT-FOUND 0 · UNVERIFIABLE 0)\n\n| fact_id | tag | kind | claim | command | output | why |\n|---|---|---|---|---|---|---|\n| F1 | VERIFIED | path | `app/base.php` | `ls app/base.php` | app/base.php | — |\n\n<!-- facts-json\n[\n' + JSON.stringify(result.facts[0]) + '\n]\n-->\n');
  assert.equal(result.markdown.includes('| source |'), false);
});

test('single-root absolute project paths keep check locations without source metadata', async () => {
  const dir = repo();
  const token = path.join(dir, 'app/base.php');
  const result = await sheet(dir, '`' + token + '`', [], ({ check }) => ({ command: `ls ${check}`, output_excerpt: check, tag: 'VERIFIED' }));
  assert.equal(result.claims.length, 1);
  assert.equal(result.claims[0].checkPath, 'app/base.php');
  assert.equal(result.claims[0].root.dir, dir);
  assert.equal(Object.hasOwn(result.claims[0], 'source'), false);
  assert.equal(Object.hasOwn(result.facts[0], 'source'), false);
  assert.equal(result.facts[0].tag, 'VERIFIED');
  assert.equal(result.facts[0].command, 'ls app/base.php');
  assert.equal(result.markdown.includes('| source |'), false);
  assert.deepEqual(result.downgraded, []);
});

test('directory globs reject literal find errors as NOT-FOUND', async () => {
  const dir = repo({ 'src/x/a.mjs': 'export {};\n' });
  const result = await sheet(dir, '`src/*/a.mjs`', [], () => ({ command: "find 'src/*' -name 'a.mjs'", output_excerpt: 'find: src/*: No such file or directory', tag: 'NOT-FOUND' }));
  assert.deepEqual(result.facts.map((f) => f.tag), ['UNVERIFIABLE']);
  assert.equal(result.downgraded.length, 1);
  for (const glob of ['*', '?', '[x]', '{x,y}']) {
    const token = `src/${glob}/a.mjs`;
    const checked = validateFacts({ facts: [{ fact_id: 'F1', claim: token, kind: 'path', command: `find 'src/${glob}' -name 'a.mjs'`, output_excerpt: `find: src/${glob}: No such file or directory`, tag: 'NOT-FOUND', why: null }] }, [{ token, kind: 'path' }]);
    assert.equal(checked.facts[0].tag, 'UNVERIFIABLE', token);
  }
});

test('directory glob packets suggest exact ls -d and matching output is FOUND', async () => {
  const dir = repo({ 'src/x/a.mjs': 'export {};\n' });
  for (const command of ['ls -d src/*/a.mjs', "ls -d 'src/*/a.mjs'"]) {
    const result = await sheet(dir, '`src/*/a.mjs`', [], () => ({ command, output_excerpt: 'src/x/a.mjs', tag: 'VERIFIED' }));
    assert.deepEqual(result.claims.map((c) => c.token), ['src/*/a.mjs']);
    const claimLine = result.packet.split('\n').find((line) => line.startsWith('- F1 '));
    assert.doesNotMatch(claimLine, /check with find/);
    assert.match(claimLine, /\[check with ls -d 'src\/\*\/a\.mjs'\]/);
    assert.deepEqual(result.facts.map((f) => f.tag), ['VERIFIED'], command);
    assert.deepEqual(result.downgraded, []);
  }
});

test('root boundaries: unrelated absolute paths are unavailable; duplicate labels, non-git sources and source files are sealed', async () => {
  const a = repo();
  const parent = freshDir('outside');
  const b = path.join(parent, path.basename(a));
  mkdirSync(b);
  put(b, 'app/plain.php');
  put(b, '.env', 'secret');
  symlinkSync(path.join(a, 'app'), path.join(b, 'link'));
  const fileDir = freshDir('file-source');
  const file = writeIn(fileDir, 'only.txt', 'one file\n');
  const result = await sheet(a, '`/etc/hosts` `' + b + '/app/plain.php` `' + file + '`', [b, file], () => ({ tag: 'UNVERIFIABLE', why: 'fixture' }), (cwd) => {
    assert.equal(existsSync(path.join(cwd, '_sources', path.basename(a) + '-2', 'app/plain.php')), true);
    assert.equal(existsSync(path.join(cwd, '_sources', path.basename(a) + '-2', '.env')), false);
    assert.equal(existsSync(path.join(cwd, '_sources', path.basename(a) + '-2', 'link')), false);
    assert.equal(readFileSync(path.join(cwd, '_sources', path.basename(fileDir), 'only.txt'), 'utf8'), 'one file\n');
  });
  assert.equal(result.facts.length, 3);
  assert.equal(result.facts[0].why, 'outside every --sources root and the snapshot');
  assert.equal(result.packet.includes('- F1 '), false);
  const repeated = validateFacts({ facts: [{ fact_id: 'F99', claim: 'same.php', kind: 'path', command: 'ls same.php', output_excerpt: 'same.php', tag: 'VERIFIED', why: null }] }, [
    { token: 'same.php', kind: 'path', source: 'A' }, { token: 'same.php', kind: 'path', source: 'B' },
  ]);
  assert.deepEqual(repeated.facts.map((f) => f.tag), ['UNVERIFIABLE', 'UNVERIFIABLE'], 'repeated tokens only match by id');
  const traversal = await sheet(a, '`../app/base.php`', [b], () => ({ command: 'ls _sources/app/base.php', output_excerpt: '_sources/app/base.php', tag: 'VERIFIED' }));
  assert.deepEqual(traversal.facts.map((f) => f.tag), ['UNVERIFIABLE', 'UNVERIFIABLE'], 'root prefixes never hide a .. segment');
});

test('outside git subfolder roots include only their HEAD subtree, without untracked files or sibling content', async () => {
  const a = repo();
  const b = repo({ 'module/app/Only.php': 'class Only\n', 'sibling/private.php': 'sibling\n' });
  put(b, 'module/app/untracked.php');
  const source = path.join(b, 'module');
  const result = await sheet(a, '`' + source + '/app/Only.php`', [source], ({ check }) => ({ command: `ls ${check}`, output_excerpt: check, tag: 'VERIFIED' }), (cwd) => {
    const dest = path.join(cwd, '_sources/module');
    assert.equal(existsSync(path.join(dest, 'app/Only.php')), true, 'HEAD subtree is mounted at its root');
    assert.deepEqual(['app/untracked.php', 'sibling/private.php', 'module/app/Only.php'].map((p) => existsSync(path.join(dest, p))), [false, false, false]);
  });
  assert.deepEqual(result.facts.map((f) => [f.source, f.tag]), [['module', 'VERIFIED']]);
});

async function customerCase() {
  const aFiles = {
    'CLAUDE.md': '--diff --database --force https://acme-app.example/modules /a /p /h1 /b 4.3 8.1 0.13',
    '.gitignore': 'vendor/\n.claude/plans/\n',
    'app/Services/SigmaClient.php': 'class SigmaClient\n',
    'app/Models/TenantSubscription.php': 'class TenantSubscription\n',
    'app/Models/ApiCredential.php': 'class ApiCredential\n',
    'app/Support/FeatureCatalog.php': 'class FeatureCatalog\n',
    'database/migrations/x_create_fiscal_documents.php': 'fiscal_documents\n',
    'lang/es/auth.php': 'hola\n', 'routes/api/v1.php': 'route\n',
  };
  const bFiles = {
    'app/Fiscal/FiscalSender.php': 'class FiscalSender\n',
    'app/Billing/CondominiumSubscriptionResolver.php': 'class CondominiumSubscriptionResolver\n',
    'app/Data/MoneyData.php': 'class MoneyData\n',
    'database/migrations/x.php': 'finance_movements\n',
  };
  const a = repo(aFiles), b = repo(bFiles);
  const ignored = ['.claude/plans/_coder-rules.md', '.claude/plans/work-codes.md', 'vendor/bin/pint'];
  for (const name of ignored) put(a, name);
  const symbols = ['SigmaClient.php', 'TenantSubscription', 'ApiCredential', 'FiscalSender', 'FeatureCatalog.php', 'CondominiumSubscriptionResolver.php', 'WebhookDelivery', 'MoneyData', 'fiscal_documents', 'finance_movements'];
  const tokens = [a, b, b + '/app/Fiscal/FiscalSender.php', ...symbols, ...ignored, 'ls -d lang/es/*.php', 'routes/api/*.php', '/dev/null', '/api/v1/*', '/portal/*',
    'app/Services/SigmaClient.php', 'app/Models/TenantSubscription.php', 'app/Models/ApiCredential.php', 'app/Support/FeatureCatalog.php', 'app/Fiscal/FiscalSender.php', 'app/Data/MoneyData.php', 'lang/es/auth.php', 'routes/api/v1.php', '--max-items', '2.0.0', '@scope/tool'];
  const result = await sheet(a, tokens.map((t) => '`' + t + '`').join('\n'), [a, b], ({ claim, source, check, kind }) => {
    const files = source === path.basename(b) ? bFiles : aFiles;
    const prefix = source === path.basename(b) ? '_sources/' + source + '/' : '';
    if (kind === 'symbol') {
      const hit = Object.entries(files).find(([name, content]) => claim.endsWith('.php') ? name.endsWith('/' + claim) : content.includes(claim));
      if (claim.endsWith('.php')) return { command: `find ${prefix ? prefix.slice(0, -1) : '.'} -name ${claim}`, output_excerpt: hit ? (prefix || './') + hit[0] : '', tag: hit ? 'VERIFIED' : 'NOT-FOUND' };
      const folder = hit ? hit[0].split('/')[0] : 'app';
      return { command: `grep -rn -- '${claim}' ${prefix}${folder}`, output_excerpt: hit ? prefix + hit[0] + ':1:' + hit[1].trim() : '', tag: hit ? 'VERIFIED' : 'NOT-FOUND' };
    }
    if (kind === 'flag') return { command: `grep -rn -- ${claim} ${prefix}app`, output_excerpt: '', tag: 'NOT-FOUND' };
    if (kind !== 'path') return { tag: 'UNVERIFIABLE', why: 'fixture has no registry' };
    if (prefix && (check.includes('*') || ignored.includes(claim))) return { tag: 'UNVERIFIABLE', why: 'project support path is not in this source' };
    if (check.includes('*')) return { command: `find ${path.posix.dirname(check)} -name '${path.posix.basename(check)}'`, output_excerpt: check.replace('*', check.includes('lang/') ? 'auth' : 'v1'), tag: 'VERIFIED' };
    return { command: `ls ${check}`, output_excerpt: check, tag: 'VERIFIED' };
  });
  return { result, a, b, aFiles, bFiles, symbols, ignored };
}

test('customer case: brief-only facts across two repos, real symbol presence, ignored paths and globs without downgrades', async () => {
  const { result, a, b, aFiles, bFiles, symbols, ignored } = await customerCase();
  // 2 absolute roots + 1 absolute file + 10 symbols * 2 + 3 ignored paths * 2 +
  // 2 globs * 2 + 8 filler paths * 2 + 1 flag * 2 + version + package.
  assert.equal(result.claims.length, 53, 'only the mapped brief claims');
  const pollution = ['--diff', '--database', '--force', 'https://acme-app.example/modules', '/a', '/p', '/h1', '/b', '4.3', '8.1', '0.13', '/dev/null', '/api/v1/*', '/portal/*'];
  assert.deepEqual(result.claims.filter((c) => pollution.includes(c.token)), []);
  assert.equal(result.facts.filter((f) => f.kind === 'symbol').length, 20);
  for (const symbol of symbols) {
    const rows = result.facts.filter((f) => f.claim === symbol);
    assert.equal(rows.length, 2, symbol);
    for (const row of rows) {
      const files = row.source === path.basename(b) ? bFiles : aFiles;
      const present = Object.entries(files).some(([name, content]) => symbol.endsWith('.php') ? name.endsWith('/' + symbol) : content.includes(symbol));
      assert.equal(row.tag, present ? 'VERIFIED' : 'NOT-FOUND', row.source + ': ' + symbol);
    }
  }
  assert.deepEqual(result.facts.filter((f) => f.claim === 'WebhookDelivery').map((f) => f.tag), ['NOT-FOUND', 'NOT-FOUND']);
  const unavailable = result.facts.filter((f) => ignored.includes(f.claim) && f.source === path.basename(a));
  assert.equal(unavailable.length, 3);
  assert.equal(unavailable.every((f) => f.tag === 'UNVERIFIABLE' && f.why.includes('git-ignored')), true);
  assert.deepEqual(result.facts.filter((f) => ignored.includes(f.claim) && f.tag === 'NOT-FOUND'), []);
  assert.deepEqual(result.facts.filter((f) => f.claim.includes('*') && f.tag === 'NOT-FOUND'), []);
  assert.deepEqual(result.downgraded, []);
});

test('speed: customer buildFacts takes under 8000ms and batches check-ignore exactly once per root', async () => {
  const { result, a, b } = await customerCase();
  assert.deepEqual(result.ignoreCalls.map((c) => c.cwd), [a, b]);
  assert.equal(result.ignoreCalls.every((c) => c.argv.join(' ') === 'git check-ignore -z --stdin'), true);
  assert.ok(result.elapsed < 8000, `customer case took ${result.elapsed}ms`);
});
