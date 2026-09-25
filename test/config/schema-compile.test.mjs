import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Ajv2020 } from 'ajv/dist/2020.js';
import schema from '../../schema/code-forge.schema.json' with { type: 'json' };
import { compileSchema, openAIStrictViolations } from '../../src/config/schema-compile.mjs';

/**
 * Every child schema of `node` with its pointer suffix — `properties`, `anyOf`/`oneOf`/`allOf`,
 * `prefixItems`, `items`, a schema-valued `additionalProperties`, `not`/`if`/`then`/`else`,
 * `patternProperties`, `$defs`/`definitions`. Test-side traversal, written independently of
 * production's (the test must not borrow the code it checks).
 * @param {Record<string, any>} node
 * @returns {Array<[string, Record<string, any>]>}
 */
function children(node) {
  /** @type {Array<[string, Record<string, any>]>} */
  const out = [];
  const add = (/** @type {string} */ suffix, /** @type {unknown} */ child) => {
    if (child && typeof child === 'object') out.push([suffix, /** @type {Record<string, any>} */ (child)]);
  };
  for (const [key, child] of Object.entries(node.properties ?? {})) add(`/properties/${key}`, child);
  for (const keyword of ['anyOf', 'oneOf', 'allOf', 'prefixItems']) {
    (node[keyword] ?? []).forEach((/** @type {unknown} */ child, /** @type {number} */ i) => add(`/${keyword}/${i}`, child));
  }
  for (const keyword of ['items', 'additionalProperties', 'not', 'if', 'then', 'else']) add(`/${keyword}`, node[keyword]);
  for (const keyword of ['patternProperties', '$defs', 'definitions']) {
    for (const [key, child] of Object.entries(node[keyword] ?? {})) add(`/${keyword}/${key}`, child);
  }
  return out;
}

/**
 * Recursively counts every `properties` entry reachable from `node`, and how many of those are
 * BOTH listed in their parent's `required` AND structurally nullable. `onPath` tracks only the
 * CURRENT recursion path, so a sub-schema legitimately appearing twice in unrelated branches is
 * counted twice (a global set would hide a defect at its second occurrence).
 * @param {Record<string, any>} node
 * @param {Set<object>} onPath
 * @returns {{total: number, requiredAndNullable: number}}
 */
function countCoverage(node, onPath = new Set()) {
  let total = 0;
  let requiredAndNullable = 0;
  if (!node || typeof node !== 'object' || onPath.has(node)) {
    return { total, requiredAndNullable };
  }
  onPath.add(node);
  try {
    const required = new Set(node.required ?? []);
    for (const [key, propSchema] of Object.entries(node.properties ?? {})) {
      total += 1;
      if (required.has(key) && acceptsNull(propSchema)) {
        requiredAndNullable += 1;
      }
    }
    for (const [, child] of children(node)) {
      const nested = countCoverage(child, onPath);
      total += nested.total;
      requiredAndNullable += nested.requiredAndNullable;
    }
    return { total, requiredAndNullable };
  } finally {
    onPath.delete(node);
  }
}

/**
 * Structural null check: a `type` that excludes null wins; otherwise a union branch or an enum
 * may admit it.
 * @param {Record<string, any>} propSchema
 * @returns {boolean}
 */
function acceptsNull(propSchema) {
  if (!propSchema || typeof propSchema !== 'object') return false;
  if ('type' in propSchema) {
    const types = Array.isArray(propSchema.type) ? propSchema.type : [propSchema.type];
    if (!types.includes('null')) return false;
    return true;
  }
  if (Array.isArray(propSchema.anyOf)) return propSchema.anyOf.some(acceptsNull);
  if (Array.isArray(propSchema.oneOf)) return propSchema.oneOf.some(acceptsNull);
  if (Array.isArray(propSchema.enum)) return propSchema.enum.includes(null);
  return false;
}

/**
 * A required property is wrapped `{anyOf: [original, {type: "null"}]}` — unwrap to inspect the
 * ORIGINAL sub-schema's own shape.
 * @param {Record<string, any>} node
 * @returns {Record<string, any>}
 */
function unwrapNullable(node) {
  if (Array.isArray(node?.anyOf)) {
    const nonNullBranch = node.anyOf.find((b) => b.type !== 'null');
    if (nonNullBranch) return nonNullBranch;
  }
  return node;
}

