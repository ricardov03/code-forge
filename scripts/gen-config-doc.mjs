#!/usr/bin/env node
/**
 * Generates `docs/reference/config.md` from `schema/code-forge.schema.json` (plan §1.3; block
 * B16). The schema is the single source of truth for every `.code-forge.yml` key — this script
 * only reads the schema file and `$defs` inside it; it never imports `src/config/**` (B16's
 * `depends_on` is B13a only, so it stays out of the config blocks' seam).
 *
 * `node scripts/gen-config-doc.mjs`         writes docs/reference/config.md
 * `node scripts/gen-config-doc.mjs --check` prints nothing on a match and exits 0; on a mismatch
 *   prints the first differing line to stderr and exits 1. Never edit docs/reference/config.md by
 *   hand — regenerate it and commit the result.
 */

import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.join(__dirname, '..');
const SCHEMA_PATH = path.join(REPO_ROOT, 'schema', 'code-forge.schema.json');
const OUT_PATH = path.join(REPO_ROOT, 'docs', 'reference', 'config.md');

/**
 * @param {any} node @param {Record<string, any>} defs
 * @returns {any} `node` itself, or the `$defs` entry it points at when `node.$ref` is set.
 */
function resolveRef(node, defs) {
  if (node && typeof node === 'object' && typeof node.$ref === 'string') {
    const name = node.$ref.split('/').pop();
    return defs[String(name)];
  }
  return node;
}

/**
 * A short, table-cell-safe type label. Pipes inside a union are escaped (`\|`) so they never read
 * as a markdown table column separator.
 * @param {any} nodeIn @param {Record<string, any>} defs
 * @returns {string}
 */
function typeLabel(nodeIn, defs) {
  const node = resolveRef(nodeIn, defs);
  if (!node || typeof node !== 'object') return 'unknown';
  if (node.const !== undefined) return `const \`${JSON.stringify(node.const)}\``;
  if (Array.isArray(node.enum)) return `enum: ${node.enum.map((v) => `\`${v}\``).join(', ')}`;
  if (Array.isArray(node.anyOf)) return node.anyOf.map((sub) => typeLabel(sub, defs)).join(' \\| ');
  if (node.type === 'array') return `array<${typeLabel(node.items, defs)}>`;
  if (Array.isArray(node.type)) return node.type.join(' \\| ');
  return typeof node.type === 'string' ? node.type : 'object';
}

/** @param {any} nodeIn @param {Record<string, any>} defs @returns {string} */
function defaultLabel(nodeIn, defs) {
  const node = resolveRef(nodeIn, defs);
  return node && node.default !== undefined ? `\`${JSON.stringify(node.default)}\`` : '—';
}

/** @param {any} nodeIn @param {Record<string, any>} defs @returns {string} */
function notesLabel(nodeIn, defs) {
  const node = resolveRef(nodeIn, defs) ?? {};
  const parts = [];
  if (node.minimum !== undefined) parts.push(`min ${node.minimum}`);
  if (node.maximum !== undefined) parts.push(`max ${node.maximum}`);
  if (node.exclusiveMinimum !== undefined) parts.push(`> ${node.exclusiveMinimum}`);
  if (node.minLength !== undefined) parts.push(`minLength ${node.minLength}`);
  if (node.minItems !== undefined) parts.push(`minItems ${node.minItems}`);
  if (typeof node.description === 'string' && node.type !== 'object') parts.push(node.description);
  return parts.join('; ');
}

/** @param {string[]} keys @returns {string} `` `a` ``, `` `a` and `b` `` or `` `a`, `b` and `c` `` */
function backtickList(keys) {
  const quoted = keys.map((k) => `\`${k}\``);
  if (quoted.length <= 1) return quoted.join('');
  return `${quoted.slice(0, -1).join(', ')} and ${quoted[quoted.length - 1]}`;
}

/**
 * The intro sentence naming the top-level required keys — built from `schema.required` (never
 * hard-coded, so a schema amendment that adds/removes a required key changes this sentence on the
 * next regeneration instead of silently going stale) plus a note for whichever of those required
 * keys carry no schema-level `default` (an omitted one is refused, not defaulted).
 * @param {any} schema @param {Record<string, any>} defs
 * @returns {string}
 */
function requiredKeysSentence(schema, defs) {
  const required = Array.isArray(schema.required) ? schema.required : [];
  if (required.length === 0) return 'No top-level key is required by the schema.';
  const noDefault = required.filter((key) => {
    const propNode = resolveRef(schema.properties?.[key], defs);
    return !propNode || propNode.default === undefined;
  });
  const be = required.length === 1 ? 'is' : 'are';
  let sentence = `${backtickList(required)} ${be} required.`;
  if (noDefault.length > 0) {
    const have = noDefault.length === 1 ? 'has' : 'have';
    const it = noDefault.length === 1 ? 'it' : 'any of them';
    sentence += ` ${backtickList(noDefault)} ${have} no schema-level default; a config omitting ${it} is refused.`;
  }
  const withDefault = required.filter((k) => !noDefault.includes(k));
  if (withDefault.length > 0) {
    const have = withDefault.length === 1 ? 'has' : 'have';
    sentence += ` ${backtickList(withDefault)} ${have} a schema-level default, so omitting it is not itself refused.`;
  }
  return sentence;
}

