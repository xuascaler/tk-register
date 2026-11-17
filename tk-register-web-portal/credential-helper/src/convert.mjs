import { randomBytes } from 'node:crypto';

export const AUTH_FIELDS = [
  'access_token', 'account_uuid', 'claude_device_ids', 'disabled', 'email',
  'expired', 'id_token', 'last_refresh', 'organization_name', 'organization_uuid',
  'refresh_token', 'type', 'proxy_url', 'verification_status',
];

const object = (value) => value && typeof value === 'object' && !Array.isArray(value) ? value : {};
const first = (...values) => values.find((value) => typeof value === 'string' && value.trim())?.trim() || '';

function packageProxy(value) {
  const proxy = object(value);
  if (proxy.kind === 'local' || !proxy.host || !proxy.port) return '';
  if (!['socks5', 'socks5h', 'http', 'https'].includes(proxy.kind)) return '';
  const host = String(proxy.host);
  const address = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host;
  try {
    const url = new URL(`${proxy.kind}://${address}:${proxy.port}`);
    if (proxy.username) url.username = String(proxy.username);
    if (proxy.password) url.password = String(proxy.password);
    return url.toString().replace(/\/$/, '');
  } catch { return ''; }
}

export function beijingTime(value) {
  if (value == null || value === '') return '';
  let milliseconds;
  if (typeof value === 'number' || /^\d+(\.\d+)?$/.test(String(value))) {
    const number = Number(value);
    milliseconds = number < 1e12 ? number * 1000 : number;
  } else milliseconds = Date.parse(String(value));
  if (!Number.isFinite(milliseconds)) return '';
  const date = new Date(milliseconds + 8 * 60 * 60 * 1000);
  return Number.isNaN(date.getTime()) ? '' : `${date.toISOString().slice(0, 19)}+08:00`;
}

export function convertDocument(document, options = {}) {
  if (typeof document === 'string') {
    try { document = JSON.parse(document); }
    catch { throw Object.assign(new Error('JSON 格式有误，请检查后重试。'), { status: 400 }); }
  }
  if (!document || typeof document !== 'object' || Array.isArray(document)) {
    throw Object.assign(new Error('请提供一个账号 JSON 对象。'), { status: 400 });
  }
  const original = document;
  document = object(document.credential).export || document.export || document.auth || document;
  const accounts = document.accounts;
  const index = Number(options.account_index ?? 0);
  if (accounts && (!Array.isArray(accounts) || !Number.isInteger(index) || !accounts[index])) {
    throw Object.assign(new Error('所选账号不存在，请检查 JSON 中的 accounts。'), { status: 400 });
  }
  const record = accounts ? accounts[index] : document;
  const credential = object(record.credentials || record.claudeAiOauth || record.claude_ai_oauth || record);
  const extra = object(record.extra);
  const account = object(credential.account);
  const organization = object(credential.organization);
  const profile = object(credential.oauth_account);
  const notes = [];
  const access = first(credential.access_token, credential.accessToken);
  const refresh = first(credential.refresh_token, credential.refreshToken);
  if (!access && !refresh) {
    throw Object.assign(new Error('没有找到 access_token 或 refresh_token。'), { status: 400 });
  }
  const email = first(credential.email, credential.email_address, credential.emailAddress,
    account.email, account.email_address, profile.account_email, extra.email_address, extra.email, record.email);
  const accountId = first(credential.account_uuid, credential.accountUuid, account.uuid,
    profile.account_uuid, extra.account_uuid, record.account_uuid);
  const organizationId = first(credential.organization_uuid, credential.org_uuid, credential.orgUuid,
    organization.uuid, profile.organization_uuid, extra.org_uuid, extra.organization_uuid, record.org_uuid);
  let organizationName = first(credential.organization_name, credential.organizationName,
    organization.name, extra.organization_name, record.organization_name);
  if (!organizationName && email) {
    organizationName = `${email}'s Organization`;
    notes.push('organization_name was derived from the email address; the actual organization name was not confirmed by the upstream.');
  }
  const expired = beijingTime(credential.expired || credential.expires_at || credential.expiresAt);
  if (!expired) notes.push('The credential contains no valid expiry. Supply the actual expired value before submission.');
  let lastRefresh = beijingTime(credential.last_refresh || credential.lastRefresh || credential.refreshed_at);
  if (!lastRefresh) {
    const exported = document.exported_at || original.exported_at;
    lastRefresh = beijingTime(exported) || beijingTime(options.now ?? Date.now());
    notes.push(exported
      ? 'last_refresh uses the file export time; the actual refresh time is unknown.'
      : 'last_refresh uses the conversion time; the actual refresh time is unknown.');
  }
  let devices = credential.claude_device_ids || credential.deviceIds || record.claude_device_ids;
  devices = Array.isArray(devices) ? devices.filter((id) => typeof id === 'string' && id.trim()) : [];
  const identityKey = accountId ? `account:${accountId}` : email ? `email:${email}` : null;
  if (!devices.length) {
    devices = options.deviceStore?.get(identityKey) || [randomBytes(32).toString('hex').slice(0, 63)];
    notes.push('claude_device_ids contains a generated template identifier, retained for this authorization.');
  }
  if (identityKey) options.deviceStore?.set(identityKey, devices);
  const importedProxy = first(credential.proxy_url, record.proxy_url, original.proxy_url) || packageProxy(original.proxy);
  const proxy = Object.hasOwn(options, 'proxy_url') ? options.proxy_url : importedProxy;
  const auth = {
    access_token: access,
    account_uuid: accountId,
    claude_device_ids: devices,
    disabled: typeof credential.disabled === 'boolean' ? credential.disabled : false,
    email,
    expired,
    id_token: first(credential.id_token, credential.idToken),
    last_refresh: lastRefresh,
    organization_name: organizationName,
    organization_uuid: organizationId,
    refresh_token: refresh,
    type: 'claude',
    proxy_url: typeof proxy === 'string' ? proxy : '',
    verification_status: 'verified',
  };
  if (!refresh) notes.push('The credential contains no refresh_token; it remains empty.');
  if (!email || !accountId || !organizationId) notes.push('Some account identity fields are missing. Supply their actual values before submission.');
  const name = (email || accountId || 'claude').replace(/[^a-zA-Z0-9@._-]/g, '_').slice(0, 100);
  return { auth, notes, filename: `auth-${name}.json` };
}
