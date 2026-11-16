import { recentDailyRange, type DailyRow, type ModelSelection, type DailyMetric } from './daily-usage';

export const overviewStatuses = ['active', 'pending', 'offline', 'banned', 'sync_failed', 'login_failed', 're_login_failed', 'bound_failed', 'cooking_failed', 'limited', 'unknown'];
export type OverviewPeriod = 'day' | 'week' | 'month' | 'all_time';
export type OverviewRangeSelection = OverviewPeriod | 'custom';
export function overviewQuery(sources: string, period: OverviewRangeSelection, range: { start: string; end: string }) {
  // Always fetch every model once. Changing the model selection is local and
  // must not change account counts; selected-period costs are filtered locally.
  return new URLSearchParams({ sources, models: 'all', ...(period === 'custom' ? { start_date: range.start, end_date: range.end } : { period }) });
}
export type OverviewSource = { id: string; label: string; total: number; statuses: Record<string, number>; cost_usd: number | null; cost_known: number; collected_days: number };
export type OverviewData = {
  summary_available?: boolean; summary_generated_at?: string | null; summary_period?: string;
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
export function overviewModels(data: OverviewData | null) { return data?.summary_available === false ? [] : [...new Set([...(data?.rows.map(row => row.model) || []), ...(data?.model_costs.map(row => row.model) || [])])].sort(); }
export function selectedOverviewModels(data: OverviewData, selection: ModelSelection) { const models = overviewModels(data); return selection === null ? models : models.filter(model => selection.includes(model)); }
export type OverviewCosts = {
  total: number | null;
  sources: Map<string, number | null>;
  models: Map<string, number | null>;
  cells: Map<string, Map<string, number | null>>;
};
export function overviewCosts(data: OverviewData | null, selection: ModelSelection): OverviewCosts {
  const selected = data ? selectedOverviewModels(data, selection) : [];
  const enabled = data?.summary_available !== false && (selection === null || selected.length > 0);
  const cents = new Map<string, Map<string, number>>();
  for (const source of data?.sources || []) {
    if (enabled && source.collected_days > 0) cents.set(source.id, new Map(selected.map(model => [model, 0])));
  }
  // The server allocates each source/model amount in cents. Add those cells
  // once, then use their row/column totals in every overview cost display.
  for (const row of data?.model_costs || []) {
    const source = cents.get(row.source_id);
    if (source?.has(row.model)) source.set(row.model, source.get(row.model)! + Math.round(row.cost_usd * 100));
  }
  const sources = new Map<string, number | null>(), models = new Map<string, number | null>(), cells = new Map<string, Map<string, number | null>>();
  const modelCents = new Map(selected.map(model => [model, 0]));
  let totalCents = 0;
  for (const source of data?.sources || []) {
    const sourceCents = cents.get(source.id);
    const sourceTotal = sourceCents ? [...sourceCents.values()].reduce((sum, value) => sum + value, 0) : null;
    sources.set(source.id, sourceTotal === null ? null : sourceTotal / 100);
    cells.set(source.id, new Map(selected.map(model => [model, sourceCents ? sourceCents.get(model)! / 100 : null])));
    if (sourceTotal !== null) totalCents += sourceTotal;
    for (const [model, value] of sourceCents || []) modelCents.set(model, modelCents.get(model)! + value);
  }
  for (const [model, value] of modelCents) models.set(model, cents.size ? value / 100 : null);
  return { total: cents.size ? totalCents / 100 : null, sources, models, cells };
}
export function overviewModelCosts(data: OverviewData, source: OverviewSource, models: string[]) {
  const costs = overviewCosts(data, !models.length && !overviewModels(data).length ? null : models);
  return source.id === 'total' ? costs.total : costs.sources.get(source.id) ?? null;
}
export function validOverviewRange(start: string, end: string, now = new Date()) {
  return [start, end].every(value => /^\d{4}-\d{2}-\d{2}$/.test(value) && Number.isFinite(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value)
    && start >= '1970-01-01' && start <= end && end <= recentDailyRange(1, now).end;
}
export function overviewSeries(data: OverviewData, models: string[], metric: DailyMetric) {
  if (data.summary_available === false) return [];
  // A successfully collected period may have no model rows at all. Keep its
  // zero total visible, without inventing a model or overriding deselection.
  const aggregateOnly = !models.length && !overviewModels(data).length;
  const byDate = new Map<string, Map<string, DailyRow>>();
  for (const row of data.rows) { if (!models.includes(row.model)) continue; if (!byDate.has(row.date)) byDate.set(row.date, new Map()); byDate.get(row.date)!.set(row.model, row); }
  return data.days.map(day => {
    const rows = models.map(model => byDate.get(day.date)?.get(model)).filter((row): row is DailyRow => !!row);
    const values = aggregateOnly ? [day.collected > 0 ? 0 : null] : models.map(model => {
      const row = byDate.get(day.date)?.get(model);
      // Show the successfully collected accounts, including confirmed zero bills.
      // Older responses may still mark these days as partial.
      return day.collected > 0 ? row ? Number(row[metric]) : 0 : null;
    });
    return { ...day, rows, values, value: values.some(v => v !== null) ? metric === 'cost_usd' ? values.reduce<number>((sum, value) => sum + Math.round((value || 0) * 100), 0) / 100 : values.reduce<number>((sum, value) => sum + (value || 0), 0) : null };
  });
}