/**
 * Depth-first walk over `properties` and typed `additionalProperties` maps, in the order the
 * schema file lists them (`Object.entries` preserves string-key insertion order — the walk is
 * therefore stable across runs, which is what makes `--check` byte-identical meaningful).
 * @param {any} nodeIn @param {string} pathStr @param {Record<string, any>} defs
 * @param {{path: string, type: string, required: string, default: string, notes: string}[]} rows
 */
function walk(nodeIn, pathStr, defs, rows) {
  const node = resolveRef(nodeIn, defs);
  if (!node || typeof node !== 'object') return;
  if (node.type === 'object') {
    const required = new Set(Array.isArray(node.required) ? node.required : []);
    if (node.properties && typeof node.properties === 'object') {
      for (const [key, child] of Object.entries(node.properties)) {
        const childPath = pathStr ? `${pathStr}.${key}` : key;
        rows.push({
          path: childPath,
          type: typeLabel(child, defs),
          required: required.has(key) ? 'yes' : '',
          default: defaultLabel(child, defs),
          notes: notesLabel(child, defs),
        });
        walk(child, childPath, defs, rows);
      }
    }
    if (node.additionalProperties && typeof node.additionalProperties === 'object') {
      const childPath = pathStr ? `${pathStr}.<key>` : '<key>';
      const ap = node.additionalProperties;
      rows.push({
        path: childPath,
        type: typeLabel(ap, defs),
        required: '',
        default: defaultLabel(ap, defs),
        notes: 'user-defined key' + (notesLabel(ap, defs) ? `; ${notesLabel(ap, defs)}` : ''),
      });
      walk(ap, childPath, defs, rows);
    }
  } else if (node.type === 'array' && node.items) {
    walk(node.items, `${pathStr}[]`, defs, rows);
  }
}

/** @returns {string} the exact bytes `docs/reference/config.md` should hold. */
export function renderConfigDoc() {
  const schema = JSON.parse(readFileSync(SCHEMA_PATH, 'utf8'));
  const defs = schema.$defs ?? {};
  const rows = [];
  walk(schema, '', defs, rows);

  const lines = [];
  lines.push('# `.code-forge.yml` configuration reference');
  lines.push('');
  lines.push(
    `Generated by \`node scripts/gen-config-doc.mjs\` from \`schema/code-forge.schema.json\` — do ` +
      `not hand-edit this file. Run \`node scripts/gen-config-doc.mjs --check\` in CI to catch a ` +
      `schema change that was not followed by a regeneration.`,
  );
  lines.push('');
  if (typeof schema.description === 'string') {
    lines.push(schema.description);
    lines.push('');
  }
  lines.push(requiredKeysSentence(schema, defs));
  lines.push('');
  lines.push(
    'Every other key defaults as shown, or is entirely absent from a minimal file. ' +
      '`additionalProperties: false` applies everywhere a key set is fixed; the rows marked ' +
      '`<key>` are the handful of open-ended maps where the schema accepts a caller-chosen name ' +
      '(a question id, a level name, a key name, a provider id).',
  );
  lines.push('');
  lines.push('| Key | Type | Required | Default | Notes |');
  lines.push('|---|---|---|---|---|');
  for (const row of rows) {
    const notes = row.notes.replaceAll('|', '\\|');
    lines.push(`| \`${row.path}\` | ${row.type} | ${row.required} | ${row.default} | ${notes} |`);
  }
  lines.push('');
  return lines.join('\n');
}

function main() {
  const check = process.argv.includes('--check');
  const generated = renderConfigDoc();

  if (!check) {
    writeFileSync(OUT_PATH, generated);
    process.stdout.write(`wrote ${path.relative(REPO_ROOT, OUT_PATH)}\n`);
    return 0;
  }

  let onDisk;
  try {
    onDisk = readFileSync(OUT_PATH, 'utf8');
  } catch {
    process.stderr.write(`gen-config-doc --check: ${path.relative(REPO_ROOT, OUT_PATH)} does not exist\n`);
    return 1;
  }
  if (onDisk === generated) {
    process.stdout.write('gen-config-doc --check: docs/reference/config.md is up to date\n');
    return 0;
  }
  const onDiskLines = onDisk.split('\n');
  const generatedLines = generated.split('\n');
  let i = 0;
  while (i < onDiskLines.length && i < generatedLines.length && onDiskLines[i] === generatedLines[i]) i += 1;
  process.stderr.write(
    `gen-config-doc --check: docs/reference/config.md is stale (first difference at line ${i + 1})\n` +
      `  on disk:    ${JSON.stringify(onDiskLines[i] ?? '<eof>')}\n` +
      `  generated:  ${JSON.stringify(generatedLines[i] ?? '<eof>')}\n`,
  );
  return 1;
}

/**
 * True when this file is the process entry point. Both sides go through `realpathSync`: Node
 * resolves a symlinked entry to its real path for `import.meta.url` but leaves `argv[1]` as typed,
 * so a plain `path.resolve` comparison is false through any symlink (macOS `/var` -> `/private/var`
 * included) and `--check` would then exit 0 without checking anything.
 * @returns {boolean}
 */
function isEntryPoint() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
  } catch {
    return false;
  }
}

const isMain = isEntryPoint();
if (isMain) {
  process.exitCode = main();
}
