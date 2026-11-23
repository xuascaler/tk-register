import { t, currentLanguage, translateMessage } from '../i18n';
import { ArrowUpRight, ChevronLeft, ChevronRight, Clock3 } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { displayError, type DisplayError, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { formatTime } from './helpers';
import { ModelFilter } from './ModelFilter';
import { dailyDate, dailyDateRange, dailyMetrics, dailyModels, dailyMoney, dailySeries, dailyTokens, dailyTotals, filterDailyRows, recentDailyRange, validDailyRange, type DailyData, type DailyMetric, type ModelSelection } from './daily-usage';
import { OverviewChart } from './OverviewChart';
import type { OverviewData } from './overview';
import './daily-usage.css';
import './overview.css';

const colors = ['#365f91', '#688bb8', '#9aafca', '#637d83', '#a4bab6', '#7d7c9c', '#b6a5bd', '#b49978'];
function DailyChart({ data, selection, metric }: { data: DailyData; selection: ModelSelection; metric: DailyMetric }) {
  const [active, setActive] = useState<number | null>(null);
  const series = dailySeries(data, selection, metric), models = dailyModels(data.rows || []);
  const maximum = Math.max(1, ...series.map(day => day.value || 0));
  const width = Math.max(780, series.length * 24), height = 245, left = 65, top = 14, bottom = 210;
  const step = (width - left - 20) / Math.max(1, series.length), bar = Math.min(42, step * .58);
  const selected = selection === null ? models : models.filter(m => selection.includes(m));
  const selectedDay = active === null ? null : series[active];
  const emptySelection = selection !== null && !selected.length;
  const number = (value: number) => metric === 'cost_usd' ? dailyMoney(value) : Intl.NumberFormat(currentLanguage(), { notation: 'compact', maximumFractionDigits: 1 }).format(value);
  useEffect(() => setActive(null), [metric, selection, data]);
  if (emptySelection) return <div className='usage-chart-empty'><h3>{t('Select models to view daily usage')}</h3><p>{t('Choose one or more models from the filter above.')}</p></div>;
  return <>
    <div className='daily-chart-wrap' onPointerLeave={() => setActive(null)}>
      <div className='daily-chart-scroll'><svg className='daily-chart' viewBox={`0 0 ${width} ${height}`} style={{ minWidth: width > 900 ? width : 580 }} role='img' aria-label={t('Daily {{metric}} by model. Missing days are marked with a dash.', { metric: t(dailyMetrics[metric]).toLowerCase() })}>
        {[0, .25, .5, .75, 1].map(fraction => <g key={fraction}><line className='usage-gridline' x1={left} x2={width - 20} y1={bottom - fraction * (bottom - top)} y2={bottom - fraction * (bottom - top)} /><text x={left - 10} y={bottom - fraction * (bottom - top) + 4} textAnchor='end'>{number(maximum * fraction)}</text></g>)}
        {series.map((day, index) => {
          const x = left + step * (index + .5); let offset = 0;
          return <g key={day.date} tabIndex={0} role='button' aria-label={`${dailyDate(day.date, true)}: ${day.value === null ? t('Not collected') : number(day.value)}`} onPointerEnter={() => setActive(index)} onFocus={() => setActive(index)} onClick={() => setActive(index)} onBlur={() => setActive(null)} onKeyDown={event => { if (event.key === 'Escape') setActive(null); if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') { event.preventDefault(); const next = event.key === 'ArrowRight' ? event.currentTarget.nextElementSibling : event.currentTarget.previousElementSibling; if (next instanceof SVGElement) next.focus(); } }}>
            <rect x={x - step / 2} y={top} width={step} height={bottom - top} fill={active === index ? '#edf3fa' : 'transparent'} />
            {day.value === null ? <text x={x} y={bottom - 9} textAnchor='middle'>—</text> : day.rows.map(row => {
              const value = Number(row[metric]), h = value / maximum * (bottom - top), y = bottom - offset - h; offset += h;
              return <rect key={row.model} x={x - bar / 2} y={y} width={bar} height={h} fill={colors[models.indexOf(row.model) % colors.length]} />;
            })}
            {day.value === 0 && <circle cx={x} cy={bottom - 2} r={2} fill='#688bb8' />}
            {(series.length <= 10 || index % Math.ceil(series.length / 8) === 0 || index === series.length - 1) && <text x={x} y={bottom + 24} textAnchor='middle'>{dailyDate(day.date)}</text>}
          </g>;
        })}
      </svg></div>
      {selectedDay && <div className='daily-chart-tooltip' role='status'><strong>{dailyDate(selectedDay.date)}</strong><span>{selectedDay.value === null ? t('No daily bill collected') : t(dailyMetrics[metric])}</span>
        {selectedDay.rows.map(row => <div key={row.model}><span><i style={{ background: colors[models.indexOf(row.model) % colors.length] }} />{row.model}</span><b>{metric === 'cost_usd' ? dailyMoney(row.cost_usd) : dailyTokens(row[metric])}</b></div>)}
        {selectedDay.value !== null && <div className='daily-tooltip-total'><span>{t('Total')}</span><b>{metric === 'cost_usd' ? dailyMoney(selectedDay.value) : dailyTokens(selectedDay.rows.reduce((sum, row) => sum + BigInt(row[metric]), 0n))}</b></div>}
        {(selectedDay.error || selectedDay.status === 'pending') && <p>{selectedDay.error ? translateMessage(selectedDay.error) : t('Waiting for scheduled collection.')}</p>}
      </div>}
    </div>
    <div className='daily-legend' aria-label={t('Selected models')}>{selected.map(model => <span key={model}><i style={{ background: colors[models.indexOf(model) % colors.length] }} />{model}</span>)}</div>
  </>;
}

export function DailyUsage({ endpoint, compact, onOpen }: { endpoint: string; compact: boolean; onOpen: () => void }) {
  const [range, setRange] = useState(() => recentDailyRange(7)), [draft, setDraft] = useState(range);
  const [selection, setSelection] = useState<ModelSelection>(null), [metric, setMetric] = useState<DailyMetric>('cost_usd');
  const [data, setData] = useState<DailyData | null>(null), [error, setError] = useState<DisplayError>(''), [dateError, setDateError] = useState(''), [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1), [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError(''); setData(null);
    request<DailyData>(`${endpoint}/daily-usage?${new URLSearchParams({ start_date: range.start, end_date: range.end })}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setData(endpoint.startsWith('/admin/') || result.model_costs_enabled === true ? result : { ...result, model_costs_enabled: false, rows: undefined }); })
      .catch(cause => { if (!controller.signal.aborted) setError(displayError(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [endpoint, range.start, range.end, attempt]);
  const modelCostsEnabled = endpoint.startsWith('/admin/') || data?.model_costs_enabled === true;
  const chartData = useMemo<OverviewData | null>(() => data ? { ...data, model_costs_enabled: false, earliest_date: null, latest_account_data: null, sources: [], days: data.days.map(day => ({ date: day.date, status: day.collected_at ? 'complete' : 'missing', expected: 1, collected: day.collected_at ? 1 : 0 })) } : null, [data]);
  const models = useMemo(() => dailyModels(data?.rows || []), [data]);
  const rows = useMemo(() => modelCostsEnabled ? filterDailyRows(data?.rows || [], selection) : data?.daily_totals || [], [data, selection, modelCostsEnabled]);
  const totals = useMemo(() => dailyTotals(rows), [rows]);
  const collected = data?.days.filter(day => day.collected_at).length || 0;
  const incomplete = data?.days.some(day => day.status !== 'complete');
  const noSelection = modelCostsEnabled && models.length > 0 && selection !== null && !models.some(m => selection.includes(m));
  const noModelData = modelCostsEnabled && !models.length;
  const known = !!data && collected > 0 && !noSelection;
  const allModels = !modelCostsEnabled || selection === null || (models.length > 0 && models.every(model => selection.includes(model)));
  const headlineCost = allModels && data?.total_cost_usd !== undefined ? data.total_cost_usd : known ? totals.cost : null;
  const partialModels = (data?.total_cost_days ?? 0) > collected;
  const updated = data?.days.map(day => day.collected_at).filter((v): v is string => !!v).sort().at(-1);
  const pageCount = Math.max(1, Math.ceil(rows.length / 20));
  const visiblePage = Math.min(page, pageCount), pageRows = rows.slice((visiblePage - 1) * 20, visiblePage * 20);
  function changeRange(next: typeof range) { setRange(next); setDraft(next); setPage(1); setDateError(''); }
  return <section className={`daily-usage ${compact ? 'daily-compact' : ''}`} aria-labelledby='daily-usage-title'>
    <div className='daily-heading'><div><h2 id='daily-usage-title'>{t('Daily usage')}</h2><p className='usage-small'>{t(modelCostsEnabled ? 'Daily tokens and cost, broken down by model.' : 'Daily tokens and cost, for all models.')}</p></div>{compact && <button className='usage-text-button' onClick={onOpen}>{t('View daily details')}<ArrowUpRight aria-hidden='true' /></button>}</div>
    <div className='daily-filters'>
      <div className='daily-date-control'><span className='daily-filter-label'>{t('Date range')}</span><div className='daily-presets'>{[7, 30].map(count => { const preset = recentDailyRange(count); return <button type='button' key={count} aria-pressed={range.start === preset.start && range.end === preset.end} onClick={() => changeRange(preset)}>{t('Last {{days}} days', { days: count })}</button>; })}</div></div>
      {!compact && <form className='daily-custom-dates' onSubmit={e => { e.preventDefault(); if (!validDailyRange(draft.start, draft.end)) { setDateError('Choose up to 90 complete days, ending no later than yesterday.'); return; } changeRange(draft); }}>
        <label>{t('From')}<input type='date' value={draft.start} max={recentDailyRange(1).end} onChange={e => setDraft(d => ({ ...d, start: e.target.value }))} required /></label><label>{t('To')}<input type='date' value={draft.end} max={recentDailyRange(1).end} onChange={e => setDraft(d => ({ ...d, end: e.target.value }))} required /></label><button className='button pale' type='submit' disabled={loading}>{t('Apply')}</button>
      </form>}
      {modelCostsEnabled && <ModelFilter models={models} value={selection} onChange={value => { setSelection(value); setPage(1); }} />}
    </div>
    <ErrorNotice>{dateError || error}</ErrorNotice>{error && <button className='button pale' onClick={() => setAttempt(v => v + 1)}>{t('Try again')}</button>}
    {loading ? <Loading label={t('Loading daily usage…')} /> : data && <>
      <div className='daily-range-caption'>{dailyDateRange(data.start_date, data.end_date)}<span>Asia/Shanghai</span></div>
      {!compact && <div className='usage-metrics daily-summary'><div><span>{allModels ? t('Total cost') : t('Selected model subtotal')}</span><strong>{headlineCost === null ? '—' : dailyMoney(headlineCost)}</strong><small>{allModels ? t('All models · selected period') : t('Available model details')}</small></div><div><span>{t('Input tokens')}</span><strong>{known ? dailyTokens(totals.input) : '—'}</strong><small>{partialModels ? t('Available days') : modelCostsEnabled ? t('Selected models and period') : t('Selected period')}</small></div><div><span>{t('Output tokens')}</span><strong>{known ? dailyTokens(totals.output) : '—'}</strong><small>{partialModels ? t('Available days') : modelCostsEnabled ? t('Selected models and period') : t('Selected period')}</small></div></div>}
      {incomplete && <p className='daily-data-notice' role='status'>{collected ? t('Daily details are available for only part of this period.') : t('No daily usage details are available for this period.')}{data.days.some(day => day.status === 'failed' || day.status === 'stale') && <span> {t('A collection or account match needs attention. Details are available below.')}</span>}</p>}
      {noModelData ? (compact && <div className='usage-chart-empty'><h3>{t('No model usage data for this period.')}</h3></div>) : <div className='daily-chart-panel'><div className='usage-metric-tabs daily-metric-tabs' role='group' aria-label={t('Daily chart metric')}>{(Object.keys(dailyMetrics) as DailyMetric[]).map(key => <button key={key} type='button' aria-pressed={metric === key} onClick={() => setMetric(key)}>{t(dailyMetrics[key])}</button>)}</div>{modelCostsEnabled ? <DailyChart data={data} metric={metric} selection={selection} /> : chartData && <OverviewChart data={chartData} models={[]} metric={metric} />}</div>}
      {!compact && <div className='daily-table-panel'><div className='daily-table-heading'><h3>{t('Daily breakdown')}</h3><span>{t('All token metrics · cost in $')}</span></div><div className='usage-table-scroll'><table className='daily-table'><caption className='sr-only'>{t('Daily usage for the selected period. Dates use Asia/Shanghai.')}</caption><thead><tr><th scope='col'>{t('Date')}</th>{modelCostsEnabled && <th scope='col'>{t('Model')}</th>}<th scope='col'>{t('Input tokens')}</th><th scope='col'>{t('Cache write')}</th><th scope='col'>{t('Cache read')}</th><th scope='col'>{t('Output tokens')}</th><th scope='col'>{t('Cost')}</th></tr></thead><tbody>{pageRows.map(row => <tr key={`${row.date}:${'model' in row ? row.model : ''}`}><td><time dateTime={row.date}>{dailyDate(row.date)}</time></td>{modelCostsEnabled && <td>{'model' in row ? String(row.model) : ''}</td>}<td>{dailyTokens(row.input_tokens)}</td><td>{dailyTokens(row.cache_write_tokens)}</td><td>{dailyTokens(row.cache_read_tokens)}</td><td>{dailyTokens(row.output_tokens)}</td><td className='daily-row-cost'>{dailyMoney(row.cost_usd)}</td></tr>)}</tbody></table></div>
        {!rows.length && <div className='usage-empty'><h2>{noSelection ? t('No models selected') : modelCostsEnabled ? t('No model usage data for this period.') : collected ? t('No daily usage recorded') : t('Daily usage is not available yet')}</h2><p>{noSelection ? t('Choose one or more models above.') : collected ? modelCostsEnabled ? t('No bill lines were returned for the collected days and selected models.') : t('No bill lines were returned for the collected days.') : t('Daily bills are collected at 03:00 the following day.')}</p></div>}
        {!!rows.length && <div className='usage-pagination'><span>{t('{{count}} rows · Page {{page}} of {{pages}}', { count: rows.length.toLocaleString(currentLanguage()), page: visiblePage, pages: pageCount })}</span><div><button className='button pale' disabled={visiblePage <= 1} onClick={() => setPage(visiblePage - 1)}><ChevronLeft aria-hidden='true' />{t('Previous')}</button><button className='button pale' disabled={visiblePage >= pageCount} onClick={() => setPage(visiblePage + 1)}>{t('Next')}<ChevronRight aria-hidden='true' /></button></div></div>}
      </div>}
      {data.days.some(day => day.error) && <details className='daily-collection-details'><summary>{t('Collection details')}</summary>{data.days.filter(day => day.error).map(day => <p key={day.date}><time dateTime={day.date}>{dailyDate(day.date, true)}</time> {translateMessage(day.error || '')}{day.collected_at && ' ' + t('Showing the last successful data.')}</p>)}</details>}
      <div className='usage-footnote daily-footnote'><Clock3 aria-hidden='true' /><span>{t('Collected daily at 03:00 (Asia/Shanghai) for the previous day.')}{updated && <> {t('Last collected {{time}}.', { time: formatTime(updated, 'Asia/Shanghai') })}</>}</span></div>
    </>}
  </section>;
}
