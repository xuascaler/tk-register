import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recentDailyRange, validDailyRange, dailyModels, dailyTotals, dailySeries, filterDailyRows, type DailyData } from '../src/accounts/daily-usage';
const data: DailyData = { start_date: '2026-11-13', end_date: '2026-11-14', timezone: 'Asia/Shanghai', collection_time: '03:00', currency: 'CNY', exchange_rate_cny_per_usd: 8, days: [{ date: '2026-11-13', status: 'missing', collected_at: null }, { date: '2026-11-14', status: 'complete', collected_at: '2026-11-15T03:00:00+08:00' }], rows: [{ date: '2026-11-14', model: 'a', input_tokens: '9007199254740993', output_tokens: '10', cache_read_tokens: '30', cache_write_tokens: '40', cost_cny: '80', cost_usd: 10 }, { date: '2026-11-14', model: 'b', input_tokens: '7', output_tokens: '20', cache_read_tokens: '0', cache_write_tokens: '0', cost_cny: '16', cost_usd: 2 }] };
test('date presets use complete Beijing days and validate custom ranges', () => {
 assert.deepEqual(recentDailyRange(7, new Date('2026-11-14T16:01:00Z')), { start: '2026-11-08', end: '2026-11-14' });
 assert.equal(validDailyRange('2026-11-08','2026-11-14',new Date('2026-11-14T16:01:00Z')), true);
 for (const [a,b] of [['2026-02-30','2026-03-01'],['2026-11-15','2026-11-15'],['2026-01-01','2026-11-14'],['','']]) assert.equal(validDailyRange(a,b,new Date('2026-11-15T01:00:00Z')), false);
});
test('all models is the default; multi-selection drives totals and preserves exact counters', () => {
 assert.deepEqual(dailyModels(data.rows!), ['a','b']);
 assert.equal(filterDailyRows(data.rows!,null).length,2);
 assert.deepEqual(dailyTotals(data.rows!), { cost: 12, input: 9007199254741000n, output: 30n });
 assert.equal(dailyTotals(filterDailyRows(data.rows!,['b'])).cost,2);
 assert.equal(filterDailyRows(data.rows!,[]).length,0);
});
test('chart changes metrics without changing rows and never converts missing days to zero', () => {
 assert.deepEqual(dailySeries(data,null,'cost_usd').map(d=>d.value), [null,12]);
 assert.deepEqual(dailySeries(data,['b'],'output_tokens').map(d=>d.value), [null,20]);
 assert.equal(dailySeries(data,['b'],'cache_read_tokens')[1].value,0);
 assert.equal(data.rows!.length,2);
});
