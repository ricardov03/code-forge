/**
 * A minimal `pyproject.toml` fixture for `detect.mjs` (plan §2.2 row 4). Carries a `[tool.mypy]`
 * section so `types` is detected as `["mypy"]` ("if configured" — the positive case).
 */

import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  await writeFile(
    path.join(dir, 'pyproject.toml'),
    ['[project]', 'name = "fixture"', 'version = "0.1.0"', '', '[tool.mypy]', 'strict = true', ''].join('\n'),
  );
}
