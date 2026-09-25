import assert from 'node:assert/strict';
import { test } from 'node:test';
import schema from '../../schema/code-forge.schema.json' with { type: 'json' };
import { resolveSchemaPath } from '../../src/config/schema-paths.mjs';

// ── Acceptance: every key of §1.3 present in the schema (37 paths enumerated) ─

/**
 * One representative dotted path per §1.3 table row (a compound row that bundles two genuinely
 * distinct top-level namespaces — "commit.trailers, wip.ready_prs" — contributes one path per
 * namespace; a handful of rows with several important children contribute a second path). 37
 * total, matching the acceptance clause's count.
 */
const CONFIG_CONTRACT_PATHS = Object.freeze([
  'version',
  'project.slug',
  'project.languages.chat',
  'provider',
  'levels.L0.model',
  'levels.L3.model',
  'orchestrator',
  'system1.provider',
  'thresholds.default.act',
  'thresholds.findings.fix',
  'escalation.stop_at',
  'review.multimodel',
  'review.budgets.quick_in',
  'review.context.whole_file_max_lines',
  'review.block_budget_tokens',
  'review.session_timeout_s',
  'engine',
  'harnesses',
  'harness.claude.worktree',
  'caps.coders',
  'gates.test',
  'gates.extra.secret_scan',
  'gates.full_suite_threshold_files',
  'proof.tiers.high.min_msi',
  'proof.tiers.light',
  'proof.js.tool',
  'proof.isolation',
  'proof.export.copy_untracked',
  'budget.block_lines',
  'autonomy.coder_may_open_draft_pr',
  'commit.trailers',
  'wip.ready_prs',
  'production.markers',
  'known_extra',
  'calibration.shadow_rate',
  'keys.jev',
  'telemetry',
]);

test('the config contract enumerates exactly 37 paths', () => {
  assert.equal(CONFIG_CONTRACT_PATHS.length, 37);
});

test('every one of the 37 config-contract paths resolves to a real schema node', () => {
  const missing = CONFIG_CONTRACT_PATHS.filter((p) => resolveSchemaPath(schema, p) === undefined);
  assert.deepEqual(missing, [], `missing from schema: ${missing.join(', ')}`);
});

test('a bogus key under a FIXED-shape parent (caps.nonexistent) resolves to undefined — proves resolveSchemaPath does not silently accept anything under a closed object (MINOR §163/164)', () => {
  assert.equal(resolveSchemaPath(schema, 'caps.nonexistent'), undefined);
  assert.equal(resolveSchemaPath(schema, 'gates.nonexistent'), undefined);
});

test('a key under an OPEN-ENDED map (thresholds.<any-question-id>) DOES resolve for any name — documented, intentional map behavior, not a gap', () => {
  assert.notEqual(resolveSchemaPath(schema, 'thresholds.some_made_up_question_id.act'), undefined);
  assert.notEqual(resolveSchemaPath(schema, 'known_extra.some_made_up_provider'), undefined);
});

test('mutation: deleting one schema property turns a specific path missing (proves the check can fail)', () => {
  const mutated = structuredClone(schema);
  delete mutated.properties.caps.properties.coders;
  assert.equal(resolveSchemaPath(mutated, 'caps.coders'), undefined);
  // Everything else must still resolve — proves the mutation was surgical, not accidentally global.
  const stillMissing = CONFIG_CONTRACT_PATHS.filter((p) => p !== 'caps.coders' && resolveSchemaPath(mutated, p) === undefined);
  assert.deepEqual(stillMissing, []);
});

// ── additionalProperties: false everywhere a key set is fixed ───────────────

/**
 * The ONE object node that is allowed BOTH fixed `properties` AND an open-ended (typed)
 * `additionalProperties`: `thresholds` has `default`/`findings` plus arbitrary `<question>` keys.
 */
const OPEN_WITH_FIXED_KEYS = new Set(['#/properties/thresholds']);

/**
 * A "typed" value schema: a non-empty schema object that declares `type` or `$ref`. `{}` (accepts
 * anything, no more typed than `true`), `true`, `null` and a missing value are all NOT typed.
 * @param {unknown} value
 * @returns {boolean}
 */
function isTypedSchema(value) {
  return (
    value !== null &&
    typeof value === 'object' &&
    !Array.isArray(value) &&
    Object.keys(value).length > 0 &&
    ('type' in value || '$ref' in value)
  );
}

