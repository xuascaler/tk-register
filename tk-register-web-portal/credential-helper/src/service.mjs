import { createHash, randomBytes } from 'node:crypto';
import { createOAuthService } from './oauth.mjs';
import { convertDocument } from './convert.mjs';
import { requestJson as defaultRequestJson } from './transport.mjs';
import { isPublicIP, parseProxy } from './proxy.mjs';
import { fail } from './errors.mjs';

export const FLOW_TTL_MS = 30 * 60 * 1000;
const OPERATION_BUDGET_MS = 50000; // Leave headroom below Go's 55-second helper deadline.
const fields = {
  start: ['owner_id', 'proxy_url', 'mode', 'setup_kind'],
  exchange: ['owner_id', 'flow_id', 'code'],
  import: ['owner_id', 'proxy_url', 'kind', 'document', 'account_index', 'session_key', 'access_token', 'refresh_token', 'expires_at'],
  verify: ['owner_id', 'flow_id', 'proxy_url'],
  'proxy-test': ['owner_id', 'proxy_url'],
};

export function normalizeOwner(value) {
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value < 0) throw fail('invalid_owner', 'Invalid owner.');
    return String(value);
  }
  if (typeof value !== 'string' || !value.trim() || value.length > 128 || /[\x00-\x1f\x7f]/.test(value)) {
    throw fail('invalid_owner', 'Invalid owner.');
  }
  return value.trim();
}

function validate(operation, body) {
  if (!body || typeof body !== 'object' || Array.isArray(body)
      || Object.keys(body).some(key => !fields[operation].includes(key))) {
    throw fail('invalid_request', 'Unexpected request fields.');
  }
  const owner = normalizeOwner(body.owner_id);
  for (const key of ['proxy_url', 'flow_id', 'code', 'session_key', 'access_token', 'refresh_token']) {
    if (body[key] !== undefined && (typeof body[key] !== 'string' || body[key].length > 65536)) {
      throw fail('invalid_request', 'Invalid field.');
    }
  }
  if (body.account_index !== undefined && (!Number.isSafeInteger(body.account_index) || body.account_index < 0)) {
    throw fail('invalid_request', 'Invalid account index.');
  }
  if (body.expires_at !== undefined && !['string', 'number'].includes(typeof body.expires_at)) {
    throw fail('invalid_request', 'Invalid expiration.');
  }
  return owner;
}

