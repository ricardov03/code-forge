/**
 * A stack with NO marker evidence at all (plan §2.2: "unknown stack ⇒ every gate `null`"). Writes
 * one unrelated file so the directory isn't literally empty — detection must still see no
 * evidence, not merely "no files".
 */

import { writeFile } from 'node:fs/promises';
import path from 'node:path';

/** @param {string} dir - an existing empty directory */
export async function build(dir) {
  await writeFile(path.join(dir, 'README.md'), '# fixture with no recognized stack\n');
}
