import http from 'node:http';
import { createHash, timingSafeEqual } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { createCredentialService } from './service.mjs';
import { fail, publicError } from './errors.mjs';

const MAX_BODY = 3 * 1024 * 1024;
const hash = value => createHash('sha256').update(value).digest();

export function createHelperServer({ token, service = createCredentialService() } = {}) {
  if (typeof token !== 'string' || !token.trim() || token !== token.trim() || /[\r\n\0]/.test(token)) {
    service.close?.();
    throw new Error('TK_REGISTER_CREDENTIAL_HELPER_TOKEN must be configured.');
  }
  const expected = hash(`Bearer ${token}`);
  const server = http.createServer(async (request, response) => {
    response.setHeader('Cache-Control', 'no-store');
    response.setHeader('Content-Type', 'application/json; charset=utf-8');
    const send = (status, data) => { response.writeHead(status); response.end(JSON.stringify(data)); };
    try {
      if (typeof request.headers.authorization !== 'string' || !timingSafeEqual(hash(request.headers.authorization), expected)) {
        throw fail('unauthorized', 'Unauthorized.', 401);
      }
      if (request.method === 'GET' && request.url === '/healthz') return send(200, { ok: true });
      const route = { '/start': 'start', '/exchange': 'exchange', '/import': 'import', '/verify': 'verify', '/proxy-test': 'proxyTest' }[request.url];
      if (request.method !== 'POST' || !route) throw fail('not_found', 'Not found.', 404);
      if (!/^application\/json(?:\s*;|$)/i.test(request.headers['content-type'] || '')) {
        throw fail('invalid_request', 'JSON content type required.', 415);
      }
      let size = 0;
      const chunks = [];
      for await (const chunk of request) {
        size += chunk.length;
        if (size > MAX_BODY) throw fail('invalid_request', 'Request too large.', 413);
        chunks.push(chunk);
      }
      let body;
      try { body = JSON.parse(Buffer.concat(chunks).toString('utf8')); }
      catch { throw fail('invalid_request', 'Invalid JSON.'); }
      const result = await service[route](body);
      send(200, result);
    } catch (error) {
      const safe = publicError(error);
      if (!response.headersSent && !response.destroyed) send(safe.status, safe.body);
    }
  });
  server.requestTimeout = 60000;
  server.headersTimeout = 10000;
  server.keepAliveTimeout = 5000;
  server.maxHeadersCount = 32;
  server.on('close', () => service.close?.());
  return server;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const server = createHelperServer({ token: process.env.TK_REGISTER_CREDENTIAL_HELPER_TOKEN });
    server.listen(3003, '127.0.0.1', () => process.stdout.write('Credential helper listening on 127.0.0.1:3003\n'));
    server.on('error', () => { process.stderr.write('Credential helper listener failed.\n'); process.exit(1); });
    const stop = () => { server.close(() => process.exit(0)); setTimeout(() => process.exit(0), 5000).unref(); };
    process.on('SIGTERM', stop);
    process.on('SIGINT', stop);
  } catch {
    process.stderr.write('Credential helper requires TK_REGISTER_CREDENTIAL_HELPER_TOKEN.\n');
    process.exitCode = 1;
  }
}