export function createCredentialService({ requestJson = defaultRequestJson, now = Date.now, maxFlows = 200, maxOwnerFlows = 10 } = {}) {
  const flows = new Map();
  const imports = new Map();
  const prune = () => {
    for (const [id, flow] of flows) if (flow.expires <= now()) flows.delete(id);
    for (const [digest, id] of imports) if (!flows.has(id)) imports.delete(digest);
  };
  const timer = setInterval(prune, 60000);
  timer.unref();

  async function checkProxy(proxy, expectedIP = '', deadline = Infinity) {
    const started = now();
    const remaining = deadline - now();
    if (remaining <= 0) throw fail('upstream_timeout', 'Operation timed out.', 504);
    const reply = await requestJson('https://api.ipify.org/?format=json', { proxy_url: proxy, timeout: Math.min(15000, remaining) });
    if (!isPublicIP(reply?.ip)) throw fail('proxy_check_failed', 'Invalid exit IP.', 502);
    if (expectedIP && expectedIP !== reply.ip) throw fail('proxy_ip_mismatch', 'Proxy exit IP changed.', 409);
    return { ok: true, ip: reply.ip, checked_at: new Date(now()).toISOString(), elapsed_ms: Math.max(0, now() - started) };
  }

  function newFlow(owner, proxyURL) {
    prune();
    parseProxy(proxyURL);
    // Repeated completed authorizations should not exhaust the pending-flow limit.
    // Never evict a running request or a flow that has not obtained credentials.
    const ownerCount = () => [...flows.values()].filter(f => f.owner === owner).length;
    for (const [id, candidate] of flows) {
      if (ownerCount() < maxOwnerFlows && flows.size < maxFlows) break;
      if (candidate.result && !candidate.pending && (ownerCount() < maxOwnerFlows || candidate.owner === owner)) flows.delete(id);
    }
    if (flows.size >= maxFlows || [...flows.values()].filter(f => f.owner === owner).length >= maxOwnerFlows) {
      throw fail('too_many_sessions', 'Too many authorizations.', 429);
    }
    const flow = { id: randomBytes(24).toString('base64url'), owner, proxy: proxyURL,
      expires: now() + FLOW_TTL_MS, deadline: now() + OPERATION_BUDGET_MS,
      devices: new Map(), result: null, pending: null, check: null };
    const boundedRequest = (url, options) => {
      const remaining = flow.deadline - now();
      if (remaining <= 0) throw fail('upstream_timeout', 'Operation timed out.', 504);
      return requestJson(url, { ...options, timeout: Math.min(options?.timeout || 25000, remaining) });
    };
    flow.oauth = createOAuthService({ requestJson: boundedRequest, now, verifyProxy: async (proxy, expected = '') => {
      if (proxy !== flow.proxy) throw fail('proxy_mismatch', 'Proxy mismatch.', 409);
      flow.check = await checkProxy(proxy, expected || flow.check?.ip || '', flow.deadline);
      return flow.check;
    } });
    flows.set(flow.id, flow);
    return flow;
  }

  function getFlow(owner, id) {
    prune();
    const flow = flows.get(id);
    // Deliberately return the same error for missing, expired, and other-user flows.
    if (!flow || flow.owner !== owner) throw fail('flow_not_found', 'Flow unavailable.', 404);
    return flow;
  }

  function base(flow) {
    return { owner_id: flow.owner, flow_id: flow.id, proxy_check: flow.check,
      expires_at: new Date(flow.expires).toISOString() };
  }

  function finish(flow, document, notes = [], index = undefined) {
    let converted;
    try { converted = convertDocument(document, { proxy_url: flow.proxy, now: now(), account_index: index, deviceStore: flow.devices }); }
    catch { throw fail('invalid_document', 'Invalid credential document.'); }
    // "verified" is the original export-format marker, not a claim that credentials were verified.
    const warnings = [...notes, ...converted.notes,
      'verification_status is an export-format marker; successful supplier validation is still required.'];
    if (!converted.auth.access_token) warnings.push('The access_token field is missing; supply the actual value before submission.');
    flow.result = { ...base(flow), auth: converted.auth, notes: [...new Set(warnings)] };
    return structuredClone(flow.result);
  }

  async function start(body) {
    const owner = validate('start', body);
    const flow = newFlow(owner, body.proxy_url);
    try {
      const result = await flow.oauth.start({ mode: body.mode ?? 'oauth', setup_kind: body.setup_kind ?? 'full', proxy_url: flow.proxy });
      flow.oauthID = result.session_id;
      return { ...base(flow), auth_url: result.auth_url };
    } catch (error) { flows.delete(flow.id); throw error; }
  }

  async function exchange(body) {
    const owner = validate('exchange', body);
    const flow = getFlow(owner, body.flow_id);
    if (!flow.oauthID) throw fail('invalid_request', 'This flow is not an OAuth authorization.');
    if (!body.code?.trim()) throw fail('code_required', 'Code required.');
    const digest = createHash('sha256').update(body.code.trim()).digest('hex');
    if (flow.codeDigest && flow.codeDigest !== digest) throw fail('code_mismatch', 'Code changed.', 409);
    flow.codeDigest = digest;
    if (flow.result) return structuredClone(flow.result);
    if (!flow.pending) {
      flow.deadline = now() + OPERATION_BUDGET_MS;
      flow.pending = (async () => {
        try {
          const result = await flow.oauth.exchange({ session_id: flow.oauthID, code: body.code });
          return finish(flow, result.credential, result.notes);
        } catch (error) {
          // A mistyped/rejected code may be corrected. After successful redemption preserve the binding.
          if (!flow.oauth.hasCredential(flow.oauthID)) flow.codeDigest = null;
          throw error;
        }
      })().finally(() => { flow.pending = null; });
    }
    return structuredClone(await flow.pending);
  }

  async function importCredential(body) {
    const owner = validate('import', body);
    if (!['json', 'cookie', 'token'].includes(body.kind)) throw fail('invalid_request', 'Invalid import kind.');
    prune();
    const digest = createHash('sha256').update(JSON.stringify([owner, body.proxy_url, body.kind,
      body.document, body.account_index, body.session_key, body.access_token, body.refresh_token, body.expires_at])).digest('hex');
    const flow = flows.get(imports.get(digest)) || newFlow(owner, body.proxy_url);
    imports.set(digest, flow.id);
    if (flow.result) return structuredClone(flow.result);
    if (!flow.pending) {
      flow.deadline = now() + OPERATION_BUDGET_MS;
      flow.pending = (async () => {
      if (body.kind === 'json') {
        // Parse before making any network request. Never use a proxy embedded in the document.
        let document = body.document;
        try { if (typeof document === 'string') document = JSON.parse(document); }
        catch { throw fail('invalid_document', 'Invalid JSON.'); }
        if (!document || typeof document !== 'object') throw fail('invalid_document', 'Object required.');
        if (Array.isArray(document)) document = { accounts: document };
        flow.check = await checkProxy(flow.proxy, '', flow.deadline);
        return finish(flow, document, [], body.account_index);
      }
      const result = body.kind === 'cookie'
        ? await flow.oauth.cookie({ session_key: body.session_key, proxy_url: flow.proxy })
        : await flow.oauth.inspect({ access_token: body.access_token, refresh_token: body.refresh_token,
          expires_at: body.expires_at, proxy_url: flow.proxy });
      return finish(flow, result.credential, result.notes);
      })().finally(() => { flow.pending = null; });
    }
    // Retain the bounded flow after network errors: Cookie token redemption may already have succeeded.
    return structuredClone(await flow.pending);
  }

  async function verify(body) {
    const owner = validate('verify', body);
    const flow = getFlow(owner, body.flow_id);
    if (body.proxy_url !== undefined && body.proxy_url !== flow.proxy) throw fail('proxy_mismatch', 'Proxy mismatch.', 409);
    const check = await checkProxy(flow.proxy, flow.check?.ip);
    flow.check = check;
    return base(flow);
  }

  async function proxyTest(body) {
    const owner = validate('proxy-test', body);
    parseProxy(body.proxy_url);
    // Deliberately stateless: no OAuth service, flow, credential cache, or supplier request.
    const check = await checkProxy(body.proxy_url, '', now() + 15000);
    return { owner_id: owner, proxy_check: check };
  }

  return { start, exchange, import: importCredential, verify, proxyTest, close() { clearInterval(timer); flows.clear(); imports.clear(); } };
}
