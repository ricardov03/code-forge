// B720: [a, b, a.localeCompare(b)] rows for gatecheck.LocaleCompare, made
// with Node's default locale under an empty environment (en-US). Run from
// native/gatecheck: env -i PATH="$PATH" node testdata/b720_collate.js > testdata/b720_collate.json
let seed = 7201;
const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
const chars = [];
for (let c = 0; c < 128; c++) chars.push(String.fromCharCode(c));
const extra = ['é', 'É', 'é', 'ü', 'ñ', 'Å'];
const pool = [...chars, ...chars.slice(32, 127), ...'0123456789-:.TZ'.split(''), ...extra];
const strs = [];
for (let i = 0; i < 6000; i++) {
  const n = Math.floor(rnd() * 7);
  let s = '';
  for (let j = 0; j < n; j++) s += pool[Math.floor(rnd() * pool.length)];
  strs.push(s);
}
for (let i = 0; i < 1500; i++) {
  const d = new Date(Math.floor(rnd() * 4e12)).toISOString();
  const k = Math.floor(rnd() * d.length);
  strs.push(d, d.slice(0, k) + pool[Math.floor(rnd() * pool.length)] + d.slice(k + 1), d.replace('Z', ''), d.toLowerCase());
}
const rows = [];
for (let i = 0; i < strs.length; i++) {
  for (const j of [i + 1, (i * 31 + 7) % strs.length]) {
    if (j >= strs.length) continue;
    rows.push([strs[i], strs[j], strs[i].localeCompare(strs[j])]);
  }
}
for (const c of chars) for (const d of chars) rows.push([c, d, c.localeCompare(d)], ['a' + c + 'B', 'a' + d + 'b', ('a' + c + 'B').localeCompare('a' + d + 'b')]);
process.stdout.write(JSON.stringify(rows));
