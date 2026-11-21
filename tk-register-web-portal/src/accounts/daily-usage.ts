export type DailyMetric = 'cost_usd' | 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'cache_write_tokens';
export const dailyMetrics: Record<DailyMetric, string> = { cost_usd: 'Cost', input_tokens: 'Input tokens', output_tokens: 'Output tokens', cache_read_tokens: 'Cache read', cache_write_tokens: 'Cache write' };
export type DailyRow = { date: string; model: string; input_tokens: string; output_tokens: string; cache_read_tokens: string; cache_write_tokens: string; cost_cny: string; cost_usd: number };
export type DailyTotal = Omit<DailyRow, 'model'>;
export type DailyDay = { date: string; status: 'complete' | 'stale' | 'missing' | 'failed' | 'pending'; collected_at: string | null; error?: string };
export type ModelUsageLedger = {
  model_usage_source?: 'raw' | 'modified';
  total_cost_usd?: number | null;
  total_cost_days?: number;
  daily_costs?: { date: string; cost_usd: number | null; collected: number; expected: number }[];
};
export function modelUsageLabel(source?: 'raw' | 'modified') {
  if (source === 'modified') return 'Allocated costs and estimated tokens';
  if (source === 'raw') return 'Upstream costs and tokens';
  return 'Daily costs and tokens';
}
export type DailyData = ModelUsageLedger & { start_date: string; end_date: string; timezone: string; collection_time: string; currency: string; exchange_rate_cny_per_usd: number; model_costs_enabled?: boolean; daily_totals?: DailyTotal[]; rows?: DailyRow[]; days: DailyDay[] };
export type ModelSelection = string[] | null;
export function recentDailyRange(count: number, now = new Date()) {
  const china = new Date(now.getTime() + 8 * 3600_000).toISOString().slice(0, 10);
  const midnight = Date.parse(`${china}T00:00:00Z`);
  return { start: new Date(midnight - count * 86400_000).toISOString().slice(0, 10), end: new Date(midnight - 86400_000).toISOString().slice(0, 10) };
}
export function validDailyRange(start: string, end: string, now = new Date()) {
  const dates = [start, end].map(value => new Date(`${value}T00:00:00Z`));
  return dates.every((date, i) => Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === [start, end][i]) && start <= end && end <= recentDailyRange(1, now).end && dates[1].getTime() - dates[0].getTime() <= 89 * 86400_000;
}
export function dailyModels(rows: DailyRow[]) { return [...new Set(rows.map(row => row.model))].sort(); }
export function filterDailyRows(rows: DailyRow[], models: ModelSelection) { return models === null ? rows : rows.filter(row => models.includes(row.model)); }
export function dailyTotals(rows: Pick<DailyRow, 'cost_usd' | 'input_tokens' | 'output_tokens'>[]) {
  return rows.reduce((sum, row) => ({ cost: sum.cost + row.cost_usd, input: sum.input + BigInt(row.input_tokens), output: sum.output + BigInt(row.output_tokens) }), { cost: 0, input: 0n, output: 0n });
}
export function dailySeries(data: DailyData, models: ModelSelection, metric: DailyMetric) {
  const filtered = filterDailyRows(data.rows || [], models);
  return data.days.map(day => ({ ...day, rows: filtered.filter(row => row.date === day.date), value: day.collected_at ? filtered.filter(row => row.date === day.date).reduce((sum, row) => sum + Number(row[metric]), 0) : null }));
}
export function dailyMoney(value: number) { return `${value.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })} $`; }
export function dailyTokens(value: string | bigint) { return BigInt(value).toLocaleString('en-US'); }
export function dailyDate(date: string) { return new Date(`${date}T12:00:00Z`).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' }); }
