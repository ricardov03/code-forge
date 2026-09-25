/**
 * Per-provider schema compilation (plan §1.3, §5.2, O36) — the S2 session's answer schema and
 * (in a later block) `s2.mjs`'s compiled-schema-per-provider both need this.
 *
 * OpenAI's structured-output "strict" mode has three rules a hand-written schema violates by
 * default: (1) every property under an object must be listed in `required` (optional fields are
 * instead modeled as **nullable** required fields), (2) every object must set
 * `additionalProperties: false`, (3) nothing may be missing from `required`. Claude and Grok take
 * the source schema unchanged (they support ordinary JSON Schema `required`/optional).
 *
 * **Nullability.** `makeNullable()` does ONE thing, unconditionally, for anything it cannot PROVE
 * already accepts null: move the WHOLE original sub-schema into one `anyOf` branch and add
 * `{type: 'null'}` as the other. That is correct regardless of what was inside — enum, const, an
 * object, a `$ref`, a `not`, an `if/then/else`, a nested `anyOf` — because `null` gets its own
 * branch that doesn't depend on anything the original constrains. `schemaAcceptsNull()` is
 * deliberately conservative (round-2 MINOR): a sibling `type`/`const`/`enum` that excludes null
 * wins over a union branch that allows it (`{type:'object', anyOf:[…,{type:'null'}]}` rejects
 * null), and `$ref`/`not`/`if`/`then`/`else` are never "proven" — wrapping a schema that already
 * accepted null is harmless, skipping one that didn't is the bug.
 *
 * **`additionalProperties: false`.** Every object node that owns a `properties` map gets
 * `additionalProperties: false` unless it already declares an open-ended one, and a bare
 * `{type: 'object'}` with no `properties` and no `additionalProperties` is closed to
 * `{properties: {}, required: [], additionalProperties: false}`. A node that stays open-ended
 * (`additionalProperties: true` or a schema — one of this project's deliberately open maps such as
 * `thresholds`, `known_extra`) has NO representation in OpenAI strict mode; forcing it to `false`
 * would silently drop the map semantics the source intends. Such nodes are left as they are and
 * reported by {@link openAIStrictViolations}; `compileSchema(…, 'openai', {strict: true})` throws
 * naming every one. B9's S2 answer schema has none; only the full project config schema (this
 * package's own test) does.
 */

/**
 * @typedef {Record<string, any>} SchemaNode - see `schema-paths.mjs`'s typedef of the same name —
 *   a schema fragment's shape genuinely varies (`properties`, `$ref`, `anyOf`, `$defs`, …).
 */

/**
 * @param {SchemaNode} schema
 * @param {"anthropic" | "openai" | "xai"} provider
 * @param {{strict?: boolean}} [opts] - `strict: true` (openai only) throws when the compiled
 *   schema still has a node OpenAI strict mode cannot accept, naming each JSON pointer.
 * @returns {SchemaNode} a fresh, independent schema object — never the same reference as `schema`.
 */
export function compileSchema(schema, provider, opts = {}) {
  const clone = structuredClone(schema);
  if (provider !== 'openai') {
    return clone;
  }
  compileForOpenAI(clone, new Set());
  if (opts.strict === true) {
    const violations = openAIStrictViolations(clone);
    if (violations.length > 0) {
      throw new Error(`schema cannot be made OpenAI-strict at: ${violations.join(', ')} (open-ended object: additionalProperties is not false)`);
    }
  }
  return clone;
}

/**
 * @param {SchemaNode | boolean} schema - a boolean schema (`true`/`false`) is legal JSON Schema.
 * @returns {boolean} true only when `schema` provably validates `null`. Conservative: anything it
 *   cannot prove (a `$ref`, `not`, `if`/`then`/`else`) counts as "does not accept null".
 */
function schemaAcceptsNull(schema) {
  if (schema === true) {
    return true;
  }
  if (!schema || typeof schema !== 'object') {
    return false;
  }
  if ('type' in schema) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (!types.includes('null')) {
      return false;
    }
  }
  if ('const' in schema && schema.const !== null) {
    return false;
  }
  if (Array.isArray(schema.enum) && !schema.enum.includes(null)) {
    return false;
  }
  for (const unprovable of ['$ref', 'not', 'if', 'then', 'else']) {
    if (unprovable in schema) {
      return false;
    }
  }
  if (Array.isArray(schema.anyOf) && !schema.anyOf.some(schemaAcceptsNull)) {
    return false;
  }
  if (Array.isArray(schema.oneOf) && schema.oneOf.filter(schemaAcceptsNull).length !== 1) {
    return false;
  }
  if (Array.isArray(schema.allOf) && !schema.allOf.every(schemaAcceptsNull)) {
    return false;
  }
  return true;
}

