import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { curvePath, type XY } from './curve';
import { dailyDate, dailyMetrics, dailyMoney, dailyTokens, type DailyMetric } from './daily-usage';
import { overviewModels, overviewSeries, type OverviewData } from './overview';

export const overviewColors = ['#365f91', '#688bb8', '#9aafca', '#637d83', '#7d7c9c', '#b49978', '#a4bab6', '#b6a5bd'];
export function OverviewChart({ data, models, metric, selectionEmpty = false }: { data: OverviewData; models: string[]; metric: DailyMetric; selectionEmpty?: boolean }) {
  const [active, setActive] = useState<number | null>(null);
  const wrap = useRef<HTMLDivElement>(null), [width, setWidth] = useState(1000);
  const palette = overviewModels(data), aggregateOnly = !palette.length && !models.length && !selectionEmpty;
  const hasCollectedData = data.days.some(day => day.collected > 0);
  const chartSeries = aggregateOnly ? [''] : models;
  useLayoutEffect(() => {
    const element = wrap.current; if (!element) return;
    const measure = () => setWidth(Math.max(300, element.getBoundingClientRect().width));
    measure(); const observer = new ResizeObserver(measure); observer.observe(element);
    return () => observer.disconnect();
  }, [models.length, aggregateOnly, hasCollectedData]);
  const series = useMemo(() => overviewSeries(data, models, metric), [data, models, metric]);
  const color = (model: string) => overviewColors[Math.max(0, palette.indexOf(model)) % overviewColors.length];
  const lines = series.length > 14, height = 310, left = width < 600 ? 48 : 68, right = width - 14, top = 24, bottom = 266;
  const maximum = Math.max(1, ...series.map(day => lines ? Math.max(0, ...day.values.map(v => v || 0)) : day.value || 0)) * 1.1;
  const step = (right - left) / Math.max(1, series.length), x = (i: number) => lines ? left + (right - left) * i / Math.max(1, series.length - 1) : left + step * (i + .5);
  const y = (value: number) => bottom - value / maximum * (bottom - top);
  const number = (value: number) => metric === 'cost_usd' ? dailyMoney(value) : new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(value);
  const day = active === null ? null : series[active];
  useEffect(() => setActive(null), [data, metric, models]);
  if (data.summary_available === false) return <div className='overview-chart-placeholder' role='img' aria-label='Daily usage chart'><svg viewBox='0 0 1000 310' preserveAspectRatio='none' aria-hidden='true'>{[24, 84.5, 145, 205.5, 266].map(y => <line key={y} className='usage-gridline' x1='0' x2='1000' y1={y} y2={y} />)}</svg></div>;
  if (!models.length && (!aggregateOnly || !hasCollectedData)) return <div className='usage-chart-empty'><h3>{palette.length || selectionEmpty ? 'Select models to view daily usage' : 'No daily usage available'}</h3><p>{palette.length || selectionEmpty ? 'Choose one or more models above.' : 'No daily bills have been collected for this period.'}</p></div>;
  return <>
    {!!models.length && <div className='daily-legend overview-chart-legend'>{models.map(model => <span key={model}><i style={{ background: color(model) }} />{model}</span>)}</div>}
    <div ref={wrap} className='overview-chart-wrap' onPointerLeave={() => setActive(null)}>
      <div className='daily-chart-scroll'><svg className={`daily-chart overview-daily-chart ${lines ? 'is-curve' : 'is-bar'}`} viewBox={`0 0 ${width} ${height}`} role='img' aria-label={`Daily ${dailyMetrics[metric].toLowerCase()}${aggregateOnly ? '' : ' by model'}. ${lines ? 'Smooth curves' : 'Stacked bars'}. Use arrow keys to inspect dates.`}>
        {[0, .25, .5, .75, 1].map(f => <g key={f}><line className='usage-gridline' x1={left} x2={right} y1={y(maximum * f)} y2={y(maximum * f)} /><text x={left - 10} y={y(maximum * f) + 4} textAnchor='end'>{new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(maximum * f)}</text></g>)}
        {lines ? chartSeries.map((model, modelIndex) => {
          const segments: XY[][] = []; let segment: XY[] = [];
          series.forEach((point, i) => { const value = point.values[modelIndex]; if (value === null) { if (segment.length) segments.push(segment); segment = []; } else segment.push({ x: x(i), y: y(value) }); });
          if (segment.length) segments.push(segment);
          return <g key={model}>{segments.map((points, i) => points.length === 1 ? <circle key={i} cx={points[0].x} cy={points[0].y} r={3} fill={color(model)} /> : <path key={i} d={curvePath(points)} fill='none' stroke={color(model)} strokeWidth={3.5} strokeLinecap='round' strokeLinejoin='round' vectorEffect='non-scaling-stroke' />)}</g>;
        }) : series.map((point, i) => {
          let offset = 0;
          return <g key={point.date}>{point.values.map((value, m) => { if (value === null) return null; const top = y(offset + value), h = y(offset) - top; offset += value; return <rect key={chartSeries[m]} x={x(i) - Math.min(34, step * .56) / 2} y={top} width={Math.min(34, step * .56)} height={h} fill={color(chartSeries[m])} />; })}{point.value === 0 && <circle cx={x(i)} cy={bottom - 2} r={2} fill='#688bb8' />}</g>;
        })}
        {series.map((point, i) => <g key={point.date}>{point.value === null && (series.length <= 31 || i % Math.ceil(series.length / 31) === 0) && <text x={x(i)} y={bottom - 8} textAnchor='middle'>—</text>}{(i % Math.ceil(series.length / (width < 600 ? 4 : 7)) === 0 || i === series.length - 1) && <text x={x(i)} y={bottom + 24} textAnchor={i === 0 ? 'start' : i === series.length - 1 ? 'end' : 'middle'}>{dailyDate(point.date)}</text>}</g>)}
        {active !== null && day && <g><line x1={x(active)} x2={x(active)} y1={top} y2={bottom} stroke='#839bb8' strokeDasharray='4 4' />{lines && day.values.map((value, i) => value === null ? null : <circle key={chartSeries[i]} cx={x(active)} cy={y(value)} r={4.5} fill={color(chartSeries[i])} />)}</g>}
        <rect className='overview-chart-target' x={left} y={top} width={right - left} height={bottom - top} fill='transparent' tabIndex={0} role='button' aria-label='Inspect daily usage; use left and right arrow keys' onPointerMove={e => { const rect = e.currentTarget.getBoundingClientRect(); const fraction = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width)); setActive(Math.min(series.length - 1, lines ? Math.round(fraction * (series.length - 1)) : Math.floor(fraction * series.length))); }} onFocus={() => setActive(0)} onBlur={() => setActive(null)} onKeyDown={e => { if (e.key === 'Escape') setActive(null); if (['ArrowLeft','ArrowRight','Home','End'].includes(e.key)) { e.preventDefault(); setActive(current => e.key === 'Home' ? 0 : e.key === 'End' ? series.length - 1 : Math.max(0, Math.min(series.length - 1, (current || 0) + (e.key === 'ArrowLeft' ? -1 : 1)))); } }} />
      </svg></div>
      {day && <div className={`daily-chart-tooltip ${active !== null && active > series.length / 2 ? 'overview-tooltip-left' : ''}`} role='status'><strong>{day.date}</strong><span>{day.value === null ? 'No daily bill collected' : dailyMetrics[metric]}</span>
        {models.map((model, i) => { const row = day.rows.find(row => row.model === model); return <div key={model}><span><i style={{ background: color(model) }} />{model}</span><b>{day.values[i] === null ? '—' : metric === 'cost_usd' ? dailyMoney(day.values[i]!) : dailyTokens(row?.[metric] || '0')}</b></div>; })}
        {day.value !== null && <div className='daily-tooltip-total'><span>Total</span><b>{metric === 'cost_usd' ? number(day.value) : dailyTokens(day.rows.reduce((sum, row) => sum + BigInt(row[metric]), 0n))}</b></div>}
      </div>}
    </div>
  </>;
}
