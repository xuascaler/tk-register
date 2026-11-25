import assert from 'node:assert/strict';
import test from 'node:test';
import { APIError } from '../src/api.ts';
import { setLanguage, t } from '../src/i18n/index.ts';
import { maskedProxyURL, proxyProblem, proxyUnknownMessage } from '../src/accounts/proxy-url.ts';

test('uncertain writes cannot be presented as a retryable update failure', () => {
  for (const error of [new APIError('timeout', 0), new APIError('bad response', 200), new APIError('gateway', 502), new APIError('new wording', 504, 'AUTH_JSON_UPDATE_UNCONFIRMED'), new APIError('new wording', 409, 'ACCOUNT_CREDENTIALS_UNCONFIRMED'), new APIError('unavailable', 503, 'UNAVAILABLE')]) {
    assert.equal(proxyProblem(error, true).kind, 'unknown');
    assert.equal(proxyProblem(error, true).message, proxyUnknownMessage);
  }
  assert.equal(proxyProblem(new APIError('offline', 0), false).kind, 'error');
  assert.equal(proxyProblem(new APIError('rejected', 502, 'AUTH_JSON_UPDATE_REJECTED'), true).kind, 'error');
  for (const [code, kind] of [['ACCOUNT_CREDENTIALS_MISSING', 'missing'], ['ACCOUNT_CREDENTIALS_CHANGED', 'conflict'], ['AUTH_JSON_NOT_EDITABLE', 'blocked']]) assert.equal(proxyProblem(new APIError('changed upstream wording', 409, code)).kind, kind);
});

test('proxy result masks credentials and omits query and path values', () => {
  assert.equal(maskedProxyURL('http://secret-user:secret-pass@proxy.example:8080/secret?token=secret#secret'), 'http://••••:••••@proxy.example:8080');
  assert.equal(maskedProxyURL('socks5h://[2001:db8::1]:1080'), 'socks5h://[2001:db8::1]:1080');
  assert.equal(maskedProxyURL('not-a-url'), '—');
});

test('approved unconfirmed-result copy is available in both languages', () => {
  try {
    setLanguage('zh-CN');
    assert.equal(t('Close'), '关闭');
    assert.equal(t('Unable to confirm the update result'), '暂时无法确认更新结果');
    assert.equal(t(proxyUnknownMessage), '未收到上游的明确响应，代理地址可能已经更新。此提示不代表更新失败，请勿立即重复提交。');
  } finally { setLanguage('en-US'); }
  assert.equal(t(proxyUnknownMessage), proxyUnknownMessage);
});
