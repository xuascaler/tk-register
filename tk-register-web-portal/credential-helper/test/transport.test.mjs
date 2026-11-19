import test from 'node:test';
import assert from 'node:assert/strict';
import net from 'node:net';
import { once } from 'node:events';
import { createTransport } from '../src/transport.mjs';

// Protocol mocks are loopback-only. Production DNS policy is still exercised; only the
// injected test dialer redirects a validated public destination to this local mock.
async function mockProxy(t, scheme, responseStatus = 200, options = {}) {
  const observed = { connect: '', authorization: '', request: '', socksUser: '', socksTarget: '' };
  const connections = new Set();
  const server = net.createServer(socket => {
    connections.add(socket); socket.on('close', () => connections.delete(socket)); socket.on('error', () => {});
    let buffer = Buffer.alloc(0), stage = scheme.startsWith('socks') ? 'greeting' : 'connect';
    socket.on('data', data => {
      buffer = Buffer.concat([buffer, data]);
      while (buffer.length) {
        if (stage === 'greeting') {
          if (buffer.length < 2 || buffer.length < 2 + buffer[1]) return;
          buffer = buffer.subarray(2 + buffer[1]); socket.write(Buffer.from([5, options.socksMethod ?? 2])); stage = 'auth';
        } else if (stage === 'auth') {
          if (buffer.length < 2 || buffer.length < 3 + buffer[1]) return;
          const userLength = buffer[1], passwordLength = buffer[2 + userLength], length = 3 + userLength + passwordLength;
          if (buffer.length < length) return;
          observed.socksUser = buffer.subarray(2, 2 + userLength).toString();
          buffer = buffer.subarray(length); socket.write(Buffer.from([1, options.socksAuthStatus ?? 0])); stage = 'socksConnect';
        } else if (stage === 'socksConnect') {
          if (buffer.length < 5 || buffer.length < 7 + buffer[4]) return;
          assert.equal(buffer[3], 3);
          observed.socksTarget = buffer.subarray(5, 5 + buffer[4]).toString();
          buffer = buffer.subarray(7 + buffer[4]);
          socket.write(Buffer.from([5, 0, 0, 1, 0, 0, 0, 0, 0, 0])); stage = 'request';
        } else {
          const end = buffer.indexOf('\r\n\r\n'); if (end < 0) return;
          const headers = buffer.subarray(0, end + 4).toString(); buffer = buffer.subarray(end + 4);
          if (stage === 'connect') {
            observed.connect = headers.split('\r\n')[0];
            observed.authorization = headers.match(/Proxy-Authorization: ([^\r]+)/i)?.[1] || '';
            socket.write(`HTTP/1.1 ${options.connectStatus ?? 200} Proxy reply\r\n\r\n`); stage = 'request';
          } else {
            observed.request = headers;
            const body = options.responseBody ?? JSON.stringify({ ip: '8.8.4.4', secret_upstream_detail: 'never shown on error' });
            socket.end(`HTTP/1.1 ${responseStatus} Mock\r\nContent-Type: ${options.contentType || 'application/json'}\r\nContent-Length: ${Buffer.byteLength(body) + (options.truncateResponse ? 10 : 0)}\r\nConnection: close\r\n\r\n${body}`);
            return;
          }
        }
      }
    });
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => { for (const c of connections) c.destroy(); server.close(); });
  return { observed, port: server.address().port };
}

for (const scheme of ['http', 'https', 'socks5', 'socks5h']) {
  test(`${scheme} proxy protocol pins DNS destination, authenticates proxy and retains TLS names`, async t => {
    const mock = await mockProxy(t, scheme);
    const tlsNames = [], dialAddresses = [];
    let lookups = 0;
    const transport = createTransport({
      lookup: async () => { lookups++; return [{ address: '8.8.8.8', family: 4 }]; },
      dial: async (options, track) => {
        dialAddresses.push(options.host);
        const socket = track(net.connect({ host: '127.0.0.1', port: mock.port }));
        await once(socket, 'connect'); return socket;
      },
      secure: async (socket, hostname) => { tlsNames.push(hostname); return socket; },
    });
    const response = await transport('https://api.ipify.org/?format=json', {
      proxy_url: `${scheme}://demo:password@proxy.example:1080`, timeout: 2000,
    });
    assert.equal(response.ip, '8.8.4.4'); assert.equal(lookups, 1);
    assert.deepEqual(dialAddresses, ['8.8.8.8']);
    assert.deepEqual(tlsNames, scheme === 'https' ? ['proxy.example', 'api.ipify.org'] : ['api.ipify.org']);
    if (scheme.startsWith('socks')) {
      assert.equal(mock.observed.socksUser, 'demo'); assert.equal(mock.observed.socksTarget, 'api.ipify.org');
    } else {
      assert.equal(mock.observed.connect, 'CONNECT api.ipify.org:443 HTTP/1.1');
      assert.equal(mock.observed.authorization, 'Basic ' + Buffer.from('demo:password').toString('base64'));
    }
    assert.match(mock.observed.request, /GET \/\?format=json HTTP\/1\.1/);
    assert.ok(!mock.observed.request.includes('Proxy-Authorization'));
  });
}

test('upstream errors never include body and redirects are not followed', async t => {
  for (const status of [302, 429, 500]) {
    const mock = await mockProxy(t, 'http', status);
    let dials = 0;
    const transport = createTransport({
      lookup: async () => [{ address: '8.8.8.8', family: 4 }],
      dial: async (options, track) => { dials++; const socket = track(net.connect({ host: '127.0.0.1', port: mock.port })); await once(socket, 'connect'); return socket; },
      secure: async socket => socket,
    });
    await assert.rejects(transport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example' }), error => {
      assert.equal(error.code, status === 429 ? 'upstream_rate_limited' : 'upstream_rejected');
      assert.ok(!JSON.stringify(error).includes('secret_upstream_detail')); return true;
    });
    assert.equal(dials, 1);
  }
});

