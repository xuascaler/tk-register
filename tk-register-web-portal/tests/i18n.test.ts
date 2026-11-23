import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError, displayError, localizedError } from '../src/api.ts';
import { statusLabel, statusTone } from '../src/accounts/helpers.ts';
import { preferredLanguage, setLanguage, translateMessage, t } from '../src/i18n/index.ts';

test('a saved choice wins over the first supported browser language', () => {
  assert.equal(preferredLanguage('en-US', ['zh-CN']), 'en-US');
  assert.equal(preferredLanguage(null, ['fr-FR', 'zh-Hans', 'en-US']), 'zh-CN');
  assert.equal(preferredLanguage('unsupported', ['en-GB', 'zh-CN']), 'en-US');
  assert.equal(preferredLanguage(null, ['ja-JP']), 'en-US');
});

test('switching languages translates labels and parameters without changing status semantics', () => {
  try {
    setLanguage('zh-CN');
    assert.equal(t('Resend in {{seconds}}s', { seconds: 12 }), '12 秒后可重发');
    assert.equal(translateMessage('Enter 42 or less.'), '请输入小于或等于 42 的数值。');
    for (const [code, label] of Object.entries({
      active: '使用中', pending: '处理中', offline: '已下线', banned: '已封禁', sync_failed: '同步失败',
      login_failed: '登录失败', re_login_failed: '重新登录失败', bound_failed: '绑定失败', cooking_failed: '设置失败', unknown: '未知',
    })) assert.equal(statusLabel(code), label, code);
    assert.equal(statusTone('active'), 'positive');
    assert.equal(statusTone('banned'), 'negative');
  } finally {
    setLanguage('en-US');
  }
  assert.equal(statusLabel('active'), 'Active');
});

test('known error codes survive wording changes and unmapped errors keep their English detail', () => {
  const known = new APIError('Server wording has changed.', 400, 'PASSWORD_UNCHANGED');
  const unknown = new APIError('A newly added upstream error.', 409, 'NEW_ERROR');
  try {
    setLanguage('zh-CN');
    assert.equal(localizedError(displayError(known)), '新密码须与当前密码不同。');
    assert.equal(localizedError(displayError(unknown)), unknown.message);
    assert.match(localizedError(new APIError('Query quota reached.', 429, 'SUPPLIER_RATE_LIMITED', 23, 'accounts')), /账号查询。请在 23 秒后重试/);
  } finally {
    setLanguage('en-US');
  }
  assert.equal(localizedError(known), known.message);
});
