import { Clock3, Info } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import SourceFilter from '../members/SourceFilter';
import { selectedSources, useMemberScope } from '../members/MemberScope';
import { ModelFilter } from './ModelFilter';
import OverviewDateFilter from './OverviewDateFilter';
import { dailyTokens, dailyTotals, recentDailyRange, type DailyMetric, type ModelSelection } from './daily-usage';
import { formatCost, formatTime, statusLabel, statusTone } from './helpers';
import { overviewCosts, overviewModels, overviewQuery, overviewStatuses, overviewTotals, selectedOverviewModels, type OverviewData, type OverviewSource, type OverviewRangeSelection } from './overview';
import { OverviewChart, overviewColors } from './OverviewChart';
import './daily-usage.css';
import './overview.css';

export default function AccountsOverview({ admin = false, raw = false }: { admin?: boolean; raw?: boolean }) {
  const scope = useMemberScope();
  const [range, setRange] = useState(() => recentDailyRange(7));
  const [period, setPeriod] = useState<OverviewRangeSelection>('all_time');
  const [selection, setSelection] = useState<ModelSelection>(null), [metric, setMetric] = useState<DailyMetric>('cost_usd');
  const [table, setTable] = useState<'status' | 'models'>('status');
  const [data, setData] = useState<OverviewData | null>(null), [loading, setLoading] = useState(true), [error, setError] = useState('');
  // Retain the confirmed control mode while a new period or source is loading.
  const [periodDropdown, setPeriodDropdown] = useState(!admin);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(''); setData(null);
    const query = overviewQuery(scope.selection, period, range, admin);
    request<OverviewData>(`${raw ? '/admin/raw-accounts/overview' : admin ? '/admin/accounts/overview' : '/accounts/overview'}?${query}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) { setPeriodDropdown(!admin && result.model_costs_enabled !== true); setData(admin || result.model_costs_enabled === true ? result : { ...result, model_costs_enabled: false, rows: undefined, model_costs: undefined }); } })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [admin, raw, scope.selection, period, range.start, range.end, attempt]);
  const modelCostsEnabled = admin || data?.model_costs_enabled === true;
  const visibleTable = modelCostsEnabled ? table : 'status';
  const summaryUnavailable = data?.summary_available === false;
  const savedSummary = data?.summary_available !== undefined;
  const missingAllTime = summaryUnavailable && period === 'all_time';
  const displayedRange = missingAllTime ? { start: '', end: '' } : data?.start_date && data.end_date ? { start: data.start_date, end: data.end_date } : range;
  const rangeLabel = !missingAllTime && data?.start_date && data.end_date ? `${data.start_date} – ${data.end_date}` : '—';
  const usageEndDate = missingAllTime ? '' : data?.end_date;
  const sources = summaryUnavailable ? scope.sources.filter(source => selectedSources(scope.selection, scope.sources).includes(source.id)).map(source => ({ ...source, total: 0, statuses: {}, cost_usd: null, cost_known: 0, collected_days: 0 } as OverviewSource)) : data?.sources || [];
  const models = useMemo(() => overviewModels(data), [data]);
  const selected = useMemo(() => data ? selectedOverviewModels(data, selection) : [], [data, selection]);
  const total = useMemo(() => overviewTotals(data?.sources || []), [data]);
  const costs = useMemo(() => overviewCosts(data, selection), [data, selection]);
  const statuses = useMemo(() => [...overviewStatuses, ...Object.keys(total.statuses).filter(key => !overviewStatuses.includes(key)).sort()], [total]);
  const periodRows = useMemo(() => summaryUnavailable ? [] : data?.rows?.filter(row => selected.includes(row.model)) || [], [data, selected, summaryUnavailable]);
  const periodTotals = useMemo(() => dailyTotals(modelCostsEnabled ? periodRows : data?.daily_totals || []), [periodRows, modelCostsEnabled, data]);
  const incomplete = !summaryUnavailable && !!data?.days.some(day => day.collected === 0);
  const hasCollectedData = !summaryUnavailable && !!data?.days.some(day => day.collected > 0);
  const knownPeriod = hasCollectedData && (!modelCostsEnabled || selected.length > 0 || selection === null && models.length === 0);
  const allModels = !modelCostsEnabled || selection === null || (selected.length > 0 && selected.length === models.length);
  const hasLedger = data?.total_cost_usd !== undefined;
  const headlineCost = allModels && hasLedger ? data.total_cost_usd ?? null : costs.total;
  const sourceCost = (source: OverviewSource) => allModels && hasLedger ? source.cost_usd : costs.sources.get(source.id) ?? null;
  const partialModels = useMemo(() => {
    const detailCoverage = new Map(data?.days.map(day => [day.date, day.collected]));
    return data?.daily_costs?.some(day => day.collected > (detailCoverage.get(day.date) ?? 0)) ?? false;
  }, [data]);
  function chooseDays(days: number) { setRange(recentDailyRange(days)); setPeriod(days === 1 ? 'day' : days === 30 ? 'month' : 'week'); }
  function number(value: number) { return summaryUnavailable ? '—' : value.toLocaleString(); }
  function money(value: number | null) { return summaryUnavailable ? '—' : formatCost(value, 'USD'); }
  return <section className={`accounts-overview ${admin ? 'accounts-overview-admin' : ''} ${summaryUnavailable ? 'is-summary-unavailable' : ''}`} aria-label='Accounts overview'>
    <div className={`overview-filters ${modelCostsEnabled ? '' : 'is-totals-only'}`}>
      <SourceFilter id='overview-source' searchable={admin} />
      <OverviewDateFilter dropdown={!admin && periodDropdown} allowCustom={admin} range={displayedRange} allTime={period === 'all_time'} period={period} onDays={chooseDays} onAllTime={() => setPeriod('all_time')} onApply={next => { setRange(next); setPeriod('custom'); }} />
      {modelCostsEnabled && <ModelFilter models={models} value={selection} onChange={setSelection} />}
    </div>
    <ErrorNotice>{error}</ErrorNotice>{error && <button className='button pale' onClick={() => setAttempt(n => n + 1)}>Try again</button>}
    {loading ? <Loading label='Loading overview…' /> : data && <>
      <p className='overview-updated'>{savedSummary ? 'Summary generated' : 'Saved account data'} · {summaryUnavailable ? <span className='overview-unavailable'>—</span> : formatTime(savedSummary ? data.summary_generated_at || null : data.latest_account_data, undefined, true)}</p>
      {scope.selection === 'none' && <p className='daily-data-notice'>{admin ? 'Select one or more sources to view accounts.' : 'Select one or more sources to view your accounts.'}</p>}
      <section className='overview-summary' aria-labelledby='overview-summary-heading'>
        <h2 id='overview-summary-heading' className='sr-only'>Account summary</h2>
        <div className='overview-summary-top'><div><span className='overview-stat-label'>Total accounts</span><strong className='overview-stat-value'>{number(total.total)}</strong><p>{admin ? 'Selected users · saved status' : 'Selected sources · saved status'}</p></div><div className='overview-summary-spent'><span className='overview-stat-label'>{allModels ? 'Total spent · selected period' : 'Selected model subtotal'}</span><strong className='overview-stat-value'>{money(headlineCost)}</strong></div></div>
        <div className='overview-status-grid'>{statuses.map(key => <div key={key}><span>{statusLabel(key)}</span><strong className={`overview-number ${total.statuses[key] ? statusTone(key) : 'is-zero'}`}>{number(total.statuses[key] || 0)}</strong></div>)}</div>
        {modelCostsEnabled && <section className='overview-model-summary' aria-labelledby='overview-model-heading'><div className='overview-section-heading'><div><h2 id='overview-model-heading'>Model costs</h2><p>{rangeLabel}</p></div><strong className='overview-period-total'>{money(costs.total)}</strong></div>
        {partialModels && hasCollectedData && <p className='overview-note'>Subtotal for days with available model details.</p>}
        <div className='overview-model-grid'>{selected.map(model => <div key={model}><span><i style={{ background: overviewColors[models.indexOf(model) % overviewColors.length] }} />{model}</span><strong>{money(costs.models.get(model) ?? null)}</strong></div>)}</div>
        {!summaryUnavailable && !selected.length && <p className='overview-note'>{models.length || selection !== null ? 'Select one or more models to view costs.' : 'No model usage data for this period.'}</p>}
      </section>}
      </section>
      <section className='overview-sources' aria-labelledby='overview-sources-heading'><div className='overview-section-heading'><div className='overview-source-title'><h2 id='overview-sources-heading'>{admin ? 'Accounts by user' : 'Accounts by source'}</h2><span className='overview-source-info' tabIndex={0} aria-label='How source totals are calculated' aria-describedby='overview-source-explanation'><Info aria-hidden='true' /><span id='overview-source-explanation' role='tooltip'>{admin ? 'Each user includes only accounts they directly own. Their sub-users are listed separately. Each account is counted once.' : 'myself includes your own accounts. Each direct member includes accounts available through them, including their sub-users. Each account is counted once.'}</span></span></div>{modelCostsEnabled && <div className='usage-metric-tabs' role='group' aria-label='Source breakdown'><button type='button' aria-pressed={visibleTable === 'status'} onClick={() => setTable('status')}>Status</button><button type='button' aria-pressed={visibleTable === 'models'} onClick={() => setTable('models')}>Model costs</button></div>}</div>
        <div className='overview-table-shell'><div className='usage-table-scroll' tabIndex={0} role='region' aria-label='Source breakdown table'><table className={`overview-source-table ${visibleTable === 'models' ? 'is-models' : 'is-status'}`}><caption className='sr-only'>{visibleTable === 'status' ? `Saved account counts by ${admin ? 'user' : 'source'} and status, plus selected-period spend` : `Selected-period model costs by ${admin ? 'user' : 'source'}`}</caption>
          <thead><tr><th scope='col'>{admin ? 'User' : 'Source'}</th>{visibleTable === 'status' ? <><th scope='col'>All</th>{statuses.map(key => <th key={key} scope='col'>{statusLabel(key)}</th>)}<th scope='col'>{allModels ? 'Total spent' : 'Selected model subtotal'}</th></> : <>{selected.map(model => <th scope='col' key={model}>{model}</th>)}<th scope='col'>{partialModels ? 'Available model subtotal' : 'Model subtotal'}</th></>}</tr></thead>
          <tbody>{sources.map(source => <tr key={source.id}><th scope='row'>{source.label}</th>{visibleTable === 'status' ? <><td>{number(source.total)}</td>{statuses.map(key => <td key={key} className={`overview-number ${source.statuses[key] ? statusTone(key) : 'is-zero'}`}>{number(source.statuses[key] || 0)}</td>)}<td className='overview-cost'>{money(sourceCost(source))}</td></> : <>{selected.map(model => <td key={model} className='overview-cost'>{money(costs.cells.get(source.id)?.get(model) ?? null)}</td>)}<td className='overview-cost'>{money(costs.sources.get(source.id) ?? null)}</td></>}</tr>)}</tbody>
          <tfoot><tr><th scope='row'>Total</th>{visibleTable === 'status' ? <><td>{number(total.total)}</td>{statuses.map(key => <td key={key} className={`overview-number ${total.statuses[key] ? statusTone(key) : 'is-zero'}`}>{number(total.statuses[key] || 0)}</td>)}<td className='overview-cost'>{money(headlineCost)}</td></> : <>{selected.map(model => <td key={model} className='overview-cost'>{money(costs.models.get(model) ?? null)}</td>)}<td className='overview-cost'>{money(costs.total)}</td></>}</tr></tfoot>
        </table></div></div>
        <p className='overview-note'>{visibleTable === 'status' ? modelCostsEnabled ? 'Status uses saved account data. The all-model total includes all available daily bills.' : 'Status uses saved account data. Costs include all models for the selected period.' : 'Costs include collected daily bills for the selected models and period.'}</p>
      </section>
      <section className='daily-compact overview-usage' aria-labelledby='overview-usage-heading'><div className='overview-section-heading'><div><h2 id='overview-usage-heading'>Daily usage</h2><p>{rangeLabel} · Asia/Shanghai</p></div><span className='overview-note'>{data.days.length > 14 ? 'Daily trend' : 'Daily breakdown'}</span></div>
        <div className='overview-chart-controls'><div className='usage-metric-tabs' role='group' aria-label='Daily chart metric'>{([['cost_usd', 'Cost'], ['input_tokens', 'Input tokens'], ['output_tokens', 'Output tokens']] as const).map(([key, label]) => <button key={key} type='button' aria-pressed={metric === key} onClick={() => setMetric(key)}>{label}</button>)}</div><div className='overview-chart-total'><strong>{metric === 'cost_usd' ? money(modelCostsEnabled ? costs.total : headlineCost) : knownPeriod ? dailyTokens(metric === 'input_tokens' ? periodTotals.input : periodTotals.output) : '—'}</strong><span>{modelCostsEnabled ? 'Model subtotal' : 'Selected period'}</span></div></div>
        {(incomplete || partialModels) && (modelCostsEnabled || metric !== 'cost_usd') && <p className='daily-data-notice'>{hasCollectedData ? 'The chart includes available usage details. Dates without details appear as gaps.' : 'No usage details are available for this period.'}</p>}
        <OverviewChart data={data} models={selected} metric={metric} selectionEmpty={modelCostsEnabled && selection !== null && !selected.length} />
        <p className='usage-footnote daily-footnote'><Clock3 aria-hidden='true' /><span>{usageEndDate ? `Usage through ${usageEndDate} · ` : ''}Collected daily at 03:00 · Asia/Shanghai</span></p>
      </section>
    </>}
  </section>;
}
