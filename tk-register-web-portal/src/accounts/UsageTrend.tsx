import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage, request } from '../api';
import { ErrorNotice, Loading } from '../ui';
import { chartSegments, curvePath, hoverPoint, type ChartPoint } from './curve';
import { formatCost, formatTime, formatTokens, type Metric } from './helpers';
import type { AccountDetails, SnapshotPage } from './types';

export const metricNames: Record<Metric, string> = { cost_total_usd: 'Total spent', input_tokens_total: 'Input tokens', output_tokens_total: 'Output tokens' };

function valueLabel(value: number | null, metric: Metric) {
  return metric === 'cost_total_usd' ? formatCost(value, 'USD') : formatTokens(value);
}

function SnapshotTable({ data, endpoint, days, metric }: { data: AccountDetails; endpoint: string; days: string; metric: Metric }) {
  const [page, setPage] = useState(data.snapshot_page);
  const [result, setResult] = useState<SnapshotPage>({ items: data.snapshots, total: data.snapshot_count, page: data.snapshot_page, page_size: data.snapshot_page_size });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setError('');
    if (page === data.snapshot_page) {
      setResult({ items: data.snapshots, total: data.snapshot_count, page, page_size: data.snapshot_page_size });
      setLoading(false);
      return () => controller.abort();
    }
    setLoading(true);
    const query = new URLSearchParams({ days, page: String(page), page_size: String(data.snapshot_page_size) });
    request<SnapshotPage>(`${endpoint}?${query}`, { signal: controller.signal })
      .then(next => { if (!controller.signal.aborted) setResult(next); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [data, days, endpoint, page, attempt]);
  return <div className='usage-observations' aria-busy={loading}>
    <div className='usage-data-heading'><span>Original observations</span><span>Page 1 contains the most recent records.</span></div>
    <ErrorNotice>{error}</ErrorNotice>
    {error && <button className='button pale' onClick={() => setAttempt(value => value + 1)}>Try again</button>}
    {loading ? <Loading label='Loading observations…' /> : <div className='usage-data-scroll'><table className='usage-data-table'>
      <caption className='sr-only'>Original collected usage values, page {result.page}</caption>
      <thead><tr><th scope='col'>Collected at</th><th scope='col'>{metricNames[metric]}</th></tr></thead>
      <tbody>{result.items.map(row => <tr key={row.id}><td>{formatTime(row.collected_at, data.timezone)}</td><td>{valueLabel(row[metric], metric)}</td></tr>)}</tbody>
    </table>{!result.items.length && <p className='usage-small'>No observations in this date range.</p>}</div>}
    {result.total > 0 && <div className='usage-pagination usage-observation-pagination'><span>{result.total.toLocaleString()} observations · Page {result.page} of {Math.max(1, Math.ceil(result.total / result.page_size))}</span><div>
      <button className='button pale' disabled={loading || result.page <= 1} onClick={() => { setPage(result.page - 1); setAttempt(value => value + 1); }}><ChevronLeft aria-hidden='true' />Newer</button>
      <button className='button pale' disabled={loading || result.page * result.page_size >= result.total} onClick={() => { setPage(result.page + 1); setAttempt(value => value + 1); }}>Older<ChevronRight aria-hidden='true' /></button>
    </div></div>}
  </div>;
}

export function UsageTrend({ data, endpoint, days, metric }: { data: AccountDetails; endpoint: string; days: string; metric: Metric }) {
  const target = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(640);
  const [table, setTable] = useState(false);
  const [selected, setSelected] = useState<ChartPoint | null>(null);
  const [focused, setFocused] = useState(false);
  const segments = useMemo(() => chartSegments(data.trend[metric]), [data.trend, metric]);
  const points = useMemo(() => segments.flat(), [segments]);
  useEffect(() => { setSelected(null); }, [segments]);
  useEffect(() => {
    const element = target.current;
    if (!element) return;
    const observer = new ResizeObserver(entries => setWidth(Math.max(240, entries[0].contentRect.width)));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const height = 260, left = width < 420 ? 56 : 72, right = 20, top = 24, bottom = 44;
  const first = points[0]?.time ?? 0, last = points.at(-1)?.time ?? first;
  const minimum = points.length ? Math.min(...points.map(point => point.value)) : 0;
  const maximum = points.length ? Math.max(...points.map(point => point.value)) : 0;
  const span = Math.max(1, maximum - minimum), low = Math.max(0, minimum - span * .12), high = maximum + span * .12;
  const x = (time: number) => left + (last === first ? .5 : (time - first) / (last - first)) * (width - left - right);
  const y = (value: number) => top + (1 - (value - low) / (high - low)) * (height - top - bottom);
  const ticks = width < 500 ? 2 : 4;
  const tooltipWidth = Math.min(232, width - 16);
  const selectedLabel = selected ? `${formatTime(selected.collected_at, data.timezone)}: ${valueLabel(selected.value, metric)}` : '';
  return <div ref={target} className='usage-trend'>
    <div className='usage-chart-unit'>{metric === 'cost_total_usd' ? 'Cumulative spend' : `Cumulative ${metric === 'input_tokens_total' ? 'input' : 'output'} tokens`}</div>
    {!points.length ? <div className='usage-chart-empty'><h3>No usage data yet</h3><p>A trend will appear after successful collections.</p></div> : <div className='usage-chart-stage'>
      <svg className='usage-chart' width={width} height={height} viewBox={`0 0 ${width} ${height}`} role='img' tabIndex={0}
        aria-label={`${metricNames[metric]} over time. ${points.length} displayed observations. Gaps indicate unavailable data or a counter reset. Use left and right arrow keys to inspect observations.`}
        onFocus={() => { setFocused(true); setSelected(current => current || points.at(-1)!); }}
        onBlur={() => { setFocused(false); setSelected(null); }}
        onPointerLeave={() => { if (!focused) setSelected(null); }}
        onPointerMove={event => {
          const bounds = event.currentTarget.getBoundingClientRect();
          const pos = (event.clientX - bounds.left) * width / bounds.width;
          if (pos < left || pos > width - right) { if (!focused) setSelected(null); return; }
          if (first === last && Math.abs(pos - x(first)) > 6) { if (!focused) setSelected(null); return; }
          const time = first + (pos - left) / (width - left - right) * (last - first);
          setSelected(hoverPoint(segments, time, (last - first) * 6 / (width - left - right)));
        }}
        onKeyDown={event => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Escape'].includes(event.key)) return;
          event.preventDefault();
          if (event.key === 'Escape') { setSelected(null); return; }
          const index = selected ? points.findIndex(point => point.snapshot_id === selected.snapshot_id) : points.length - 1;
          const next = event.key === 'Home' ? 0 : event.key === 'End' ? points.length - 1 : Math.max(0, Math.min(points.length - 1, index + (event.key === 'ArrowLeft' ? -1 : 1)));
          setSelected(points[next]);
        }}>
        {[0, 1, 2, 3].map(index => { const value = low + (high - low) * index / 3, pos = y(value); return <g key={index}><line x1={left} y1={pos} x2={width - right} y2={pos} className='usage-gridline' /><text x={left - 10} y={pos + 4} textAnchor='end'>{new Intl.NumberFormat('en-US', { notation: 'compact', minimumFractionDigits: metric === 'cost_total_usd' ? 2 : 0, maximumFractionDigits: metric === 'cost_total_usd' ? 2 : 1 }).format(value)}{metric === 'cost_total_usd' ? ' $' : ''}</text></g>; })}
        {Array.from({ length: first === last ? 1 : ticks }, (_, index) => { const time = first === last ? first : first + (last - first) * index / (ticks - 1); return <text key={index} x={x(time)} y={height - 16} textAnchor={first === last ? 'middle' : index === 0 ? 'start' : index === ticks - 1 ? 'end' : 'middle'}>{new Intl.DateTimeFormat('en-US', { ...(width >= 500 || last - first >= 86400_000 ? { month: 'short', day: 'numeric' } : {}), timeZone: data.timezone, ...(last - first < 86400_000 ? { hour: '2-digit', minute: '2-digit' } : {}) }).format(time)}</text>; })}
        {segments.map((segment, index) => segment.length > 1
          ? <path key={index} fill='none' className='usage-chart-line' d={curvePath(segment.map(point => ({ x: x(point.time), y: y(point.value) })))} />
          : <line key={index} className='usage-chart-line' x1={x(segment[0].time) - 2} x2={x(segment[0].time) + 2} y1={y(segment[0].value)} y2={y(segment[0].value)} />)}
        {selected && <g aria-hidden='true'><line className='usage-chart-guide' x1={x(selected.time)} x2={x(selected.time)} y1={top} y2={height - bottom} /><circle className='usage-chart-point' cx={x(selected.time)} cy={y(selected.value)} r={4} /></g>}
      </svg>
      {selected && <div className='usage-chart-tooltip' aria-hidden='true' style={{ width: tooltipWidth, left: Math.max(8, Math.min(width - tooltipWidth - 8, x(selected.time) + 12)), top: Math.max(2, Math.min(height - bottom - 70, y(selected.value) - 76)) }}><span>{formatTime(selected.collected_at, data.timezone)}</span><strong>{valueLabel(selected.value, metric)}</strong></div>}
      <span className='sr-only' role='status'>{selectedLabel}</span>
    </div>}
    <div className='usage-chart-bottom'><span>Collection times use {data.timezone}. Gaps are not zero usage.{data.trend_reduced && ' The curve shows representative observations; original records remain available below.'}</span><button className='usage-text-button' onClick={() => setTable(value => !value)} aria-expanded={table}>{table ? 'Hide data' : 'View data'}</button></div>
    {table && <SnapshotTable data={data} endpoint={endpoint} days={days} metric={metric} />}
  </div>;
}
