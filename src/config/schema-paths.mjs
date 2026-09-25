/**
 * A small, shared JSON-Schema navigator used by tests (schema-coverage, the 37-path enumeration)
 * and by `schema-compile.mjs`. Handles exactly what `schema/code-forge.schema.json` actually
 * uses between two dotted-path segments: a `properties` lookup, a typed `additionalProperties`
 * map (`thresholds.<question>`, `known_extra.<provider>`, `keys.<name>`), a `$ref` (`levels.L0`
 * points at `#/$defs/level`), and an `anyOf` branch (`review.second_levels` is `anyOf: [null,
 * {additionalProperties: level}]` — nullable-but-otherwise-a-map). Nothing richer than that (no
 * `oneOf`/`allOf`) appears anywhere in this schema.
 *
 * @typedef {Record<string, any>} SchemaNode - a raw JSON-Schema fragment. Left as an index-typed
 *   object rather than a precise union: a schema node's shape genuinely varies (bare `properties`,
 *   `$ref`, `anyOf`, `additionalProperties`, `$defs`, …) and this module's whole job is walking
 *   between those shapes generically.
 */

/**
 * @param {SchemaNode} node
 * @param {SchemaNode} rootSchema
 * @returns {SchemaNode} `node` itself, or the `$defs` target its `$ref` points at.
 */
export function deref(node, rootSchema) {
  if (node && typeof node === 'object' && typeof node.$ref === 'string') {
    const parts = node.$ref.replace(/^#\//, '').split('/');
    let target = rootSchema;
    for (const part of parts) {
      target = target?.[part];
    }
    return target;
  }
  return node;
}

/**
 * Resolves `segments` starting from `node` (already expected to need dereferencing itself).
 * @param {SchemaNode} node
 * @param {string[]} segments
 * @param {SchemaNode} rootSchema
 * @returns {SchemaNode | undefined}
 */
function resolveFrom(node, segments, rootSchema) {
  const current = deref(node, rootSchema);
  if (!current) {
    return undefined;
  }
  if (segments.length === 0) {
    return current;
  }
  const [segment, ...rest] = segments;

  const props = current.properties;
  if (props && Object.prototype.hasOwnProperty.call(props, segment)) {
    return resolveFrom(props[segment], rest, rootSchema);
  }
  if (typeof current.additionalProperties === 'object' && current.additionalProperties !== null) {
    return resolveFrom(current.additionalProperties, rest, rootSchema);
  }
  if (Array.isArray(current.anyOf)) {
    for (const branch of current.anyOf) {
      const resolved = resolveFrom(branch, segments, rootSchema);
      if (resolved !== undefined) {
        return resolved;
      }
    }
  }
  return undefined;
}

/**
 * Walks a dotted path (`"levels.L0.model"`, `"review.second_levels.L2.provider"`) through the
 * schema, dereferencing `$ref`s and `anyOf` branches as needed at every step.
 *
 * @param {SchemaNode} rootSchema
 * @param {string} dottedPath - the empty string resolves to `rootSchema` itself (zero segments).
 * @returns {SchemaNode | undefined} the leaf sub-schema, or `undefined` if any segment is missing.
 */
export function resolveSchemaPath(rootSchema, dottedPath) {
  // `''.split('.')` is `['']` (one EMPTY-STRING segment), not zero segments — without this guard
  // the empty path would look up `properties['']` and fail, instead of meaning "the root itself".
  const segments = dottedPath === '' ? [] : dottedPath.split('.');
  return resolveFrom(rootSchema, segments, rootSchema);
}
