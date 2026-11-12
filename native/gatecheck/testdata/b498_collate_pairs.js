// B498: [a, b, sign(a.localeCompare(b))] pairs over ASCII and the Latin letters
// in collate_latin.go, biased to strings that differ only in marks or case.
const letters = [];
for (const [lo, hi] of [[0xc0, 0x24f], [0x1e00, 0x1eff]]) for (let cp = lo; cp <= hi; cp++) {
  const ch = String.fromCodePoint(cp), nfd = ch.normalize('NFD');
  if (nfd !== ch && /^[A-Za-z][̀-ͯ]+$/.test(nfd)) letters.push(ch);
}
let seed = 498;
const rnd = (n) => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed % n; };
const ascii = Array.from({ length: 95 }, (_, i) => String.fromCharCode(32 + i));
const near = (ch) => { // a char with the same base letter
  const base = ch.normalize('NFD')[0].toLowerCase();
  const pool = letters.filter((l) => l.normalize('NFD')[0].toLowerCase() === base).concat([base, base.toUpperCase()]);
  return pool[rnd(pool.length)];
};
const word = (n) => { let s = ''; for (let i = 0; i < n; i++) s += rnd(3) ? letters[rnd(letters.length)] : (rnd(2) ? ascii[rnd(95)] : 'aeiouAEIOU'[rnd(10)]); return s; };
const out = [];
for (let i = 0; i < 3000; i++) {
  const a = word(1 + rnd(6));
  let b;
  switch (rnd(4)) {
    case 0: b = word(1 + rnd(6)); break;
    case 1: b = [...a].map((c) => (rnd(3) ? c : (/[A-Za-z]/.test(c.normalize('NFD')[0]) ? near(c) : c))).join(''); break;
    case 2: b = a + word(rnd(2)); break;
    default: b = [...a].map((c) => (rnd(2) ? c : c.normalize('NFD')[0])).join('');
  }
  const x = JSON.stringify([a + '.txt', 'F' + rnd(9), 'ab' + rnd(99)]), y = JSON.stringify([b + '.txt', 'F' + rnd(9), 'ab' + rnd(99)]);
  out.push([a, b, Math.sign(a.localeCompare(b))], [x, y, Math.sign(x.localeCompare(y))]);
}
console.log(JSON.stringify(out));
