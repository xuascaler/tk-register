import net from 'node:net';
import tls from 'node:tls';
import http from 'node:http';
import { once } from 'node:events';
import { resolveProxy } from './proxy.mjs';
import { fail } from './errors.mjs';

const MAX_RESPONSE = 2 * 1024 * 1024;

export function allowedTarget(value) {
  let url;
  try { url = new URL(value); } catch { throw fail('invalid_request', 'Invalid target.'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.hash || (url.port && url.port !== '443')) {
    throw fail('invalid_request', 'Invalid target.');
  }
  const valid = (url.hostname === 'api.ipify.org' && url.pathname === '/' && url.search === '?format=json')
    || (url.hostname === 'platform.claude.com' && url.pathname === '/v1/oauth/token' && !url.search)
    || (url.hostname === 'api.anthropic.com' && url.pathname === '/api/oauth/profile' && !url.search)
    || (url.hostname === 'claude.ai' && !url.search && (url.pathname === '/api/organizations'
      || /^\/v1\/oauth\/[a-zA-Z0-9_-]{1,128}\/authorize$/.test(url.pathname)));
  if (!valid) throw fail('invalid_request', 'Target not allowed.');
  return url;
}

class Reader {
  constructor(socket) {
    this.socket = socket;
    this.buffer = Buffer.alloc(0);
    this.error = null;
    this.waiter = null;
    this.onData = chunk => {
      this.buffer = Buffer.concat([this.buffer, chunk]);
      if (this.buffer.length > 32768) this.error = new Error('Proxy response too large');
      this.wake();
    };
    this.onError = () => { this.error = new Error('Proxy closed'); this.wake(); };
    socket.on('data', this.onData);
    socket.on('error', this.onError);
    socket.on('end', this.onError);
    socket.on('close', this.onError);
  }
  wake() { if (this.waiter) { const fn = this.waiter; this.waiter = null; fn(); } }
  async wait() {
    if (this.error) throw this.error;
    await new Promise(resolve => { this.waiter = resolve; });
    if (this.error) throw this.error;
  }
  async bytes(size) {
    while (this.buffer.length < size) await this.wait();
    const data = this.buffer.subarray(0, size);
    this.buffer = this.buffer.subarray(size);
    return data;
  }
  async headers() {
    while (true) {
      const i = this.buffer.indexOf('\r\n\r\n');
      if (i >= 0) return this.bytes(i + 4);
      if (this.buffer.length > 16384) throw new Error('Proxy response too large');
      await this.wait();
    }
  }
  release() {
    this.socket.pause();
    this.socket.off('data', this.onData);
    this.socket.off('error', this.onError);
    this.socket.off('end', this.onError);
    this.socket.off('close', this.onError);
    if (this.buffer.length) this.socket.unshift(this.buffer);
  }
}

async function connectHTTP(socket, proxy, target) {
  const reader = new Reader(socket);
  try {
    const authorization = proxy.username
      ? `Proxy-Authorization: Basic ${Buffer.from(`${proxy.username}:${proxy.password}`).toString('base64')}\r\n` : '';
    socket.write(`CONNECT ${target.hostname}:443 HTTP/1.1\r\nHost: ${target.hostname}:443\r\n${authorization}\r\n`);
    const reply = (await reader.headers()).toString('ascii');
    if (!/^HTTP\/1\.[01] 200(?: |\r)/.test(reply)) throw fail('upstream_network_error', 'Proxy CONNECT rejected.', 502);
  } finally { reader.release(); }
}

async function connectSOCKS(socket, proxy, target) {
  const reader = new Reader(socket);
  try {
    socket.write(proxy.username ? Buffer.from([5, 2, 0, 2]) : Buffer.from([5, 1, 0]));
    const method = await reader.bytes(2);
    if (method[0] !== 5 || ![0, 2].includes(method[1]) || (method[1] === 2 && !proxy.username)) {
      throw fail('upstream_network_error', 'Proxy authentication rejected.', 502);
    }
    if (method[1] === 2) {
      const user = Buffer.from(proxy.username), password = Buffer.from(proxy.password);
      socket.write(Buffer.concat([Buffer.from([1, user.length]), user, Buffer.from([password.length]), password]));
      const authentication = await reader.bytes(2);
      if (authentication[0] !== 1 || authentication[1] !== 0) throw fail('upstream_network_error', 'Proxy authentication rejected.', 502);
    }
    // SOCKS5 and SOCKS5h both resolve the fixed upstream hostname at the proxy.
    const host = Buffer.from(target.hostname);
    socket.write(Buffer.concat([Buffer.from([5, 1, 0, 3, host.length]), host, Buffer.from([1, 187])]));
    const reply = await reader.bytes(4);
    if (reply[0] !== 5 || reply[1] !== 0 || reply[2] !== 0) throw fail('upstream_network_error', 'Proxy connection rejected.', 502);
    if (reply[3] === 1) await reader.bytes(6);
    else if (reply[3] === 4) await reader.bytes(18);
    else if (reply[3] === 3) { const length = (await reader.bytes(1))[0]; await reader.bytes(length + 2); }
    else throw fail('upstream_network_error', 'Invalid proxy response.', 502);
  } finally { reader.release(); }
}

async function defaultDial(options, track = value => value) {
  const socket = track(net.connect(options));
  await once(socket, 'connect');
  return socket;
}

async function defaultSecure(socket, hostname) {
  const secure = tls.connect({ socket, servername: net.isIP(hostname) ? undefined : hostname,
    rejectUnauthorized: true, minVersion: 'TLSv1.2', ALPNProtocols: ['http/1.1'],
    // For IP proxies Node does not automatically set SNI. Explicitly verify the IP SAN.
    checkServerIdentity: (_name, cert) => tls.checkServerIdentity(hostname, cert) });
  await once(secure, 'secureConnect');
  return secure;
}

/** Test dependency injection never comes from environment variables or HTTP input. */
export function createTransport({ lookup, dial = defaultDial, secure = defaultSecure } = {}) {
  return async function requestJson(urlValue, { method = 'GET', headers = {}, body, proxy_url, timeout = 25000 } = {}) {
    const target = allowedTarget(urlValue);
    if (!['GET', 'POST'].includes(method)) throw fail('invalid_request', 'Invalid method.');
    const sockets = new Set();
    let request, agent, timedOut = false, finished = false;
    const track = socket => {
      // The connect may finish after a DNS/connect timeout: close late sockets too.
      if (finished) socket.destroy();
      sockets.add(socket);
      socket.on('error', () => {});
      return socket;
    };
    const destroy = () => { request?.destroy(); agent?.destroy(); for (const socket of sockets) socket.destroy(); };
    let timer;
    try {
      const operation = async () => {
        const proxy = await resolveProxy(proxy_url, { lookup });
        if (finished) throw fail('upstream_timeout', 'Request timed out.', 504);
        let socket = track(await dial({ host: proxy.address, port: proxy.port, family: proxy.family }, track));
        if (proxy.protocol === 'https:') socket = track(await secure(socket, proxy.hostname));
        if (proxy.protocol === 'http:' || proxy.protocol === 'https:') await connectHTTP(socket, proxy, target);
        else await connectSOCKS(socket, proxy, target);
        socket = track(await secure(socket, target.hostname));
        if (finished) throw fail('upstream_timeout', 'Request timed out.', 504);
        // Send HTTP over the already authenticated TLS tunnel. There is no default dial fallback.
        agent = new http.Agent({ keepAlive: false, maxSockets: 1 });
        agent.createConnection = () => socket;
        const payload = body === undefined ? undefined : Buffer.from(JSON.stringify(body));
        const response = await new Promise((resolve, reject) => {
          request = http.request({ hostname: target.hostname, port: 443, method,
            path: target.pathname + target.search, agent,
            headers: { Accept: 'application/json', ...headers, Host: target.hostname,
              ...(payload ? { 'Content-Type': 'application/json', 'Content-Length': payload.length } : {}) } }, resolve);
          request.on('error', reject);
          request.end(payload);
          socket.resume();
        });
        const chunks = [];
        let size = 0;
        for await (const chunk of response) {
          size += chunk.length;
          if (size > MAX_RESPONSE) throw fail('upstream_invalid_response', 'Response too large.', 502);
          chunks.push(chunk);
        }
        if (response.statusCode < 200 || response.statusCode >= 300) {
          throw fail(response.statusCode === 429 ? 'upstream_rate_limited' : 'upstream_rejected',
            'Upstream rejected the request.', response.statusCode === 429 ? 429 : 502,
            { upstreamStatus: response.statusCode });
        }
        try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); }
        catch { throw fail('upstream_invalid_response', 'Invalid JSON response.', 502); }
      };
      return await Promise.race([operation(), new Promise((_resolve, reject) => {
        timer = setTimeout(() => { timedOut = true; finished = true; destroy(); reject(fail('upstream_timeout', 'Request timed out.', 504)); },
          Math.max(1, Math.min(Number(timeout) || 25000, 60000)));
      })]);
    } catch (error) {
      if (timedOut) throw fail('upstream_timeout', 'Request timed out.', 504);
      if (error?.code && error?.status) throw error;
      throw fail('upstream_network_error', 'Proxy transport failed.', 502);
    } finally {
      finished = true;
      clearTimeout(timer);
      destroy();
    }
  };
}

export const requestJson = createTransport();
