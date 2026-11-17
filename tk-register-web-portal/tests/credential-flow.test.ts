import assert from 'node:assert/strict';
import test from 'node:test';
import { authEditorValue, automaticDraft, completionPatch, emailDiffers, flowMetadata, importAccountOptions, parseImportDocument, readFlowMetadata, submissionPassword, validateProxy } from '../src/submissions/credential-flow.ts';

const auth = { id_token: '', access_token: 'fixture-access', refresh_token: 'fixture-refresh', last_refresh: '2026-11-18T00:00:00Z', email: 'authorized@example.com', account_uuid: 'account-id', organization_uuid: 'org-id', organization_name: 'Fixture org', claude_device_ids: ['device-id'], type: 'claude', expired: '2027-11-18T00:00:00Z', proxy_url: 'https://proxy.example:443', verification_status: 'verified' };

test('automatic defaults use Shanghai calendar dates and clamp the previous month', () => {
  const draft = automaticDraft(new Date('2026-03-30T18:00:00Z'));
  assert.equal(draft.register_time, '2026-02-28');
  assert.equal(draft.plan_start_time, '2026-03-31');
  assert.equal(draft.plan_end_time, '2026-04-30');
  assert.equal(draft.recharge_time, '2026-03-31');
  assert.deepEqual([draft.plan, draft.register_location, draft.payment_method, draft.register_device, draft.balance, draft.tags, draft.password], ['claude/cc-max-20x', 'US', 'physical_credit_card', 'Windows', '100', '速刷', '']);
  assert.equal(automaticDraft(new Date('2024-03-31T00:00:00Z')).register_time, '2024-02-29');
  assert.equal(automaticDraft(new Date('2027-01-31T00:00:00Z')).register_time, '2026-12-31');
});

