// Prints Node's checkBlockReviews refusals as code|file|finding|detail for
// each dumped line (B498_LATECAP_DUMP); run: node b498_latecap.mjs <gate-check.mjs> <dump>
import { readFileSync } from 'node:fs';
const { checkBlockReviews } = await import(process.argv[2]);
const key = Buffer.from('k'.repeat(32));
const repr = (v) => JSON.stringify(v) ?? 'undefined';
for (const l of readFileSync(process.argv[3], 'utf8').split('\n').filter(Boolean)) {
  const rows = JSON.parse(l).map((s) => JSON.parse(s));
  const r = checkBlockReviews({ block: 'B1', runId: 'r1', files: [{ file: 'a.mjs', content_hash: 'h2' }], rows, key, proofFiles: [], highPaths: [], unowned: [] });
  console.log(JSON.stringify(r.refusals.map((x) => `${x.code}|${repr(x.file)}|${repr(x.finding)}|${x.detail}`)));
}
