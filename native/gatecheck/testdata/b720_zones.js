// B720: Date.parse of local wall times around zone offset changes. Run from
// native/gatecheck: node testdata/b720_zones.js > testdata/b720_zones.json
// For each zone it finds every offset change from 1900 to 2050 (and near the
// TimeClip bounds) and parses wall times every 15 minutes within 2 hours.
// Each row is [text, Date.parse(text), [[instant, Node's minutes east of UTC
// there], ...]]; a test skips a row whose probes Go's tz data reads otherwise.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
const self = fileURLToPath(import.meta.url);
const zones = ['America/New_York', 'Europe/London', 'Australia/Lord_Howe', 'America/Sao_Paulo', 'Asia/Kathmandu', 'Pacific/Apia', 'America/St_Johns', 'Europe/Moscow', 'Africa/Casablanca', 'Pacific/Chatham'];
const pad = (n, w = 2) => String(n).padStart(w, '0');
const wall = (ms) => { const d = new Date(ms); const y = d.getUTCFullYear(); const ys = y >= 0 && y <= 9999 ? pad(y, 4) : (y < 0 ? '-' : '+') + pad(Math.abs(y), 6); return `${ys}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:00`; };
if (process.argv[2] === 'child') {
  const inputs = [];
  const off = (ms) => new Date(ms).getTimezoneOffset();
  const step = 6 * 3600000;
  for (let t = Date.UTC(1900, 0, 1); t < Date.UTC(2050, 0, 1); t += step) {
    if (off(t) === off(t + step)) continue;
    let lo = t, hi = t + step;
    while (hi - lo > 60000) { const mid = lo + Math.floor((hi - lo) / 2 / 60000) * 60000; if (off(mid) === off(lo)) lo = mid; else hi = mid; }
    const local = hi - off(hi - 1) * 60000;
    for (let w = local - 2 * 3600000; w <= local + 2 * 3600000; w += 15 * 60000) inputs.push([wall(w), [w + off(hi - 1) * 60000, w + off(hi) * 60000]]);
  }
  for (const b of [8.64e15, -8.64e15]) for (let w = b - 2 * 86400000; w <= b + 2 * 86400000; w += 60 * 60000) inputs.push([wall(w), []]);
  // Far years: September and April windows of 275759 and -271820.
  for (const y of [275759, -271820, 100000, -100000]) for (const m of [2, 3, 8, 9, 10]) for (let d = 1; d <= 31; d += 3) for (const h of [0, 1, 2, 3, 23]) inputs.push([`${y < 0 ? '-' : '+'}${pad(Math.abs(y), 6)}-${pad(m + 1)}-${pad(d)}T${pad(h)}:30:00`, []]);
  // Probes: Node's offset (minutes east) at the result and at the instants the
  // wall time names under the offsets before and after the change.
  const probe = (u) => [u, -off(u)];
  process.stdout.write(JSON.stringify(inputs.map(([s, at]) => { const v = Date.parse(s); return Number.isNaN(v) ? [s, null, []] : [s, v, [v, ...at].map(probe)]; })));
} else {
  const out = {};
  for (const tz of zones) out[tz] = JSON.parse(execFileSync(process.execPath, [self, 'child'], { env: { PATH: process.env.PATH, TZ: tz }, maxBuffer: 1 << 28 }).toString());
  process.stdout.write(JSON.stringify(out));
}
