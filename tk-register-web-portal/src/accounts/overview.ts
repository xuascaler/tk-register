import { recentDailyRange, type DailyRow, type ModelSelection, type DailyMetric } from './daily-usage';

export const overviewStatuses = ['active', 'pending', 'offline', 'banned', 'sync_failed', 'login_failed', 're_login_failed', 'bound_failed', 'cooking_failed', 'limited', 'unknown'];
export type OverviewSource = { id: string; label: string; total: number; statuses: Record<string, number>; cost_usd: number | null; cost_known: number; collected_days: number };
export type OverviewData = {
  start_date: string; end_date: string; earliest_date: string | null; timezone: string; collection_time: string;
  exchange_rate_cny_per_usd: number; latest_account_data: string | null;
  sources: OverviewSource[]; rows: DailyRow[];
  model_costs: { source_id: string; model: string; cost_cny: string; cost_usd: number }[];
  days: { date: string; status: 'complete' | 'partial' | 'missing'; expected: number; collected: number }[];
};
export function overviewTotals(sources: OverviewSource[]): OverviewSource {
  return sources.reduce<OverviewSource>((sum, row) => ({ id: 'total', label: 'Total', total: sum.total + row.total,
    statuses: Object.entries(row.statuses).reduce((counts, [key, value]) => ({ ...counts, [key]: (counts[key] || 0) + value }), sum.statuses),
    cost_usd: row.cost_usd === null ? sum.cost_usd : (sum.cost_usd || 0) + row.cost_usd,
    cost_known: sum.cost_known + row.cost_known, collected_days: sum.collected_days + row.collected_days,
  }), { id: 'total', label: 'Total', total: 0, statuses: {}, cost_usd: null, cost_known: 0, collected_days: 0 });
}
export function overviewModels(data: OverviewData | null) { return [...new Set(data?.rows.map(row => row.model) || [])].sort(); }
export function selectedOverviewModels(data: OverviewData, selection: ModelSelection) { const models = overviewModels(data); return selection === null ? models : models.filter(model => selection.includes(model)); }
export function overviewModelCosts(data: OverviewData, source: OverviewSource, models: string[]) {
  if (!source.collected_days || !models.length) return null;
  return data.model_costs.filter(row => (source.id === 'total' || row.source_id === source.id) && models.includes(row.model)).reduce((sum, row) => sum + row.cost_usd, 0);
}
export function validOverviewRange(start: string, end: string, now = new Date()) {
  return [start, end].every(value => /^\d{4}-\d{2}-\d{2}$/.test(value) && Number.isFinite(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value)
    && start >= '1970-01-01' && start <= end && end <= recentDailyRange(1, now).end;
}
export function overviewSeries(data: OverviewData, models: string[], metric: DailyMetric) {
  const byDate = new Map<string, Map<string, DailyRow>>();
  for (const row of data.rows) { if (!models.includes(row.model)) continue; if (!byDate.has(row.date)) byDate.set(row.date, new Map()); byDate.get(row.date)!.set(row.model, row); }
  return data.days.map(day => {
    const rows = models.map(model => byDate.get(day.date)?.get(model)).filter((row): row is DailyRow => !!row);
    const values = models.map(model => {
      const row = byDate.get(day.date)?.get(model);
      // A missing bill/model is only a zero once the whole day is collected.
      return row ? Number(row[metric]) : day.status === 'complete' ? 0 : null;
    });
    return { ...day, rows, values, value: values.some(v => v !== null) ? values.reduce<number>((sum, value) => sum + (value || 0), 0) : null };
  });
}
