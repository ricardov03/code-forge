// B709: [string, Date.parse, near a zone change] rows for the plain legacy
// date forms Go reads like V8 (gatecheck jsLocalDate). Run under each TZ.
const out = [];
const full = ['January','February','March','April','May','June','July','August','September','October','November','December'];
const abbr = full.map((m) => m.slice(0, 3));
const days = ['Monday','Tuesday','Wednesday','Thursday','Friday','Saturday','Sunday'];
const times = ['', ' 0:00', ' 00:00', ' 9:05', ' 10:20', ' 23:59', ' 24:00', ' 24:01', ' 25:00', ' 10:60', ' 10:20:30', ' 10:20:60', ' 10:20:30.5', ' 10:20:30.123456', ' 24:00:00', ' 24:00:00.1'];
const zones = ['', 'Z', ' Z', ' GMT', ' UTC', 'GMT'];
const dates = [[2020, 1, 2], [2026, 7, 15], [2099, 12, 31], [2024, 2, 29], [2026, 2, 30], [2026, 4, 31], [1999, 6, 1], [2020, 0, 5], [2020, 13, 5], [2020, 5, 0], [2020, 5, 32], [999, 5, 5], [99, 5, 5]];
const pad = (n, w) => String(n).padStart(w, '0');
let i = 0;
for (const [y, m, d] of dates) for (const t of times) {
  i++;
  const z = t === '' ? '' : zones[i % zones.length];
  const tail = t + z;
  const mon = m >= 1 && m <= 12 ? (i % 2 ? full[m - 1] : abbr[m - 1]) : 'Foo';
  const wd = i % 3 === 0 ? days[i % 7] + ', ' : i % 3 === 1 ? days[i % 7].slice(0, 3) + ' ' : '';
  out.push(`${pad(y, 4)}-${i % 2 ? pad(m, 2) : m}-${i % 2 ? d : pad(d, 2)}${t === '' ? '' : tail}`);
  out.push(`${pad(y, 4)}/${i % 2 ? m : pad(m, 2)}/${pad(d, i % 2 ? 2 : 1)}${tail}`);
  out.push(`${i % 2 ? m : pad(m, 2)}/${d}/${pad(y, 4)}${tail}`);
  if (mon !== 'Foo') {
    out.push(`${wd}${mon} ${d}${i % 2 ? ',' : ''} ${pad(y, 4)}${tail}`);
    out.push(`${wd}${d} ${mon} ${pad(y, 4)}${tail}`);
  }
}
// near a zone change (America/New_York 2026-03-08 and 2026-11-01, local forms)
const near = ['2026-3-8 02:30', '2026/03/08 03:30', '11/1/2026 01:30', 'Nov 1 2026 01:30', 'March 8, 2026', '2026-11-1'];
for (const s of near) out.push(s);
console.log(JSON.stringify(out.map((s) => [s, Date.parse(s), near.includes(s)])));