/**
 * Walks EVERY reachable schema node — `properties`, `$defs`/`definitions`, `items`/`prefixItems`,
 * `oneOf`/`anyOf`/`allOf` branches, `patternProperties`, and a schema-VALUED
 * `additionalProperties` — and checks EVERY object node, with or without `properties` (round-2
 * MAJOR: a bare `{type:'object'}` used to be skipped entirely). An object node is one whose `type`
 * is `"object"` (or an array containing it) OR that owns a `properties` map. Each must either set
 * `additionalProperties: false`, or be a typed map (no `properties`, a typed value schema), or be
 * the one documented open node with fixed keys (`OPEN_WITH_FIXED_KEYS`, typed value schema).
 * @param {Record<string, any>} node
 * @param {string} pointer
 * @param {string[]} violations
 * @param {Set<object>} [onPath]
 * @returns {number} how many object nodes were checked (so a caller can prove the walk reached them).
 */
function walkAdditionalProperties(node, pointer, violations, onPath = new Set()) {
  if (!node || typeof node !== 'object' || onPath.has(node)) return 0;
  onPath.add(node);
  let checked = 0;
  const recurse = (/** @type {any} */ child, /** @type {string} */ childPointer) => {
    checked += walkAdditionalProperties(child, childPointer, violations, onPath);
  };
  try {
    const isObjectType = node.type === 'object' || (Array.isArray(node.type) && node.type.includes('object'));
    const hasProperties = node.properties !== null && typeof node.properties === 'object';
    if (isObjectType || hasProperties) {
      checked += 1;
      const closed = node.additionalProperties === false;
      const typedMap = !hasProperties && isTypedSchema(node.additionalProperties);
      const documentedOpen = OPEN_WITH_FIXED_KEYS.has(pointer) && isTypedSchema(node.additionalProperties);
      if (!closed && !typedMap && !documentedOpen) {
        violations.push(pointer);
      }
    }
    if (hasProperties) {
      for (const [key, child] of Object.entries(node.properties)) {
        recurse(child, `${pointer}/properties/${key}`);
      }
    }
    for (const unionKeyword of ['oneOf', 'anyOf', 'allOf', 'prefixItems']) {
      if (Array.isArray(node[unionKeyword])) {
        node[unionKeyword].forEach((branch, i) => recurse(branch, `${pointer}/${unionKeyword}/${i}`));
      }
    }
    for (const keyword of ['items', 'additionalProperties', 'not', 'if', 'then', 'else']) {
      if (node[keyword] && typeof node[keyword] === 'object') {
        recurse(node[keyword], `${pointer}/${keyword}`);
      }
    }
    for (const keyword of ['patternProperties', '$defs', 'definitions']) {
      if (node[keyword] && typeof node[keyword] === 'object') {
        for (const [key, child] of Object.entries(node[keyword])) {
          recurse(child, `${pointer}/${keyword}/${key}`);
        }
      }
    }
  } finally {
    onPath.delete(node);
  }
  return checked;
}

/**
 * Independent oracle for how many object nodes the real schema has: counted from the raw JSON
 * text (`"type": "object"`), not by any walker. Every object node in this schema declares its
 * type, so the walker must check exactly this many.
 */
const OBJECT_NODE_COUNT_FROM_TEXT = (JSON.stringify(schema).match(/"type":"object"/g) ?? []).length;

test('every object node in the schema is closed, a typed map, or the one documented open node — and the walk checks ALL of them (count from the raw JSON text)', () => {
  const violations = [];
  const checked = walkAdditionalProperties(schema, '#', violations);
  assert.deepEqual(violations, []);
  assert.ok(OBJECT_NODE_COUNT_FROM_TEXT > 30, `sanity: expected a substantial object count, got ${OBJECT_NODE_COUNT_FROM_TEXT}`);
  assert.equal(checked, OBJECT_NODE_COUNT_FROM_TEXT, 'the walker must reach every object node the schema text declares');
});

test('mutation: an object schema missing additionalProperties: false is caught by the sweep above', () => {
  const mutated = structuredClone(schema);
  delete mutated.properties.caps.additionalProperties;
  const violations = [];
  walkAdditionalProperties(mutated, '#', violations);
  assert.deepEqual(violations, ['#/properties/caps']);
});

test('mutation: a NESTED defect reachable only through $defs → properties → items is caught at exactly its own pointer', () => {
  const mutated = structuredClone(schema);
  delete mutated.$defs.level.properties.fallback.items.additionalProperties;
  const violations = [];
  walkAdditionalProperties(mutated, '#', violations);
  assert.deepEqual(violations, ['#/$defs/level/properties/fallback/items']);
});

