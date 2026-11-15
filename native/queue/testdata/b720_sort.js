// B720: records V8 Array.prototype.sort comparator calls for the Go v8Sort
// port. Run: node testdata/b720_sort.js > testdata/b720_sort.json
let seed = 720;
const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
const cases = [];
const shapes = ['random', 'ties', 'sorted', 'reversed', 'runs', 'saw', 'gallop'];
for (let i = 0; i < 280; i++) {
  const shape = shapes[i % shapes.length];
  const n = i < 70 ? i % 70 : Math.floor(rnd() * 600) + 2;
  let v = [];
  for (let k = 0; k < n; k++) {
    switch (shape) {
      case 'random': v.push(Math.floor(rnd() * 100000)); break;
      case 'ties': v.push(Math.floor(rnd() * 4)); break;
      case 'sorted': v.push(k + (rnd() < 0.05 ? -3 : 0)); break;
      case 'reversed': v.push(n - k + (rnd() < 0.05 ? 5 : 0)); break;
      case 'runs': v.push((k % 37) * (Math.floor(k / 37) % 2 ? -1 : 1)); break;
      case 'saw': v.push(k % 13); break;
      case 'gallop': v.push(k < n / 2 ? k * 2 : (k - Math.floor(n / 2)) * 2 + (rnd() < 0.5 ? 1 : 0) + (k % 50 === 0 ? 1000 : 0)); break;
    }
  }
  const items = v.map((x, id) => ({ x, id }));
  let h = 0, calls = 0;
  items.sort((a, b) => { calls++; h = (h * 31 + a.id * 1009 + b.id) % 1000000007; return a.x - b.x; });
  cases.push({ values: v, calls, hash: h, order: items.map((o) => o.id) });
}
process.stdout.write(JSON.stringify(cases));
