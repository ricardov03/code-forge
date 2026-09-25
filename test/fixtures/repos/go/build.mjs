/** A minimal `go.mod` fixture for `detect.mjs` (plan §2.2 row 5). */

import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  await writeFile(path.join(dir, 'go.mod'), 'module example.com/fixture\n\ngo 1.22\n');
}
