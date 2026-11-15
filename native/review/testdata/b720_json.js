// B720: V8 JSON.parse messages for JSONParseMessage. Run from native/review:
// node testdata/b720_json.js > testdata/b720_json.json
let seed = 720;
const rnd = (n) => { seed = (seed * 1103515245 + 12345) % 2147483648; return Math.floor(seed / 2147483648 * n); };
const bases = ['{"pid":4242,"start":"2026-11-15T05:00:00.000Z","host":"h"}', '{"pid":1}', '[1,2,3]', '"text"', '{"a":{"b":[true,false,null,-1.5e+3]}}', 'null', '{}'];
const pieces = ['', ' ', '\n', '\r', '\r\n', '\t', 'x', '"', '\\', '\\u', '\\u12', '\\x', '\\é', '\\Ā', '{', '}', '[', ']', ',', ':', '-', '0', '01', '.', 'e', 'E+', 'tru', 'nul', 'é', '😀', '\u0001', ' ', "'", 'NaN'];
const out = [];
// A lone surrogate prints as U+FFFD on stderr; sources keep whole characters.
const add = (s) => { if (!s.isWellFormed()) return; let m = null; try { JSON.parse(s); } catch (e) { m = e.message.toWellFormed(); } out.push({ src: s, message: m }); };
for (const s of ['', 'NaN', 'Infinity', 'undefined', '[object Object]', ' ', ' ', 'nul', '-', '1.', '1e', '"\\u0041']) add(s);
for (let i = 0; i < 6000; i++) {
  let s = bases[rnd(bases.length)];
  const edits = 1 + rnd(3);
  for (let k = 0; k < edits; k++) {
    const at = rnd(s.length + 1);
    switch (rnd(4)) {
      case 0: s = s.slice(0, at) + pieces[rnd(pieces.length)] + s.slice(at); break;
      case 1: s = s.slice(0, at) + s.slice(at + 1 + rnd(3)); break;
      case 2: s = s.slice(0, at); break;
      default: s = pieces[rnd(pieces.length)].repeat(rnd(3)) + s + pieces[rnd(pieces.length)];
    }
  }
  add(s);
}
process.stdout.write(JSON.stringify(out));
