import assert from 'node:assert/strict';
import { test } from 'node:test';
import { overviewTotals, overviewSeries, overviewModelCosts, overviewModels, overviewCosts, overviewQuery, validOverviewRange, type OverviewData } from '../src/accounts/overview';
const data: OverviewData = {
  start_date: '2026-11-12', end_date: '2026-11-15', earliest_date: '2026-01-01', timezone: 'Asia/Shanghai', collection_time: '03:00', exchange_rate_cny_per_usd: 8, latest_account_data: null,
  sources: [{ id: 'myself', label: 'myself', total: 2, statuses: { active: 1, login_failed: 1 }, cost_usd: 12, cost_known: 1, collected_days: 2 }, { id: '2', label: 'member@example.com', total: 3, statuses: { active: 2, sync_failed: 1 }, cost_usd: null, cost_known: 0, collected_days: 0 }],
  model_costs: [{ source_id: 'myself', model: 'sonnet', cost_cny: '80', cost_usd: 10 }],
  rows: [{ date: '2026-11-13', model: 'sonnet', input_tokens: '9007199254740993', output_tokens: '20', cache_read_tokens: '0', cache_write_tokens: '0', cost_cny: '80', cost_usd: 10 }],
  days: [{ date: '2026-11-12', status: 'missing', expected: 2, collected: 0 }, { date: '2026-11-13', status: 'partial', expected: 2, collected: 1 }, { date: '2026-11-14', status: 'complete', expected: 2, collected: 2 }, { date: '2026-11-15', status: 'partial', expected: 2, collected: 1 }],
};
test('overview shows successful accounts, zero bills and only gaps days with no successes', () => {
  const total = overviewTotals(data.sources);
  assert.equal(total.total, 5); assert.equal(total.statuses.active, 3); assert.equal(total.cost_usd, 12); assert.equal(total.cost_known, 1);
  assert.equal(overviewTotals([]).cost_usd, null);
  assert.equal(overviewModelCosts(data, data.sources[1], ['sonnet']), null);
  assert.equal(overviewModelCosts(data, total, []), null);
  assert.equal(overviewModelCosts(data, total, ['sonnet']), 10);
  const points = overviewSeries(data, ['sonnet', 'opus'], 'cost_usd');
  assert.deepEqual(points.map(p => p.values), [[null, null], [10, 0], [0, 0], [0, 0]]);
  assert.deepEqual(points.map(p => p.value), [null, 10, 0, 0]);
  const inputPoints = overviewSeries(data, ['sonnet', 'opus'], 'input_tokens');
  assert.deepEqual(inputPoints.map(p => p.values), [[null, null], [Number('9007199254740993'), 0], [0, 0], [0, 0]]);
  const withoutSuccess = { ...data, days: data.days.map(day => ({ ...day, collected: 0 })) };
  assert.deepEqual(overviewSeries(withoutSuccess, ['sonnet', 'opus'], 'cost_usd').map(p => p.values), [[null, null], [null, null], [null, null], [null, null]]);
});
test('overview permits long history and rejects incomplete, future or invalid calendar ranges', () => {
  const now = new Date('2026-11-15T16:01:00Z');
  assert.equal(validOverviewRange('2026-01-01','2026-11-15',now),true);
  for (const range of [['2026-02-30','2026-11-15'],['2026-11-16','2026-11-16'],['2026-11-15','2026-11-14'],['','']]) assert.equal(validOverviewRange(range[0],range[1],now),false);
});
test('overview shows aggregate zero without model rows and preserves explicit model deselection', () => {
  const emptyBills: OverviewData = { ...data, rows: [], model_costs: [] };
  for (const metric of ['cost_usd', 'input_tokens', 'output_tokens'] as const) {
    const points = overviewSeries(emptyBills, [], metric);
    assert.deepEqual(points.map(point => point.values), [[null], [0], [0], [0]]);
    assert.deepEqual(points.map(point => point.value), [null, 0, 0, 0]);
    assert.ok(points.every(point => point.rows.length === 0));
  }
  assert.equal(overviewModelCosts(emptyBills, emptyBills.sources[0], []), 0);
  assert.equal(overviewModelCosts(emptyBills, overviewTotals(emptyBills.sources), []), 0);
  assert.equal(overviewModelCosts(emptyBills, emptyBills.sources[1], []), null);
  const noSuccesses: OverviewData = { ...emptyBills, days: emptyBills.days.map(day => ({ ...day, collected: 0 })) };
  assert.deepEqual(overviewSeries(noSuccesses, [], 'cost_usd').map(point => point.value), [null, null, null, null]);
  assert.equal(overviewModelCosts(data, data.sources[0], []), null);
  assert.deepEqual(overviewSeries(data, [], 'cost_usd').map(point => point.value), [null, null, null, null]);
});

test('overview preset requests select cached periods explicitly, while custom ranges stay custom', () => {
  const range = { start: '2026-11-09', end: '2026-11-15' };
  for (const period of ['day', 'week', 'month', 'all_time'] as const) {
    for (const sources of ['all', 'myself,2']) {
      const query = overviewQuery(sources, period, range);
      assert.deepEqual(Object.fromEntries(query), { sources, models: 'all', period });
    }
  }
  assert.deepEqual(Object.fromEntries(overviewQuery('all', 'custom', range)), { sources: 'all', models: 'all', start_date: range.start, end_date: range.end });
});
test('unavailable summaries do not present old rows as model data or zero costs', () => {
  const unavailable = { ...data, summary_available: false };
  assert.deepEqual(overviewModels(unavailable), []);
  assert.equal(overviewModelCosts(unavailable, data.sources[0], ['sonnet']), null);
  assert.equal(overviewModelCosts(unavailable, overviewTotals(data.sources), []), null);
  for (const metric of ['cost_usd', 'input_tokens', 'output_tokens'] as const) {
    assert.deepEqual(overviewSeries(unavailable, ['sonnet'], metric), []);
  }
});
test('local model filtering leaves all-source status counts unchanged', () => {
  const before = overviewTotals(data.sources);
  overviewSeries(data, [], 'cost_usd');
  overviewModelCosts(data, before, []);
  assert.deepEqual(overviewTotals(data.sources), before);
  assert.equal(before.total, 5);
  assert.equal(before.cost_usd, 12);
});

