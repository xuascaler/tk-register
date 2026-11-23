import { t } from '../i18n';
import { AuthFieldError, authFields, emptyDraft, MAX_AUTH_BYTES, parseAuthJSON, vendorTimestamp, type AccountDraft } from './validation';
import { APIError } from '../api';

export type CredentialMethod = 'oauth' | 'setup' | 'json' | 'cookie' | 'token';
export type CredentialGoal = 'submit' | 'export';
export type ProxyCheck = { ok: true; ip: string; checked_at: string; elapsed_ms: number };
export type CredentialFlow = {
  flow_id: string;
  auth_url?: string;
  expires_at: string;
  status?: 'awaiting_code' | 'ready';
  auth?: Record<string, unknown>;
  notes?: string[];
  proxy_check?: Partial<ProxyCheck> & { exit_ip?: string };
};

export function readProxyCheck(value: unknown): ProxyCheck {
  const check = value as Partial<ProxyCheck> | null;
  if (!check || check.ok !== true || typeof check.ip !== 'string' || !check.ip || check.ip.length > 64 || !/^[\da-f:.]+$/i.test(check.ip) || typeof check.checked_at !== 'string' || !Number.isFinite(Date.parse(check.checked_at)) || typeof check.elapsed_ms !== 'number' || !Number.isFinite(check.elapsed_ms) || check.elapsed_ms < 0 || check.elapsed_ms > 60000) throw new Error('The proxy test result could not be confirmed. Please try again.');
  return { ok: true, ip: check.ip, checked_at: check.checked_at, elapsed_ms: check.elapsed_ms };
}

export function proxyTestFailure(cause: unknown) {
  const code = cause instanceof APIError ? cause.code : '';
  const messages: Record<string, string> = {
    PROXY_TEST_TIMEOUT: 'Connection timed out. Check the proxy and try again.',
    PROXY_AUTHENTICATION_FAILED: 'Proxy authentication failed. Check the username and password.',
    PROXY_CONNECTION_FAILED: 'Unable to connect through this proxy. Check the proxy and try again.',
    PROXY_TEST_UNAVAILABLE: 'Connection testing is temporarily unavailable. Please try again.',
    INVALID_PROXY: 'Enter a valid HTTP, HTTPS or SOCKS5 proxy URL.',
    PROXY_CHANGED: 'The proxy exit IP has changed. Start a new authorization before continuing.',
    CREDENTIAL_FLOW_NOT_FOUND: 'This authorization has expired. Start a new authorization.',
    RATE_LIMITED: 'Too many connection tests. Please wait and try again.',
  };
  return { code, message: messages[code] || 'The proxy connection could not be tested. Please try again.' };
}

export function automaticDraft(now = new Date()): AccountDraft {
  const today = vendorTimestamp(now, 'Asia/Shanghai').slice(0, 10);
  const [year, month, day] = today.split('-').map(Number);
  const previousMonth = new Date(Date.UTC(year, month - 2, 1));
  const lastDay = new Date(Date.UTC(year, month - 1, 0)).getUTCDate();
  previousMonth.setUTCDate(Math.min(day, lastDay));
  const end = new Date(`${today}T12:00:00Z`);
  end.setUTCDate(end.getUTCDate() + 30);
  return { ...emptyDraft(), register_time: previousMonth.toISOString().slice(0, 10), plan_start_time: today, plan_end_time: end.toISOString().slice(0, 10), payment_method: 'physical_credit_card', register_device: 'Windows', balance: '100', recharge_time: today, tags: '速刷' };
}

// Rejection sampling avoids modulo bias. This password belongs to the
// submission only; it never changes the Claude login password.
export function submissionPassword(random: Pick<Crypto, 'getRandomValues'> = crypto) {
  const groups = ['ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz', '0123456789', '!@#$%^&*'];
  const alphabet = groups.join('');
  const pick = (size: number) => {
    const limit = Math.floor(256 / size) * size;
    for (;;) { const byte = random.getRandomValues(new Uint8Array(1))[0]; if (byte < limit) return byte % size; }
  };
  const result = groups.map(group => group[pick(group.length)]);
  while (result.length < 24) result.push(alphabet[pick(alphabet.length)]);
  for (let index = result.length - 1; index > 0; index--) { const selected = pick(index + 1); [result[index], result[selected]] = [result[selected], result[index]]; }
  return result.join('');
}

export function validateProxy(value: string) {
  if (!value.trim()) return 'Enter the account proxy.';
  try {
    const url = new URL(value.trim());
    if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(url.protocol) || !url.hostname) throw new Error();
  } catch { return 'Use an http://, https://, socks5:// or socks5h:// proxy URL.'; }
  return '';
}

export function parseImportDocument(text: string): unknown {
  if (new TextEncoder().encode(text).length > MAX_AUTH_BYTES) throw new Error('Choose a JSON file no larger than 2 MB.');
  let value: unknown;
  try { value = JSON.parse(text); if (typeof value === 'string') value = JSON.parse(value); }
  catch { throw new Error('Enter valid credential JSON.'); }
  if (!value || typeof value !== 'object') throw new Error('Credential JSON must contain an object or account array.');
  // The helper consumes an accounts container; accept a pasted root array by
  // normalizing it before we display indices or send the document.
  return Array.isArray(value) ? { accounts: value } : value;
}