/**
 * Makes `propSchema` nullable, in place, unconditionally wrapping whatever it already was.
 * @param {SchemaNode} propSchema
 */
function makeNullable(propSchema) {
  if (!propSchema || typeof propSchema !== 'object' || schemaAcceptsNull(propSchema)) {
    return;
  }
  /** @type {SchemaNode} */
  const original = {};
  for (const key of Object.keys(propSchema)) {
    original[key] = propSchema[key];
    delete propSchema[key];
  }
  propSchema.anyOf = [original, { type: 'null' }];
}

/**
 * @param {SchemaNode} node
 * @returns {boolean}
 */
function isObjectTyped(node) {
  return node.type === 'object' || (Array.isArray(node.type) && node.type.includes('object'));
}

/**
 * Every child schema of `node` with its JSON-pointer suffix — the ONE traversal both the compiler
 * and {@link openAIStrictViolations} use, so neither can skip a keyword the other visits.
 * @param {SchemaNode} node
 * @returns {Array<[string, SchemaNode]>}
 */
function childSchemas(node) {
  /** @type {Array<[string, SchemaNode]>} */
  const out = [];
  const push = (/** @type {string} */ suffix, /** @type {unknown} */ child) => {
    if (child && typeof child === 'object') {
      out.push([suffix, /** @type {SchemaNode} */ (child)]);
    }
  };
  for (const [key, child] of Object.entries(node.properties ?? {})) push(`/properties/${key}`, child);
  for (const keyword of ['anyOf', 'oneOf', 'allOf', 'prefixItems']) {
    if (Array.isArray(node[keyword])) node[keyword].forEach((child, i) => push(`/${keyword}/${i}`, child));
  }
  for (const keyword of ['items', 'additionalProperties', 'not', 'if', 'then', 'else']) push(`/${keyword}`, node[keyword]);
  for (const keyword of ['patternProperties', '$defs', 'definitions']) {
    for (const [key, child] of Object.entries(node[keyword] ?? {})) push(`/${keyword}/${key}`, child);
  }
  return out;
}

/**
 * Recursively rewrites every object node: a `properties` map makes every property `required` and
 * nullable and closes the node (unless it already has an open-ended `additionalProperties`); a
 * bare `{type: 'object'}` is closed to an empty strict object. `visited` guards against revisiting
 * the same shared node twice — not against a genuine schema cycle, which this project's schema
 * does not have.
 * @param {SchemaNode} node
 * @param {Set<SchemaNode>} visited
 */
function compileForOpenAI(node, visited) {
  if (!node || typeof node !== 'object' || visited.has(node)) {
    return;
  }
  visited.add(node);

  if (node.properties && typeof node.properties === 'object') {
    const keys = Object.keys(node.properties);
    node.required = keys;
    if (node.additionalProperties === undefined) {
      node.additionalProperties = false;
    }
    for (const key of keys) {
      makeNullable(node.properties[key]);
    }
  } else if (isObjectTyped(node) && node.additionalProperties === undefined && node.patternProperties === undefined) {
    node.properties = {};
    node.required = [];
    node.additionalProperties = false;
  }

  for (const [, child] of childSchemas(node)) {
    compileForOpenAI(child, visited);
  }
}

/**
 * Every object node in an (already compiled) schema that OpenAI strict mode cannot accept: an
 * object-typed or `properties`-owning node whose `additionalProperties` is not exactly `false`.
 * @param {SchemaNode} compiled
 * @returns {string[]} JSON pointers (`#/properties/thresholds`), in traversal order.
 */
export function openAIStrictViolations(compiled) {
  /** @type {string[]} */
  const out = [];
  /** @type {Set<SchemaNode>} */
  const visited = new Set();
  const walk = (/** @type {SchemaNode} */ node, /** @type {string} */ pointer) => {
    if (!node || typeof node !== 'object' || visited.has(node)) {
      return;
    }
    visited.add(node);
    if ((isObjectTyped(node) || (node.properties && typeof node.properties === 'object')) && node.additionalProperties !== false) {
      out.push(pointer);
    }
    for (const [suffix, child] of childSchemas(node)) {
      walk(child, `${pointer}${suffix}`);
    }
  };
  walk(compiled, '#');
  return out;
}