test('overview uses one cent matrix for selected-period source, model and total costs', () => {
  const fixture: OverviewData = { ...data,
    sources: [
      { ...data.sources[0], cost_usd: 99999, collected_days: 2 },
      { ...data.sources[1], cost_usd: 88888, collected_days: 2 },
      { ...data.sources[1], id: '3', cost_usd: 77777, collected_days: 0 },
      { ...data.sources[1], id: '4', cost_usd: 66666, collected_days: 2 },
    ],
    model_costs: [
      { source_id: 'myself', model: 'sonnet', cost_cny: '0.072', cost_usd: 0.01 },
      { source_id: 'myself', model: 'opus', cost_cny: '0.144', cost_usd: 0.02 },
      { source_id: '2', model: 'sonnet', cost_cny: '0.72', cost_usd: 0.10 },
      { source_id: '2', model: 'opus', cost_cny: '1.44', cost_usd: 0.20 },
    ],
    rows: [],
  };
  assert.deepEqual(overviewModels(fixture), ['opus', 'sonnet'], 'Model selection also includes matrix models');
  const all = overviewCosts(fixture, null);
  assert.equal(all.total, 0.33);
  assert.deepEqual([...all.sources], [['myself', 0.03], ['2', 0.30], ['3', null], ['4', 0]]);
  assert.deepEqual([...all.models], [['opus', 0.22], ['sonnet', 0.11]]);
  assert.equal(all.cells.get('2')?.get('opus'), 0.20);
  assert.equal(all.cells.get('3')?.get('opus'), null);
  assert.equal(all.cells.get('4')?.get('opus'), 0);
  const sonnet = overviewCosts(fixture, ['sonnet']);
  assert.equal(sonnet.total, 0.11);
  assert.deepEqual([...sonnet.sources], [['myself', 0.01], ['2', 0.10], ['3', null], ['4', 0]]);
  assert.equal(overviewCosts(fixture, []).total, null);
  assert.ok([...overviewCosts(fixture, []).sources.values()].every(value => value === null));
  assert.equal(overviewCosts(fixture, ['missing-model']).total, null);
  assert.equal(overviewCosts({ ...fixture, summary_available: false }, null).total, null);
  assert.equal(overviewCosts({ ...fixture, model_costs: [] }, null).total, 0);
  assert.equal(overviewCosts({ ...fixture, model_costs: [] }, []).total, null);
  assert.equal(overviewCosts({ ...fixture, sources: fixture.sources.map(source => ({ ...source, collected_days: 0 })) }, null).total, null);
});
test('overview daily cost totals use cents while token metrics retain their original values', () => {
  const fixture: OverviewData = { ...data, rows: [
    { ...data.rows![0], model: 'sonnet', cost_usd: 0.10, input_tokens: '123' },
    { ...data.rows![0], model: 'opus', cost_usd: 0.20, input_tokens: '456' },
  ] };
  const cost = overviewSeries(fixture, ['sonnet', 'opus'], 'cost_usd');
  assert.equal(cost[1].value, 0.30);
  assert.equal(overviewSeries(fixture, ['sonnet', 'opus'], 'input_tokens')[1].value, 579);
});


test('disabled overview uses only backend totals and keeps exact token counters', () => {
  const total = { date: '2026-11-13', cost_cny: '2.376', cost_usd: 0.33, input_tokens: '9007199254740993', output_tokens: '44', cache_read_tokens: '0', cache_write_tokens: '0' };
  const disabled: OverviewData = { ...data, model_costs_enabled: false, total_cost_usd: 0.33, sources: [{ ...data.sources[0], cost_usd: 0.33 }], rows: undefined, model_costs: undefined, daily_totals: [total] };
  assert.deepEqual(overviewModels(disabled), []);
  const costs = overviewCosts(disabled, ['sonnet']);
  assert.equal(costs.total, 0.33); assert.equal(costs.sources.get('myself'), 0.33); assert.equal(costs.models.size, 0); assert.equal(costs.cells.size, 0);
  const points = overviewSeries(disabled, [], 'cost_usd');
  assert.deepEqual(points.map(point => point.value), [null, 0.33, 0, 0]);
  assert.equal(overviewSeries(disabled, [], 'input_tokens')[1].total?.input_tokens, '9007199254740993');
  assert.deepEqual(Object.fromEntries(overviewQuery('all', 'all_time', { start: '', end: '' }, false)), { sources: 'all', period: 'all_time' });
});

test('an unavailable bill leaves a gap in both cost and token charts', () => {
  const historical: OverviewData = {
    ...data, model_costs_enabled: false,
    days: [{ date: '2026-11-14', status: 'missing', expected: 1, collected: 0 }],
    daily_costs: [{ date: '2026-11-14', expected: 1, collected: 0, cost_usd: null }],
    daily_totals: [], rows: undefined, model_costs: undefined,
  };
  assert.deepEqual(overviewSeries(historical, [], 'cost_usd').map(point => point.value), [null]);
  assert.deepEqual(overviewSeries(historical, [], 'input_tokens').map(point => point.value), [null]);
  assert.deepEqual(overviewSeries({ ...historical, model_costs_enabled: true }, [], 'cost_usd').map(point => point.value), [null]);
});