export function importAccountOptions(text: string) {
  if (!text.trim()) return [];
  const value = parseImportDocument(text);
  const object = value as Record<string, unknown>;
  const credential = object.credential && typeof object.credential === 'object' ? object.credential as Record<string, unknown> : {};
  const content = credential.export || object.export || object.auth || object;
  const accounts = content && typeof content === 'object' ? (content as Record<string, unknown>).accounts : undefined;
  const items = Array.isArray(accounts) ? accounts : [];
  return items.map((item, index) => {
    const account = item && typeof item === 'object' ? item as Record<string, unknown> : {};
    const label = typeof account.email === 'string' ? account.email : typeof account.account === 'string' ? account.account : typeof account.name === 'string' ? account.name : t('Account {{index}}', { index: index + 1 });
    return { value: String(index), label: `${index + 1} · ${label}` };
  });
}

export function authEditorValue(auth: Record<string, unknown>) {
  return JSON.stringify(Object.fromEntries([...authFields.map(key => [key, auth[key] ?? (key === 'claude_device_ids' ? [] : '')]), ...Object.entries(auth)]), null, 2);
}

export function completionPatch(original: Record<string, unknown>, text: string, requireComplete = true) {
  let auth: Record<string, unknown>;
  if (requireComplete) auth = parseAuthJSON(text);
  else {
    if (new TextEncoder().encode(text).length > MAX_AUTH_BYTES) throw new Error('Auth JSON must be 2 MB or smaller.');
    let parsed: unknown;
    try { parsed = JSON.parse(text); if (typeof parsed === 'string') parsed = JSON.parse(parsed); }
    catch { throw new Error('Enter a valid JSON object.'); }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('Auth JSON must contain an object.');
    auth = parsed as Record<string, unknown>;
    for (const key of authFields) {
      if (auth[key] === undefined) continue;
      if (key === 'claude_device_ids' ? !Array.isArray(auth[key]) || (auth[key] as unknown[]).some(value => typeof value !== 'string') : typeof auth[key] !== 'string') throw new AuthFieldError('{{field}} has an invalid value.', { field: key });
    }
  }
  const patch: Record<string, unknown> = {};
  if (JSON.stringify(original.disabled) !== JSON.stringify(auth.disabled)) throw new Error('The disabled flag is managed by the server.');
  for (const key of authFields) {
    if (JSON.stringify(original[key]) === JSON.stringify(auth[key])) continue;
    if (auth[key] === undefined && (original[key] === undefined || original[key] === null || original[key] === '')) continue;
    if (key === 'proxy_url' || key === 'verification_status') throw new Error('Proxy verification is managed by the server. Generate a new flow to change the proxy.');
    const existing = original[key];
    if (existing !== undefined && existing !== null && existing !== '' && !(Array.isArray(existing) && !existing.length)) throw new AuthFieldError('The authorized {{field}} cannot be replaced. Complete missing fields only.', { field: key });
    patch[key] = auth[key];
  }
  return { auth, patch };
}

export function emailDiffers(declared: string, authorized: unknown) {
  return Boolean(declared.trim() && typeof authorized === 'string' && declared.trim().toLowerCase() !== authorized.trim().toLowerCase());
}

const preferenceKeys = ['plan', 'register_location', 'register_time', 'plan_start_time', 'plan_end_time', 'payment_method', 'register_device', 'balance', 'recharge_time', 'warranty_period', 'tags'] as const;
export type FlowMetadata = {
  version: 1;
  flow_id: string;
  expires_at: string;
  method: CredentialMethod;
  goal: CredentialGoal;
  setup_kind: string;
  vendor_id: string;
  protocol_version_id: string;
  client_request_key: string;
  preferences: Partial<AccountDraft>;
};
export function flowMetadata(flow: CredentialFlow, options: Omit<FlowMetadata, 'version' | 'flow_id' | 'expires_at' | 'preferences'>, draft: AccountDraft): FlowMetadata {
  return { version: 1, flow_id: flow.flow_id, expires_at: flow.expires_at, ...options, preferences: Object.fromEntries(preferenceKeys.map(key => [key, draft[key]])) };
}
export function readFlowMetadata(value: string | null, now = Date.now()): FlowMetadata | null {
  if (!value) return null;
  try {
    const data = JSON.parse(value) as FlowMetadata;
    if (data.version !== 1 || typeof data.flow_id !== 'string' || !data.flow_id || !Number.isFinite(Date.parse(data.expires_at)) || Date.parse(data.expires_at) <= now || !['oauth', 'setup', 'json', 'cookie', 'token'].includes(data.method) || !['submit', 'export'].includes(data.goal)) return null;
    // Rebuild the allowlist even when browser storage has been modified.
    return { version: 1, flow_id: data.flow_id, expires_at: data.expires_at, method: data.method, goal: data.goal, setup_kind: data.setup_kind === 'official' ? 'official' : 'full', vendor_id: typeof data.vendor_id === 'string' ? data.vendor_id : '', protocol_version_id: typeof data.protocol_version_id === 'string' ? data.protocol_version_id : '', client_request_key: typeof data.client_request_key === 'string' ? data.client_request_key : '', preferences: Object.fromEntries(preferenceKeys.filter(key => typeof data.preferences?.[key] === 'string').map(key => [key, data.preferences[key]])) };
  } catch { return null; }
}
