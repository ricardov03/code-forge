import { readFileSync } from 'node:fs';
const { checkBlockReviews } = await import(process.argv[2]);
const key = Buffer.from('k'.repeat(32));
for (const l of readFileSync(process.argv[3], 'utf8').split('\n').filter(Boolean)) {
  const rows = JSON.parse(l).map((s) => JSON.parse(s));
  const r = checkBlockReviews({ block: 'B1', runId: 'r1', files: [{ file: 'a.mjs', content_hash: 'h2' }], rows, key, proofFiles: [], highPaths: [], unowned: [] });
  console.log(JSON.stringify((r.refusals ?? r).map?.((x) => [x.code, x.detail]) ?? r));
}