test('compileSchema returns a fresh object — never the same reference as the input, for every provider', () => {
  for (const provider of /** @type {const} */ (['anthropic', 'openai', 'xai'])) {
    const compiled = compileSchema(schema, provider);
    assert.notEqual(compiled, schema);
  }
});

test('compileSchema does not mutate the input schema', () => {
  const before = JSON.stringify(schema);
  compileSchema(schema, 'openai');
  assert.equal(JSON.stringify(schema), before);
});

test('compileSchema for claude/grok (non-openai) returns the source schema unchanged (deep-equal)', () => {
  for (const provider of /** @type {const} */ (['anthropic', 'xai'])) {
    const compiled = compileSchema(schema, provider);
    assert.deepEqual(compiled, schema);
  }
});

// ── The "100%" clause tied to the SOURCE schema's own property count ──────────

test('compileSchema for openai keeps the EXACT SAME property count as the source schema, all required+nullable', () => {
  const sourceCoverage = countCoverage(schema);
  const compiled = compileSchema(schema, 'openai');
  const compiledCoverage = countCoverage(compiled);

  assert.ok(sourceCoverage.total > 30, `expected a substantial source property count, got ${sourceCoverage.total}`);
  assert.equal(compiledCoverage.total, sourceCoverage.total, 'compiling must never drop or add a property');
  assert.equal(
    compiledCoverage.requiredAndNullable,
    compiledCoverage.total,
    `${compiledCoverage.requiredAndNullable}/${compiledCoverage.total} properties are required+nullable`,
  );
});

test('mutation: a compiled openai schema missing nullability on one property is caught (proves the count can fail)', () => {
  const compiled = compileSchema(schema, 'openai');
  const capsProps = unwrapNullable(compiled.properties.caps).properties;
  assert.ok(Array.isArray(capsProps.coders.anyOf) && capsProps.coders.anyOf.some((b) => b.type === 'null'));
  capsProps.coders.anyOf = capsProps.coders.anyOf.filter((b) => b.type !== 'null');

  const { total, requiredAndNullable } = countCoverage(compiled);
  assert.equal(requiredAndNullable, total - 1, 'the mutated property must be the only one now failing the count');
});

test("openai's required list for a compiled object contains every one of its own property keys", () => {
  const compiled = compileSchema(schema, 'openai');
  const capsSchema = unwrapNullable(compiled.properties.caps);
  assert.deepEqual([...capsSchema.required].sort(), Object.keys(capsSchema.properties).sort());
});

test('a $ref property (e.g. levels.L0) is wrapped in anyOf[ref, null] rather than dropping the ref', () => {
  const compiled = compileSchema(schema, 'openai');
  const levels = unwrapNullable(compiled.properties.levels);
  const l0 = levels.properties.L0;
  assert.equal(l0.$ref, undefined, 'the bare $ref must be replaced, not left dangling alongside anyOf');
  assert.ok(Array.isArray(l0.anyOf) && l0.anyOf.length === 2);
  assert.ok(l0.anyOf.some((b) => b.$ref === '#/$defs/level'));
  assert.ok(l0.anyOf.some((b) => b.type === 'null'));
});

// ── additionalProperties: false on EVERY compiled object node (counted) ───────

/**
 * Walks every node (independent traversal above) and returns the pointer of every object node —
 * `type: "object"` or owning `properties` — together with whether it is closed.
 * @param {Record<string, any>} root
 * @returns {{objectNodes: string[], open: string[]}}
 */
function objectNodeCensus(root) {
  /** @type {string[]} */
  const objectNodes = [];
  /** @type {string[]} */
  const open = [];
  const walk = (/** @type {Record<string, any>} */ node, /** @type {string} */ pointer, /** @type {Set<object>} */ onPath) => {
    if (onPath.has(node)) return;
    onPath.add(node);
    const isObject = node.type === 'object' || (Array.isArray(node.type) && node.type.includes('object')) || (node.properties && typeof node.properties === 'object');
    if (isObject) {
      objectNodes.push(pointer);
      if (node.additionalProperties !== false) open.push(pointer);
    }
    for (const [suffix, child] of children(node)) walk(child, `${pointer}${suffix}`, onPath);
    onPath.delete(node);
  };
  walk(root, '#', new Set());
  return { objectNodes, open };
}

/**
 * An S2-style answer schema (the real target of the openai path, B9): nested objects, an array of
 * objects, an optional enum, a bare `{type:'object'}` metadata bag, and a `$defs` entry.
 */