test('submission passwords have 24 random characters and every required character class', () => {
  const values = Array.from({ length: 50 }, () => submissionPassword());
  assert.equal(new Set(values).size, values.length);
  for (const value of values) {
    assert.equal(value.length, 24);
    for (const pattern of [/[A-Z]/, /[a-z]/, /[0-9]/, /[!@#$%^&*]/]) assert.match(value, pattern);
  }
});

test('proxy validation requires a supported complete proxy URL', () => {
  for (const value of ['', 'proxy.example:443', 'file:///tmp/secret', 'javascript:alert(1)', 'https://']) assert.ok(validateProxy(value));
  for (const value of ['https://user:password@proxy.example:443', 'socks5://127.0.0.1:1080', 'socks5h://proxy.example:1080']) assert.equal(validateProxy(value), '');
});

test('JSON imports accept object/encoded object and expose multi-account selection indices', () => {
  assert.deepEqual(parseImportDocument(JSON.stringify(JSON.stringify(auth))), auth);
  assert.deepEqual(importAccountOptions(JSON.stringify({ accounts: [{ email: 'one@example.com' }, { account: 'two@example.com' }] })), [{ value: '0', label: '1 · one@example.com' }, { value: '1', label: '2 · two@example.com' }]);
  assert.deepEqual(importAccountOptions(JSON.stringify([{ name: 'First' }, {}])), [{ value: '0', label: '1 · First' }, { value: '1', label: '2 · Account 2' }]);
  assert.deepEqual(parseImportDocument('[{"email":"one@example.com"}]'), { accounts: [{ email: 'one@example.com' }] });
  assert.deepEqual(importAccountOptions(JSON.stringify({ credential: { export: { accounts: [{ email: 'nested@example.com' }] } } })), [{ value: '0', label: '1 · nested@example.com' }]);
  assert.deepEqual(importAccountOptions(JSON.stringify({ items: [{ email: 'unsupported@example.com' }] })), []);
  assert.deepEqual(importAccountOptions(JSON.stringify(auth)), []);
  assert.throws(() => parseImportDocument('invalid'), /valid credential JSON/);
  assert.throws(() => parseImportDocument('true'), /object or account array/);
  assert.throws(() => parseImportDocument(JSON.stringify({ text: '中'.repeat(700000) })), /2 MB/);
});

test('missing credential fields can be completed without replacing authorized values', () => {
  const incomplete: Record<string, unknown> = { ...auth, refresh_token: '', organization_name: '', claude_device_ids: [] };
  const value = completionPatch(incomplete, JSON.stringify(auth));
  assert.deepEqual(value.patch, { refresh_token: 'fixture-refresh', organization_name: 'Fixture org', claude_device_ids: ['device-id'] });
  assert.deepEqual(value.auth, auth);
  assert.deepEqual(completionPatch(auth, JSON.stringify(auth)).patch, {});
  assert.throws(() => completionPatch(auth, JSON.stringify({ ...auth, email: 'other@example.com' })), /cannot be replaced/);
  assert.throws(() => completionPatch(auth, JSON.stringify({ ...auth, access_token: 'different' })), /cannot be replaced/);
});

test('server proxy verification cannot be edited even when absent in an incomplete response', () => {
  assert.throws(() => completionPatch(auth, JSON.stringify({ ...auth, proxy_url: 'http://different.example' })), /managed by the server/);
  const incomplete = { ...auth } as Record<string, unknown>; delete incomplete.verification_status;
  assert.throws(() => completionPatch(incomplete, JSON.stringify(auth)), /managed by the server/);
  assert.throws(() => completionPatch({ ...auth, disabled: false }, JSON.stringify({ ...auth, disabled: true })), /disabled flag/);
});

test('auth editor adds missing standard keys and preserves the existing credential material', () => {
  const text = authEditorValue({ access_token: 'fixture-access', extra: true });
  const value = JSON.parse(text);
  assert.equal(value.access_token, 'fixture-access');
  assert.equal(value.refresh_token, '');
  assert.deepEqual(value.claude_device_ids, []);
  assert.equal(value.extra, true);
  assert.throws(() => completionPatch({ access_token: 'fixture-access' }, text), /refresh_token/);
});

test('export-only accepts partial standard credentials while submission still requires complete auth', () => {
  const partial = { ...auth, refresh_token: '', account_uuid: '', organization_uuid: '' };
  assert.deepEqual(completionPatch(partial, authEditorValue(partial), false).auth, partial);
  assert.throws(() => completionPatch(partial, authEditorValue(partial)), /refresh_token/);
  assert.throws(() => completionPatch(partial, '[]', false), /object/);
  assert.throws(() => completionPatch(partial, JSON.stringify({ ...partial, access_token: 'replacement' }), false), /cannot be replaced/);
  assert.throws(() => completionPatch(partial, JSON.stringify({ ...partial, proxy_url: 'http://different.example' }), false), /managed by the server/);
});

test('email confirmation compares declared and authorized emails without case sensitivity', () => {
  assert.equal(emailDiffers('', 'authorized@example.com'), false);
  assert.equal(emailDiffers(' AUTHORIZED@example.com ', 'authorized@example.com'), false);
  assert.equal(emailDiffers('declared@example.com', 'authorized@example.com'), true);
});

test('recoverable metadata excludes all credentials, proxy, declared email, password and remark', () => {
  const draft = { ...automaticDraft(), password: 'SECRET-password', account: 'SECRET-email', auth_json: 'SECRET-auth', remark: 'SECRET-remark' };
  const flow = { flow_id: 'flow-1', expires_at: '2027-11-18T00:00:00Z', auth, auth_url: 'https://claude.ai/oauth?SECRET-url' };
  const metadata = flowMetadata(flow, { method: 'oauth', goal: 'submit', setup_kind: 'full', vendor_id: '1', protocol_version_id: '2', client_request_key: 'request-key' }, draft);
  assert.equal(JSON.stringify(metadata).includes('SECRET'), false);
  assert.equal(JSON.stringify(metadata).includes('fixture-access'), false);
  assert.equal(JSON.stringify(metadata).includes('proxy.example'), false);
  const restored = readFlowMetadata(JSON.stringify({ ...metadata, password: 'SECRET', auth, preferences: { ...metadata.preferences, password: 'SECRET', auth_json: 'SECRET', account: 'SECRET', remark: 'SECRET' } }), Date.parse('2026-11-18T00:00:00Z'));
  assert.deepEqual(restored, metadata);
  assert.equal(readFlowMetadata(JSON.stringify(metadata), Date.parse('2028-01-01T00:00:00Z')), null);
  assert.equal(readFlowMetadata('not JSON'), null);
});
