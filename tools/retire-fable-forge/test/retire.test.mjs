import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const BIN = fileURLToPath(new URL('../bin/retire-fable-forge.mjs', import.meta.url));
const ORIGINAL = '---\nname: fable-forge\ndescription: old pipeline\n---\n\n# Fable Forge\nbody line\n';

let parent;
before(() => {
  parent = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'retire-ff-')));
});
after(() => fs.rmSync(parent, { recursive: true, force: true }));

let n = 0;
/** A fresh temp HOME and temp cwd under the one parent. */
function sandbox() {
  const root = path.join(parent, `case-${++n}`);
  const home = path.join(root, 'home');
  const cwd = path.join(root, 'cwd');
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(cwd, { recursive: true });
  return { root, home, cwd };
}

/** @param {string} dir @param {string} [skillMd] */
function makeSkill(dir, skillMd = ORIGINAL) {
  fs.mkdirSync(path.join(dir, 'references'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'SKILL.md'), skillMd);
  fs.writeFileSync(path.join(dir, 'references', 'solo-spawn-protocol.md'), 'protocol\n');
}

/** Run the CLI with no TTY (stdin ignored), temp HOME and temp cwd. */
function run(args, { home, cwd }) {
  const r = spawnSync(process.execPath, [BIN, ...args], {
    cwd,
    env: { PATH: process.env.PATH, HOME: home },
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf8',
  });
  return { code: r.status, out: r.stdout + r.stderr };
}

/** Every file path + content under dir (links reported, not followed). */
function snapshot(dir) {
  const out = {};
  const walk = (d) => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      const rel = path.relative(dir, p);
      if (e.isSymbolicLink()) out[rel] = `link:${fs.readlinkSync(p)}`;
      else if (e.isDirectory()) {
        out[`${rel}/`] = 'dir';
        walk(p);
      } else out[rel] = fs.readFileSync(p, 'utf8');
    }
  };
  walk(dir);
  return out;
}

function backups(home) {
  const d = path.join(home, '.code-forge-retired');
  return fs.existsSync(d) ? fs.readdirSync(d) : [];
}

