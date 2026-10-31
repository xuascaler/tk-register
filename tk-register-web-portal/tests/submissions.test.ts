import assert from 'node:assert/strict';
import test from 'node:test';
import {
  authFields,
  billRange,
  emptyDraft,
  MAX_AUTH_BYTES,
  parseAuthJSON,
  registrationLocations,
  submissionParameters,
  validateDraft,
  validDate,
  vendorTimestamp,
} from '../src/submissions/validation.ts';

const auth = {
  id_token: '',
  access_token: 'test-access',
  refresh_token: 'test-refresh',
  last_refresh: '2026-10-24T10:00:00+08:00',
  email: 'account@example.com',
  account_uuid: 'account-uuid',
  organization_uuid: 'organization-uuid',
  organization_name: 'Test',
  claude_device_ids: ['device-1'],
  type: 'claude',
  expired: '2026-11-24T10:00:00Z',
  proxy_url: 'socks5h://127.0.0.1:1080',
  verification_status: 'verified',
};
function validDraft() {
  return {
    ...emptyDraft(),
    account: 'account@example.com',
    password: 'test-only-password',
    auth_json: JSON.stringify(auth),
    register_time: '2026-10-24',
    plan_start_time: '2026-10-24',
    plan_end_time: '2026-11-24',
  };
}

test('submission accepts both object JSON and a JSON-encoded object string', () => {
  assert.deepEqual(parseAuthJSON(JSON.stringify(auth)), auth);
  assert.deepEqual(parseAuthJSON(JSON.stringify(JSON.stringify(auth))), auth);
  assert.deepEqual(validateDraft(validDraft()), {});
});
test('every required auth property must be present, while id_token may be empty', () => {
  for (const key of authFields) {
    const value = { ...auth } as Record<string, unknown>;
    delete value[key];
    assert.throws(() => parseAuthJSON(JSON.stringify(value)), /Missing fields/);
  }
  assert.throws(
    () => parseAuthJSON(JSON.stringify({ ...auth, access_token: '' })),
    /access_token/,
  );
  assert.throws(
    () => parseAuthJSON(JSON.stringify({ ...auth, claude_device_ids: [] })),
    /claude_device_ids/,
  );
  assert.throws(
    () => parseAuthJSON(JSON.stringify({ ...auth, claude_device_ids: [null] })),
    /claude_device_ids/,
  );
  assert.throws(() => parseAuthJSON('[]'), /object/);
});
test('auth validation rejects invalid timestamps, proxy protocol and oversize UTF-8 input', () => {
  for (const timestamp of [
    '2026-10-24',
    '2026-02-31T10:00:00Z',
    '2026-10-24T99:00:00Z',
    '2026-10-24T10:00:00',
  ])
    assert.throws(
      () => parseAuthJSON(JSON.stringify({ ...auth, last_refresh: timestamp })),
      /RFC 3339/,
    );
  assert.throws(
    () =>
      parseAuthJSON(JSON.stringify({ ...auth, proxy_url: 'file:///tmp/key' })),
    /proxy_url/,
  );
  assert.throws(
    () => parseAuthJSON('中'.repeat(Math.ceil(MAX_AUTH_BYTES / 3) + 1)),
    /2 MB/,
  );
});
test('submission dates, whitespace, optional enumerations and percentage are validated', () => {
  assert.equal(validDate('2026-02-29'), false);
  assert.equal(validDate('2024-02-29'), true);
  assert.ok(
    validateDraft({ ...validDraft(), account: 'account @example.com' }).account,
  );
  assert.ok(
    validateDraft({ ...validDraft(), plan_end_time: '2026-10-20' })
      .plan_end_time,
  );
  for (const balance of ['101', '-1', 'NaN', '1e2', ' 0 '])
    assert.ok(validateDraft({ ...validDraft(), balance }).balance);
  for (const balance of ['0', '100', '99.9', ''])
    assert.equal(
      validateDraft({ ...validDraft(), balance }).balance,
      undefined,
    );
  assert.ok(
    validateDraft({ ...validDraft(), warranty_period: '2' }).warranty_period,
  );
  assert.ok(
    validateDraft({ ...validDraft(), payment_method: 'third_party_payment"' })
      .payment_method,
  );
  assert.ok(
    validateDraft({ ...validDraft(), register_location: 'CN' })
      .register_location,
  );
});
test('registration locations accept all five supported countries and preserve the country code', () => {
  assert.deepEqual(registrationLocations, [
    { value: 'US', label: 'United States' },
    { value: 'MY', label: 'Malaysia' },
    { value: 'ID', label: 'Indonesia' },
    { value: 'SG', label: 'Singapore' },
    { value: 'PH', label: 'Philippines' },
  ]);
  for (const { value: register_location } of registrationLocations) {
    const draft = { ...validDraft(), register_location };
    assert.deepEqual(validateDraft(draft), {});
    assert.equal(submissionParameters(draft).register_location, register_location);
  }
  for (const register_location of ['', 'CN', 'us', 'USA', 'Malaysia', ' US ']) {
    assert.ok(validateDraft({ ...validDraft(), register_location }).register_location);
  }
});
test('payload omits unfilled optional fields, preserves zero and sends tags as an array', () => {
  const parameters = submissionParameters(validDraft());
  assert.equal('balance' in parameters, false);
  assert.equal('tags' in parameters, false);
  assert.deepEqual(parameters.auth_json, auth);
  const optional = submissionParameters({
    ...validDraft(),
    balance: '0',
    warranty_period: '7',
    payment_method: 'third_party_payment',
    tags: '速刷',
  });
  assert.equal(optional.balance, 0);
  assert.equal(optional.warranty_period, 7);
  assert.deepEqual(optional.tags, ['速刷']);
  assert.equal(optional.payment_method, 'third_party_payment');
});
test('query dates follow platform timezone rather than the browser timezone', () => {
  const now = new Date('2026-10-24T18:30:45Z');
  assert.equal(vendorTimestamp(now, 'Asia/Shanghai'), '2026-10-25 02:30:45');
  assert.deepEqual(billRange(7, 'Asia/Shanghai', now), {
    start_ds: '2026-10-19',
    end_ds: '2026-10-25',
  });
  assert.deepEqual(billRange(7, 'America/Los_Angeles', now), {
    start_ds: '2026-10-18',
    end_ds: '2026-10-24',
  });
});
