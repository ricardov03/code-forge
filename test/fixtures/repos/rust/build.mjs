/** A minimal Cargo fixture for `detect.mjs` (plan §2.2 row 3: `Cargo.toml`). */

import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  await writeFile(path.join(dir, 'Cargo.toml'), '[package]\nname = "fixture"\nversion = "0.1.0"\nedition = "2021"\n');
}
