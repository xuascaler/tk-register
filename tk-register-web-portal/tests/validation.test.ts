import assert from 'node:assert/strict';
import test from 'node:test';
import {
  passwordWithinLimit,
  readableDuration,
  settingsPayload,
  validCode,
  validEmail,
  validateSetting,
} from '../src/validation.ts';
import type { Setting } from '../src/api.ts';

test('registration accepts email usernames up to 50 characters and alphanumeric codes', () => {
  assert.equal(validEmail('member@example.com'), true);
  assert.equal(validEmail(`${'a'.repeat(38)}@example.com`), true);
  assert.equal(validEmail(`${'a'.repeat(39)}@example.com`), false);
  for (const invalid of [
    'username',
    'user @example.com',
    'user@@example.com',
    'user@example',
  ])
    assert.equal(validEmail(invalid), false);
  for (const code of ['A1B2C3', '002be3', 'abcDEF', '123456'])
    assert.equal(validCode(code), true);
  for (const code of ['12345', '1234567', '12&abc', '123 56'])
    assert.equal(validCode(code), false);
});
test('settings payload preserves booleans and sends only changed values, omitting unchanged secrets', () => {
  const settings: Setting[] = [
    {
      key: 'registration_enabled',
      category: 'access',
      type: 'boolean',
      value: 'true',
      description: '',
    },
    {
      key: 'smtp_password',
      category: 'email',
      type: 'secret',
      secret: true,
      value: '',
      configured: true,
      description: '',
    },
    {
      key: 'email_send_cooldown_seconds',
      category: 'verification',
      type: 'integer',
      value: '60',
      description: '',
    },
    {
      key: 'smtp_host',
      category: 'email',
      type: 'string',
      value: 'smtp.example.com',
      description: '',
    },
  ];
  assert.deepEqual(
    settingsPayload(settings, {
      registration_enabled: 'false',
      smtp_password: '',
      email_send_cooldown_seconds: '120',
      smtp_host: 'smtp.example.com',
    }),
    { registration_enabled: 'false', email_send_cooldown_seconds: '120' },
  );
  assert.deepEqual(
    settingsPayload([settings[1]], { smtp_password: 'replacement' }),
    { smtp_password: 'replacement' },
  );
});
test('configuration validation checks integer boundaries without converting blanks to zero', () => {
  const port: Setting = {
    key: 'smtp_port',
    value: 587,
    category: 'email',
    type: 'integer',
    min: 1,
    max: 65535,
    description: '',
  };
  for (const invalid of ['', ' ', '-1', '3.5', '65536', '1e99'])
    assert.notEqual(validateSetting(port, invalid), '');
  for (const valid of [1, 587, '65535'])
    assert.equal(validateSetting(port, valid), '');
  assert.equal(readableDuration(600), '10 minutes');
  assert.equal(readableDuration(28800), '8 hours');
  assert.equal(readableDuration(604800), '7 days');
});

test('password length respects the backend UTF-8 byte limit', () => {
  assert.equal(passwordWithinLimit('a'.repeat(72)), true);
  assert.equal(passwordWithinLimit('a'.repeat(73)), false);
  assert.equal(passwordWithinLimit('密'.repeat(24)), true);
  assert.equal(passwordWithinLimit('密'.repeat(25)), false);
});