const S2_STYLE_SCHEMA = Object.freeze({
  type: 'object',
  properties: {
    verdict: { type: 'string', enum: ['pass', 'fail'] },
    confidence: { type: 'number', minimum: 0, maximum: 1 },
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'integer' },
          severity: { type: 'string', enum: ['fix', 'nit'] },
          location: { $ref: '#/$defs/location' },
        },
        required: ['file'],
      },
    },
    meta: { type: 'object' },
    summary: { type: 'object', properties: { text: { type: 'string' } } },
  },
  required: ['verdict'],
  $defs: {
    location: { type: 'object', properties: { start: { type: 'integer' }, end: { type: 'integer' } } },
  },
});

test('S2-style schema: EVERY one of its 5 object nodes compiles to additionalProperties: false (counted), including the bare {type:"object"} meta bag', () => {
  const compiled = compileSchema(S2_STYLE_SCHEMA, 'openai');
  const { objectNodes, open } = objectNodeCensus(compiled);
  // root, findings.items, meta, summary, $defs.location — 5 in the SOURCE; the compiled tree
  // reaches each through its nullable wrapper, so pointers change but the count doesn't.
  assert.equal(objectNodes.length, objectNodeCensus(S2_STYLE_SCHEMA).objectNodes.length);
  assert.equal(objectNodes.length, 5);
  assert.deepEqual(open, []);
  assert.deepEqual(openAIStrictViolations(compiled), []);
  assert.doesNotThrow(() => compileSchema(S2_STYLE_SCHEMA, 'openai', { strict: true }));
});

test('a bare {type:"object"} property compiles to an empty strict object: properties {}, required [], additionalProperties false', () => {
  const compiled = compileSchema(S2_STYLE_SCHEMA, 'openai');
  const meta = unwrapNullable(compiled.properties.meta);
  assert.deepEqual(meta, { type: 'object', properties: {}, required: [], additionalProperties: false });
});

test('the full config schema: EXACTLY the 6 open-ended map nodes stay open (reported), every other object node is closed', () => {
  const compiled = compileSchema(schema, 'openai');
  const expectedOpen = [
    '#/properties/system1/anyOf/0/properties/criteria_extra/anyOf/0',
    '#/properties/system1/anyOf/0/properties/criteria_extra/anyOf/0/additionalProperties/properties/criteria/anyOf/0',
    '#/properties/thresholds/anyOf/0',
    '#/properties/review/anyOf/0/properties/second_levels/anyOf/1',
    '#/properties/known_extra/anyOf/0',
    '#/properties/keys/anyOf/0',
  ];
  // Independent census (test traversal) and the production reporter must agree on the same list.
  assert.deepEqual(objectNodeCensus(compiled).open.slice().sort(), expectedOpen.slice().sort());
  assert.deepEqual(openAIStrictViolations(compiled).slice().sort(), expectedOpen.slice().sort());
});

test('compileSchema(config schema, "openai", {strict: true}) throws, naming every open pointer', () => {
  assert.throws(
    () => compileSchema(schema, 'openai', { strict: true }),
    (err) => err instanceof Error && err.message.includes('#/properties/thresholds/anyOf/0') && err.message.includes('#/properties/keys/anyOf/0'),
  );
});

test('a node with a typed (open-ended) additionalProperties keeps it — never silently closed to false', () => {
  const compiled = compileSchema(schema, 'openai');
  const thresholds = unwrapNullable(compiled.properties.thresholds);
  assert.notEqual(thresholds.additionalProperties, false);
  assert.equal(typeof thresholds.additionalProperties, 'object');
});

// ── Nullability is REAL: production compileSchema, checked by ajv ─────────────

/**
 * Wraps `fixture` as the single required property `x` of an object, compiles it through the REAL
 * `compileSchema(…, 'openai')`, and asks ajv whether the compiled `x` accepts null AND whether the
 * whole compiled document accepts `{x: null}`.
 * @param {Record<string, any>} fixture
 * @returns {{propertyAcceptsNull: boolean, documentAcceptsNull: boolean}}
 */
function compiledNullability(fixture) {
  const compiled = compileSchema({ type: 'object', properties: { x: fixture }, required: ['x'] }, 'openai');
  const ajv = new Ajv2020({ strict: false });
  return {
    propertyAcceptsNull: ajv.compile(compiled.properties.x)(null),
    documentAcceptsNull: ajv.compile(compiled)({ x: null }),
  };
}

