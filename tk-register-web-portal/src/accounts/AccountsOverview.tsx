import { Clock3, Info } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import SourceFilter from '../members/SourceFilter';
import { useMemberScope } from '../members/MemberScope';
import { ModelFilter } from './ModelFilter';
import OverviewDateFilter from './OverviewDateFilter';
import { dailyMoney, dailyTokens, dailyTotals, recentDailyRange, type DailyMetric, type ModelSelection } from './daily-usage';
import { formatCost, formatTime, statusLabel, statusTone } from './helpers';
import { overviewModelCosts, overviewModels, overviewStatuses, overviewTotals, selectedOverviewModels, type OverviewData, type OverviewSource } from './overview';
import { OverviewChart, overviewColors } from './OverviewChart';
import './daily-usage.css';
import './overview.css';

export default function AccountsOverview() {
  const scope = useMemberScope();
  const [range, setRange] = useState(() => recentDailyRange(7));
  const [allTime, setAllTime] = useState(false);
  const [selection, setSelection] = useState<ModelSelection>(null), [metric, setMetric] = useState<DailyMetric>('cost_usd');
  const [table, setTable] = useState<'status' | 'models'>('status');
  const [data, setData] = useState<OverviewData | null>(null), [loading, setLoading] = useState(true), [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError(''); setData(null);
    const query = new URLSearchParams({ sources: scope.selection, ...(allTime ? { range: 'all' } : { start_date: range.start, end_date: range.end }) });
    request<OverviewData>(`/accounts/overview?${query}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) { setData(result); } })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [scope.selection, allTime, range.start, range.end, attempt]);
  const models = useMemo(() => overviewModels(data), [data]);
  const selected = useMemo(() => data ? selectedOverviewModels(data, selection) : [], [data, selection]);
  const total = useMemo(() => overviewTotals(data?.sources || []), [data]);
  const statuses = useMemo(() => [...overviewStatuses, ...Object.keys(total.statuses).filter(key => !overviewStatuses.includes(key)).sort()], [total]);
  const periodRows = useMemo(() => data?.rows.filter(row => selected.includes(row.model)) || [], [data, selected]);
  const periodTotals = useMemo(() => dailyTotals(periodRows), [periodRows]);
  const incomplete = !!data?.days.some(day => day.status !== 'complete');
  const knownPeriod = !!data?.days.some(day => day.collected > 0) && selected.length > 0;
  function chooseDays(days: number) { const next = recentDailyRange(days); setAllTime(false); setRange(next); }
  function costFor(source: OverviewSource, modelList: string[]) { return formatCost(data ? overviewModelCosts(data, source, modelList) : null, 'USD'); }
  return <section className='accounts-overview' aria-label='Accounts overview'>
    <div className='overview-filters'>
      <SourceFilter id='overview-source' />
      <OverviewDateFilter range={allTime && data ? { start: data.start_date, end: data.end_date } : range} allTime={allTime} onDays={chooseDays} onAllTime={() => setAllTime(true)} onApply={next => { setRange(next); setAllTime(false); }} />
      <ModelFilter models={models} value={selection} onChange={setSelection} />
    </div>
    <ErrorNotice>{error}</ErrorNotice>{error && <button className='button pale' onClick={() => setAttempt(n => n + 1)}>Try again</button>}
    {loading ? <Loading label='Loading overview…' /> : data && <>
      <p className='overview-updated'>Latest account data · {formatTime(data.latest_account_data, undefined, true)}</p>
      {scope.selection === 'none' && <p className='daily-data-notice'>Select one or more sources to view your accounts.</p>}
      <section className='overview-summary' aria-labelledby='overview-summary-heading'>
        <h2 id='overview-summary-heading' className='sr-only'>Account summary</h2>
        <div className='overview-summary-top'><div><span className='overview-stat-label'>Total accounts</span><strong className='overview-stat-value'>{total.total.toLocaleString()}</strong><p>Selected sources · current status</p></div><div className='overview-summary-spent'><span className='overview-stat-label'>Total spent · cumulative</span><strong className='overview-stat-value'>{formatCost(total.cost_usd, 'USD')}</strong></div></div>
        <div className='overview-status-grid'>{statuses.map(key => <div key={key}><span>{statusLabel(key)}</span><strong className={`overview-number ${total.statuses[key] ? statusTone(key) : 'is-zero'}`}>{(total.statuses[key] || 0).toLocaleString()}</strong></div>)}</div>
        {total.cost_known < total.total && total.total > 0 && <p className='overview-note'>Cumulative spend is available for {total.cost_known.toLocaleString()} of {total.total.toLocaleString()} accounts. Unavailable values are not counted as zero.</p>}
        <section className='overview-model-summary' aria-labelledby='overview-model-heading'><div className='overview-section-heading'><div><h2 id='overview-model-heading'>Model costs</h2><p>{data.start_date} – {data.end_date} · selected period</p></div><strong className='overview-period-total'>{knownPeriod ? dailyMoney(periodTotals.cost) : '—'}</strong></div>
        <div className='overview-model-grid'>{selected.map(model => <div key={model}><span><i style={{ background: overviewColors[models.indexOf(model) % overviewColors.length] }} />{model}</span><strong>{costFor(total, [model])}</strong></div>)}</div>
        {!selected.length && <p className='overview-note'>{models.length ? 'Select one or more models to view costs.' : 'No model bill lines have been collected for this period.'}</p>}
      </section>
      </section>
      <section className='overview-sources' aria-labelledby='overview-sources-heading'><div className='overview-section-heading'><div className='overview-source-title'><h2 id='overview-sources-heading'>Accounts by source</h2><span className='overview-source-info' tabIndex={0} aria-label='How source totals are calculated' aria-describedby='overview-source-explanation'><Info aria-hidden='true' /><span id='overview-source-explanation' role='tooltip'>myself includes your own accounts. Each direct member includes accounts available through them, including their sub-users. Each account is counted once.</span></span></div><div className='usage-metric-tabs' role='group' aria-label='Source breakdown'><button type='button' aria-pressed={table === 'status'} onClick={() => setTable('status')}>Status</button><button type='button' aria-pressed={table === 'models'} onClick={() => setTable('models')}>Model costs</button></div></div>
        <div className='overview-table-shell'><div className='usage-table-scroll' tabIndex={0} role='region' aria-label='Source breakdown table'><table className={`overview-source-table ${table === 'models' ? 'is-models' : 'is-status'}`}><caption className='sr-only'>{table === 'status' ? 'Current account counts by source and status, plus cumulative spend' : 'Selected-period model costs by source'}</caption>
          <thead><tr><th scope='col'>Source</th>{table === 'status' ? <><th scope='col'>All</th>{statuses.map(key => <th key={key} scope='col'>{statusLabel(key)}</th>)}<th scope='col'>Total spent</th></> : <>{selected.map(model => <th scope='col' key={model}>{model}</th>)}<th scope='col'>Period total</th></>}</tr></thead>
          <tbody>{data.sources.map(source => <tr key={source.id}><th scope='row'>{source.label}</th>{table === 'status' ? <><td>{source.total.toLocaleString()}</td>{statuses.map(key => <td key={key} className={`overview-number ${source.statuses[key] ? statusTone(key) : 'is-zero'}`}>{(source.statuses[key] || 0).toLocaleString()}</td>)}<td className='overview-cost'>{formatCost(source.cost_usd, 'USD')}</td></> : <>{selected.map(model => <td key={model} className='overview-cost'>{costFor(source, [model])}</td>)}<td className='overview-cost'>{costFor(source, selected)}</td></>}</tr>)}</tbody>
          <tfoot><tr><th scope='row'>Total</th>{table === 'status' ? <><td>{total.total.toLocaleString()}</td>{statuses.map(key => <td key={key} className={`overview-number ${total.statuses[key] ? statusTone(key) : 'is-zero'}`}>{(total.statuses[key] || 0).toLocaleString()}</td>)}<td className='overview-cost'>{formatCost(total.cost_usd, 'USD')}</td></> : <>{selected.map(model => <td key={model} className='overview-cost'>{costFor(total, [model])}</td>)}<td className='overview-cost'>{costFor(total, selected)}</td></>}</tr></tfoot>
        </table></div></div>
        <p className='overview-note'>{table === 'status' ? 'Status and cumulative spend use the latest saved account data.' : 'Costs include collected daily bills for the selected models and period.'}</p>
      </section>
      <section className='daily-compact overview-usage' aria-labelledby='overview-usage-heading'><div className='overview-section-heading'><div><h2 id='overview-usage-heading'>Daily usage</h2><p>{data.start_date} – {data.end_date} · Asia/Shanghai</p></div><span className='overview-note'>{data.days.length > 14 ? 'Daily trend' : 'Daily breakdown'}</span></div>
        <div className='overview-chart-controls'><div className='usage-metric-tabs' role='group' aria-label='Daily chart metric'>{([['cost_usd', 'Cost'], ['input_tokens', 'Input tokens'], ['output_tokens', 'Output tokens']] as const).map(([key, label]) => <button key={key} type='button' aria-pressed={metric === key} onClick={() => setMetric(key)}>{label}</button>)}</div><div className='overview-chart-total'><strong>{knownPeriod ? metric === 'cost_usd' ? dailyMoney(periodTotals.cost) : dailyTokens(metric === 'input_tokens' ? periodTotals.input : periodTotals.output) : '—'}</strong><span>Selected period</span></div></div>
        {incomplete && <p className='daily-data-notice'>{knownPeriod ? 'Some daily bills are incomplete or unavailable. Totals show collected data only; missing days remain gaps.' : 'Daily bills are not available for every day in this period. Missing data is shown as —.'}</p>}
        <OverviewChart data={data} models={selected} metric={metric} />
        <p className='usage-footnote daily-footnote'><Clock3 aria-hidden='true' /><span>Complete days through {data.end_date} · Collected daily at 03:00 · Asia/Shanghai</span></p>
      </section>
    </>}
  </section>;
}
