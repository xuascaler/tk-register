import assert from 'node:assert/strict';
import test from 'node:test';
import { formatCost, formatTokens, formatPercent, trendSegments } from '../src/accounts/helpers.ts';
import type { AccountSnapshot } from '../src/accounts/types.ts';

function sample(hour: number, value: number | null, currency: string | null = 'CNY'): AccountSnapshot {
  return { id: String(hour), account_id: '1', snapshot_date: '2026-11-01', collected_at: new Date(Date.UTC(2026, 10, 1, hour)).toISOString(), source_updated_at: null, account_status: 'active', input_tokens_total: value, output_tokens_total: value, cost_total: value === null ? null : value * 7.2, currency, cost_total_usd: currency === 'CNY' ? value : null, exchange_rate_cny_per_usd: 7.2, usage_5h_percent: null, reset_5h_at: null, usage_7d_percent: null, reset_7d_at: null, data_source: 'website' };
}

test('missing values are distinct from genuine zero usage and unknown currency', () => {
  assert.equal(formatCost(null, 'USD'), '—');
  assert.equal(formatCost(123, null), '—');
  assert.match(formatCost(0, 'USD'), /^0\.00 \$$/);
  assert.equal(formatTokens(null), '—');
  assert.equal(formatTokens(0), '0');
  assert.equal(formatPercent(null), '—');
  assert.equal(formatPercent(0), '0%');
});
test('trends leave a gap for missing data and missed collection intervals', () => {
  const parts = trendSegments([sample(0, 1), sample(8, null), sample(16, 3), sample(40, 5)], 'cost_total_usd', 8 * 3600);
  assert.deepEqual(parts.map(p => p.map(v => v.value)), [[1], [3], [5]]);
});
test('spend trends keep counter resets and unavailable conversions as gaps', () => {
  const parts = trendSegments([sample(0, 10), sample(8, 20), sample(16, 2), sample(24, 3, 'EUR'), sample(32, 4)], 'cost_total_usd', 8 * 3600);
  assert.deepEqual(parts.map(p => p.map(v => v.value)), [[10, 20], [2], [4]]);
});
test('spend trends use server USD values without converting or rounding again', () => {
  const row = { ...sample(0, 0), cost_total: 6800.24, cost_total_usd: 944.4777777777778 };
  const points = trendSegments([row], 'cost_total_usd', 2 * 3600).flat();
  assert.equal(points[0].value, row.cost_total_usd);
  assert.match(formatCost(points[0].value, 'USD'), /^944\.48 \$$/);
  assert.equal(row.cost_total, 6800.24);
});
test('a zero dollar observation remains in the trend while missing USD is a gap', () => {
  const missing = { ...sample(2, 100), cost_total_usd: null };
  const parts = trendSegments([sample(0, 0), missing, sample(4, 10)], 'cost_total_usd', 2 * 3600);
  assert.deepEqual(parts.map(p => p.map(v => v.value)), [[0], [10]]);
  assert.match(formatCost(parts[0][0].value, 'USD'), /^0\.00 \$$/);
  assert.equal(formatCost(missing.cost_total_usd, 'USD'), '—');
});
test('trend timestamps are sorted without changing input snapshots', () => {
  const rows = [sample(8, 2), sample(0, 1)];
  assert.deepEqual(trendSegments(rows, 'input_tokens_total', 8 * 3600)[0].map(p => p.value), [1, 2]);
  assert.equal(rows[0].id, '8');
});
