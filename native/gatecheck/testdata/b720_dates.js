// B720: Date.parse corpus for V8DateParse. Run from native/gatecheck:
//   node testdata/b720_dates.js > testdata/b720_dates.json
// It runs itself under TZ=UTC, America/New_York and Asia/Kathmandu.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
const self = fileURLToPath(import.meta.url);
if (process.argv[2] === 'child') {
  const inputs = JSON.parse(readFileSync(0, 'utf8'));
  process.stdout.write(JSON.stringify(inputs.map((s) => { const v = Date.parse(s); return Number.isNaN(v) ? null : v; })));
} else {
  main();
}
function main() {
let seed = 720;
const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
const pick = (a) => a[Math.floor(rnd() * a.length)];
const num = () => { const n = 1 + Math.floor(rnd() * (rnd() < 0.2 ? 12 : 4)); let s = ''; for (let i = 0; i < n; i++) s += String(Math.floor(rnd() * 10)); if (rnd() < 0.3) s = '0'.repeat(Math.floor(rnd() * 3)) + s; return s; };
const words = ['Jan', 'January', 'feb', 'MARCH', 'Apr', 'may', 'June', 'jul', 'Aug', 'Sept', 'oct', 'Nov', 'December', 'decx', 'am', 'PM', 'a', 'p', 'ut', 'UTC', 'utcx', 'z', 'Z', 'GMT', 'gmtx', 'EST', 'edt', 'CST', 'cdt', 'MST', 'mdt', 'PST', 'pdt', 'T', 't', 'Tue', 'Monday', 'foo', 'x', 'été', '日', '[', '_', '`', '{', '~', '\u007f', 'J ', ' '];
const syms = [':', '-', '+', '.', ')', '(', '/', ',', ' ', ' ', ' ', '\t', '\n', ' ', '　', '﻿', ' ', '\u0000', '(x)', '((a)b)', '@', '!', '#'];
const parts = [() => num(), () => num(), () => num(), () => pick(words), () => pick(syms), () => pick(syms)];
const inputs = new Set(['', ' ', '0', '2026', '2026-05-01', '2026-05-01T10:00:00.000Z', 'Z', 'T', '(', ')', '\u0000']);
const iso = (d) => d.toISOString();
for (let i = 0; i < 2500; i++) {
  const ms = Math.floor((rnd() * 2 - 1) * 8.64e15);
  const d = new Date(ms);
  inputs.add(iso(d)); inputs.add(d.toString()); inputs.add(d.toUTCString()); inputs.add(d.toLocaleString('en-US', { timeZone: 'UTC' }));
}
// near the bounds
for (const ms of [8.64e15, -8.64e15, 8.64e15 - 1, -8.64e15 + 1]) { const d = new Date(ms); inputs.add(iso(d)); inputs.add(d.toUTCString()); inputs.add(iso(d).replace('Z', '')); inputs.add(iso(d).slice(0, 10)); }
for (const s of ['+275760-09-13T00:00:00', '+275760-09-13T00:00:00.001Z', '+275760-09-12T23:00', '-271821-04-20T00:00', '-271821-04-19T23:00:00', '-271821-04-20T01:00', '+275760-09-13', '-271821-04-20', '-271821-04-19', 'Sep 13 275760', 'Sep 13 275760 00:00 GMT', 'Apr 19 -271821', '-000000-01-01', '+000000-01-01T00:00Z', '+1000000-01-01', '1000000-01-01', 'Jan 1 1000000', 'Jan 1 1000001', 'Jan 1 999999999', 'Jan 1 2000 10:00 GMT+65535:00', '1 1 2000 10:00 +596523:00', '1 1 2000 10:00 +596524:00', '1 1 2000 10:00 +1193046:28', '1 1 2000 10:00 +1193047:00', '1 1 2000 10:00 -596523:14', '1 1 2000 10:00 -596523:15', '1 1 2000 10:00 +357913:56', '1 1 2000 10:00 +357914', 'Jan 1 2000 12 am', 'Jan 1 2000 10:00 13 pm', 'Jan 1 2000 10:00 pm', 'Jan 1 2000 12:30 AM', 'Jan 1 2000 0:30 PM', 'Jan 2000', '5 Jan', 'Jan 5', '2000 Jan', '99 Jan 5', '50/1/1', '1/1/50', '1/1/49', '1/1/100', 'Jan 1 2000 10:00 UTC+5', 'Jan 1 2000 10:00 UTC-0530', 'Jan 1 2000 10:00 EST+1', 'Jan 1 2000 24:00', 'Jan 1 2000 24:00:01', '2000-01-01T24:00', '2000-01-01T24:00:00.000Z', '2000-01-01T24:00:00.001Z', '2000-01-01T24:01Z', 'Jan 1 2000 10:00:00.1234567891234', '2000-01-01T10:00:00.0001234567891234Z', 'Jan 1 2000 10::', 'Jan 1 2000 10:: 5', '10:00 Jan 1 2000', 'Thu, 01 Jan 1970 00:00:00 GMT+0100 (CET)', 'Jan 1 2000 10:00 ) ', 'x Jan 1 2000', 'x5 Jan 1 2000', 'Jan 1 2000 x']) inputs.add(s);
// ISO-like with mutations
const isoParts = () => {
  let s = pick(['', '+', '-']) + (rnd() < 0.5 ? num() : String(1900 + Math.floor(rnd() * 200)));
  if (rnd() < 0.8) s += '-' + pick([num(), '01', '12', '13', '00', '1']);
  if (rnd() < 0.7) s += '-' + pick([num(), '01', '31', '32', '00']);
  if (rnd() < 0.7) { s += pick(['T', 't', ' ', 'T ']) + pick(['00', '23', '24', '12', num()]) + ':' + pick(['00', '59', '60', num()]); if (rnd() < 0.6) s += ':' + pick(['00', '59', '60', num()]); if (rnd() < 0.5) s += '.' + num(); }
  if (rnd() < 0.5) s += pick(['Z', 'z', '+05:30', '-0800', '+24:00', '-23:59', '+1', '+123', '+12345', ' GMT', ' UTC+3', ' PM', ' am', ' (PST)']);
  return s;
};
for (let i = 0; i < 6000; i++) inputs.add(isoParts());
for (let i = 0; i < 12000; i++) {
  const n = 1 + Math.floor(rnd() * 9);
  let s = '';
  for (let j = 0; j < n; j++) s += pick(parts)();
  inputs.add(s);
}
const list = [...inputs];
const out = { inputs: list };
for (const tz of ['UTC', 'America/New_York', 'Asia/Kathmandu']) {
  out[tz] = JSON.parse(execFileSync(process.execPath, [self, 'child'], { input: JSON.stringify(list), env: { PATH: process.env.PATH, TZ: tz }, maxBuffer: 1 << 28 }).toString());
}
process.stdout.write(JSON.stringify(out));
}