describe('retire-fable-forge', () => {
  test('nothing found: exit 0, "nothing to retire", 0 files created', () => {
    const s = sandbox();
    const before = snapshot(s.root);
    const r = run(['--remove', '--yes'], s);
    assert.equal(r.code, 0);
    assert.match(r.out, /nothing to retire/);
    assert.deepEqual(snapshot(s.root), before);
  });

  test('dry run (no flag, and --alias --dry-run) lists all 3 installs and changes 0 files', () => {
    const s = sandbox();
    makeSkill(path.join(s.home, '.claude', 'skills', 'fable-forge'));
    makeSkill(path.join(s.cwd, '.claude', 'skills', 'fable-forge'), `${ORIGINAL}project\n`);
    const target = path.join(s.root, 'src-skill');
    makeSkill(target, `${ORIGINAL}linked\n`);
    fs.mkdirSync(path.join(s.home, '.agents', 'skills'), { recursive: true });
    fs.symlinkSync(target, path.join(s.home, '.agents', 'skills', 'fable-forge'));
    fs.mkdirSync(path.join(s.home, '.claude', 'projects', 'p1', 'memory'), { recursive: true });
    fs.writeFileSync(
      path.join(s.home, '.claude', 'projects', 'p1', 'memory', 'MEMORY.md'),
      '# mem\n- use /fable-forge for plans\n- other\n',
    );
    const before = snapshot(s.root);
    for (const args of [[], ['--dry-run'], ['--alias', '--dry-run', '--yes'], ['--remove', '--dry-run', '--yes']]) {
      const r = run(args, s);
      assert.equal(r.code, 0, r.out);
      assert.match(r.out, /fable-forge installs \(3\):/);
      assert.match(r.out, new RegExp(`\\.agents/skills/fable-forge  symlink -> ${target}`));
      assert.match(r.out, /\[user\] .*\.claude\/skills\/fable-forge  directory/);
      assert.match(r.out, /\[project\] .*cwd\/\.claude\/skills\/fable-forge  directory/);
      assert.match(r.out, /MEMORY\.md:2: - use \/fable-forge for plans/);
      assert.match(r.out, /dry run: nothing changed/);
    }
    assert.deepEqual(snapshot(s.root), before);
  });

  test('two identical folders are reported as a copy', () => {
    const s = sandbox();
    makeSkill(path.join(s.home, '.claude', 'skills', 'fable-forge'));
    makeSkill(path.join(s.home, '.agents', 'skills', 'fable-forge'));
    const r = run([], s);
    assert.equal(r.code, 0);
    assert.equal((r.out.match(/copy \(same SKILL\.md as /g) ?? []).length, 2);
  });

  test('--alias --yes backs up and leaves a <=5-line SKILL.md naming fable-forge and /code-forge', () => {
    const s = sandbox();
    const skill = path.join(s.home, '.claude', 'skills', 'fable-forge');
    makeSkill(skill);
    const r = run(['--alias', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    const md = fs.readFileSync(path.join(skill, 'SKILL.md'), 'utf8');
    const lines = md.trimEnd().split('\n');
    assert.equal(lines.length, 5);
    assert.equal(lines[1], 'name: fable-forge');
    assert.match(md, /use \/code-forge instead/);
    const b = backups(s.home);
    assert.equal(b.length, 1);
    assert.match(b[0], /^fable-forge-\d{4}-\d\d-\d\dT[\d-]+Z-user-claude\.tgz$/);
    const listing = execFileSync('tar', ['-tzf', path.join(s.home, '.code-forge-retired', b[0])], {
      encoding: 'utf8',
    });
    assert.match(listing, /^fable-forge\/SKILL\.md$/m);
    assert.match(r.out, /^restore: /m);
  });

  test('--remove --yes on a directory deletes it after a verified backup; restore command restores it', () => {
    const s = sandbox();
    const skill = path.join(s.cwd, '.claude', 'skills', 'fable-forge');
    makeSkill(skill);
    const original = snapshot(skill);
    const r = run(['--remove', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    assert.equal(fs.existsSync(skill), false);
    assert.match(r.out, /backup: .*project-claude\.tgz \(verified\)[\s\S]*removed: /);
    const restore = /^restore: (.+)$/m.exec(r.out);
    assert.ok(restore);
    assert.ok(!/~|\$HOME/.test(restore[1]));
    execFileSync('/bin/sh', ['-c', restore[1]], { cwd: s.cwd, env: { PATH: process.env.PATH, HOME: s.home } });
    assert.deepEqual(snapshot(skill), original);
  });

  test('restore after --alias brings back the original SKILL.md', () => {
    const s = sandbox();
    const skill = path.join(s.home, '.agents', 'skills', 'fable-forge');
    makeSkill(skill);
    const r = run(['--alias', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    execFileSync('/bin/sh', ['-c', /^restore: (.+)$/m.exec(r.out)[1]], { cwd: s.cwd, env: { PATH: process.env.PATH, HOME: s.home } });
    assert.equal(fs.readFileSync(path.join(skill, 'SKILL.md'), 'utf8'), ORIGINAL);
  });

  test('--remove --yes on a symlink unlinks it and leaves the target untouched', () => {
    const s = sandbox();
    const target = path.join(s.root, 'repo-skill');
    makeSkill(target);
    const targetBefore = snapshot(target);
    const link = path.join(s.home, '.claude', 'skills', 'fable-forge');
    fs.mkdirSync(path.dirname(link), { recursive: true });
    fs.symlinkSync(target, link);
    const r = run(['--remove', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    assert.equal(fs.lstatSync(link, { throwIfNoEntry: false }), undefined);
    assert.deepEqual(snapshot(target), targetBefore);
    assert.match(r.out, /unlinked: /);
    execFileSync('/bin/sh', ['-c', /^restore: (.+)$/m.exec(r.out)[1]], { cwd: s.cwd, env: { PATH: process.env.PATH, HOME: s.home } });
    assert.equal(fs.readlinkSync(link), target);
  });

  test('--alias --yes on a symlink replaces the link with an alias folder; target untouched', () => {
    const s = sandbox();
    const target = path.join(s.root, 'repo-skill');
    makeSkill(target);
    const targetBefore = snapshot(target);
    const link = path.join(s.home, '.claude', 'skills', 'fable-forge');
    fs.mkdirSync(path.dirname(link), { recursive: true });
    fs.symlinkSync(target, link);
    const r = run(['--alias', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    assert.equal(fs.lstatSync(link).isDirectory(), true);
    assert.match(fs.readFileSync(path.join(link, 'SKILL.md'), 'utf8'), /^name: fable-forge$/m);
    assert.deepEqual(snapshot(target), targetBefore);
  });

  test('a folder without "name: fable-forge" is refused (exit 1, 0 changes, 0 backups)', () => {
    for (const md of ['---\nname: something-else\n---\n', '---\nname: code-forge\n---\n', 'no frontmatter\n']) {
      const s = sandbox();
      makeSkill(path.join(s.home, '.claude', 'skills', 'fable-forge'), md);
      const before = snapshot(s.root);
      const r = run(['--remove', '--yes'], s);
      assert.equal(r.code, 1, r.out);
      assert.match(r.out, /REFUSED: /);
      assert.deepEqual(snapshot(s.root), before);
      assert.equal(backups(s.home).length, 0);
    }
  });

  test('no TTY and no --yes is refused (exit 1, 0 changes)', () => {
    const s = sandbox();
    makeSkill(path.join(s.home, '.claude', 'skills', 'fable-forge'));
    const before = snapshot(s.root);
    for (const mode of ['--alias', '--remove']) {
      const r = run([mode], s);
      assert.equal(r.code, 1);
      assert.match(r.out, /--yes is required when there is no TTY/);
    }
    assert.deepEqual(snapshot(s.root), before);
  });

  test('usage errors exit 2', () => {
    const s = sandbox();
    assert.equal(run(['--alias', '--remove'], s).code, 2);
    assert.equal(run(['--bogus'], s).code, 2);
    assert.equal(run(['--home'], s).code, 2);
  });

  test('--home points detection and backups at that directory', () => {
    const s = sandbox();
    const other = path.join(s.root, 'other-home');
    makeSkill(path.join(other, '.claude', 'skills', 'fable-forge'));
    const r = run(['--remove', '--yes', '--home', other], s);
    assert.equal(r.code, 0, r.out);
    assert.equal(backups(other).length, 1);
    assert.equal(backups(s.home).length, 0);
  });

  test('--alias on a folder whose SKILL.md is a symlink leaves the outside file byte-identical', () => {
    const s = sandbox();
    const skill = path.join(s.home, '.claude', 'skills', 'fable-forge');
    makeSkill(skill);
    const outside = path.join(s.root, 'outside-SKILL.md');
    fs.writeFileSync(outside, ORIGINAL);
    const outsideBytes = fs.readFileSync(outside);
    fs.rmSync(path.join(skill, 'SKILL.md'));
    fs.symlinkSync(outside, path.join(skill, 'SKILL.md'));
    const r = run(['--alias', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    assert.deepEqual(fs.readFileSync(outside), outsideBytes);
    assert.equal(fs.lstatSync(path.join(skill, 'SKILL.md')).isFile(), true);
    assert.match(fs.readFileSync(path.join(skill, 'SKILL.md'), 'utf8'), /^name: fable-forge$/m);
  });

  test('a symlinked skills root is found once, reported, and --remove --yes exits 0', () => {
    const s = sandbox();
    const skill = path.join(s.home, '.claude', 'skills', 'fable-forge');
    makeSkill(skill);
    fs.mkdirSync(path.join(s.home, '.codex'), { recursive: true });
    fs.symlinkSync(path.join(s.home, '.claude', 'skills'), path.join(s.home, '.codex', 'skills'));
    const dry = run([], s);
    assert.equal(dry.code, 0, dry.out);
    assert.match(dry.out, /fable-forge installs \(1\):/);
    assert.equal((dry.out.match(/also reached via .*\.codex\/skills\/fable-forge \(symlinked skills root\)/g) ?? []).length, 1);
    const r = run(['--remove', '--yes'], s);
    assert.equal(r.code, 0, r.out);
    assert.equal((r.out.match(/^removed: /gm) ?? []).length, 1);
    assert.equal(fs.existsSync(skill), false);
    assert.equal(backups(s.home).length, 1);
  });

  test('a code-forge folder next to fable-forge is untouched by --remove and --alias', () => {
    for (const mode of ['--remove', '--alias']) {
      const s = sandbox();
      const cf = path.join(s.home, '.claude', 'skills', 'code-forge');
      makeSkill(cf, '---\nname: code-forge\ndescription: new\n---\n');
      makeSkill(path.join(s.home, '.claude', 'skills', 'fable-forge'));
      const cfBefore = snapshot(cf);
      const r = run([mode, '--yes'], s);
      assert.equal(r.code, 0, r.out);
      assert.deepEqual(snapshot(cf), cfBefore);
      assert.equal(backups(s.home).length, 1);
    }
  });

  test('backup cannot be written: non-zero exit, skill folder byte-identical, no "removed:"', () => {
    const s = sandbox();
    const skill = path.join(s.home, '.claude', 'skills', 'fable-forge');
    makeSkill(skill);
    fs.writeFileSync(path.join(s.home, '.code-forge-retired'), 'not a directory\n');
    const before = snapshot(skill);
    const r = run(['--remove', '--yes'], s);
    assert.notEqual(r.code, 0);
    assert.deepEqual(snapshot(skill), before);
    assert.doesNotMatch(r.out, /removed:/);
    assert.match(r.out, /^failed: /m);
  });
});