test('transport retains only real status, response kind and fixed upstream error codes', async t => {
  for (const [status, body, contentType, kind, upstreamCode] of [
    [401, JSON.stringify({ error: { type: 'authentication_error', message: 'private-response-secret' } }), 'application/json', 'json', 'authentication_error'],
    [400, JSON.stringify({ error: 'invalid_grant', error_description: 'private-response-secret' }), 'application/json', 'json', 'invalid_grant'],
    [403, JSON.stringify({ error: { type: 'private-response-secret' } }), 'application/json', 'json', undefined],
    [403, '<!doctype html><html>private-response-secret</html>', 'text/html', 'html', undefined],
    [200, 'private-response-secret', 'text/plain', 'other', undefined],
  ]) {
    const mock = await mockProxy(t, 'http', status, { responseBody: body, contentType });
    const transport = createTransport({
      lookup: async () => [{ address: '8.8.8.8', family: 4 }],
      dial: async (_options, track) => { const socket = track(net.connect({ host: '127.0.0.1', port: mock.port })); await once(socket, 'connect'); return socket; },
      secure: async socket => socket,
    });
    await assert.rejects(transport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example' }), error => {
      assert.equal(error.upstreamStatus, status);
      assert.equal(error.responseKind, kind);
      assert.equal(error.upstreamCode, upstreamCode);
      assert.equal(error.code, status === 200 ? 'upstream_invalid_response' : 'upstream_rejected');
      assert(!JSON.stringify(error).includes('private-response-secret'));
      assert(!error.message.includes('private-response-secret'));
      return true;
    });
  }
});

test('an interrupted response retains received status without exposing partial body contents', async t => {
  const mock = await mockProxy(t, 'http', 503, { responseBody: '<html>private-partial-secret', contentType: 'text/html', truncateResponse: true });
  const transport = createTransport({
    lookup: async () => [{ address: '8.8.8.8', family: 4 }],
    dial: async (_options, track) => { const socket = track(net.connect({ host: '127.0.0.1', port: mock.port })); await once(socket, 'connect'); return socket; },
    secure: async socket => socket,
  });
  await assert.rejects(transport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example' }), error => {
    assert.equal(error.code, 'upstream_network_error');
    assert.equal(error.upstreamStatus, 503); assert.equal(error.responseKind, 'html');
    assert(!JSON.stringify(error).includes('private-partial-secret'));
    return true;
  });
});

test('proxy negotiation timeout closes the only connection and never attempts direct access', async t => {
  const sockets = new Set();
  const server = net.createServer(socket => { sockets.add(socket); socket.on('error', () => {}); socket.on('close', () => sockets.delete(socket)); });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => { for (const socket of sockets) socket.destroy(); server.close(); });
  let dials = 0, client;
  const transport = createTransport({
    lookup: async () => [{ address: '8.8.8.8', family: 4 }],
    dial: async (options, track) => {
      dials++; client = track(net.connect({ host: '127.0.0.1', port: server.address().port }));
      await once(client, 'connect'); return client;
    },
  });
  await assert.rejects(transport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example', timeout: 30 }), { code: 'upstream_timeout' });
  assert.equal(dials, 1); assert.equal(client.destroyed, true);
});

test('known HTTP and SOCKS authentication failures are distinguishable from connection errors', async t => {
  for (const [scheme, options] of [
    ['http', { connectStatus: 407 }],
    ['https', { connectStatus: 407 }],
    ['socks5', { socksAuthStatus: 1 }],
    ['socks5h', { socksAuthStatus: 1 }],
    ['socks5', { socksMethod: 255 }],
  ]) {
    const mock = await mockProxy(t, scheme, 200, options);
    let dials = 0;
    const transport = createTransport({
      lookup: async () => [{ address: '8.8.8.8', family: 4 }],
      dial: async (_options, track) => {
        dials++;
        const socket = track(net.connect({ host: '127.0.0.1', port: mock.port }));
        await once(socket, 'connect'); return socket;
      },
      secure: async socket => socket,
    });
    await assert.rejects(transport('https://api.ipify.org/?format=json', {
      proxy_url: `${scheme}://demo:mock-password@proxy.example:1080`, timeout: 2000,
    }), error => {
      assert.equal(error.code, 'proxy_authentication_failed');
      assert.equal(error.status, 502);
      assert.ok(!error.message.includes('mock-password'));
      assert.ok(!error.message.includes('proxy.example'));
      return true;
    });
    assert.equal(dials, 1);
    assert.equal(mock.observed.request, '');
  }
});
