import assert from 'node:assert/strict';
import { test } from 'node:test';
import { overviewTotals, overviewSeries, overviewModelCosts, validOverviewRange, type OverviewData } from '../src/accounts/overview';
const data: OverviewData = {
  start_date: '2026-11-12', end_date: '2026-11-15', earliest_date: '2026-01-01', timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 8, latest_account_data: null,
  sources: [{ id: 'myself', label: 'myself', total: 2, statuses: { active: 1, login_failed: 1 }, cost_usd: 12, cost_known: 1, collected_days: 2 }, { id: '2', label: 'member@example.com', total: 3, statuses: { active: 2, sync_failed: 1 }, cost_usd: null, cost_known: 0, collected_days: 0 }],
  model_costs: [{ source_id: 'myself', model: 'sonnet', cost_cny: '80', cost_usd: 10 }],
  rows: [{ date: '2026-11-13', model: 'sonnet', input_tokens: '9007199254740993', output_tokens: '20', cache_read_tokens: '0', cache_write_tokens: '0', cost_cny: '80', cost_usd: 10 }],
  days: [{ date: '2026-11-12', status: 'missing', expected: 2, collected: 0 }, { date: '2026-11-13', status: 'partial', expected: 2, collected: 1 }, { date: '2026-11-14', status: 'complete', expected: 2, collected: 2 }, { date: '2026-11-15', status: 'partial', expected: 2, collected: 1 }],
};
test('overview distinguishes known zero, partial totals, absent data and unselected sources', () => {
  const total = overviewTotals(data.sources);
  assert.equal(total.total, 5); assert.equal(total.statuses.active, 3); assert.equal(total.cost_usd, 12); assert.equal(total.cost_known, 1);
  assert.equal(overviewTotals([]).cost_usd, null);
  assert.equal(overviewModelCosts(data, data.sources[1], ['sonnet']), null);
  assert.equal(overviewModelCosts(data, total, []), null);
  assert.equal(overviewModelCosts(data, total, ['sonnet']), 10);
  const points = overviewSeries(data, ['sonnet', 'opus'], 'cost_usd');
  assert.deepEqual(points.map(p => p.values), [[null, null], [10, null], [0, 0], [null, null]]);
  assert.deepEqual(points.map(p => p.value), [null, 10, 0, null]);
});
test('overview permits long history and rejects incomplete, future or invalid calendar ranges', () => {
  const now = new Date('2026-11-15T16:01:00Z');
  assert.equal(validOverviewRange('2026-01-01','2026-11-15',now),true);
  for (const range of [['2026-02-30','2026-11-15'],['2026-11-16','2026-11-16'],['2026-11-15','2026-11-14'],['','']]) assert.equal(validOverviewRange(range[0],range[1],now),false);
});
