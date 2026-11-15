import assert from 'node:assert/strict';
import test from 'node:test';
import { lookupResponse, authJSONUpdateResponse, snapshotFields } from '../src/docs/data.ts';

test('documentation examples and field descriptions use the current rate', () => {
  for (const rate of [7.2, 8, 7.123456]) {
    for (const snapshot of [lookupResponse(rate).data.snapshot, authJSONUpdateResponse(rate).data.account.snapshot]) {
      assert.equal(snapshot.exchange_rate_cny_per_usd, rate);
      assert.equal(snapshot.cost_total_usd, snapshot.cost_total / rate);
    }
    const description = snapshotFields(rate).find(field => field.name === 'exchange_rate_cny_per_usd')?.description;
    assert.ok(description?.includes(`1 USD = ${rate} CNY`));
    assert.ok(!description?.includes('administrator'));
  }
});
