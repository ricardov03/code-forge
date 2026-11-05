import assert from 'node:assert/strict';
import { mkdir, readFile, rm, rmdir } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';

const { version } = JSON.parse(await readFile(new URL('../../package.json', import.meta.url), 'utf8'));

/** A private protocol fixture; cleanup removes the socket before state comparison.
 * @param {{home: string}} fixture
 * @param {string} run
 * @param {(request: Record<string, any>) => Promise<Record<string, any> | null>} answer
 */
export async function serveDaemon(fixture, run, answer) {
  const directory = path.join(fixture.home, '.code-forge/runs', run);
  const socketPath = path.join(directory, 'native.sock');
  await mkdir(directory, { recursive: true });
  const sockets = new Set();
  const requests = [];
  const server = net.createServer((socket) => {
    sockets.add(socket);
    socket.on('error', () => {});
    socket.on('close', () => sockets.delete(socket));
    let buffer = '', pending = Promise.resolve();
    socket.on('data', (chunk) => {
      buffer += chunk;
      while (buffer.includes('\n')) {
        const at = buffer.indexOf('\n'), request = JSON.parse(buffer.slice(0, at));
        buffer = buffer.slice(at + 1);
        requests.push(request);
        pending = pending.then(async () => {
          const reply = request.method === 'ping' ? { ok: true, version, run } : await answer(request);
          if (reply === null) socket.destroy();
          else socket.write(JSON.stringify(reply) + '\n');
        }).catch(() => { socket.destroy(); });
      }
    });
  });
  await new Promise((resolve) => server.listen(socketPath, () => resolve(undefined)));
  return {
    requests,
    async cleanup() {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => server.close(resolve));
      await rm(socketPath, { force: true });
      try { await rmdir(directory); } catch (err) { if (err.code !== 'ENOTEMPTY') throw err; }
      assert.equal(sockets.size, 0, 'all daemon connections closed');
    },
  };
}
