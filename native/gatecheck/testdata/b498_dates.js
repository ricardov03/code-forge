// B498: [string, Date.parse] pairs for the date forms Go reads like V8.
const out = [];
const mons = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
const days = ['0','00','1','01','9','28','29','30','31','32','33','45','99'];
const years = ['26','0026','49','0049','50','99','0099','100','0100','1969','2024','2026','9999'];
const hours = ['0','00','1','01','9','10','23','24','25','99'];
const mins = ['0','00','5','05','59','60','99'];
const secs = [null,'0','00','7','59'];
const offs = ['+0000','-0530','+0545','+2300','-1200','+1400'];
const names = ['', ' (Central European Standard Time)', ' (Nepal Time)'];
let i = 0;
for (const mon of mons) for (const d of days) for (const y of years) {
  i++;
  const h = hours[i % hours.length], mi = mins[(i * 3) % mins.length], s = secs[(i * 7) % secs.length];
  const time = h + ':' + mi + (s === null ? '' : ':' + s);
  out.push(`${i % 3 ? 'Tue, ' : ''}${d} ${mon} ${y} ${time} ${i % 2 ? 'GMT' : 'UTC'}`);
  out.push(`${i % 2 ? '' : 'Wed '}${mon} ${d} ${y} ${time} GMT${offs[i % offs.length]}${names[i % names.length]}`);
}
for (const h of hours) for (const mi of mins) for (const s of secs) {
  const time = h + ':' + mi + (s === null ? '' : ':' + s);
  out.push(`Tue, 11 Nov 2026 ${time} GMT`, `Nov 30 2026 ${time} GMT-0530`);
}
for (const m of [0, 1, 2, 4, 12, 13, 14, 20, 99]) for (const d of [0, 1, 28, 29, 30, 31, 32, 33, 40, 99]) for (const y of ['2024', '2026', '0000', '+275760', '-271821']) {
  const date = `${y}-${String(m).padStart(2, '0')}-${String(d).padStart(2, '0')}`;
  out.push(date, date + 'T24:00:00Z', date + 'T10:00:00.5+05:45', date + 'T23:59:59Z');
}
console.log(JSON.stringify(out.map((s) => [s, Date.parse(s)])));