const NULLABILITY_FIXTURES = /** @type {Array<[string, Record<string, any>]>} */ ([
  ['{type:"string", enum:[...]}', { type: 'string', enum: ['patch', 'code'] }],
  ['{type:"string", const:"L3"}', { type: 'string', const: 'L3' }],
  ['{type:"boolean", anyOf:[{const:true}]}', { type: 'boolean', anyOf: [{ const: true }] }],
  ['{type:"object", anyOf:[{…}, {type:"null"}]} (a null BRANCH that the sibling type still excludes)', { type: 'object', anyOf: [{ properties: { a: { type: 'string' } } }, { type: 'null' }] }],
  ['{not:{type:"null"}}', { not: { type: 'null' } }],
  ['{not:{const:null}}', { not: { const: null } }],
  ['{if/then} that rejects null', { if: { type: 'null' }, then: false }],
]);

for (const [label, fixture] of NULLABILITY_FIXTURES) {
  test(`ajv red test: ${label} — the source rejects null; production compileSchema makes it accept null`, () => {
    const ajv = new Ajv2020({ strict: false });
    assert.equal(ajv.compile(fixture)(null), false, 'precondition: the SOURCE sub-schema rejects null');
    assert.deepEqual(compiledNullability(fixture), { propertyAcceptsNull: true, documentAcceptsNull: true });
  });
}

// ── End-to-end: EVERY required property, at EVERY depth, validates null through ajv ─────────

/**
 * Checks, through ajv, that every REQUIRED property reachable anywhere in `root` (the same full
 * traversal as `countCoverage` — union branches, items, map values, `$defs`, …) accepts null.
 * Each property is compiled standalone with the root's `$defs` embedded so a `$ref` resolves.
 * @param {Record<string, any>} root
 * @returns {{checked: number, failures: string[]}}
 */
function checkEveryPropertyNullable(root) {
  let checked = 0;
  /** @type {string[]} */
  const failures = [];
  const walk = (/** @type {Record<string, any>} */ node, /** @type {string} */ pointer, /** @type {Set<object>} */ onPath) => {
    if (onPath.has(node)) return;
    onPath.add(node);
    const required = new Set(node.required ?? []);
    for (const [key, propSchema] of Object.entries(node.properties ?? {})) {
      checked += 1;
      const ajv = new Ajv2020({ strict: false });
      const standalone = { ...structuredClone(propSchema), $defs: root.$defs };
      if (!required.has(key) || !ajv.compile(standalone)(null)) {
        failures.push(`${pointer}/properties/${key}`);
      }
    }
    for (const [suffix, child] of children(node)) walk(child, `${pointer}${suffix}`, onPath);
    onPath.delete(node);
  };
  walk(root, '#', new Set());
  return { checked, failures };
}

test('every OpenAI-compiled property at EVERY depth validates null through ajv — and the walk checks exactly countCoverage(compiled).total properties', () => {
  const compiled = compileSchema(schema, 'openai');
  const { checked, failures } = checkEveryPropertyNullable(compiled);
  assert.deepEqual(failures, [], `properties that do NOT validate null: ${failures.join(', ')}`);
  assert.equal(checked, countCoverage(compiled).total, 'the ajv walk must visit every property the coverage count sees');
  assert.equal(checked, countCoverage(schema).total, 'and that is every property of the SOURCE schema');
});

test('mutation: a NESTED defect (the null branch dropped from $defs.level.fallback[].effort, 4 levels deep) is caught at exactly that pointer', () => {
  const compiled = compileSchema(schema, 'openai');
  const fallbackItems = unwrapNullable(compiled.$defs.level.properties.fallback).items;
  const effort = fallbackItems.properties.effort;
  assert.ok(Array.isArray(effort.anyOf) && effort.anyOf.some((b) => b.type === 'null'), 'precondition: effort was wrapped');
  effort.anyOf = effort.anyOf.filter((b) => b.type !== 'null');

  const { failures } = checkEveryPropertyNullable(compiled);
  assert.deepEqual(failures, ['#/$defs/level/properties/fallback/anyOf/0/items/properties/effort']);
});

test('mutation: a nested property dropped from `required` (inside review.budgets) is caught at exactly that pointer', () => {
  const compiled = compileSchema(schema, 'openai');
  const budgets = unwrapNullable(unwrapNullable(compiled.properties.review).properties.budgets);
  budgets.required = budgets.required.filter((/** @type {string} */ k) => k !== 'out');

  const { failures } = checkEveryPropertyNullable(compiled);
  assert.deepEqual(failures, ['#/properties/review/anyOf/0/properties/budgets/anyOf/0/properties/out']);
});
