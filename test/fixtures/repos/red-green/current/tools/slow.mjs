// A test command that never finishes on its own: records its pid in the marker file, then waits.
import { writeFileSync } from 'node:fs';

writeFileSync(process.argv[2], String(process.pid));
setTimeout(() => {}, 60000);