test('mutation: a MAP VALUE schema that is a bare {type:"object"} (no properties, no additionalProperties) is caught at exactly that pointer', () => {
  const mutated = /** @type {any} */ (structuredClone(schema));
  mutated.properties.system1.properties.criteria_extra.additionalProperties = { type: 'object' };
  const violations = [];
  walkAdditionalProperties(mutated, '#', violations);
  assert.deepEqual(violations, ['#/properties/system1/properties/criteria_extra/additionalProperties']);
});

test('mutation: a bare {type:"object"} nested in an anyOf branch (review.second_levels) is caught at exactly that pointer', () => {
  const mutated = structuredClone(schema);
  mutated.properties.review.properties.second_levels.anyOf[1] = { type: 'object' };
  const violations = [];
  walkAdditionalProperties(mutated, '#', violations);
  assert.deepEqual(violations, ['#/properties/review/properties/second_levels/anyOf/1']);
});

// ── Each open-ended map genuinely carries a TYPED additionalProperties schema ──

const OPEN_MAPS = /** @type {const} */ ([
  ['thresholds', ['thresholds']],
  ['system1.criteria_extra', ['system1', 'criteria_extra']],
  ['known_extra', ['known_extra']],
  ['keys', ['keys']],
]);

/**
 * @param {Record<string, any>} root
 * @param {readonly string[]} segments
 */
function mapNode(root, segments) {
  return segments.reduce((node, segment) => node.properties[segment], root);
}

test('every one of the 4 documented open-ended maps has a TYPED additionalProperties (non-empty, declares type or $ref)', () => {
  for (const [name, segments] of OPEN_MAPS) {
    const node = mapNode(schema, segments);
    assert.equal(isTypedSchema(node.additionalProperties), true, `${name}.additionalProperties must be a typed schema, got: ${JSON.stringify(node.additionalProperties)}`);
  }
});

test('mutation: setting a map\'s additionalProperties to {} fails BOTH the typed-map check and the sweep, at exactly that map', () => {
  for (const [name, segments] of OPEN_MAPS) {
    const mutated = structuredClone(schema);
    mapNode(mutated, segments).additionalProperties = {};
    assert.equal(isTypedSchema(mapNode(mutated, segments).additionalProperties), false, name);
    const violations = [];
    walkAdditionalProperties(mutated, '#', violations);
    assert.deepEqual(violations, [`#/properties/${segments.join('/properties/')}`], name);
  }
});

// ── The new traversal branches (items / additionalProperties-as-schema), on a SYNTHETIC fixture ─
// (the real project schema routes every object through $defs, which the OLD walker already
// reached — these branches need a purpose-built fixture to prove they are actually walked.)

test('mutation: an object nested under array ITEMS, missing additionalProperties: false, is caught at exactly its own pointer', () => {
  const fixture = {
    type: 'object',
    additionalProperties: false,
    properties: {
      list: {
        type: 'array',
        items: { type: 'object', properties: { x: { type: 'string' } } }, // missing additionalProperties: false
      },
    },
  };
  const violations = [];
  walkAdditionalProperties(fixture, '#', violations);
  assert.deepEqual(violations, ['#/properties/list/items']);
});

test('mutation: an object nested under a MAP\'s (additionalProperties) value schema, missing additionalProperties: false, is caught at exactly its own pointer', () => {
  const fixture = {
    type: 'object',
    additionalProperties: false,
    properties: {
      byId: {
        type: 'object',
        additionalProperties: { type: 'object', properties: { x: { type: 'string' } } }, // the VALUE schema is missing its own additionalProperties: false
      },
    },
  };
  const violations = [];
  walkAdditionalProperties(fixture, '#', violations);
  assert.deepEqual(violations, ['#/properties/byId/additionalProperties']);
});

test('control: the same two fixtures with additionalProperties: false correctly added produce 0 violations (proves the mutations above are real, not permanent false positives)', () => {
  const cleanArrayFixture = {
    type: 'object',
    additionalProperties: false,
    properties: {
      list: { type: 'array', items: { type: 'object', additionalProperties: false, properties: { x: { type: 'string' } } } },
    },
  };
  const cleanMapFixture = {
    type: 'object',
    additionalProperties: false,
    properties: {
      byId: { type: 'object', additionalProperties: { type: 'object', additionalProperties: false, properties: { x: { type: 'string' } } } },
    },
  };
  const violations = [];
  walkAdditionalProperties(cleanArrayFixture, '#', violations);
  walkAdditionalProperties(cleanMapFixture, '#', violations);
  assert.deepEqual(violations, []);
});
