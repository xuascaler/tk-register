import { spawn as spawnProcess } from 'node:child_process';
import { resolveProxy } from './proxy.mjs';
import { fail, safeErrorDetails } from './errors.mjs';
import { recordUpstreamRejection } from './diagnostics.mjs';

const binary = () => process.env.TK_REGISTER_CHROME_TRANSPORT_BIN || '/usr/local/bin/tk-credential-request';
const maxReply = 2 * 1024 * 1024 + 4096;
const failures = new Map([
  ['invalid_request', 400], ['proxy_forbidden', 400], ['proxy_authentication_failed', 502],
  ['upstream_timeout', 504], ['upstream_network_error', 502], ['upstream_rejected', 502],
  ['upstream_session_not_fresh', 502],
  ['upstream_rate_limited', 429], ['upstream_invalid_response', 502], ['internal_error', 502],
]);

function operationFor(value, method) {
  let url;
  try { url = new URL(value); } catch { throw fail('invalid_request', 'Invalid target.'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.hash || url.search || (url.port && url.port !== '443')) throw fail('invalid_request', 'Invalid target.');
  if (url.hostname === 'claude.ai' && url.pathname === '/api/organizations' && method === 'GET') return { operation: 'organizations' };
  const match = url.pathname.match(/^\/v1\/oauth\/([a-zA-Z0-9_-]{1,128})\/authorize$/);
  if (url.hostname === 'claude.ai' && match && method === 'POST') return { operation: 'authorize', organization_id: match[1] };
  if (url.hostname === 'platform.claude.com' && url.pathname === '/v1/oauth/token' && method === 'POST') return { operation: 'token' };
  throw fail('invalid_request', 'Invalid target.');
}

// One process per request isolates cookies/connections and keeps credentials off
// command lines and disk. Test overrides cannot be supplied through the API.
export function createChromeTransport({ spawn = spawnProcess, resolve = resolveProxy, executable = binary } = {}) {
  return async function requestChromeJson(url, { method = 'GET', body, session_key, proxy_url, timeout = 25000 } = {}) {
    const operation = operationFor(url, method);
    const budget = Math.max(1, Math.min(Number(timeout) || 25000, 60000));
    const deadline = Date.now() + budget;
    let dnsTimer;
    let proxy;
    try {
      proxy = await Promise.race([resolve(proxy_url), new Promise((_, reject) => {
        dnsTimer = setTimeout(() => reject(fail('upstream_timeout', 'Request timed out.', 504)), budget);
      })]);
    } finally { clearTimeout(dnsTimer); }
    const remaining = deadline - Date.now();
    if (remaining <= 0) throw fail('upstream_timeout', 'Request timed out.', 504);
    const payload = JSON.stringify({ ...operation, ...(session_key ? { session_key } : {}),
      ...(body === undefined ? {} : { body }), proxy_url, proxy_address: proxy.address, timeout_ms: remaining });
    if (Buffer.byteLength(payload) > 3 * 1024 * 1024) throw fail('invalid_request', 'Request too large.');
    return new Promise((resolve, reject) => {
      let child, timer, done = false, size = 0;
      const chunks = [];
      const finish = (error, value) => {
        if (done) return;
        done = true; clearTimeout(timer);
        if (error) { child?.kill('SIGKILL'); reject(error); } else resolve(value);
      };
      const invalid = () => fail('upstream_invalid_response', 'Invalid transport response.', 502);
      timer = setTimeout(() => finish(fail('upstream_timeout', 'Request timed out.', 504)), remaining);
      try { child = spawn(executable(), [], { stdio: ['pipe', 'pipe', 'ignore'], windowsHide: true }); }
      catch { finish(fail('internal_error', 'Credential transport unavailable.', 502)); return; }
      child.on('error', () => finish(fail('internal_error', 'Credential transport unavailable.', 502)));
      child.stdin.on('error', () => finish(fail('internal_error', 'Credential transport unavailable.', 502)));
      child.stdout.on('data', chunk => {
        size += chunk.length;
        if (size > maxReply) { finish(invalid()); return; }
        chunks.push(chunk);
      });
      child.on('close', code => {
        if (done) return;
        if (code !== 0) { finish(invalid()); return; }
        let result;
        try { result = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { finish(invalid()); return; }
        if (result?.ok === true && Object.hasOwn(result, 'data')) { finish(null, result.data); return; }
        const error = result?.error;
        if (result?.ok !== false || !failures.has(error?.code) || error.status !== failures.get(error.code)) { finish(invalid()); return; }
        recordUpstreamRejection(result.diagnostic, [session_key, proxy_url, proxy.username, proxy.password, proxy.hostname,
          body?.code, body?.code_verifier, body?.code_challenge, body?.state, body?.organization_uuid,
          body?.access_token, body?.refresh_token, body?.id_token]);
        finish(fail(error.code, 'Credential request failed.', error.status, safeErrorDetails(error)));
      });
      child.stdin.end(payload);
    });
  };
}

export const requestChromeJson = createChromeTransport();
