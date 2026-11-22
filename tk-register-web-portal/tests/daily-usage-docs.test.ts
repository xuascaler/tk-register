import assert from 'node:assert/strict';
import test from 'node:test';
import { curlExample, dailyUsageErrors, dailyUsageFields, dailyUsageResponse, docTopics, endpoints, sampleDailyUsage } from '../src/docs/data.ts';

test('daily usage documentation uses the batch read contract and personal-token scope', () => {
  assert.ok(docTopics.some(topic => topic.id === 'daily-usage'));
  assert.equal(endpoints['daily-usage'].method, 'POST');
  assert.equal(endpoints['daily-usage'].scope, 'accounts:read');
  assert.equal(endpoints['daily-usage'].limit, '60 requests / minute');
  assert.deepEqual(dailyUsageFields.map(field => field.name), ['username', 'accounts', 'date']);
  const example = curlExample('daily-usage', 'https://tk.example.test/tk-register/api/v1');
  assert.ok(example.includes('https://tk.example.test/tk-register/api/v1/vendors/tk-register-0/claude/accounts/daily-usage'));
  const body = example.split("<<'JSON'\n")[1].split('\nJSON')[0];
  assert.deepEqual(JSON.parse(body), sampleDailyUsage);
  assert.ok(example.includes('Authorization: Bearer ${TK_REGISTER_API_TOKEN}'));
  assert.ok(example.includes('Content-Type: application/json'));
});

test('daily usage examples omit model fields by default and distinguish missing bills from zero', () => {
  const { data } = dailyUsageResponse(7.2);
  assert.equal(data.model_costs_enabled, false);
  assert.equal(Object.hasOwn(data, 'model_totals'), false);
  assert.ok(data.accounts.every(account => !Object.hasOwn(account, 'models')));
  assert.deepEqual(data.summary, { status: 'partial', requested_accounts: 4, available_accounts: 2, no_data_accounts: 1, not_found_accounts: 1, cost_cny: '72.00000000', cost_usd: '10.00' });
  assert.equal(data.accounts[1].cost_cny, '0.00000000');
  assert.equal(data.accounts[1].cost_usd, '0.00');
  for (const item of data.accounts.slice(2)) {
    assert.equal(item.cost_cny, null);
    assert.equal(item.cost_usd, null);
  }
});

test('daily usage model examples preserve decimal strings, the current rate, and USD sums', () => {
  const cents = (value: string) => Math.round(Number(value) * 100);
  for (const rate of [7.2, 8, 7.123456]) {
    const { data } = dailyUsageResponse(rate, true);
    assert.equal(data.exchange_rate_cny_per_usd, String(rate));
    assert.equal(data.model_costs_enabled, true);
    assert.equal(data.summary.cost_usd, (72 / rate).toFixed(2));
    for (const item of data.accounts) {
      assert.ok(Array.isArray(item.models));
      if (item.cost_usd === null) { assert.deepEqual(item.models, []); continue; }
      assert.match(item.cost_cny!, /^\d+\.\d{8}$/);
      assert.match(item.cost_usd, /^\d+\.\d{2}$/);
      assert.equal(item.models!.reduce((total, model) => total + cents(model.cost_usd), 0), cents(item.cost_usd));
    }
    assert.equal(data.model_totals!.reduce((total, model) => total + cents(model.cost_usd), 0), cents(data.summary.cost_usd));
    assert.doesNotMatch(JSON.stringify(data), /model_usage_source|fable_ratio|modified|raw|cache/);
  }
});

test('daily usage errors distinguish invalid dates, unavailable local services, and read timeouts', () => {
  for (const [code, status] of [['INVALID_DATE', '400'], ['REQUEST_TOO_LARGE', '413'], ['INVALID_CONTENT_TYPE', '415'], ['RATE_LIMITED', '429'], ['UNAVAILABLE', '503'], ['REQUEST_TIMEOUT', '504']]) {
    assert.equal(dailyUsageErrors.find(error => error.code === code)?.status, status);
  }
  assert.ok(dailyUsageErrors.find(error => error.code === 'INVALID_REQUEST')?.description.includes('before deduplication'));
});
