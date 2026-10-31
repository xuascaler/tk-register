import assert from 'node:assert/strict';
import test from 'node:test';
import { formatCost, formatTokens, formatPercent, trendSegments } from '../src/accounts/helpers.ts';
import type { AccountSnapshot } from '../src/accounts/types.ts';

function sample(hour: number, value: number | null, currency: string | null = 'CNY'): AccountSnapshot {
  return { id: String(hour), account_id: '1', snapshot_date: '2026-11-01', collected_at: new Date(Date.UTC(2026, 10, 1, hour)).toISOString(), source_updated_at: null, account_status: 'active', input_tokens_total: value, output_tokens_total: value, cost_total: value, currency, usage_5h_percent: null, reset_5h_at: null, usage_7d_percent: null, reset_7d_at: null, data_source: 'website' };
}

test('missing values are distinct from genuine zero usage and unknown currency', () => {
  assert.equal(formatCost(null, 'CNY'), '—');
  assert.equal(formatCost(123, null), '—');
  assert.match(formatCost(0, 'CNY'), /0\.00/);
  assert.equal(formatTokens(null), '—');
  assert.equal(formatTokens(0), '0');
  assert.equal(formatPercent(null), '—');
  assert.equal(formatPercent(0), '0%');
});
test('trends leave a gap for missing data and missed collection intervals', () => {
  const parts = trendSegments([sample(0, 1), sample(8, null), sample(16, 3), sample(40, 5)], 'cost_total', 8);
  assert.deepEqual(parts.map(p => p.map(v => v.value)), [[1], [3], [5]]);
});
test('trends never join different currencies or counter resets', () => {
  const parts = trendSegments([sample(0, 10), sample(8, 20), sample(16, 2), sample(24, 3, 'USD')], 'cost_total', 8);
  assert.deepEqual(parts.map(p => p.map(v => v.value)), [[10, 20], [2], [3]]);
});
test('trend timestamps are sorted without changing input snapshots', () => {
  const rows = [sample(8, 2), sample(0, 1)];
  assert.deepEqual(trendSegments(rows, 'input_tokens_total', 8)[0].map(p => p.value), [1, 2]);
  assert.equal(rows[0].id, '8');
});
